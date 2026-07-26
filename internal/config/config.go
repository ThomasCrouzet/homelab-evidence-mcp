// Package config loads and validates YAML configuration at startup.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
	"gopkg.in/yaml.v3"
)

const (
	schemaVersion       = 1
	maxConfigBytes      = 1 << 20 // 1 MiB
	maxDisplayNameRunes = 256
	maxBindingRunes     = 256
	maxHealthcheckTags  = 32
	maxTagRunes         = 128
	maxRedactRuleRunes  = 4096
	maxSources          = 64
	maxServices         = 1000
	maxHeadersPerSource = 64
	maxRedactRules      = 128
)

// Config represents fully validated runtime configuration.
type Config struct {
	Version  int
	Limits   Limits
	Audit    Audit
	Sources  map[string]Source
	Services []Service
	Redact   []RedactRule
}

// Limits groups global budgets locked at startup.
type Limits struct {
	DefaultWindow         time.Duration
	MaxIncidentWindow     time.Duration
	MaxCronWindow         time.Duration
	MaxLogLines           int
	MaxEvidenceItems      int
	PerSourceTimeout      time.Duration
	TotalTimeout          time.Duration
	MaxLogLineBytes       int
	MaxBodyBytes          int64
	EvidenceCacheTTL      time.Duration
	EvidenceCacheMax      int
	SourceCacheTTL        time.Duration
	MaxToolCallsPerMinute int
	MaxConcurrentTools    int
	WarnEmptyBindings     bool
}

// Audit configures the optional audit log; stderr always receives events.
type Audit struct {
	File     string
	MaxBytes int64
	MaxFiles int
}

// Source represents a named adapter instance.
type Source struct {
	Kind        string // gatus | docker | loki | healthchecks | beszel | ntfy
	BaseURL     string
	TokenEnv    string
	TokenFile   string
	TokenHeader string // optional; X-Api-Key for Healthchecks, Authorization otherwise
	// Headers holds only static non-secret headers.
	Headers map[string]string
}

// Service represents a canonical registry entry.
type Service struct {
	ID          string
	DisplayName string
	Sources     ServiceSources
}

// ServiceSources associates optional adapter-specific identities.
type ServiceSources struct {
	Gatus        *GatusRef
	Docker       *DockerRef
	Loki         *LokiRef
	Healthchecks *HealthchecksRef
	Beszel       *BeszelRef
	Ntfy         *NtfyRef
}

// GatusRef links a service to a Gatus endpoint key.
type GatusRef struct {
	Source      string
	EndpointKey string
}

// DockerRef links a service to a Docker container name.
type DockerRef struct {
	Source        string
	ContainerName string
}

// LokiRef links a service to a predefined LogQL selector.
type LokiRef struct {
	Source   string
	Selector string
}

// HealthchecksRef links a service to Healthchecks filters.
type HealthchecksRef struct {
	Source    string
	CheckName string
	CheckTags []string
	CheckUUID string
	// StatusFilter optionally filters by status; an empty value accepts all.
	StatusFilter string
}

// BeszelRef links a service to a Beszel system.
type BeszelRef struct {
	Source     string
	SystemName string
}

// NtfyRef links a service to an ntfy topic defined in configuration.
type NtfyRef struct {
	Source string
	Topic  string
}

// RedactRule represents a redaction rule.
type RedactRule struct {
	Exact string
	Regex string
}

// Raw file structures, before validation.
type fileConfig struct {
	Version  int                   `yaml:"version"`
	Limits   fileLimits            `yaml:"limits"`
	Audit    fileAudit             `yaml:"audit"`
	Sources  map[string]fileSource `yaml:"sources"`
	Services []fileService         `yaml:"services"`
	Redact   []fileRedact          `yaml:"redact"`
}

type fileLimits struct {
	DefaultWindow         string `yaml:"default_window"`
	MaxIncidentWindow     string `yaml:"max_incident_window"`
	MaxCronWindow         string `yaml:"max_cron_window"`
	MaxLogLines           int    `yaml:"max_log_lines"`
	MaxEvidenceItems      int    `yaml:"max_evidence_items"`
	PerSourceTimeout      string `yaml:"per_source_timeout"`
	TotalTimeout          string `yaml:"total_timeout"`
	MaxLogLineBytes       int    `yaml:"max_log_line_bytes"`
	MaxBodyBytes          int    `yaml:"max_body_bytes"`
	EvidenceCacheTTL      string `yaml:"evidence_cache_ttl"`
	EvidenceCacheMax      int    `yaml:"evidence_cache_max"`
	SourceCacheTTL        string `yaml:"source_cache_ttl"`
	MaxToolCallsPerMinute int    `yaml:"max_tool_calls_per_minute"`
	MaxConcurrentTools    int    `yaml:"max_concurrent_tools"`
	WarnEmptyBindings     *bool  `yaml:"warn_empty_bindings"`
}

