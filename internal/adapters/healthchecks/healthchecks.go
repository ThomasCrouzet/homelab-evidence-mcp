// Package healthchecks contains the read-only Management API v3 client.
package healthchecks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
)

// Client uses a read-only key in the header from the configuration.
type Client struct {
	HTTP     *httpx.LockedClient
	DestName string
	Headers  map[string]string // holds authentication. Do not log the values.
	Redact   *redaction.Engine
	Now      func() time.Time
}

// check holds only the Healthchecks fields used.
const (
	maxTransitionChecks = 32
	maxFlipsPerCheck    = 200
)

var transitionKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

type flip struct {
	Timestamp string `json:"timestamp"`
	Up        *int   `json:"up"`
}

type flipsResponse struct {
	Flips *[]flip `json:"flips"`
}

// TransitionResult gives dated state changes and coverage counters.
type TransitionResult struct {
	Items           []evidence.Item
	Truncated       bool
	Omitted         int
	MissingIdentity int
	Discarded       int
	Requests        int
	Failures        int
	// Unauthorized counts per-check history requests that failed with 401 or 403.
	Unauthorized int
}

type check struct {
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	Tags      string `json:"tags"`
	Grace     int    `json:"grace"`
	Status    string `json:"status"`
	LastPing  string `json:"last_ping"`
	NextPing  string `json:"next_ping"`
	UUID      string `json:"uuid"`       // present only with a read-write key
	UniqueKey string `json:"unique_key"` // present with a read-only key
	// The client omits ping_url, update_url, pause_url, and badge_url.
}

type listResponse struct {
	Checks *[]check `json:"checks"`
}

// Filter selects checks for a service.
type Filter struct {
	Name      string
	Tags      []string
	UUID      string
	UniqueKey string
	Status    string // optional status filter
}

// ErrNoMatch reports a configured binding that selected no check.
// The error does not include the identity value.
var ErrNoMatch = errors.New("healthchecks binding matched no check")

// ErrAmbiguousBinding reports a name or identity binding that selected more
// than one check. The error does not include an identity value.
var ErrAmbiguousBinding = errors.New("healthchecks binding matched more than one check")

// FilterFromRef makes a filter from the configuration.
func FilterFromRef(ref *config.HealthchecksRef) Filter {
	if ref == nil {
		return Filter{}
	}
	return Filter{
		Name:      ref.CheckName,
		Tags:      ref.CheckTags,
		UUID:      ref.CheckUUID,
		UniqueKey: ref.CheckUniqueKey,
		Status:    ref.StatusFilter,
	}
}

func (f Filter) selects() bool {
	return f.Name != "" || f.UUID != "" || f.UniqueKey != "" || len(f.Tags) > 0 || f.Status != ""
}

// Status gives the current state of the selected checks.
func (c *Client) Status(ctx context.Context, serviceID string, f Filter) ([]evidence.Item, error) {
	requestedAt := c.now()
	checks, observedAt, err := c.list(ctx, requestedAt)
	if err != nil {
		return nil, err
	}
	retrievedAt := c.now()
	matched, err := selectChecks(checks, f)
	if err != nil {
		return nil, err
	}
	out := make([]evidence.Item, 0, len(matched))
	for _, ch := range matched {
		out = append(out, c.itemFrom(serviceID, ch, observedAt, retrievedAt))
	}
	return out, nil
}

