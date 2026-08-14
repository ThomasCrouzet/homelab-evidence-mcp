// Package evidence defines the evidence model shared by adapters.
package evidence

import (
	"sort"
	"time"
)

// Kind is the closed vocabulary of evidence types.
type Kind string

const (
	KindEndpointCheck Kind = "endpoint_check"
	KindContainer     Kind = "container_state"
	KindLogLine       Kind = "log_line"
	KindCronCheck     Kind = "cron_check"
	KindMetricSample  Kind = "metric_sample"
	KindNotification  Kind = "notification"
)

// Severity is a closed vocabulary without causal interpretation.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityError    Severity = "error"
	SeverityCritical Severity = "critical"
	SeverityUnknown  Severity = "unknown"
)

// Freshness describes how old an evidence item is at collection time.
type Freshness string

const (
	FreshnessLive    Freshness = "live"
	FreshnessRecent  Freshness = "recent"
	FreshnessStale   Freshness = "stale"
	FreshnessUnknown Freshness = "unknown"
	FreshnessCached  Freshness = "cached"
	FreshnessMissing Freshness = "missing"
)

// SourceKind identifies an adapter family.
type SourceKind string

const (
	SourceGatus        SourceKind = "gatus"
	SourceDocker       SourceKind = "docker"
	SourceLoki         SourceKind = "loki"
	SourceHealthchecks SourceKind = "healthchecks"
	SourceBeszel       SourceKind = "beszel"
	SourceNtfy         SourceKind = "ntfy"
)

// Item represents bounded, attributed, and explicitly truncated evidence.
type Item struct {
	ID                string         `json:"id"`
	ServiceID         string         `json:"service_id,omitempty"`
	Source            SourceKind     `json:"source"`
	SourceID          string         `json:"source_id"`
	Kind              Kind           `json:"kind"`
	ObservedAt        time.Time      `json:"observed_at"`
	RetrievedAt       time.Time      `json:"retrieved_at"`
	WindowStart       *time.Time     `json:"window_start,omitempty"`
	WindowEnd         *time.Time     `json:"window_end,omitempty"`
	Summary           string         `json:"summary"`
	Severity          Severity       `json:"severity"`
	Attributes        map[string]any `json:"attributes,omitempty"`
	Truncated         bool           `json:"truncated"`
	RedactionsApplied int            `json:"redactions_applied"`
	Freshness         Freshness      `json:"freshness"`
}

// SourceOutcome describes the result of querying a source.
type SourceOutcome struct {
	SourceName string     `json:"source_name"`
	Kind       SourceKind `json:"kind"`
	Status     string     `json:"status"` // ok | error | skipped | timeout | absent
	Error      string     `json:"error,omitempty"`
	ItemCount  int        `json:"item_count"`
	Truncated  bool       `json:"truncated"`
	DurationMS int64      `json:"duration_ms"`
}

// Bundle wraps a multi-source response.
type Bundle struct {
	ServiceID      string          `json:"service_id,omitempty"`
	RequestedStart time.Time       `json:"requested_start,omitempty"`
	RequestedEnd   time.Time       `json:"requested_end,omitempty"`
	EffectiveStart time.Time       `json:"effective_start,omitempty"`
	EffectiveEnd   time.Time       `json:"effective_end,omitempty"`
	Items          []Item          `json:"items"`
	Sources        []SourceOutcome `json:"sources"`
	Truncated      bool            `json:"truncated"`
	Warnings       []string        `json:"warnings,omitempty"`
	FactualSummary string          `json:"factual_summary,omitempty"`
	RetrievedAt    time.Time       `json:"retrieved_at"`
}

// KeepNewest sorts items and retains the newest max entries.
// The returned slice stays in SortItems order (oldest first among keepers).
func KeepNewest(items []Item, max int) ([]Item, bool) {
	if max <= 0 || len(items) <= max {
		SortItems(items)
		return items, false
	}
	SortItems(items)
	return items[len(items)-max:], true
}

// SortItems sorts evidence by time, source, source identity, then id.
func SortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if !a.ObservedAt.Equal(b.ObservedAt) {
			return a.ObservedAt.Before(b.ObservedAt)
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.SourceID != b.SourceID {
			return a.SourceID < b.SourceID
		}
		return a.ID < b.ID
	})
}

// SortOutcomes sorts outcomes by kind then name.
func SortOutcomes(outcomes []SourceOutcome) {
	sort.SliceStable(outcomes, func(i, j int) bool {
		if outcomes[i].Kind != outcomes[j].Kind {
			return outcomes[i].Kind < outcomes[j].Kind
		}
		return outcomes[i].SourceName < outcomes[j].SourceName
	})
}

// ComputeFreshness classifies age relative to now.
func ComputeFreshness(observed, now time.Time) Freshness {
	if observed.IsZero() {
		return FreshnessUnknown
	}
	age := now.Sub(observed)
	if age < 0 {
		if -age < 2*time.Minute {
			return FreshnessLive
		}
		return FreshnessUnknown
	}
	switch {
	case age <= 2*time.Minute:
		return FreshnessLive
	case age <= 15*time.Minute:
		return FreshnessRecent
	default:
		return FreshnessStale
	}
}
