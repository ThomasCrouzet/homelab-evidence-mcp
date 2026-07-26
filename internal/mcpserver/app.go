// Package mcpserver expose les outils MCP sur stdio.
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

// App regroupe les dépendances d’exécution.
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

// NewApp construit les adaptateurs et résout les jetons.
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
	hc := httpx.NewLockedClient(httpx.Options{
		Timeout:   cfg.Limits.TotalTimeout,
		MaxBody:   cfg.Limits.MaxBodyBytes,
		UserAgent: version.UserAgent(),
		CacheTTL:  cfg.Limits.SourceCacheTTL,
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

// Server construit le serveur MCP et enregistre les outils.
func (a *App) Server() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: version.Version}, nil)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "evidence_capabilities",
		Description: "Return server version, active adapters, covered services, limits, and compatibility notes. Never includes base URLs or secrets.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, a.toolCapabilities)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_services",
		Description: "List canonical services with per-source coverage. Optional prefix filter and pagination.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, a.toolListServices)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "service_status",
		Description: "Multi-source status snapshot for one service_id (Gatus, Docker, Healthchecks, Beszel). Loki/ntfy are not queried.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, a.toolServiceStatus)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "incident_context",
		Description: "Collect bounded multi-source evidence timeline for a service_id. Deterministic correlation only; no root-cause claims.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, a.toolIncidentContext)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_logs",
		Description: "Search Loki logs for a service_id using the preconfigured selector. Optional text filter; caller cannot change URL or stream selector labels.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, a.toolSearchLogs)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "failed_crons",
		Description: "List Healthchecks checks that are currently down, in grace, or paused. Never returns ping URLs.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, a.toolFailedCrons)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_evidence",
		Description: "Return one previously emitted evidence item by opaque process-local id (short TTL cache).",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, a.toolGetEvidence)
	return s
}

// RunStdio sert MCP sur stdin/stdout ; les journaux restent sur stderr.
func RunStdio(ctx context.Context, app *App) error {
	server := app.Server()
	return server.Run(ctx, &mcp.StdioTransport{})
}

// toolBudget limite les appels simultanés et par minute.
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