// FailedInWindow gives checks with a current state of down, grace, or paused.
// The list route cannot prove a past incident or recovery.
func (c *Client) FailedInWindow(ctx context.Context, serviceID string, f Filter, start, end time.Time) ([]evidence.Item, error) {
	requestedAt := c.now()
	checks, observedAt, err := c.list(ctx, requestedAt)
	if err != nil {
		return nil, err
	}
	retrievedAt := c.now()
	var matched []check
	if serviceID != "" || f.selects() {
		matched, err = selectChecks(checks, f)
		if err != nil {
			return nil, err
		}
	} else {
		matched = checks
	}
	// Allow collection time to be at most 30 seconds after the end time that the caller set before the request.
	// Do not use the current state as proof of a historical state.
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
	// Management API v3 gives access to the list at GET /api/v3/checks/.
	path := "/api/v3/checks/"
	// LockedClient.Get closes the body before it gives the response.
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
	// Some deployments give a JSON array without a wrapper.
	var arr []check
	if err := json.Unmarshal(body, &arr); err != nil || arr == nil {
		return nil, time.Time{}, fmt.Errorf("healthchecks decode: expected checks array")
	}
	return arr, observedAt, nil
}

// selectChecks applies the configured binding. A name, UUID, or unique key
// identifies one check, so more than one match fails closed. Tag-only bindings
// can deliberately aggregate several checks.
func selectChecks(checks []check, f Filter) ([]check, error) {
	if !f.selects() {
		return nil, nil
	}
	matched := filterChecks(checks, f)
	if len(matched) == 0 {
		return nil, ErrNoMatch
	}
	if f.Name != "" || f.UUID != "" || f.UniqueKey != "" {
		if len(matched) > 1 {
			return nil, ErrAmbiguousBinding
		}
	}
	return matched, nil
}

