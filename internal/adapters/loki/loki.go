// Package loki contains the read-only Loki query_range adapter.
package loki

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
)

// Allowed label keys in attributes.
var allowedLabels = map[string]struct{}{
	"job":       {},
	"namespace": {},
	"container": {},
	"pod":       {},
	"app":       {},
	"host":      {},
	"service":   {},
	"filename":  {},
	"level":     {},
	"compose":   {},
}

// Client gets data from Loki with a selector from the configuration.
type Client struct {
	HTTP         *httpx.LockedClient
	DestName     string
	Headers      map[string]string
	Redact       *redaction.Engine
	Now          func() time.Time
	MaxLineBytes int
}

// QueryOptions contains the limits for a log search.
type QueryOptions struct {
	ServiceID string
	Selector  string // predefined in the registry
	Start     time.Time
	End       time.Time
	Limit     int
	// Text adds an optional substring filter.
	Text string
	// Regex adds an optional regular expression filter with limits from the configuration. It must compile.
	Regex string
}

// Search sends a query_range request and gives lines as evidence items.
func (c *Client) Search(ctx context.Context, opt QueryOptions) ([]evidence.Item, bool, error) {
	now := c.now()
	if opt.Selector == "" {
		return nil, false, fmt.Errorf("loki selector is required")
	}
	if opt.Limit <= 0 {
		opt.Limit = 50
	}
	if opt.Limit > 1000 {
		return nil, false, fmt.Errorf("limit exceeds 1000")
	}
	if opt.End.IsZero() {
		opt.End = now
	}
	if opt.Start.IsZero() || !opt.Start.Before(opt.End) {
		return nil, false, fmt.Errorf("invalid time window")
	}

	q, err := buildQuery(opt.Selector, opt.Text, opt.Regex)
	if err != nil {
		return nil, false, err
	}

	vals := url.Values{}
	vals.Set("query", q)
	vals.Set("start", strconv.FormatInt(opt.Start.UnixNano(), 10))
	vals.Set("end", strconv.FormatInt(opt.End.UnixNano(), 10))
	vals.Set("limit", strconv.Itoa(opt.Limit))
	vals.Set("direction", "forward")

	path := "/loki/api/v1/query_range?" + vals.Encode()
	// LockedClient.Get closes the body before it gives the response.
	resp, body, err := c.HTTP.Get(ctx, c.DestName, path, c.Headers) //nolint:bodyclose
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, false, fmt.Errorf("loki rate limited (429)")
	}
	if resp.StatusCode != http.StatusOK {
		// Do not give the body, which may contain the internal query.
		return nil, false, fmt.Errorf("loki HTTP %d", resp.StatusCode)
	}

	var raw lokiResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, false, fmt.Errorf("loki decode: invalid JSON")
	}
	if raw.Status != "success" {
		return nil, false, fmt.Errorf("loki returned non-success status")
	}
	if raw.Data.Result == nil {
		return nil, false, fmt.Errorf("loki decode: missing result array")
	}
	if raw.Data.ResultType != "" && raw.Data.ResultType != "streams" {
		return nil, false, fmt.Errorf("loki decode: unexpected result type")
	}

	maxLineBytes := c.MaxLineBytes
	if maxLineBytes <= 0 {
		maxLineBytes = 2048
	}

	var items []evidence.Item
	for _, stream := range raw.Data.Result {
		labels, labelRedactions, labelsTruncated := c.filterLabels(stream.Stream)
		for _, pair := range stream.Values {
			if len(pair) < 2 {
				continue
			}
			ts, ok := parseNano(pair[0])
			if !ok {
				continue
			}
			if ts.Before(opt.Start) || ts.After(opt.End) {
				continue
			}
			line := pair[1]
			// Untrusted content is data. Sanitize it, then redact it.
			line = redaction.SanitizeControl(line)
			cleaned, rn := c.redact(line)
			// Mark instruction-like text. Do not run it.
			cleaned = redaction.NeutralizeInstructionLike(cleaned)
			cleaned, lineTruncated := redaction.TruncateBytes(cleaned, maxLineBytes)

			sum := cleaned
			summaryTruncated := false
			if utf8.RuneCountInString(sum) > 200 {
				sum, summaryTruncated = redaction.Truncate(sum, 200)
			}
			attrs := map[string]any{
				"line": cleaned,
			}
			for k, v := range labels {
				attrs["label_"+k] = v
			}
			items = append(items, evidence.Item{
				ID:                evidence.NewOpaqueID("ev"),
				ServiceID:         opt.ServiceID,
				Source:            evidence.SourceLoki,
				SourceID:          streamSourceID(labels),
				Kind:              evidence.KindLogLine,
				ObservedAt:        ts,
				RetrievedAt:       now,
				WindowStart:       timePtr(opt.Start),
				WindowEnd:         timePtr(opt.End),
				Summary:           sum,
				Severity:          severityFromLine(cleaned),
				Attributes:        attrs,
				Truncated:         lineTruncated || summaryTruncated || labelsTruncated,
				RedactionsApplied: rn + labelRedactions,
				Freshness:         evidence.ComputeFreshness(ts, now),
			})
		}
	}
	// Put the items in the same order for the same input.
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].ObservedAt.Equal(items[j].ObservedAt) {
			return items[i].ObservedAt.Before(items[j].ObservedAt)
		}
		return items[i].ID < items[j].ID
	})
	kept, truncated := evidence.KeepNewest(items, opt.Limit)
	return kept, truncated, nil
}

