// Package mcpserver gives access to MCP tools through stdio.
package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/beszel"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/docker"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/gatus"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/healthchecks"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/loki"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/ntfy"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/audit"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/registry"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/version"
)

const (
	serverName                  = "homelab-evidence-mcp"
	maxConcurrentSourceRequests = 8
)

var (
	hintTrue  = true
	hintFalse = false
)

func readOnlyAnns(openWorld bool) *mcp.ToolAnnotations {
	ann := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	if openWorld {
		ann.OpenWorldHint = &hintTrue
	} else {
		ann.OpenWorldHint = &hintFalse
	}
	return ann
}

// App holds runtime dependencies.
type App struct {
	Cfg      *config.Config
	Registry *registry.Registry
	HTTP     *httpx.LockedClient
	Redact   *redaction.Engine
	Cache    *evidence.Cache
	Audit    *audit.Logger
	Log      *slog.Logger

	gatus  map[string]*gatus.Client
	dock   map[string]*docker.Client
	loki   map[string]*loki.Client
	hc     map[string]*healthchecks.Client
	beszel map[string]*beszel.Client
	ntfy   map[string]*ntfy.Client

	budget    *toolBudget
	sourceSem chan struct{}
}

// NewApp makes adapters and gets tokens.
func NewApp(cfg *config.Config, log *slog.Logger, aud *audit.Logger) (*App, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	if aud == nil {
		aud = audit.New(os.Stderr)
	}
	rules := make([]redaction.Rule, 0, len(cfg.Redact))
	for _, r := range cfg.Redact {
		rules = append(rules, redaction.Rule{Exact: r.Exact, Regex: r.Regex})
	}
	eng, err := redaction.New(rules)
	if err != nil {
		return nil, err
	}
	roots, err := config.LoadExtraCertPool(cfg.TLS.CAFile)
	if err != nil {
		return nil, err
	}
	hc := httpx.NewLockedClient(httpx.Options{
		Timeout:   cfg.Limits.TotalTimeout,
		MaxBody:   cfg.Limits.MaxBodyBytes,
		UserAgent: version.UserAgent(),
		CacheTTL:  cfg.Limits.SourceCacheTTL,
		RootCAs:   roots,
	})
	app := &App{
		Cfg:       cfg,
		Registry:  registry.New(cfg),
		HTTP:      hc,
		Redact:    eng,
		Cache:     evidence.NewCache(cfg.Limits.EvidenceCacheTTL, cfg.Limits.EvidenceCacheMax),
		Audit:     aud,
		Log:       log,
		gatus:     map[string]*gatus.Client{},
		dock:      map[string]*docker.Client{},
		loki:      map[string]*loki.Client{},
		hc:        map[string]*healthchecks.Client{},
		beszel:    map[string]*beszel.Client{},
		ntfy:      map[string]*ntfy.Client{},
		budget:    newToolBudget(cfg.Limits.MaxToolCallsPerMinute, cfg.Limits.MaxConcurrentTools),
		sourceSem: make(chan struct{}, maxConcurrentSourceRequests),
	}
	for name, src := range cfg.Sources {
		if err := hc.RegisterDestination(name, src.BaseURL); err != nil {
			return nil, fmt.Errorf("source %s: %w", name, err)
		}
		tok, err := config.ResolveToken(src)
		if err != nil {
			return nil, fmt.Errorf("source %s token: %w", name, err)
		}
		if src.Kind == "healthchecks" && tok == "" {
			return nil, fmt.Errorf("source %s: healthchecks token required", name)
		}
		headers := config.AuthHeaders(src, tok)
		switch src.Kind {
		case "gatus":
			app.gatus[name] = &gatus.Client{HTTP: hc, DestName: name, Headers: headers, Redact: eng}
		case "docker":
			app.dock[name] = &docker.Client{HTTP: hc, DestName: name, Headers: headers, Redact: eng}
		case "loki":
			app.loki[name] = &loki.Client{HTTP: hc, DestName: name, Headers: headers, Redact: eng, MaxLineBytes: cfg.Limits.MaxLogLineBytes}
		case "healthchecks":
			app.hc[name] = &healthchecks.Client{HTTP: hc, DestName: name, Headers: headers, Redact: eng}
		case "beszel":
			app.beszel[name] = &beszel.Client{HTTP: hc, DestName: name, Headers: headers, Redact: eng}
		case "ntfy":
			app.ntfy[name] = &ntfy.Client{HTTP: hc, DestName: name, Headers: headers, Redact: eng, MaxLineBytes: cfg.Limits.MaxLogLineBytes}
		}
	}
	if cfg.Limits.WarnEmptyBindings {
		for _, svc := range cfg.Services {
			if !config.HasAnyBinding(svc) {
				log.Warn("service has no source bindings", "service_id", svc.ID)
			}
		}
		if len(cfg.Services) == 0 {
			log.Warn("registry has zero services")
		}
	}
	return app, nil
}

