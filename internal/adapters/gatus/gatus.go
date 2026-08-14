// Package gatus implements the read-only Gatus adapter.
package gatus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
)

// Client queries the Gatus status route and compatible variants.
type Client struct {
	HTTP     *httpx.LockedClient
	DestName string
	Headers  map[string]string
	Redact   *redaction.Engine
	Now      func() time.Time
}

// endpointStatus holds only the Gatus fields used.
type endpointStatus struct {
	Name    string           `json:"name"`
	Group   string           `json:"group"`
	Key     string           `json:"key"`
	Results []endpointResult `json:"results"`
}

type endpointResult struct {
	Status    int          `json:"status"`
	Success   bool         `json:"success"`
	Timestamp string       `json:"timestamp"`
	Duration  flexDuration `json:"duration"`
	Errors    []string     `json:"errors"`
}

// flexDuration accepts Gatus nanosecond numbers and string durations.
type flexDuration string

func (d *flexDuration) UnmarshalJSON(raw []byte) error {
	if string(raw) == "null" || len(raw) == 0 {
		*d = ""
		return nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		*d = flexDuration(s)
		return nil
	}
	var nanos int64
	if err := json.Unmarshal(raw, &nanos); err != nil {
		return err
	}
	if nanos < 0 {
		*d = ""
		return nil
	}
	*d = flexDuration(time.Duration(nanos).String())
	return nil
}

// Status fetches the latest observation for endpointKey.
func (c *Client) Status(ctx context.Context, serviceID, endpointKey string) (evidence.Item, error) {
	retrievedAt := c.now()
	list, snapshotAt, err := c.fetchAll(ctx, retrievedAt)
	if err != nil {
		return evidence.Item{}, err
	}
	ep, ok := findEndpoint(list, endpointKey)
	if !ok {
		return c.missingItem(serviceID, endpointKey, snapshotAt, retrievedAt), nil
	}
	return c.itemFromEndpoint(serviceID, ep, snapshotAt, retrievedAt), nil
}

func (c *Client) missingItem(serviceID, endpointKey string, observedAt, retrievedAt time.Time) evidence.Item {
	cleanKey, redactions, truncated := redaction.ApplyAndTruncate(c.Redact, endpointKey, 256)
	return evidence.Item{
		ID:                evidence.NewOpaqueID("ev"),
		ServiceID:         serviceID,
		Source:            evidence.SourceGatus,
		SourceID:          cleanKey,
		Kind:              evidence.KindEndpointCheck,
		ObservedAt:        observedAt,
		RetrievedAt:       retrievedAt,
		Summary:           "gatus endpoint not found in status list",
		Severity:          evidence.SeverityUnknown,
		Attributes:        map[string]any{"endpoint_key": cleanKey, "found": false},
		Truncated:         truncated,
		RedactionsApplied: redactions,
		Freshness:         evidence.FreshnessMissing,
	}
}

// EvidenceInWindow returns results between start and end.
func (c *Client) EvidenceInWindow(ctx context.Context, serviceID, endpointKey string, start, end time.Time, max int) ([]evidence.Item, bool, error) {
	retrievedAt := c.now()
	list, snapshotAt, err := c.fetchAll(ctx, retrievedAt)
	if err != nil {
		return nil, false, err
	}
	ep, ok := findEndpoint(list, endpointKey)
	if !ok {
		item := c.missingItem(serviceID, endpointKey, snapshotAt, retrievedAt)
		item.WindowStart = timePtr(start)
		item.WindowEnd = timePtr(end)
		return []evidence.Item{item}, false, nil
	}
	if max <= 0 {
		max = 20
	}
	var items []evidence.Item
	// Gatus may return unsorted history; order by timestamp.
	type pair struct {
		r  endpointResult
		ts time.Time
	}
	var pairs []pair
	for _, r := range ep.Results {
		ts, ok := parseTS(r.Timestamp)
		if !ok {
			continue
		}
		if ts.Before(start) || ts.After(end) {
			continue
		}
		pairs = append(pairs, pair{r, ts})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		return pairs[i].ts.Before(pairs[j].ts)
	})
	truncated := len(pairs) > max
	if truncated {
		pairs = pairs[len(pairs)-max:]
	}
	for _, p := range pairs {
		item := c.itemFromResult(serviceID, ep, p.r, p.ts, retrievedAt)
		item.WindowStart = timePtr(start)
		item.WindowEnd = timePtr(end)
		items = append(items, item)
	}
	return items, truncated, nil
}

func (c *Client) fetchAll(ctx context.Context, fallback time.Time) ([]endpointStatus, time.Time, error) {
	// Try the primary route, then the variant only after a 404.
	paths := []string{"/api/v1/endpoints/statuses", "/api/v1/endpoints/statuses/"}
	var lastErr error
	for _, p := range paths {
		// LockedClient.Get closes the body before returning the response.
		resp, body, err := c.HTTP.Get(ctx, c.DestName, p, c.Headers) //nolint:bodyclose
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusNotFound {
			lastErr = fmt.Errorf("gatus returned 404 for statuses")
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, time.Time{}, fmt.Errorf("gatus HTTP %d", resp.StatusCode)
		}
		var list []endpointStatus
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, time.Time{}, fmt.Errorf("gatus decode: invalid JSON")
		}
		if list == nil {
			return nil, time.Time{}, fmt.Errorf("gatus decode: expected status array")
		}
		return list, httpx.CollectionTime(resp, fallback), nil
	}
	if lastErr != nil {
		return nil, time.Time{}, lastErr
	}
	return nil, time.Time{}, fmt.Errorf("gatus: no statuses endpoint available")
}

