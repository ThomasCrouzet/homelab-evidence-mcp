// Package loki contains the read-only Loki query_range adapter.
package loki

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	"severity":  {},
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

	streamOnce sync.Once
	streamKey  []byte
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
	// Regex adds an optional regular expression filter with built-in limits. It must compile.
	Regex string
	// Stats receives source coverage counts when it is not nil.
	Stats *QueryStats
}

// QueryStats counts source records that the adapter could not use.
type QueryStats struct {
	InvalidTimestamps int
	IncompleteRecords int
	// MismatchedRecords counts lines in a stream that does not satisfy the
	// configured equality selector.
	MismatchedRecords int
}

// Discarded gives the number of unusable source records.
func (s QueryStats) Discarded() int {
	return s.InvalidTimestamps + s.IncompleteRecords + s.MismatchedRecords
}

// Search sends a query_range request and gives lines as evidence items.
func (c *Client) Search(ctx context.Context, opt QueryOptions) ([]evidence.Item, bool, error) {
	requestNow := c.now()
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
		opt.End = requestNow
	}
	if opt.Start.IsZero() || !opt.Start.Before(opt.End) {
		return nil, false, fmt.Errorf("invalid time window")
	}

	equality, err := parseEqualitySelector(opt.Selector)
	if err != nil {
		return nil, false, err
	}
	q, err := buildQuery(opt.Selector, opt.Text, opt.Regex)
	if err != nil {
		return nil, false, err
	}

	if !rfc3339Representable(opt.Start) || !rfc3339Representable(opt.End) {
		return nil, false, fmt.Errorf("loki window is outside the RFC3339 range")
	}
	vals := url.Values{}
	vals.Set("query", q)
	// Loki accepts RFC3339Nano bounds. It avoids the undefined UnixNano range
	// below 1678 and above 2262.
	vals.Set("start", opt.Start.UTC().Format(time.RFC3339Nano))
	vals.Set("end", opt.End.UTC().Format(time.RFC3339Nano))
	// Ask for one additional line so the response can report a reached limit.
	vals.Set("limit", strconv.Itoa(opt.Limit+1))
	// Loki applies the limit before this client can sort the response.
	// Request the newest entries, then return them in chronological order.
	vals.Set("direction", "backward")

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

	// The response is decoded before the retrieval time is recorded.
	retrievedAt := c.now()
	maxLineBytes := c.MaxLineBytes
	if maxLineBytes <= 0 {
		maxLineBytes = 2048
	}

	type candidate struct {
		stream   map[string]string
		streamID string
		ts       time.Time
		line     string
		order    int
	}
	var candidates []candidate
	position := 0
	for _, stream := range raw.Data.Result {
		if !labelsMatch(stream.Stream, equality) {
			if opt.Stats != nil {
				opt.Stats.MismatchedRecords += len(stream.Values)
			}
			continue
		}
		streamID := c.streamRef(stream.Stream)
		for _, pair := range stream.Values {
			if len(pair) < 2 {
				if opt.Stats != nil {
					opt.Stats.IncompleteRecords++
				}
				continue
			}
			ts, ok := parseNano(pair[0])
			if !ok {
				if opt.Stats != nil {
					opt.Stats.InvalidTimestamps++
				}
				continue
			}
			// Loki query_range uses a half-open [start, end) interval.
			if ts.Before(opt.Start) || !ts.Before(opt.End) {
				continue
			}
			candidates = append(candidates, candidate{
				stream: stream.Stream, streamID: streamID, ts: ts, line: pair[1], order: position,
			})
			position++
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if !candidates[i].ts.Equal(candidates[j].ts) {
			return candidates[i].ts.Before(candidates[j].ts)
		}
		return candidates[i].order < candidates[j].order
	})
	truncated := len(candidates) > opt.Limit
	if truncated {
		candidates = candidates[len(candidates)-opt.Limit:]
	}
	// Build evidence only for the retained candidates. Decoding and selecting
	// a large response therefore stays bounded by the source body and limit.
	items := make([]evidence.Item, 0, len(candidates))
	for _, entry := range candidates {
		labels, labelRedactions, labelsTruncated := c.filterLabels(entry.stream)
		line := redaction.SanitizeControl(entry.line)
		cleaned, rn := c.redact(line)
		cleaned = redaction.NeutralizeInstructionLike(cleaned)
		cleaned, lineTruncated := redaction.TruncateBytes(cleaned, maxLineBytes)
		sum := cleaned
		summaryTruncated := false
		if utf8.RuneCountInString(sum) > 200 {
			sum, summaryTruncated = redaction.Truncate(sum, 200)
		}
		severity, severitySource := severityFor(labels, cleaned)
		attrs := map[string]any{
			"line":            cleaned,
			"severity_source": severitySource,
		}
		for k, v := range labels {
			attrs["label_"+k] = v
		}
		items = append(items, evidence.Item{
			ID:                evidence.NewOpaqueID("ev"),
			ServiceID:         opt.ServiceID,
			Source:            evidence.SourceLoki,
			SourceID:          entry.streamID,
			Kind:              evidence.KindLogLine,
			ObservedAt:        entry.ts,
			RetrievedAt:       retrievedAt,
			WindowStart:       timePtr(opt.Start),
			WindowEnd:         timePtr(opt.End),
			Summary:           sum,
			Severity:          severity,
			Attributes:        attrs,
			Truncated:         lineTruncated || summaryTruncated || labelsTruncated,
			RedactionsApplied: rn + labelRedactions,
			Freshness:         evidence.ComputeFreshness(entry.ts, retrievedAt),
			Order:             entry.order,
		})
	}
	// KeepNewest sorts by timestamp, source, identity, and then the stable
	// response position. Opaque ids never decide which equal-time line survives.
	kept, trimmed := evidence.KeepNewest(items, opt.Limit)
	return kept, truncated || trimmed, nil
}

