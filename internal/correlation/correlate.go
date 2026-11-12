// Package correlation makes timelines with stable evidence order. It does not identify causal relations.
package correlation

import (
	"fmt"
	"strings"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

// BuildTimeline puts evidence in order and gives source-derived coverage.
func BuildTimeline(serviceID string, start, end time.Time, items []evidence.Item, sources []evidence.SourceOutcome, maxItems int) evidence.Bundle {
	now := time.Now().UTC()
	cp := append([]evidence.Item(nil), items...)
	var timelineTruncated bool
	cp, timelineTruncated = evidence.KeepNewest(cp, maxItems)

	bundle := evidence.Bundle{
		ServiceID:          serviceID,
		RequestedStart:     start.UTC(),
		RequestedEnd:       end.UTC(),
		EffectiveStart:     start.UTC(),
		EffectiveEnd:       end.UTC(),
		Items:              cp,
		Sources:            sources,
		SelectionTruncated: timelineTruncated,
		OmittedItems:       len(items) - len(cp),
		Truncated:          timelineTruncated || sourcesTruncated(sources),
		RetrievedAt:        now,
	}
	RebuildDerived(&bundle, maxItems)
	return bundle
}

// RebuildDerived recomputes item counts and item warnings from the retained
// items. Source coverage warnings stay present when related items are omitted.
func RebuildDerived(bundle *evidence.Bundle, maxItems int) {
	var warnings []string
	for _, s := range bundle.Sources {
		if s.Truncated {
			warnings = append(warnings, fmt.Sprintf(
				"source %s (%s): truncated upstream=%v local=%v",
				s.SourceName, s.Kind, s.UpstreamTruncated, s.LocalTruncated,
			))
		}
		if s.Discarded > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"source %s (%s): %d unusable source record(s)", s.SourceName, s.Kind, s.Discarded,
			))
		}
		if s.UnknownTimeRecords > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"source %s (%s): %d record(s) without a usable source time", s.SourceName, s.Kind, s.UnknownTimeRecords,
			))
		}
		switch s.Status {
		case "error", "timeout", "canceled":
			warnings = append(warnings, fmt.Sprintf(
				"source %s (%s): %s (%s)", s.SourceName, s.Kind, s.Status, s.Reason,
			))
		}
		if s.Status == "ok" && !s.Complete && s.Reason != "" && s.Reason != "truncated" &&
			s.Reason != "partial_invalid_records" {
			warnings = append(warnings, fmt.Sprintf(
				"source %s (%s): incomplete (%s)", s.SourceName, s.Kind, s.Reason,
			))
		}
	}
	if bundle.SelectionTruncated {
		if maxItems > 0 {
			warnings = append(warnings, fmt.Sprintf("timeline truncated to %d items", maxItems))
		} else {
			warnings = append(warnings, "timeline truncated")
		}
	}
	if bundle.SelectionTruncated && bundle.OmittedItems > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"selection omitted %d retrieved item(s)", bundle.OmittedItems))
	}
	if bundle.ByteBudgetTruncated {
		// The warning must not quote byte_size. That field is measured from the
		// payload that contains this warning, so a quoted value would be stale
		// and would contradict the final fixed-point accounting.
		warnings = append(warnings, fmt.Sprintf(
			"byte budget omitted retrieved evidence; budget=%d", bundle.ByteBudget))
	}

	// Flag critical or error evidence that remains in the final result.
	for _, it := range bundle.Items {
		if it.Freshness == evidence.FreshnessStale && (it.Severity == evidence.SeverityError || it.Severity == evidence.SeverityCritical) {
			warnings = append(warnings, fmt.Sprintf("stale %s evidence from %s (observed %s)", it.Severity, it.Source, it.ObservedAt.Format(time.RFC3339)))
		}
	}

	bundle.Warnings = boundWarnings(unique(warnings))
	bundle.FactualSummary = factualSummary(bundle.ServiceID, bundle.RequestedStart, bundle.RequestedEnd, bundle.Items, bundle.Sources)
}

// boundWarnings keeps source failures visible while it bounds the detail.
func boundWarnings(warnings []string) []string {
	const maxWarnings = 40
	const maxBytes = 8 << 10
	if len(warnings) == 0 {
		return nil
	}
	out := make([]string, 0, maxWarnings)
	total := 0
	for index, warning := range warnings {
		if index >= maxWarnings || total+len(warning) > maxBytes {
			out = append(out, fmt.Sprintf("%d warning(s) omitted by output bound", len(warnings)-index))
			return out
		}
		out = append(out, warning)
		total += len(warning)
	}
	return out
}

func factualSummary(serviceID string, start, end time.Time, items []evidence.Item, sources []evidence.SourceOutcome) string {
	ok, failed, incomplete := 0, 0, 0
	for _, source := range sources {
		switch source.Status {
		case "ok":
			ok++
			if !source.Complete {
				incomplete++
			}
		case "error", "timeout", "canceled":
			failed++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Service %s: %d returned evidence item(s) between %s and %s UTC.",
		serviceID, len(items), start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, " Sources with status ok=%d. Sources with status error, timeout, or canceled=%d.",
		ok, failed)
	fmt.Fprintf(&b, " Sources with incomplete coverage=%d.", incomplete)
	b.WriteString(" Item counts describe returned evidence, not total upstream records.")

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
		b.WriteString(" The collected set has no error-severity items.")
		b.WriteString(" If there is no evidence, this does not prove that no event occurred.")
		b.WriteString(" The timeline uses observed_at order. Correlation does not identify a root cause.")
		return b.String()
	}
	b.WriteString(" These are the counts by source with warning/error severity:")
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
	b.WriteString(" The timeline uses observed_at order. Correlation does not identify a root cause.")
	return b.String()
}

func sourcesTruncated(sources []evidence.SourceOutcome) bool {
	for _, source := range sources {
		if source.Truncated {
			return true
		}
	}
	return false
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