func findEndpoint(list []endpointStatus, key string) (endpointStatus, bool) {
	key = strings.TrimSpace(key)
	for _, ep := range list {
		if ep.Key == key {
			return ep, true
		}
		// Also accept the name or group_name composition used by some versions.
		if ep.Name == key {
			return ep, true
		}
		if ep.Group != "" && ep.Group+"_"+ep.Name == key {
			return ep, true
		}
	}
	return endpointStatus{}, false
}

func (c *Client) itemFromEndpoint(serviceID string, ep endpointStatus, snapshotAt, retrievedAt time.Time) evidence.Item {
	if len(ep.Results) == 0 {
		key, keyRedactions, keyCut := redaction.ApplyAndTruncate(c.Redact, ep.Key, 256)
		name, nameRedactions, nameCut := redaction.ApplyAndTruncate(c.Redact, ep.Name, 256)
		group, groupRedactions, groupCut := redaction.ApplyAndTruncate(c.Redact, ep.Group, 256)
		summary, summaryRedactions, summaryCut := redaction.ApplyAndTruncate(
			c.Redact,
			fmt.Sprintf("gatus endpoint %s has empty results", safeName(ep)),
			512,
		)
		return evidence.Item{
			ID:          evidence.NewOpaqueID("ev"),
			ServiceID:   serviceID,
			Source:      evidence.SourceGatus,
			SourceID:    key,
			Kind:        evidence.KindEndpointCheck,
			ObservedAt:  snapshotAt,
			RetrievedAt: retrievedAt,
			Summary:     summary,
			Severity:    evidence.SeverityUnknown,
			Attributes: map[string]any{
				"endpoint_key": key,
				"name":         name,
				"group":        group,
				"found":        true,
				"results":      0,
			},
			Truncated:         keyCut || nameCut || groupCut || summaryCut,
			RedactionsApplied: keyRedactions + nameRedactions + groupRedactions + summaryRedactions,
			Freshness:         evidence.FreshnessUnknown,
		}
	}
	// Pick the latest valid timestamp, without relying on array order.
	// Without a valid date, keep the first result at collection time.
	best := ep.Results[0]
	bestTS := snapshotAt
	foundValid := false
	for _, r := range ep.Results {
		ts, ok := parseTS(r.Timestamp)
		if !ok {
			continue
		}
		if !foundValid || ts.After(bestTS) {
			best = r
			bestTS = ts
			foundValid = true
		}
	}
	return c.itemFromResult(serviceID, ep, best, bestTS, retrievedAt)
}

func (c *Client) itemFromResult(serviceID string, ep endpointStatus, r endpointResult, ts, now time.Time) evidence.Item {
	sev := evidence.SeverityInfo
	rawSummary := fmt.Sprintf("gatus %s success status=%d", safeName(ep), r.Status)
	if !r.Success {
		sev = evidence.SeverityError
		rawSummary = fmt.Sprintf("gatus %s failure status=%d", safeName(ep), r.Status)
	}
	key, keyRedactions, keyCut := redaction.ApplyAndTruncate(c.Redact, ep.Key, 256)
	name, nameRedactions, nameCut := redaction.ApplyAndTruncate(c.Redact, ep.Name, 256)
	group, groupRedactions, groupCut := redaction.ApplyAndTruncate(c.Redact, ep.Group, 256)
	attrs := map[string]any{
		"endpoint_key": key,
		"name":         name,
		"group":        group,
		"success":      r.Success,
		"http_status":  r.Status,
		"found":        true,
	}
	redactions := keyRedactions + nameRedactions + groupRedactions
	truncated := keyCut || nameCut || groupCut
	if r.Duration != "" {
		duration, n, cut := redaction.ApplyAndTruncate(c.Redact, string(r.Duration), 64)
		redactions += n
		truncated = truncated || cut
		attrs["duration"] = duration
	}
	if len(r.Errors) > 0 {
		errorCount := len(r.Errors)
		if errorCount > 10 {
			errorCount = 10
			truncated = true
		}
		cleaned := make([]string, 0, errorCount)
		for _, e := range r.Errors[:errorCount] {
			s, n, cut := redaction.ApplyAndTruncate(c.Redact, e, 256)
			redactions += n
			truncated = truncated || cut
			cleaned = append(cleaned, s)
		}
		attrs["errors"] = cleaned
	}
	sum, rn, sumCut := redaction.ApplyAndTruncate(c.Redact, rawSummary, 512)
	redactions += rn
	truncated = truncated || sumCut
	return evidence.Item{
		ID:                evidence.NewOpaqueID("ev"),
		ServiceID:         serviceID,
		Source:            evidence.SourceGatus,
		SourceID:          key,
		Kind:              evidence.KindEndpointCheck,
		ObservedAt:        ts.UTC(),
		RetrievedAt:       now,
		Summary:           sum,
		Severity:          sev,
		Attributes:        attrs,
		Truncated:         truncated,
		RedactionsApplied: redactions,
		Freshness:         evidence.ComputeFreshness(ts, now),
	}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func parseTS(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func safeName(ep endpointStatus) string {
	if ep.Group != "" {
		return ep.Group + "/" + ep.Name
	}
	if ep.Name != "" {
		return ep.Name
	}
	return ep.Key
}

func timePtr(t time.Time) *time.Time {
	t = t.UTC()
	return &t
}
