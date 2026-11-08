// Package gatus contains the read-only Gatus adapter.
package gatus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
)

// ErrEndpointAmbiguous reports a selector that identifies more than one endpoint.
// The error omits the configured key.
var ErrEndpointAmbiguous = errors.New("gatus endpoint selection is ambiguous")

// Client gets data from the Gatus status route and compatible variants.
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
	Status    *int         `json:"status"`
	Success   *bool        `json:"success"`
	Timestamp string       `json:"timestamp"`
	Duration  flexDuration `json:"duration"`
	Errors    []string     `json:"errors"`
}

// WindowResult gives history items and the coverage facts around them.
// Matched is false when the configured endpoint is absent from the status list.
type WindowResult struct {
	Items     []evidence.Item
	Matched   bool
	Truncated bool
	// Discarded counts records with an unusable timestamp or no success field.
	Discarded int
}

// flexDuration accepts Gatus nanosecond numbers and duration strings.
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

// Status gets the observation with the maximum timestamp for endpointKey.
func (c *Client) Status(ctx context.Context, serviceID, endpointKey string) (evidence.Item, error) {
	retrievedAt := c.now()
	list, snapshotAt, err := c.fetchAll(ctx, retrievedAt)
	if err != nil {
		return evidence.Item{}, err
	}
	ep, ok, err := findEndpoint(list, endpointKey)
	if err != nil {
		return evidence.Item{}, err
	}
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

// EvidenceInWindow gives results between start and end.
func (c *Client) EvidenceInWindow(ctx context.Context, serviceID, endpointKey string, start, end time.Time, max int) (WindowResult, error) {
	result := WindowResult{}
	retrievedAt := c.now()
	list, snapshotAt, err := c.fetchAll(ctx, retrievedAt)
	if err != nil {
		return result, err
	}
	ep, ok, err := findEndpoint(list, endpointKey)
	if err != nil {
		return result, err
	}
	if !ok {
		// Keep identity explicit in every window. A missing endpoint is not a
		// matched empty history. The caller reports unmatched_binding.
		if snapshotAt.Before(start) || snapshotAt.After(end.Add(30*time.Second)) {
			return result, nil
		}
		// A window that includes the current collection can carry a missing
		// snapshot item. It is not a re-dated historical event.
		item := c.missingItem(serviceID, endpointKey, snapshotAt, retrievedAt)
		item.WindowStart = timePtr(start)
		item.WindowEnd = timePtr(end)
		result.Items = []evidence.Item{item}
		return result, nil
	}
	result.Matched = true
	if max <= 0 {
		max = 20
	}
	var items []evidence.Item
	// Put results in timestamp order because Gatus can give history out of order.
	type pair struct {
		r  endpointResult
		ts time.Time
	}
	var pairs []pair
	for _, r := range ep.Results {
		ts, ok := parseTS(r.Timestamp)
		if !ok {
			result.Discarded++
			continue
		}
		if ts.Before(start) || ts.After(end) {
			continue
		}
		if r.Success == nil {
			// An absent success field is not an explicit false value.
			result.Discarded++
			continue
		}
		pairs = append(pairs, pair{r, ts})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		return pairs[i].ts.Before(pairs[j].ts)
	})
	result.Truncated = len(pairs) > max
	if result.Truncated {
		pairs = pairs[len(pairs)-max:]
	}
	for index, p := range pairs {
		item := c.itemFromResult(serviceID, ep, p.r, p.ts, retrievedAt, true)
		item.WindowStart = timePtr(start)
		item.WindowEnd = timePtr(end)
		item.Order = index
		items = append(items, item)
	}
	result.Items = items
	return result, nil
}

func (c *Client) fetchAll(ctx context.Context, fallback time.Time) ([]endpointStatus, time.Time, error) {
	// Try the primary route, then the variant after a 404 or client error.
	paths := []string{"/api/v1/endpoints/statuses", "/api/v1/endpoints/statuses/"}
	var lastErr error
	for _, p := range paths {
		// LockedClient.Get closes the body before it gives the response.
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

func findEndpoint(list []endpointStatus, key string) (endpointStatus, bool, error) {
	key = strings.TrimSpace(key)
	// Exact keys take precedence over compatibility aliases. Check the complete
	// collection first so that an earlier alias cannot mask a later exact key.
	var exact []endpointStatus
	for _, ep := range list {
		if ep.Key == key {
			exact = append(exact, ep)
		}
	}
	switch len(exact) {
	case 1:
		return exact[0], true, nil
	case 0:
	default:
		return endpointStatus{}, false, ErrEndpointAmbiguous
	}
	// Some versions use a name or group_name composition. An alias is valid
	// only when it identifies one endpoint.
	var aliases []endpointStatus
	for _, ep := range list {
		if ep.Name == key {
			aliases = append(aliases, ep)
			continue
		}
		if ep.Group != "" && ep.Group+"_"+ep.Name == key {
			aliases = append(aliases, ep)
		}
	}
	switch len(aliases) {
	case 1:
		return aliases[0], true, nil
	case 0:
		return endpointStatus{}, false, nil
	default:
		return endpointStatus{}, false, ErrEndpointAmbiguous
	}
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
	// Select the maximum correct timestamp. Do not use the array order.
	// Without a correct date, keep the first result and mark the source time as
	// unknown. observed_at then describes the snapshot collection, not the event.
	best := ep.Results[0]
	var bestTS time.Time
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
	observedAt := snapshotAt
	if foundValid {
		observedAt = bestTS
	}
	return c.itemFromResult(serviceID, ep, best, observedAt, retrievedAt, foundValid)
}

func (c *Client) itemFromResult(serviceID string, ep endpointStatus, r endpointResult, ts, now time.Time, sourceTimeKnown bool) evidence.Item {
	successKnown := r.Success != nil
	sev := evidence.SeverityUnknown
	rawSummary := fmt.Sprintf("gatus %s has no success field status=%d", safeName(ep), intValue(r.Status))
	if successKnown {
		sev = evidence.SeverityInfo
		rawSummary = fmt.Sprintf("gatus %s success status=%d", safeName(ep), intValue(r.Status))
		if !*r.Success {
			sev = evidence.SeverityError
			rawSummary = fmt.Sprintf("gatus %s failure status=%d", safeName(ep), intValue(r.Status))
		}
	}
	key, keyRedactions, keyCut := redaction.ApplyAndTruncate(c.Redact, ep.Key, 256)
	name, nameRedactions, nameCut := redaction.ApplyAndTruncate(c.Redact, ep.Name, 256)
	group, groupRedactions, groupCut := redaction.ApplyAndTruncate(c.Redact, ep.Group, 256)
	attrs := map[string]any{
		"endpoint_key":      key,
		"name":              name,
		"group":             group,
		"success_known":     successKnown,
		"found":             true,
		"source_time_known": sourceTimeKnown,
	}
	if successKnown {
		attrs["success"] = *r.Success
	}
	if r.Status != nil {
		attrs["http_status"] = *r.Status
	}
	if !sourceTimeKnown {
		attrs["collection_time_derived"] = true
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
	freshness := evidence.ComputeFreshness(ts, now)
	if !sourceTimeKnown {
		// A collection time must not imply that the source event is recent.
		freshness = evidence.FreshnessUnknown
	}
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
		Freshness:         freshness,
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

func intValue(v *int) int {
	if v == nil {
		return 0
	}
	return *v
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
