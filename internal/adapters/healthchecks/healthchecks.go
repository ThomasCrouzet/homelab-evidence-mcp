// Package healthchecks implements the read-only Management API v3 client.
package healthchecks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
)

// Client uses a read-only key in the configured header.
type Client struct {
	HTTP     *httpx.LockedClient
	DestName string
	Headers  map[string]string // holds authentication; never log values
	Redact   *redaction.Engine
	Now      func() time.Time
}

// check holds only the Healthchecks fields used.
type check struct {
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Tags     string `json:"tags"`
	Grace    int    `json:"grace"`
	Status   string `json:"status"`
	LastPing string `json:"last_ping"`
	NextPing string `json:"next_ping"`
	UUID     string `json:"uuid"` // used only for internal filtering
	// Fields intentionally omitted: ping_url, update_url, pause_url, and badge_url.
}

type listResponse struct {
	Checks *[]check `json:"checks"`
}

// Filter selects checks associated with a service.
type Filter struct {
	Name   string
	Tags   []string
	UUID   string
	Status string // optional status filter
}

// FilterFromRef builds a filter from configuration.
func FilterFromRef(ref *config.HealthchecksRef) Filter {
	if ref == nil {
		return Filter{}
	}
	return Filter{Name: ref.CheckName, Tags: ref.CheckTags, UUID: ref.CheckUUID, Status: ref.StatusFilter}
}

// Status returns the current state of matching checks.
func (c *Client) Status(ctx context.Context, serviceID string, f Filter) ([]evidence.Item, error) {
	retrievedAt := c.now()
	checks, observedAt, err := c.list(ctx, retrievedAt)
	if err != nil {
		return nil, err
	}
	matched := filterChecks(checks, f)
	out := make([]evidence.Item, 0, len(matched))
	for _, ch := range matched {
		out = append(out, c.itemFrom(serviceID, ch, observedAt, retrievedAt))
	}
	return out, nil
}

// FailedInWindow returns checks that are currently down, grace, or paused.
// The list route cannot prove a past incident or recovery.
func (c *Client) FailedInWindow(ctx context.Context, serviceID string, f Filter, start, end time.Time) ([]evidence.Item, error) {
	retrievedAt := c.now()
	checks, observedAt, err := c.list(ctx, retrievedAt)
	if err != nil {
		return nil, err
	}
	var matched []check
	if serviceID != "" || f.Name != "" || f.UUID != "" || len(f.Tags) > 0 {
		matched = filterChecks(checks, f)
	} else {
		matched = checks
	}
	// Tolerate collection time slightly past an end bound computed just before
	// the network call, without projecting current state into a historical window.
	if observedAt.Before(start) || observedAt.After(end.Add(30*time.Second)) {
		return []evidence.Item{}, nil
	}
	var out []evidence.Item
	for _, ch := range matched {
		st := strings.ToLower(strings.TrimSpace(ch.Status))
		interesting := st == "down" || st == "grace" || st == "paused"
		if !interesting {
			continue
		}
		item := c.itemFrom(serviceID, ch, observedAt, retrievedAt)
		item.WindowStart = timePtr(start)
		item.WindowEnd = timePtr(end)
		out = append(out, item)
	}
	return out, nil
}

func (c *Client) list(ctx context.Context, fallback time.Time) ([]check, time.Time, error) {
	if len(c.Headers) == 0 {
		return nil, time.Time{}, fmt.Errorf("healthchecks token not configured")
	}
	// Management API v3 exposes the list at GET /api/v3/checks/.
	path := "/api/v3/checks/"
	// LockedClient.Get closes the body before returning the response.
	resp, body, err := c.HTTP.Get(ctx, c.DestName, path, c.Headers) //nolint:bodyclose
	if err != nil {
		return nil, time.Time{}, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, time.Time{}, fmt.Errorf("healthchecks authentication failed")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, time.Time{}, fmt.Errorf("healthchecks HTTP %d", resp.StatusCode)
	}
	observedAt := httpx.CollectionTime(resp, fallback)
	var lr listResponse
	if err := json.Unmarshal(body, &lr); err == nil && lr.Checks != nil {
		return *lr.Checks, observedAt, nil
	}
	// Some deployments return a bare array.
	var arr []check
	if err := json.Unmarshal(body, &arr); err != nil || arr == nil {
		return nil, time.Time{}, fmt.Errorf("healthchecks decode: expected checks array")
	}
	return arr, observedAt, nil
}