// streamRef gives a bounded process-local stream identity from the complete
// label map. It is stable for the process lifetime and never exposes a label
// that the output allowlist removes.
func (c *Client) streamRef(labels map[string]string) string {
	c.streamOnce.Do(func() {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			key = []byte("homelab-evidence-loki-stream-identity")
		}
		c.streamKey = key
	})
	mac := hmac.New(sha256.New, c.streamKey)
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var length [8]byte
	for _, key := range keys {
		value := labels[key]
		binary.BigEndian.PutUint64(length[:], uint64(len(key)))
		_, _ = mac.Write(length[:])
		_, _ = mac.Write([]byte(key))
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = mac.Write(length[:])
		_, _ = mac.Write([]byte(value))
	}
	return "stream_" + hex.EncodeToString(mac.Sum(nil)[:10])
}

var equalityRe = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*=\s*"([^"]*)"`)

// parseEqualitySelector extracts the label equality predicates from the
// configured selector. It fails closed on a selector that has no equality set.
func parseEqualitySelector(selector string) (map[string]string, error) {
	start := strings.Index(selector, "{")
	end := strings.Index(selector, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("loki selector must contain an equality label set")
	}
	body := selector[start+1 : end]
	if strings.Contains(body, "=~") || strings.Contains(body, "!=") {
		return nil, fmt.Errorf("loki selector supports equality labels only")
	}
	out := map[string]string{}
	for _, match := range equalityRe.FindAllStringSubmatch(body, -1) {
		out[match[1]] = match[2]
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("loki selector must contain an equality label")
	}
	return out, nil
}

func labelsMatch(labels, want map[string]string) bool {
	for key, value := range want {
		have, ok := labels[key]
		if !ok || have != value {
			return false
		}
	}
	return true
}

var (
	structuredLevelRe = regexp.MustCompile(`(?i)(?:^|[\s,;{])\s*"?(?:level|severity|lvl)"?\s*[=:]\s*"?([a-z]+)"?`)
	negatedIssueRe    = regexp.MustCompile(`(?i)\b(?:no|zero|without|not)\s+(?:errors?|failures?|warnings?|timeouts?|fails?)\b`)
	errorWordRe       = regexp.MustCompile(`(?i)\b(?:error|errors|failed|failure|timeout|timeouts)\b`)
	warningWordRe     = regexp.MustCompile(`(?i)\bwarn(?:ing)?s?\b`)
)

// severityFor prefers a source label, then a bounded structured level field,
// then conservative whole-word keywords. It never claims critical from an
// arbitrary word in an untrusted message.
func severityFor(labels map[string]string, line string) (evidence.Severity, string) {
	for _, key := range []string{"level", "severity"} {
		if value, ok := labels[key]; ok {
			if severity, known := severityFromLevel(value); known {
				return severity, "label"
			}
			return evidence.SeverityUnknown, "label"
		}
	}
	if match := structuredLevelRe.FindStringSubmatch(line); match != nil {
		if severity, known := severityFromLevel(match[1]); known {
			return severity, "structured"
		}
		return evidence.SeverityUnknown, "structured"
	}
	// Remove explicit negated phrases so "no errors detected" is not an error.
	stripped := negatedIssueRe.ReplaceAllString(line, " ")
	switch {
	case errorWordRe.MatchString(stripped):
		return evidence.SeverityError, "text"
	case warningWordRe.MatchString(stripped):
		return evidence.SeverityWarning, "text"
	default:
		return evidence.SeverityInfo, "text"
	}
}

func severityFromLevel(value string) (evidence.Severity, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "trace", "debug", "info", "notice":
		return evidence.SeverityInfo, true
	case "warn", "warning":
		return evidence.SeverityWarning, true
	case "error", "err":
		return evidence.SeverityError, true
	case "fatal", "crit", "critical", "panic":
		return evidence.SeverityCritical, true
	default:
		return evidence.SeverityUnknown, false
	}
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

func (c *Client) redact(s string) (string, int) {
	if c.Redact == nil {
		return redaction.SanitizeControl(s), 0
	}
	return c.Redact.Apply(s)
}

func rfc3339Representable(t time.Time) bool {
	year := t.Year()
	return year >= 0 && year <= 9999
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