type fileAudit struct {
	File     string `yaml:"file"`
	MaxBytes int    `yaml:"max_bytes"`
	MaxFiles int    `yaml:"max_files"`
}

type fileSource struct {
	Kind        string            `yaml:"kind"`
	BaseURL     string            `yaml:"base_url"`
	TokenEnv    string            `yaml:"token_env"`
	TokenFile   string            `yaml:"token_file"`
	TokenHeader string            `yaml:"token_header"`
	Headers     map[string]string `yaml:"headers"`
}

type fileService struct {
	ID          string             `yaml:"id"`
	DisplayName string             `yaml:"display_name"`
	Sources     fileServiceSources `yaml:"sources"`
}

type fileServiceSources struct {
	Gatus        *fileGatusRef        `yaml:"gatus"`
	Docker       *fileDockerRef       `yaml:"docker"`
	Loki         *fileLokiRef         `yaml:"loki"`
	Healthchecks *fileHealthchecksRef `yaml:"healthchecks"`
	Beszel       *fileBeszelRef       `yaml:"beszel"`
	Ntfy         *fileNtfyRef         `yaml:"ntfy"`
}

type fileGatusRef struct {
	Source      string `yaml:"source"`
	EndpointKey string `yaml:"endpoint_key"`
}

type fileDockerRef struct {
	Source        string `yaml:"source"`
	ContainerName string `yaml:"container_name"`
}

type fileLokiRef struct {
	Source   string `yaml:"source"`
	Selector string `yaml:"selector"`
}

type fileHealthchecksRef struct {
	Source       string   `yaml:"source"`
	CheckName    string   `yaml:"check_name"`
	CheckTags    []string `yaml:"check_tags"`
	CheckUUID    string   `yaml:"check_uuid"`
	StatusFilter string   `yaml:"status_filter"`
}

type fileBeszelRef struct {
	Source     string `yaml:"source"`
	SystemName string `yaml:"system_name"`
}

type fileNtfyRef struct {
	Source string `yaml:"source"`
	Topic  string `yaml:"topic"`
}

type fileRedact struct {
	Exact string `yaml:"exact"`
	Regex string `yaml:"regex"`
}

// ValidationError locates a configuration problem.
type ValidationError struct {
	Path    string
	Message string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Path, e.Message)
}

// ValidationErrors aggregates multiple problems.
type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	if len(e) == 0 {
		return "validation failed"
	}
	parts := make([]string, len(e))
	for i, v := range e {
		parts[i] = v.Error()
	}
	return strings.Join(parts, "; ")
}

var (
	serviceIDRe  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	sourceNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	topicRe      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	envNameRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerNameRe = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,128}$")
	yamlScalarRe = regexp.MustCompile(`!![[:alnum:]_-]+\s+('[^']*'|"[^"]*")`)
	// A Loki selector accepts labels then simple |= or |~ filters.
	lokiSelectorRe = regexp.MustCompile(`^\{[a-zA-Z_][a-zA-Z0-9_]*="[^"]*"(?:\s*,\s*[a-zA-Z_][a-zA-Z0-9_]*="[^"]*")*\}(?:\s*\|\s*[=~]\s*"[^"]*")*$`)
)