// Server makes the MCP server and adds tools.
func (a *App) Server() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: version.Version}, nil)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "evidence_capabilities",
		Description: "Give the server version, active adapters, services with adapters, limits, and compatibility notes. The response does not include base URLs or secrets.",
		Annotations: readOnlyAnns(false),
	}, a.toolCapabilities)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_services",
		Description: "Give services from the registry with per-source coverage. Use the optional prefix filter and pagination.",
		Annotations: readOnlyAnns(false),
	}, a.toolListServices)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "service_status",
		Description: "Get a multi-source status snapshot for one service_id (Gatus, Docker, Healthchecks, Beszel). The tool does not get data from Loki or ntfy.",
		Annotations: readOnlyAnns(true),
	}, a.toolServiceStatus)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "incident_context",
		Description: "Collect a multi-source evidence timeline for a service_id with the limit from the configuration. The correlation does not identify a root cause.",
		Annotations: readOnlyAnns(true),
	}, a.toolIncidentContext)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_logs",
		Description: "Examine Loki logs for a service_id with the selector from the configuration. Use the optional text filter. The caller cannot change the URL or stream selector labels.",
		Annotations: readOnlyAnns(true),
	}, a.toolSearchLogs)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "failed_crons",
		Description: "Give Healthchecks checks with a current state of down, grace, or paused. The tool does not include ping URLs.",
		Annotations: readOnlyAnns(true),
	}, a.toolFailedCrons)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_evidence",
		Description: "Give one cached evidence item by its opaque process-local id. The cache has a short TTL.",
		Annotations: readOnlyAnns(false),
	}, a.toolGetEvidence)
	return s
}

// RunStdio uses stdin and stdout for MCP. Logs stay on stderr.
func RunStdio(ctx context.Context, app *App) error {
	server := app.Server()
	return server.Run(ctx, &mcp.StdioTransport{})
}

// toolBudget sets concurrent and per-minute tool call limits.
type toolBudget struct {
	mu     sync.Mutex
	perMin int
	window time.Time
	count  int
	sem    chan struct{}
}

func newToolBudget(perMin, concurrent int) *toolBudget {
	if perMin <= 0 {
		perMin = 120
	}
	if concurrent <= 0 {
		concurrent = 8
	}
	return &toolBudget{
		perMin: perMin,
		window: time.Now(),
		sem:    make(chan struct{}, concurrent),
	}
}

func (b *toolBudget) acquire() error {
	if b == nil {
		return nil
	}
	select {
	case b.sem <- struct{}{}:
	default:
		return fmt.Errorf("too many concurrent tool calls (budget)")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if now.Sub(b.window) >= time.Minute {
		b.window = now
		b.count = 0
	}
	if b.count >= b.perMin {
		<-b.sem
		return fmt.Errorf("tool call rate limit exceeded (%d/min)", b.perMin)
	}
	b.count++
	return nil
}

func (b *toolBudget) release() {
	if b == nil {
		return
	}
	select {
	case <-b.sem:
	default:
	}
}
