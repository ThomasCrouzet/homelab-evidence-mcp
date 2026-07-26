// Package correlation construit des chronologies déterministes sans causalité.
package correlation

import (
	"fmt"
	"strings"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

// BuildTimeline trie les preuves et produit un résumé strictement factuel.
func BuildTimeline(serviceID string, start, end time.Time, items []evidence.Item, sources []evidence.SourceOutcome, maxItems int) evidence.Bundle {
	now := time.Now().UTC()
	cp := append([]evidence.Item(nil), items...)
	evidence.SortItems(cp)

	timelineTruncated := false
	if maxItems > 0 && len(cp) > maxItems {
		cp = cp[:maxItems]
		timelineTruncated = true
	}
	truncated := timelineTruncated

	var warnings []string
	failed := 0
	ok := 0
	for _, s := range sources {
		if s.Truncated {
			truncated = true
			warnings = append(warnings, fmt.Sprintf("source %s (%s): truncated", s.SourceName, s.Kind))
		}
		switch s.Status {
		case "ok":
			ok++
		case "error", "timeout":
			failed++
			warnings = append(warnings, fmt.Sprintf("source %s (%s): %s", s.SourceName, s.Kind, s.Status))
		case "absent", "skipped":
			// Ce statut ne représente pas un échec.
		}
	}
	if timelineTruncated {
		warnings = append(warnings, fmt.Sprintf("timeline truncated to %d items", maxItems))
	}

	// Signaler les preuves critiques ou en erreur devenues anciennes.
	for _, it := range cp {
		if it.Freshness == evidence.FreshnessStale && (it.Severity == evidence.SeverityError || it.Severity == evidence.SeverityCritical) {
			warnings = append(warnings, fmt.Sprintf("stale %s evidence from %s (observed %s)", it.Severity, it.Source, it.ObservedAt.Format(time.RFC3339)))
		}
	}

	summary := factualSummary(serviceID, start, end, cp, ok, failed)

	return evidence.Bundle{
		ServiceID:      serviceID,
		RequestedStart: start.UTC(),
		RequestedEnd:   end.UTC(),
		EffectiveStart: start.UTC(),
		EffectiveEnd:   end.UTC(),
		Items:          cp,
		Sources:        sources,
		Truncated:      truncated,
		Warnings:       unique(warnings),
		FactualSummary: summary,
		RetrievedAt:    now,
	}
}

func factualSummary(serviceID string, start, end time.Time, items []evidence.Item, sourcesOK, sourcesFailed int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Service %s: %d evidence item(s) between %s and %s UTC.",
		serviceID, len(items), start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, " Sources ok=%d failed=%d.", sourcesOK, sourcesFailed)

	var gatusFail, dockerBad, logErr, cronDown, beszelBad, ntfyBad int
	for _, it := range items {
		switch it.Source {
		case evidence.SourceGatus:
			if it.Severity == evidence.SeverityError || it.Severity == evidence.SeverityCritical {
				gatusFail++
			}
		case evidence.SourceDocker:
			if it.Severity == evidence.SeverityError || it.Severity == evidence.SeverityWarning {
				dockerBad++
			}
		case evidence.SourceLoki:
			if it.Severity == evidence.SeverityError || it.Severity == evidence.SeverityCritical {
				logErr++
			}
		case evidence.SourceHealthchecks:
			if it.Severity == evidence.SeverityError || it.Severity == evidence.SeverityWarning {
				cronDown++
			}
		case evidence.SourceBeszel:
			if it.Severity == evidence.SeverityError || it.Severity == evidence.SeverityWarning {
				beszelBad++
			}
		case evidence.SourceNtfy:
			if it.Severity == evidence.SeverityError || it.Severity == evidence.SeverityCritical || it.Severity == evidence.SeverityWarning {
				ntfyBad++
			}
		}
	}
	if gatusFail+dockerBad+logErr+cronDown+beszelBad+ntfyBad == 0 {
		b.WriteString(" No error-severity items in the collected set.")
		b.WriteString(" Absence of evidence is not evidence of absence.")
		return b.String()
	}
	b.WriteString(" Counts by source with warning/error severity:")
	if gatusFail > 0 {
		fmt.Fprintf(&b, " gatus=%d", gatusFail)
	}
	if dockerBad > 0 {
		fmt.Fprintf(&b, " docker=%d", dockerBad)
	}
	if logErr > 0 {
		fmt.Fprintf(&b, " loki=%d", logErr)
	}
	if cronDown > 0 {
		fmt.Fprintf(&b, " healthchecks=%d", cronDown)
	}
	if beszelBad > 0 {
		fmt.Fprintf(&b, " beszel=%d", beszelBad)
	}
	if ntfyBad > 0 {
		fmt.Fprintf(&b, " ntfy=%d", ntfyBad)
	}
	b.WriteString(".")
	b.WriteString(" Timeline is ordered by observed_at; correlation does not establish root cause.")
	return b.String()
}

func unique(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