// LoadFile reads and validates a YAML file.
// The file must not be group- or world-accessible.
func LoadFile(path string) (*Config, error) {
	if path == "" {
		return nil, errors.New("config path is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("config: path is not a regular file")
	}
	if st.Size() > maxConfigBytes {
		return nil, fmt.Errorf("config: file exceeds %d bytes", maxConfigBytes)
	}
	if insecureUnixPermissions(st.Mode()) {
		return nil, errors.New("config: file is group or world accessible (use mode 0600 or tighter)")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("config read: %w", err)
	}
	return Parse(raw)
}

// Parse validates YAML content and builds Config.
func Parse(raw []byte) (*Config, error) {
	if len(raw) == 0 {
		return nil, ValidationErrors{{Path: "version", Message: "empty configuration"}}
	}
	if len(raw) > maxConfigBytes {
		return nil, ValidationErrors{{Path: ".", Message: fmt.Sprintf("configuration exceeds %d bytes", maxConfigBytes)}}
	}
	// Reject clearly executable or include syntax.
	s := string(raw)
	for _, bad := range []string{"{{", "}}", "!!python", "!!js", "${", "`$(", "include:", "!include"} {
		if strings.Contains(s, bad) {
			return nil, ValidationErrors{{Path: ".", Message: "disallowed template or include syntax: " + bad}}
		}
	}

	var fc fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil {
		return nil, ValidationErrors{{Path: ".", Message: "yaml parse error: " + sanitizeYAMLErr(err)}}
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, ValidationErrors{{Path: ".", Message: "multiple YAML documents are not allowed"}}
		}
		return nil, ValidationErrors{{Path: ".", Message: "yaml parse error: " + sanitizeYAMLErr(err)}}
	}

	var errs ValidationErrors
	if fc.Version != schemaVersion {
		errs = append(errs, ValidationError{"version", fmt.Sprintf("must be %d", schemaVersion)})
	}

	limits, lerr := parseLimits(fc.Limits)
	errs = append(errs, lerr...)

	auditCfg, aerr := parseAudit(fc.Audit)
	errs = append(errs, aerr...)

	sources := make(map[string]Source)
	if len(fc.Sources) == 0 {
		errs = append(errs, ValidationError{"sources", "at least one source is required"})
	}
	sourceNames := make([]string, 0, len(fc.Sources))
	for name := range fc.Sources {
		sourceNames = append(sourceNames, name)
	}
	sort.Strings(sourceNames)
	if len(sourceNames) > maxSources {
		errs = append(errs, ValidationError{"sources", "must contain at most 64 sources"})
		sourceNames = sourceNames[:maxSources]
	}
	for _, name := range sourceNames {
		fs := fc.Sources[name]
		if !sourceNameRe.MatchString(name) {
			errs = append(errs, ValidationError{"sources." + name, "invalid source name"})
			continue
		}
		src, serr := parseSource(name, fs)
		if len(serr) > 0 {
			errs = append(errs, serr...)
			continue
		}
		sources[name] = src
	}

	seenIDs := map[string]struct{}{}
	var services []Service
	serviceFiles := fc.Services
	if len(serviceFiles) > maxServices {
		errs = append(errs, ValidationError{"services", "must contain at most 1000 services"})
		serviceFiles = serviceFiles[:maxServices]
	}
	for i, fsv := range serviceFiles {
		p := fmt.Sprintf("services[%d]", i)
		svc, serr := parseService(p, fsv, sources)
		if len(serr) > 0 {
			errs = append(errs, serr...)
			continue
		}
		if _, ok := seenIDs[svc.ID]; ok {
			errs = append(errs, ValidationError{p + ".id", "duplicate service id"})
			continue
		}
		seenIDs[svc.ID] = struct{}{}
		services = append(services, svc)
	}

	var redact []RedactRule
	redactFiles := fc.Redact
	if len(redactFiles) > maxRedactRules {
		errs = append(errs, ValidationError{"redact", "must contain at most 128 rules"})
		redactFiles = redactFiles[:maxRedactRules]
	}
	for i, fr := range redactFiles {
		p := fmt.Sprintf("redact[%d]", i)
		if fr.Exact == "" && fr.Regex == "" {
			errs = append(errs, ValidationError{p, "exact or regex required"})
			continue
		}
		if len([]rune(fr.Exact)) > maxRedactRuleRunes {
			errs = append(errs, ValidationError{p + ".exact", "must be <= 4096 characters"})
			continue
		}
		if len([]rune(fr.Regex)) > maxRedactRuleRunes {
			errs = append(errs, ValidationError{p + ".regex", "must be <= 4096 characters"})
			continue
		}
		if fr.Regex != "" {
			if _, err := regexp.Compile(fr.Regex); err != nil {
				errs = append(errs, ValidationError{p + ".regex", "invalid regex"})
				continue
			}
		}
		redact = append(redact, RedactRule(fr))
	}

	if len(errs) > 0 {
		return nil, errs
	}
	return &Config{
		Version:  fc.Version,
		Limits:   limits,
		Audit:    auditCfg,
		Sources:  sources,
		Services: services,
		Redact:   redact,
	}, nil
}