// filterChecks returns every check that satisfies each configured filter field.
func filterChecks(checks []check, f Filter) []check {
	var out []check
	for _, ch := range checks {
		if f.UUID != "" && !strings.EqualFold(ch.UUID, f.UUID) {
			continue
		}
		if f.UniqueKey != "" && !strings.EqualFold(ch.UniqueKey, f.UniqueKey) {
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

// Transitions reads dated up/down transitions for the selected checks.
// It keeps the service_status path as a current snapshot.
func (c *Client) Transitions(ctx context.Context, serviceID string, f Filter, start, end time.Time) (TransitionResult, error) {
	result := TransitionResult{}
	requestedAt := c.now()
	checks, _, err := c.list(ctx, requestedAt)
	if err != nil {
		return result, err
	}
	var matched []check
	if serviceID != "" || f.selects() {
		matched, err = selectChecks(checks, f)
		if err != nil {
			return result, err
		}
	} else {
		matched = checks
	}
	sort.SliceStable(matched, func(i, j int) bool {
		left, right := safeSourceID(matched[i]), safeSourceID(matched[j])
		if left != right {
			return left < right
		}
		return matched[i].UniqueKey < matched[j].UniqueKey
	})
	if len(matched) > maxTransitionChecks {
		result.Omitted = len(matched) - maxTransitionChecks
		result.Truncated = true
		matched = matched[:maxTransitionChecks]
	}

	// The flips endpoint takes Unix seconds. The upstream bounds are exclusive:
	// start is newer than the value and end is older than the value. Widen each
	// bound by one second so the local inclusive filter sees every candidate,
	// including a flip exactly on a boundary or inside a sub-second bound.
	query := url.Values{}
	query.Set("start", strconv.FormatInt(start.UTC().Unix()-1, 10))
	query.Set("end", strconv.FormatInt(end.UTC().Unix()+1, 10))
	for _, ch := range matched {
		key := strings.TrimSpace(ch.UniqueKey)
		if !transitionKeyRe.MatchString(key) {
			result.MissingIdentity++
			continue
		}
		path := "/api/v3/checks/" + url.PathEscape(key) + "/flips/?" + query.Encode()
		// LockedClient.Get closes the body before it gives the response.
		resp, body, err := c.HTTP.Get(ctx, c.DestName, path, c.Headers) //nolint:bodyclose
		result.Requests++
		if err != nil {
			result.Failures++
			continue
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			result.Unauthorized++
			continue
		}
		if resp.StatusCode != http.StatusOK {
			result.Failures++
			continue
		}
		flips, err := decodeFlips(body)
		if err != nil {
			result.Failures++
			continue
		}
		var parsed []struct {
			at time.Time
			up int
		}
		for _, entry := range flips {
			if entry.Up == nil || (*entry.Up != 0 && *entry.Up != 1) {
				result.Discarded++
				continue
			}
			at, ok := parseTS(entry.Timestamp)
			if !ok {
				result.Discarded++
				continue
			}
			if at.Before(start) || at.After(end) {
				continue
			}
			parsed = append(parsed, struct {
				at time.Time
				up int
			}{at, *entry.Up})
		}
		sort.SliceStable(parsed, func(i, j int) bool { return parsed[i].at.Before(parsed[j].at) })
		if len(parsed) > maxFlipsPerCheck {
			result.Truncated = true
			parsed = parsed[len(parsed)-maxFlipsPerCheck:]
		}
		for _, entry := range parsed {
			item := c.transitionItem(serviceID, ch, entry.at, entry.up, requestedAt, start, end)
			item.Order = len(result.Items)
			result.Items = append(result.Items, item)
		}
	}
	// The last history response defines the retrieval time for every kept item.
	retrievedAt := c.now()
	for index := range result.Items {
		result.Items[index].RetrievedAt = retrievedAt
		result.Items[index].Freshness = evidence.ComputeFreshness(result.Items[index].ObservedAt, retrievedAt)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if result.Requests > 0 && result.Failures+result.Unauthorized == result.Requests && len(result.Items) == 0 {
		if result.Unauthorized > 0 {
			return result, fmt.Errorf("healthchecks authentication failed")
		}
		return result, fmt.Errorf("healthchecks history requests failed")
	}
	return result, nil
}

func decodeFlips(body []byte) ([]flip, error) {
	var arr []flip
	if err := json.Unmarshal(body, &arr); err == nil && arr != nil {
		return arr, nil
	}
	var wrapped flipsResponse
	if err := json.Unmarshal(body, &wrapped); err != nil || wrapped.Flips == nil {
		return nil, fmt.Errorf("healthchecks history decode: expected flips array")
	}
	return *wrapped.Flips, nil
}

func (c *Client) transitionItem(serviceID string, ch check, at time.Time, up int, retrievedAt, start, end time.Time) evidence.Item {
	state := "down"
	sev := evidence.SeverityError
	if up == 1 {
		state = "up"
		sev = evidence.SeverityInfo
	}
	name, nameRedactions, nameCut := redaction.ApplyAndTruncate(c.Redact, ch.Name, 256)
	slug, slugRedactions, slugCut := redaction.ApplyAndTruncate(c.Redact, ch.Slug, 256)
	sourceID, sourceRedactions, sourceCut := redaction.ApplyAndTruncate(c.Redact, safeSourceID(ch), 256)
	rawSummary := fmt.Sprintf("healthchecks %q transitioned %s", ch.Name, state)
	sum, summaryRedactions, summaryCut := redaction.ApplyAndTruncate(c.Redact, rawSummary, 512)
	return evidence.Item{
		ID:          evidence.NewOpaqueID("ev"),
		ServiceID:   serviceID,
		Source:      evidence.SourceHealthchecks,
		SourceID:    sourceID,
		Kind:        evidence.KindCronCheck,
		ObservedAt:  at.UTC(),
		RetrievedAt: retrievedAt,
		WindowStart: timePtr(start),
		WindowEnd:   timePtr(end),
		Summary:     sum,
		Severity:    sev,
		Attributes: map[string]any{
			"name":       name,
			"slug":       slug,
			"transition": state,
			"current":    false,
		},
		Truncated:         nameCut || slugCut || sourceCut || summaryCut,
		RedactionsApplied: nameRedactions + slugRedactions + sourceRedactions + summaryRedactions,
		Freshness:         evidence.ComputeFreshness(at, retrievedAt),
	}
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
	// Do not give the UUID or ping URLs.

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