func streamSourceID(labels map[string]string) string {
	if len(labels) == 0 {
		return "stream"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ",")
}

type lokiResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string `json:"stream"`
			Values [][]string        `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func buildQuery(selector, text, rx string) (string, error) {
	q := strings.TrimSpace(selector)
	if text != "" {
		if hasControl(text) {
			return "", fmt.Errorf("search text must be single-line")
		}
		if strings.ContainsAny(text, "\"\\") {
			// Escape a double-quoted LogQL string.
			text = escapeLogQL(text)
		}
		if len(text) > 200 {
			return "", fmt.Errorf("search text too long")
		}
		q += fmt.Sprintf(` |= "%s"`, text)
	}
	if rx != "" {
		if hasControl(rx) {
			return "", fmt.Errorf("search regex must be single-line")
		}
		if len(rx) > 200 {
			return "", fmt.Errorf("search regex too long")
		}
		if _, err := regexp.Compile(rx); err != nil {
			return "", fmt.Errorf("invalid search regex")
		}
		// Give an error for abnormally complex expressions as a precaution.
		if strings.Count(rx, "+")+strings.Count(rx, "*") > 8 {
			return "", fmt.Errorf("search regex too complex")
		}
		q += fmt.Sprintf(` |~ "%s"`, escapeLogQL(rx))
	}
	return q, nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func escapeLogQL(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

func (c *Client) filterLabels(in map[string]string) (map[string]string, int, bool) {
	out := make(map[string]string)
	redactions := 0
	truncated := false
	for k, v := range in {
		if _, ok := allowedLabels[k]; !ok {
			continue
		}
		v = redaction.SanitizeControl(v)
		var n int
		v, n = c.redact(v)
		redactions += n
		var cut bool
		v, cut = redaction.Truncate(v, 128)
		truncated = truncated || cut
		out[k] = v
	}
	return out, redactions, truncated
}

func parseNano(s string) (time.Time, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	// Loki uses nanoseconds.
	sec := n / 1e9
	nsec := n % 1e9
	if sec < 0 {
		return time.Time{}, false
	}
	return time.Unix(sec, nsec).UTC(), true
}

func severityFromLine(line string) evidence.Severity {
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "fatal"), strings.Contains(l, "panic"):
		return evidence.SeverityCritical
	case strings.Contains(l, "error"), strings.Contains(l, "timeout"), strings.Contains(l, "fail"):
		return evidence.SeverityError
	case strings.Contains(l, "warn"):
		return evidence.SeverityWarning
	default:
		return evidence.SeverityInfo
	}
}

func (c *Client) redact(s string) (string, int) {
	if c.Redact == nil {
		return redaction.SanitizeControl(s), 0
	}
	return c.Redact.Apply(s)
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func timePtr(t time.Time) *time.Time {
	t = t.UTC()
	return &t
}