func parseLimits(f fileLimits) (Limits, ValidationErrors) {
	var errs ValidationErrors
	l := Limits{
		MaxLogLines:           f.MaxLogLines,
		MaxEvidenceItems:      f.MaxEvidenceItems,
		MaxLogLineBytes:       f.MaxLogLineBytes,
		MaxBodyBytes:          int64(f.MaxBodyBytes),
		EvidenceCacheMax:      f.EvidenceCacheMax,
		MaxToolCallsPerMinute: f.MaxToolCallsPerMinute,
		MaxConcurrentTools:    f.MaxConcurrentTools,
		WarnEmptyBindings:     true,
	}
	if f.WarnEmptyBindings != nil {
		l.WarnEmptyBindings = *f.WarnEmptyBindings
	}
	setDur := func(field, raw string, dest *time.Duration, def time.Duration, min, max time.Duration) {
		if raw == "" {
			*dest = def
			return
		}
		d, e := time.ParseDuration(raw)
		if e != nil {
			errs = append(errs, ValidationError{"limits." + field, "invalid duration"})
			return
		}
		if d < min || d > max {
			errs = append(errs, ValidationError{"limits." + field, fmt.Sprintf("must be between %s and %s", min, max)})
			return
		}
		*dest = d
	}
	setDur("default_window", f.DefaultWindow, &l.DefaultWindow, time.Hour, time.Minute, 168*time.Hour)
	setDur("max_incident_window", f.MaxIncidentWindow, &l.MaxIncidentWindow, 24*time.Hour, time.Minute, 168*time.Hour)
	setDur("max_cron_window", f.MaxCronWindow, &l.MaxCronWindow, 168*time.Hour, time.Hour, 720*time.Hour)
	setDur("per_source_timeout", f.PerSourceTimeout, &l.PerSourceTimeout, 3*time.Second, 200*time.Millisecond, 30*time.Second)
	setDur("total_timeout", f.TotalTimeout, &l.TotalTimeout, 10*time.Second, time.Second, 60*time.Second)
	setDur("evidence_cache_ttl", f.EvidenceCacheTTL, &l.EvidenceCacheTTL, 5*time.Minute, 30*time.Second, time.Hour)
	setDur("source_cache_ttl", f.SourceCacheTTL, &l.SourceCacheTTL, 15*time.Second, 0, 5*time.Minute)

	if l.MaxLogLines <= 0 {
		if l.MaxLogLines < 0 {
			errs = append(errs, ValidationError{"limits.max_log_lines", "must be positive"})
		}
		l.MaxLogLines = 100
	}
	if l.MaxLogLines > 500 {
		errs = append(errs, ValidationError{"limits.max_log_lines", "must be <= 500"})
	}
	if l.MaxEvidenceItems <= 0 {
		if l.MaxEvidenceItems < 0 {
			errs = append(errs, ValidationError{"limits.max_evidence_items", "must be positive"})
		}
		l.MaxEvidenceItems = 200
	}
	if l.MaxEvidenceItems > 1000 {
		errs = append(errs, ValidationError{"limits.max_evidence_items", "must be <= 1000"})
	}
	if l.MaxLogLineBytes <= 0 {
		if l.MaxLogLineBytes < 0 {
			errs = append(errs, ValidationError{"limits.max_log_line_bytes", "must be positive"})
		}
		l.MaxLogLineBytes = 2048
	}
	if l.MaxLogLineBytes > 16<<10 {
		errs = append(errs, ValidationError{"limits.max_log_line_bytes", "must be <= 16KiB"})
	}
	if l.MaxBodyBytes <= 0 {
		if l.MaxBodyBytes < 0 {
			errs = append(errs, ValidationError{"limits.max_body_bytes", "must be positive"})
		}
		l.MaxBodyBytes = 2 << 20
	}
	if l.MaxBodyBytes > 8<<20 {
		errs = append(errs, ValidationError{"limits.max_body_bytes", "must be <= 8MiB"})
	}
	if l.EvidenceCacheMax <= 0 {
		if l.EvidenceCacheMax < 0 {
			errs = append(errs, ValidationError{"limits.evidence_cache_max", "must be positive"})
		}
		l.EvidenceCacheMax = 256
	}
	if l.EvidenceCacheMax > 1000 {
		errs = append(errs, ValidationError{"limits.evidence_cache_max", "must be <= 1000"})
	}
	if l.MaxToolCallsPerMinute <= 0 {
		if l.MaxToolCallsPerMinute < 0 {
			errs = append(errs, ValidationError{"limits.max_tool_calls_per_minute", "must be positive"})
		}
		l.MaxToolCallsPerMinute = 120
	}
	if l.MaxToolCallsPerMinute > 1000 {
		errs = append(errs, ValidationError{"limits.max_tool_calls_per_minute", "must be <= 1000"})
	}
	if l.MaxConcurrentTools <= 0 {
		if l.MaxConcurrentTools < 0 {
			errs = append(errs, ValidationError{"limits.max_concurrent_tools", "must be positive"})
		}
		l.MaxConcurrentTools = 8
	}
	if l.MaxConcurrentTools > 64 {
		errs = append(errs, ValidationError{"limits.max_concurrent_tools", "must be <= 64"})
	}
	if l.TotalTimeout < l.PerSourceTimeout {
		errs = append(errs, ValidationError{"limits.total_timeout", "must be >= per_source_timeout"})
	}
	if l.DefaultWindow > l.MaxIncidentWindow {
		errs = append(errs, ValidationError{"limits.default_window", "must be <= max_incident_window"})
	}
	return l, errs
}

