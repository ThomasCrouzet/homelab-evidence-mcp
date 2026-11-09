package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/version"
)

const maxDoctorBindings = 100

// DoctorBinding gives one service binding that the doctor checked.
type DoctorBinding struct {
	ServiceID string `json:"service_id"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	Complete  bool   `json:"complete"`
	ItemCount int    `json:"item_count"`
}

// DoctorSource gives the state of one configured source.
type DoctorSource struct {
	Name            string          `json:"name"`
	Kind            string          `json:"kind"`
	Status          string          `json:"status"`
	Reason          string          `json:"reason,omitempty"`
	Error           string          `json:"error,omitempty"`
	Complete        bool            `json:"complete"`
	Capabilities    []string        `json:"capabilities"`
	Bindings        []DoctorBinding `json:"bindings"`
	OmittedBindings int             `json:"omitted_bindings,omitempty"`
}

// DoctorReport is the machine-readable result of a network diagnostic.
type DoctorReport struct {
	Version       string         `json:"version"`
	Transport     string         `json:"transport"`
	ReadOnly      bool           `json:"readonly"`
	ServicesCount int            `json:"services_count"`
	Complete      bool           `json:"complete"`
	Sources       []DoctorSource `json:"sources"`
	Warnings      []string       `json:"warnings,omitempty"`
}

// Doctor checks source reachability, decoding, and configured bindings.
// It uses the same locked HTTP client and collectors as the MCP tools.
func (a *App) Doctor(ctx context.Context) DoctorReport {
	timeout := a.Cfg.Limits.TotalTimeout
	if timeout < 30*time.Second {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	now := time.Now().UTC()
	windowStart := now.Add(-5 * time.Minute)
	names := make([]string, 0, len(a.Cfg.Sources))
	byName := make(map[string]*DoctorSource, len(a.Cfg.Sources))
	for name, source := range a.Cfg.Sources {
		names = append(names, name)
		byName[name] = &DoctorSource{
			Name:         name,
			Kind:         source.Kind,
			Status:       "unbound",
			Complete:     true,
			Capabilities: adapterCapabilities(source.Kind),
			Bindings:     []DoctorBinding{},
		}
	}
	sort.Strings(names)

	var mu sync.Mutex
	services := make(chan config.Service)
	var wg sync.WaitGroup
	workers := len(a.Cfg.Services)
	if workers > maxConcurrentSourceRequests {
		workers = maxConcurrentSourceRequests
	}
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for service := range services {
				sources := service.Sources
				if sources.Gatus != nil {
					a.doctorBinding(ctx, &mu, byName, sources.Gatus.Source, service.ID, func(ctx context.Context) (evidence.SourceOutcome, []evidence.Item) {
						return a.collectGatusStatus(ctx, service.ID, sources.Gatus)
					})
				}
				if sources.Docker != nil {
					a.doctorBinding(ctx, &mu, byName, sources.Docker.Source, service.ID, func(ctx context.Context) (evidence.SourceOutcome, []evidence.Item) {
						return a.collectDockerStatus(ctx, service.ID, sources.Docker)
					})
				}
				if sources.Healthchecks != nil {
					a.doctorBinding(ctx, &mu, byName, sources.Healthchecks.Source, service.ID, func(ctx context.Context) (evidence.SourceOutcome, []evidence.Item) {
						return a.collectHCStatus(ctx, service.ID, sources.Healthchecks)
					})
				}
				if sources.Beszel != nil {
					a.doctorBinding(ctx, &mu, byName, sources.Beszel.Source, service.ID, func(ctx context.Context) (evidence.SourceOutcome, []evidence.Item) {
						return a.collectBeszel(ctx, service.ID, sources.Beszel)
					})
				}
				if sources.Loki != nil {
					a.doctorBinding(ctx, &mu, byName, sources.Loki.Source, service.ID, func(ctx context.Context) (evidence.SourceOutcome, []evidence.Item) {
						return a.collectLoki(ctx, service.ID, sources.Loki, windowStart, now, "", "", 1)
					})
				}
				if sources.Ntfy != nil {
					a.doctorBinding(ctx, &mu, byName, sources.Ntfy.Source, service.ID, func(ctx context.Context) (evidence.SourceOutcome, []evidence.Item) {
						return a.collectNtfy(ctx, service.ID, sources.Ntfy, windowStart, now, 1)
					})
				}
			}
		}()
	}
	for _, service := range a.Cfg.Services {
		services <- service
	}
	close(services)
	wg.Wait()

	report := DoctorReport{
		Version:       version.Version,
		Transport:     "stdio",
		ReadOnly:      true,
		ServicesCount: a.Registry.Len(),
		Complete:      true,
		Sources:       make([]DoctorSource, 0, len(names)),
	}
	for _, name := range names {
		source := byName[name]
		sort.Slice(source.Bindings, func(i, j int) bool {
			return source.Bindings[i].ServiceID < source.Bindings[j].ServiceID
		})
		if len(source.Bindings) > maxDoctorBindings {
			source.OmittedBindings = len(source.Bindings) - maxDoctorBindings
			source.Bindings = source.Bindings[:maxDoctorBindings]
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("source %s has %d more bindings than the doctor output limit", name, source.OmittedBindings))
		}
		switch source.Status {
		case "ok":
			source.Complete = true
		case "unbound":
			source.Complete = true
			report.Warnings = append(report.Warnings,
				"source "+name+" has no service binding; no source request was made")
		default:
			source.Complete = false
			report.Complete = false
		}
		report.Sources = append(report.Sources, *source)
	}
	return report
}

func (a *App) doctorBinding(ctx context.Context, mu *sync.Mutex, byName map[string]*DoctorSource, sourceName, serviceID string, collect func(context.Context) (evidence.SourceOutcome, []evidence.Item)) {
	outcome, _ := collect(ctx)
	mu.Lock()
	defer mu.Unlock()
	source := byName[sourceName]
	if source == nil {
		return
	}
	binding := DoctorBinding{
		ServiceID: serviceID,
		Status:    outcome.Status,
		Reason:    outcome.Reason,
		Complete:  outcome.Complete,
		ItemCount: outcome.ItemCount,
	}
	source.Bindings = append(source.Bindings, binding)
	if outcome.Status != "ok" || !outcome.Complete {
		source.Error = outcome.Error
		if source.Reason == "" {
			source.Reason = outcome.Reason
		}
	}
	source.Status = worseDoctorStatus(source.Status, doctorStatusForOutcome(outcome))
}

func doctorStatusForOutcome(outcome evidence.SourceOutcome) string {
	switch outcome.Status {
	case "ok":
		if outcome.Complete {
			return "ok"
		}
		return "partial"
	case "timeout":
		return "timeout"
	case "canceled":
		return "canceled"
	default:
		return "error"
	}
}

func worseDoctorStatus(current, next string) string {
	rank := map[string]int{
		"unbound":  0,
		"ok":       1,
		"partial":  2,
		"canceled": 3,
		"timeout":  4,
		"error":    5,
	}
	if rank[next] > rank[current] {
		return next
	}
	return current
}

func adapterCapabilities(kind string) []string {
	switch kind {
	case "gatus":
		return []string{"service_status", "incident_context", "historical_results"}
	case "docker":
		return []string{"service_status", "incident_context", "current_snapshot"}
	case "loki":
		return []string{"search_logs", "incident_context", "historical_results"}
	case "healthchecks":
		return []string{"service_status", "failed_crons", "current_state"}
	case "beszel":
		return []string{"service_status", "incident_context", "current_snapshot"}
	case "ntfy":
		return []string{"incident_context", "stored_notifications"}
	default:
		return []string{}
	}
}

// DoctorSummary gives one short human-readable line for a report.
func DoctorSummary(report DoctorReport) string {
	counts := map[string]int{}
	for _, source := range report.Sources {
		counts[source.Status]++
	}
	parts := make([]string, 0, 6)
	for _, status := range []string{"ok", "partial", "unbound", "canceled", "timeout", "error"} {
		if counts[status] > 0 {
			parts = append(parts, status+"="+strconv.Itoa(counts[status]))
		}
	}
	return "doctor: " + strconv.Itoa(len(report.Sources)) + " source(s), " + strings.Join(parts, " ")
}