func filterChecks(checks []check, f Filter) []check {
	var out []check
	for _, ch := range checks {
		if f.UUID != "" && !strings.EqualFold(ch.UUID, f.UUID) {
			continue
		}
		if f.Name != "" && !strings.EqualFold(ch.Name, f.Name) && !strings.EqualFold(ch.Slug, f.Name) {
			continue
		}
		if len(f.Tags) > 0 && !tagsMatch(ch.Tags, f.Tags) {
			continue
		}
		if f.Status != "" && !strings.EqualFold(strings.TrimSpace(ch.Status), f.Status) {
			continue
		}
		// An empty filter matches nothing; global failed_crons handles that case.
		if f.UUID == "" && f.Name == "" && len(f.Tags) == 0 && f.Status == "" {
			continue
		}
		out = append(out, ch)
	}
	return out
}

func tagsMatch(tagStr string, want []string) bool {
	have := strings.Fields(tagStr)
	set := map[string]struct{}{}
	for _, t := range have {
		set[strings.ToLower(t)] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[strings.ToLower(w)]; !ok {
			return false
		}
	}
	return true
}

func (c *Client) itemFrom(serviceID string, ch check, observedAt, retrievedAt time.Time) evidence.Item {
	st := strings.ToLower(strings.TrimSpace(ch.Status))
	var sev evidence.Severity
	rawSummary := fmt.Sprintf("healthchecks %q status=%s", ch.Name, st)
	switch st {
	case "up":
		sev = evidence.SeverityInfo
	case "grace":
		sev = evidence.SeverityWarning
		rawSummary = fmt.Sprintf("healthchecks %q is in grace", ch.Name)
	case "down":
		sev = evidence.SeverityError
		rawSummary = fmt.Sprintf("healthchecks %q is down", ch.Name)
	case "paused", "new":
		sev = evidence.SeverityWarning
	default:
		sev = evidence.SeverityUnknown
	}

	name, nameRedactions, nameCut := redaction.ApplyAndTruncate(c.Redact, ch.Name, 256)
	slug, slugRedactions, slugCut := redaction.ApplyAndTruncate(c.Redact, ch.Slug, 256)
	status, statusRedactions, statusCut := redaction.ApplyAndTruncate(c.Redact, st, 64)
	sourceID, sourceRedactions, sourceCut := redaction.ApplyAndTruncate(c.Redact, safeSourceID(ch), 256)
	redactions := nameRedactions + slugRedactions + statusRedactions + sourceRedactions
	truncated := nameCut || slugCut || statusCut || sourceCut
	attrs := map[string]any{
		"name":   name,
		"slug":   slug,
		"status": status,
		"grace":  ch.Grace,
	}
	if ch.Tags != "" {
		rawTags := strings.Fields(ch.Tags)
		tagCount := len(rawTags)
		if tagCount > 32 {
			tagCount = 32
			truncated = true
		}
		tags := make([]string, 0, tagCount)
		for _, tag := range rawTags[:tagCount] {
			clean, n, cut := redaction.ApplyAndTruncate(c.Redact, tag, 128)
			redactions += n
			truncated = truncated || cut
			tags = append(tags, clean)
		}
		attrs["tags"] = tags
	}
	// Never expose the UUID or ping URLs.

	if lp, ok := parseTS(ch.LastPing); ok {
		attrs["last_ping"] = lp.Format(time.RFC3339)
	}
	if np, ok := parseTS(ch.NextPing); ok {
		attrs["next_ping"] = np.Format(time.RFC3339)
	}

	sum, summaryRedactions, summaryCut := redaction.ApplyAndTruncate(c.Redact, rawSummary, 512)
	redactions += summaryRedactions
	truncated = truncated || summaryCut
	return evidence.Item{
		ID:                evidence.NewOpaqueID("ev"),
		ServiceID:         serviceID,
		Source:            evidence.SourceHealthchecks,
		SourceID:          sourceID,
		Kind:              evidence.KindCronCheck,
		ObservedAt:        observedAt,
		RetrievedAt:       retrievedAt,
		Summary:           sum,
		Severity:          sev,
		Attributes:        attrs,
		Truncated:         truncated,
		RedactionsApplied: redactions,
		Freshness:         evidence.ComputeFreshness(observedAt, retrievedAt),
	}
}

func safeSourceID(ch check) string {
	if ch.Slug != "" {
		return ch.Slug
	}
	if ch.Name != "" {
		return ch.Name
	}
	return "check"
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func parseTS(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" {
		return time.Time{}, false
	}
	for _, f := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func timePtr(t time.Time) *time.Time {
	t = t.UTC()
	return &t
}