func parseAudit(f fileAudit) (Audit, ValidationErrors) {
	var errs ValidationErrors
	a := Audit{
		File:     strings.TrimSpace(f.File),
		MaxBytes: int64(f.MaxBytes),
		MaxFiles: f.MaxFiles,
	}
	if a.File == "" {
		return a, nil
	}
	if len([]rune(a.File)) > 4096 || hasControl(a.File) {
		errs = append(errs, ValidationError{"audit.file", "must be a valid path of at most 4096 characters"})
	}
	if a.MaxBytes <= 0 {
		if a.MaxBytes < 0 {
			errs = append(errs, ValidationError{"audit.max_bytes", "must be positive"})
		}
		a.MaxBytes = 10 << 20
	}
	if a.MaxBytes > 100<<20 {
		errs = append(errs, ValidationError{"audit.max_bytes", "must be <= 100MiB"})
	}
	if a.MaxFiles <= 0 {
		if a.MaxFiles < 0 {
			errs = append(errs, ValidationError{"audit.max_files", "must be positive"})
		}
		a.MaxFiles = 3
	}
	if a.MaxFiles > 20 {
		errs = append(errs, ValidationError{"audit.max_files", "must be <= 20"})
	}
	return a, errs
}

func parseSource(name string, fs fileSource) (Source, ValidationErrors) {
	var errs ValidationErrors
	kind := strings.ToLower(strings.TrimSpace(fs.Kind))
	tokenFile := strings.TrimSpace(fs.TokenFile)
	switch kind {
	case "gatus", "docker", "loki", "healthchecks", "beszel", "ntfy":
	default:
		errs = append(errs, ValidationError{"sources." + name + ".kind", "must be gatus, docker, loki, healthchecks, beszel, or ntfy"})
	}
	base := strings.TrimSpace(fs.BaseURL)
	if base == "" {
		errs = append(errs, ValidationError{"sources." + name + ".base_url", "required"})
	} else if len([]rune(base)) > 4096 || hasControl(base) {
		errs = append(errs, ValidationError{"sources." + name + ".base_url", "must be a valid URL of at most 4096 characters"})
	} else {
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, ValidationError{"sources." + name + ".base_url", "must be absolute http(s) URL with host"})
		} else if u.User != nil {
			errs = append(errs, ValidationError{"sources." + name + ".base_url", "userinfo not allowed; use token_env or token_file"})
		}
		if u != nil && u.RawQuery != "" {
			errs = append(errs, ValidationError{"sources." + name + ".base_url", "query strings are not allowed; use headers or token_env/token_file"})
		}
		if u != nil && u.Fragment != "" {
			errs = append(errs, ValidationError{"sources." + name + ".base_url", "fragments are not allowed"})
		}
		if u != nil && invalidLockedPath(u.Path) {
			errs = append(errs, ValidationError{"sources." + name + ".base_url", "path contains a control character, backslash, or dot segment"})
		}
	}
	if fs.TokenEnv != "" && fs.TokenFile != "" {
		errs = append(errs, ValidationError{"sources." + name, "set only one of token_env or token_file"})
	}
	if fs.TokenEnv != "" && !envNameRe.MatchString(fs.TokenEnv) {
		errs = append(errs, ValidationError{"sources." + name + ".token_env", "invalid environment variable name"})
	}
	if fs.TokenFile != "" && tokenFile == "" {
		errs = append(errs, ValidationError{"sources." + name + ".token_file", "must not be blank"})
	}
	if len([]rune(tokenFile)) > 4096 || hasControl(tokenFile) {
		errs = append(errs, ValidationError{"sources." + name + ".token_file", "must be a valid path of at most 4096 characters"})
	}
	if kind == "healthchecks" && fs.TokenEnv == "" && tokenFile == "" {
		errs = append(errs, ValidationError{"sources." + name, "healthchecks requires token_env or token_file"})
	}
	headerNames := make([]string, 0, len(fs.Headers))
	for headerName := range fs.Headers {
		headerNames = append(headerNames, headerName)
	}
	sort.Strings(headerNames)
	if len(headerNames) > maxHeadersPerSource {
		errs = append(errs, ValidationError{"sources." + name + ".headers", "must contain at most 64 headers"})
		headerNames = headerNames[:maxHeadersPerSource]
	}
	seenHeaders := make(map[string]struct{}, len(headerNames))
	for _, hk := range headerNames {
		hv := fs.Headers[hk]
		canonicalName := strings.ToLower(strings.TrimSpace(hk))
		if _, exists := seenHeaders[canonicalName]; exists {
			errs = append(errs, ValidationError{"sources." + name + ".headers", "duplicate header name (case-insensitive)"})
			continue
		}
		seenHeaders[canonicalName] = struct{}{}
		if !headerNameRe.MatchString(hk) {
			errs = append(errs, ValidationError{"sources." + name + ".headers", "invalid header name"})
			continue
		}
		if isSensitiveHeaderName(hk) {
			errs = append(errs, ValidationError{"sources." + name + ".headers." + hk, "authentication headers must use token_env/token_file with token_header"})
		}
		if isReservedHeaderName(hk) {
			errs = append(errs, ValidationError{"sources." + name + ".headers." + hk, "reserved header name"})
		}
		if len(hv) > 4096 || hasControl(hv) {
			errs = append(errs, ValidationError{"sources." + name + ".headers." + hk, "invalid header value"})
		}
		if looksLikeSecret(hv) {
			errs = append(errs, ValidationError{"sources." + name + ".headers." + hk, "value looks like a literal secret"})
		}
	}
	th := strings.TrimSpace(fs.TokenHeader)
	if fs.TokenHeader != "" && th == "" {
		errs = append(errs, ValidationError{"sources." + name + ".token_header", "must not be blank"})
	}
	if th != "" {
		if !headerNameRe.MatchString(th) {
			errs = append(errs, ValidationError{"sources." + name + ".token_header", "invalid header name"})
		}
		if isReservedHeaderName(th) {
			errs = append(errs, ValidationError{"sources." + name + ".token_header", "reserved header name"})
		}
		if fs.TokenEnv == "" && tokenFile == "" {
			errs = append(errs, ValidationError{"sources." + name + ".token_header", "requires token_env or token_file"})
		}
	}
	if len(errs) > 0 {
		return Source{}, errs
	}
	return Source{
		Kind:        kind,
		BaseURL:     strings.TrimRight(base, "/"),
		TokenEnv:    fs.TokenEnv,
		TokenFile:   tokenFile,
		TokenHeader: th,
		Headers:     fs.Headers,
	}, nil
}

func parseService(path string, f fileService, sources map[string]Source) (Service, ValidationErrors) {
	var errs ValidationErrors
	id := strings.TrimSpace(f.ID)
	if !serviceIDRe.MatchString(id) {
		errs = append(errs, ValidationError{path + ".id", "must match " + serviceIDRe.String()})
	}
	dn := strings.TrimSpace(f.DisplayName)
	if dn == "" {
		dn = id
	}
	if len([]rune(dn)) > maxDisplayNameRunes {
		errs = append(errs, ValidationError{path + ".display_name", "must be <= 256 characters"})
	} else if hasControl(dn) {
		errs = append(errs, ValidationError{path + ".display_name", "must not contain control characters"})
	}
	ss := ServiceSources{}
	if f.Sources.Gatus != nil {
		ref := f.Sources.Gatus
		if err := checkSourceRef(path+".sources.gatus", ref.Source, "gatus", sources); err != nil {
			errs = append(errs, *err)
		}
		endpointKey := strings.TrimSpace(ref.EndpointKey)
		if endpointKey == "" {
			errs = append(errs, ValidationError{path + ".sources.gatus.endpoint_key", "required"})
		} else if len([]rune(endpointKey)) > maxBindingRunes {
			errs = append(errs, ValidationError{path + ".sources.gatus.endpoint_key", "must be <= 256 characters"})
		} else if hasControl(endpointKey) {
			errs = append(errs, ValidationError{path + ".sources.gatus.endpoint_key", "must not contain control characters"})
		}
		ss.Gatus = &GatusRef{Source: ref.Source, EndpointKey: endpointKey}
	}
	if f.Sources.Docker != nil {
		ref := f.Sources.Docker
		if err := checkSourceRef(path+".sources.docker", ref.Source, "docker", sources); err != nil {
			errs = append(errs, *err)
		}
		containerName := strings.TrimSpace(ref.ContainerName)
		if containerName == "" {
			errs = append(errs, ValidationError{path + ".sources.docker.container_name", "required"})
		} else if len([]rune(containerName)) > maxBindingRunes {
			errs = append(errs, ValidationError{path + ".sources.docker.container_name", "must be <= 256 characters"})
		} else if hasControl(containerName) {
			errs = append(errs, ValidationError{path + ".sources.docker.container_name", "must not contain control characters"})
		}
		ss.Docker = &DockerRef{Source: ref.Source, ContainerName: containerName}
	}
	if f.Sources.Loki != nil {
		ref := f.Sources.Loki
		if err := checkSourceRef(path+".sources.loki", ref.Source, "loki", sources); err != nil {
			errs = append(errs, *err)
		}
		sel := strings.TrimSpace(ref.Selector)
		if sel == "" {
			errs = append(errs, ValidationError{path + ".sources.loki.selector", "required"})
		} else if !validLokiSelector(sel) {
			errs = append(errs, ValidationError{path + ".sources.loki.selector", "must be a simple label selector {k=\"v\"} with optional |= filters"})
		}
		ss.Loki = &LokiRef{Source: ref.Source, Selector: sel}
	}
	if f.Sources.Healthchecks != nil {
		ref := f.Sources.Healthchecks
		if err := checkSourceRef(path+".sources.healthchecks", ref.Source, "healthchecks", sources); err != nil {
			errs = append(errs, *err)
		}
		checkName := strings.TrimSpace(ref.CheckName)
		checkUUID := strings.TrimSpace(ref.CheckUUID)
		checkTags := make([]string, 0, len(ref.CheckTags))
		if len(ref.CheckTags) > maxHealthcheckTags {
			errs = append(errs, ValidationError{path + ".sources.healthchecks.check_tags", "must contain at most 32 tags"})
		}
		for i, tag := range ref.CheckTags {
			tag = strings.TrimSpace(tag)
			tagPath := fmt.Sprintf("%s.sources.healthchecks.check_tags[%d]", path, i)
			if tag == "" {
				errs = append(errs, ValidationError{tagPath, "must not be empty"})
				continue
			}
			if len([]rune(tag)) > maxTagRunes {
				errs = append(errs, ValidationError{tagPath, "must be <= 128 characters"})
				continue
			}
			if hasControl(tag) {
				errs = append(errs, ValidationError{tagPath, "must not contain control characters"})
				continue
			}
			checkTags = append(checkTags, tag)
		}
		if checkName == "" && checkUUID == "" && len(checkTags) == 0 {
			errs = append(errs, ValidationError{path + ".sources.healthchecks", "check_name, check_uuid, or check_tags required"})
		}
		if len([]rune(checkName)) > maxBindingRunes {
			errs = append(errs, ValidationError{path + ".sources.healthchecks.check_name", "must be <= 256 characters"})
		} else if hasControl(checkName) {
			errs = append(errs, ValidationError{path + ".sources.healthchecks.check_name", "must not contain control characters"})
		}
		if len([]rune(checkUUID)) > maxBindingRunes {
			errs = append(errs, ValidationError{path + ".sources.healthchecks.check_uuid", "must be <= 256 characters"})
		} else if hasControl(checkUUID) {
			errs = append(errs, ValidationError{path + ".sources.healthchecks.check_uuid", "must not contain control characters"})
		}
		sf := strings.ToLower(strings.TrimSpace(ref.StatusFilter))
		if sf != "" {
			switch sf {
			case "up", "down", "grace", "paused", "new":
			default:
				errs = append(errs, ValidationError{path + ".sources.healthchecks.status_filter", "must be up, down, grace, paused, or new"})
			}
		}
		ss.Healthchecks = &HealthchecksRef{
			Source:       ref.Source,
			CheckName:    checkName,
			CheckTags:    checkTags,
			CheckUUID:    checkUUID,
			StatusFilter: sf,
		}
	}
	if f.Sources.Beszel != nil {
		ref := f.Sources.Beszel
		if err := checkSourceRef(path+".sources.beszel", ref.Source, "beszel", sources); err != nil {
			errs = append(errs, *err)
		}
		systemName := strings.TrimSpace(ref.SystemName)
		if systemName == "" {
			errs = append(errs, ValidationError{path + ".sources.beszel.system_name", "required"})
		} else if len([]rune(systemName)) > maxBindingRunes {
			errs = append(errs, ValidationError{path + ".sources.beszel.system_name", "must be <= 256 characters"})
		} else if hasControl(systemName) {
			errs = append(errs, ValidationError{path + ".sources.beszel.system_name", "must not contain control characters"})
		}
		ss.Beszel = &BeszelRef{Source: ref.Source, SystemName: systemName}
	}
	if f.Sources.Ntfy != nil {
		ref := f.Sources.Ntfy
		if err := checkSourceRef(path+".sources.ntfy", ref.Source, "ntfy", sources); err != nil {
			errs = append(errs, *err)
		}
		topic := strings.TrimSpace(ref.Topic)
		if !topicRe.MatchString(topic) {
			errs = append(errs, ValidationError{path + ".sources.ntfy.topic", "must match " + topicRe.String()})
		}
		ss.Ntfy = &NtfyRef{Source: ref.Source, Topic: topic}
	}
	if len(errs) > 0 {
		return Service{}, errs
	}
	return Service{ID: id, DisplayName: dn, Sources: ss}, nil
}

func checkSourceRef(path, name, wantKind string, sources map[string]Source) *ValidationError {
	if !sourceNameRe.MatchString(name) {
		return &ValidationError{path + ".source", "invalid source reference"}
	}
	src, ok := sources[name]
	if !ok {
		return &ValidationError{path + ".source", "unknown source " + name}
	}
	if src.Kind != wantKind {
		return &ValidationError{path + ".source", fmt.Sprintf("source %s is kind %s, want %s", name, src.Kind, wantKind)}
	}
	return nil
}

func validLokiSelector(sel string) bool {
	if len(sel) > 512 {
		return false
	}
	if hasControl(sel) || strings.ContainsAny(sel, "`") || strings.Contains(sel, "{{") {
		return false
	}
	return lokiSelectorRe.MatchString(sel)
}

func looksLikeSecret(v string) bool {
	if len(v) >= 20 && regexp.MustCompile(`^[A-Za-z0-9_\-+/=]{20,}$`).MatchString(v) {
		return true
	}
	lv := strings.ToLower(v)
	return strings.HasPrefix(lv, "bearer ") || strings.HasPrefix(lv, "basic ")
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func invalidLockedPath(path string) bool {
	if strings.Contains(path, `\`) || hasControl(path) {
		return true
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

func isSensitiveHeaderName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch lower {
	case "authorization", "proxy-authorization", "x-api-key", "api-key", "cookie", "set-cookie":
		return true
	}
	for _, marker := range []string{"token", "secret", "password", "passwd", "credential", "apikey"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isReservedHeaderName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "host", "content-length", "user-agent", "accept", "connection",
		"transfer-encoding", "trailer", "upgrade", "proxy-connection",
		"keep-alive", "te":
		return true
	default:
		return false
	}
}

func sanitizeYAMLErr(err error) string {
	msg := err.Error()
	msg = yamlScalarRe.ReplaceAllString(msg, "!!value [REDACTED]")
	if eng, buildErr := redaction.New(nil); buildErr == nil {
		msg, _ = eng.Apply(msg)
	}
	msg = redaction.SanitizeControl(msg)
	msg, _ = redaction.Truncate(msg, 200)
	return msg
}

// ResolveToken returns a source's token, or an empty string.
func ResolveToken(src Source) (string, error) {
	if src.TokenEnv != "" {
		v := strings.TrimSpace(os.Getenv(src.TokenEnv))
		if v == "" {
			return "", fmt.Errorf("environment variable %s is empty", src.TokenEnv)
		}
		return v, nil
	}
	if src.TokenFile != "" {
		return readSecretFile(src.TokenFile)
	}
	return "", nil
}

// AuthHeaders builds static headers and optional authentication.
// Authentication values come only from the environment or a file.
func AuthHeaders(src Source, token string) map[string]string {
	out := map[string]string{}
	for k, v := range src.Headers {
		if strings.EqualFold(k, "authorization") {
			continue
		}
		out[k] = v
	}
	if token == "" {
		return out
	}
	name := strings.TrimSpace(src.TokenHeader)
	if name == "" {
		if src.Kind == "healthchecks" {
			name = "X-Api-Key"
		} else {
			name = "Authorization"
		}
	}
	val := token
	if strings.EqualFold(name, "Authorization") {
		lower := strings.ToLower(token)
		if !strings.HasPrefix(lower, "bearer ") && !strings.HasPrefix(lower, "basic ") {
			val = "Bearer " + token
		}
	}
	out[name] = val
	return out
}

// HasAnyBinding reports whether a service has at least one source.
func HasAnyBinding(s Service) bool {
	ss := s.Sources
	return ss.Gatus != nil || ss.Docker != nil || ss.Loki != nil ||
		ss.Healthchecks != nil || ss.Beszel != nil || ss.Ntfy != nil
}

func readSecretFile(path string) (string, error) {
	if strings.HasPrefix(path, "file://") {
		u, err := url.Parse(path)
		if err != nil {
			return "", err
		}
		if u.Host != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", errors.New("token file URL must not contain host, userinfo, query, or fragment")
		}
		path = u.Path
		if path == "" {
			path = u.Opaque
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("token file is not a regular file")
	}
	if st.Size() > 4<<10 {
		return "", errors.New("token file exceeds 4KiB")
	}
	if insecureUnixPermissions(st.Mode()) {
		return "", errors.New("token file is group or world accessible")
	}
	b, err := io.ReadAll(io.LimitReader(f, (4<<10)+1))
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(b))
	if value == "" {
		return "", errors.New("token file is empty")
	}
	return value, nil
}

func insecureUnixPermissions(mode os.FileMode) bool {
	return runtime.GOOS != "windows" && mode.Perm()&0o077 != 0
}
