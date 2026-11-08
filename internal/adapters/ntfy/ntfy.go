// Package ntfy contains read-only history for an ntfy topic.
// The compatible route gives NDJSON or a JSON array.
// Topics come only from configuration.
package ntfy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
)

// Client gets history for a topic from the configuration.
type Client struct {
	HTTP         *httpx.LockedClient
	DestName     string
	Headers      map[string]string
	Redact       *redaction.Engine
	Now          func() time.Time
	MaxLineBytes int
}

type message struct {
	ID       string   `json:"id"`
	Time     *int64   `json:"time"`
	Event    string   `json:"event"`
	Topic    string   `json:"topic"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority *int     `json:"priority"`
	Tags     []string `json:"tags"`
}

// Result gives replay items and every coverage or validity counter.
type Result struct {
	Items             []evidence.Item
	UpstreamTruncated bool
	LocalTruncated    bool
	// Discarded counts invalid rows, cross-topic messages, and duplicate IDs.
	Discarded int
	// Duplicates counts repeated identical message IDs that were collapsed.
	Duplicates int
	// UnknownTime counts messages without a usable source time.
	UnknownTime      int
	RetentionLimited bool
}

// ntfy priorities. Priority 3 is the source default for an omitted value.
const (
	ntfyDefaultPriority = 3
	ntfyMinimumPriority = 1
	ntfyMaximumPriority = 5
)

// severityForPriority maps notification urgency to presentation severity.
// It does not prove an incident. Values outside 1..5 are not classified.
func severityForPriority(priority int) (evidence.Severity, bool) {
	switch priority {
	case 1, 2, 3:
		return evidence.SeverityInfo, true
	case 4:
		return evidence.SeverityError, true
	case 5:
		return evidence.SeverityCritical, true
	default:
		return evidence.SeverityUnknown, false
	}
}

// effectivePriority returns the source value and whether it is valid.
// An omitted priority uses the ntfy default. An explicit out-of-range value
// stays visible as invalid.
func effectivePriority(reported *int) (int, bool) {
	if reported == nil {
		return ntfyDefaultPriority, true
	}
	if *reported < ntfyMinimumPriority || *reported > ntfyMaximumPriority {
		return *reported, false
	}
	return *reported, true
}

// History gives notifications between start and end with the specified limit.
// The boolean combines upstream and local truncation.
func (c *Client) History(ctx context.Context, serviceID, topic string, start, end time.Time, limit int) ([]evidence.Item, bool, error) {
	result, err := c.HistoryDetail(ctx, serviceID, topic, start, end, limit)
	return result.Items, result.UpstreamTruncated || result.LocalTruncated, err
}

// HistoryDetail also separates upstream and local truncation and reports
// invalid, duplicative, and undated records.
func (c *Client) HistoryDetail(ctx context.Context, serviceID, topic string, start, end time.Time, limit int) (Result, error) {
	result := Result{RetentionLimited: true}
	now := c.now()
	if topic == "" {
		return result, fmt.Errorf("ntfy topic is required")
	}
	if limit <= 0 {
		limit = 50
	}
	if !start.Before(end) {
		return result, fmt.Errorf("invalid time window")
	}
	// poll=1 gives messages from storage and does not open a stream.
	q := url.Values{}
	q.Set("poll", "1")
	q.Set("since", strconv.FormatInt(start.Unix(), 10))
	path := "/" + url.PathEscape(topic) + "/json?" + q.Encode()
	// LockedClient.Get closes the body before it gives the response.
	resp, body, err := c.HTTP.Get(ctx, c.DestName, path, c.Headers) //nolint:bodyclose
	if err != nil && !errors.Is(err, httpx.ErrBodyTooLarge) {
		return result, err
	}
	if resp == nil {
		return result, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return result, fmt.Errorf("ntfy authentication failed")
	}
	if resp.StatusCode == http.StatusNotFound {
		return result, fmt.Errorf("ntfy topic not found")
	}
	if resp.StatusCode != http.StatusOK {
		return result, fmt.Errorf("ntfy HTTP %d", resp.StatusCode)
	}
	partialBody := errors.Is(err, httpx.ErrBodyTooLarge)
	result.UpstreamTruncated = httpx.UpstreamTruncated(resp)
	msgs, invalid, decodeTruncated := decodeMessages(body, partialBody)
	result.Discarded += invalid
	result.LocalTruncated = partialBody || decodeTruncated
	if len(msgs) == 0 && invalid > 0 && !partialBody {
		return result, fmt.Errorf("ntfy decode: invalid response")
	}
	maxLineBytes := c.MaxLineBytes
	if maxLineBytes <= 0 {
		maxLineBytes = 2048
	}
	seen := make(map[string]string, len(msgs))
	type candidate struct {
		m   message
		ts  time.Time
		pos int
	}
	candidates := make([]candidate, 0, len(msgs))
	for index, m := range msgs {
		if m.Event != "message" {
			// Control events carry no notification evidence. A missing event
			// field is not a message, because the field is mandatory.
			if m.Event == "" {
				result.Discarded++
			}
			continue
		}
		if m.Topic != topic {
			// A replay must not attribute another topic to this service.
			result.Discarded++
			continue
		}
		if m.ID == "" {
			result.Discarded++
			continue
		}
		if m.Time == nil || *m.Time <= 0 {
			result.UnknownTime++
			result.Discarded++
			continue
		}
		ts, ok := sourceTime(*m.Time)
		if !ok {
			result.UnknownTime++
			result.Discarded++
			continue
		}
		if ts.Before(start) || ts.After(end) {
			continue
		}
		fingerprint := messageFingerprint(m)
		if previous, ok := seen[m.ID]; ok {
			// Collapse a repeated ID. Conflicting content is invalid.
			if previous != fingerprint {
				result.Discarded++
			} else {
				result.Duplicates++
			}
			continue
		}
		seen[m.ID] = fingerprint
		candidates = append(candidates, candidate{m: m, ts: ts, pos: index})
	}
	// Same-second messages keep the source receipt order.
	sort.SliceStable(candidates, func(i, j int) bool {
		if !candidates[i].ts.Equal(candidates[j].ts) {
			return candidates[i].ts.Before(candidates[j].ts)
		}
		return candidates[i].pos < candidates[j].pos
	})
	items := make([]evidence.Item, 0, len(candidates))
	for _, entry := range candidates {
		item := c.itemFrom(serviceID, topic, entry.m, entry.ts, now, maxLineBytes)
		item.WindowStart = timePtr(start)
		item.WindowEnd = timePtr(end)
		item.Order = entry.pos
		items = append(items, item)
	}
	kept, localTruncated := evidence.KeepNewest(items, limit)
	result.LocalTruncated = result.LocalTruncated || localTruncated
	result.Items = kept
	return result, nil
}

// sourceTime validates a Unix second value against the RFC3339 year range.
// A value outside the range cannot be marshalled and is not usable evidence.
func sourceTime(seconds int64) (time.Time, bool) {
	ts := time.Unix(seconds, 0).UTC()
	if ts.Year() < 1 || ts.Year() > 9999 {
		return time.Time{}, false
	}
	return ts, true
}

func messageFingerprint(m message) string {
	raw, err := json.Marshal(m)
	if err != nil {
		return m.ID
	}
	return string(raw)
}

// decodeMessages reads a JSON array or an NDJSON stream. It keeps complete
// records and counts rows it cannot decode. partial marks a body cut at the
// HTTP byte limit, so an incomplete final row is not a malformed row.
func decodeMessages(body []byte, partial bool) ([]message, int, bool) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, 0, false
	}
	if body[0] == '[' {
		return decodeArray(body, partial)
	}
	return decodeNDJSON(body, partial)
}

func decodeArray(body []byte, partial bool) ([]message, int, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if _, err := dec.Token(); err != nil {
		return nil, 1, partial
	}
	var out []message
	discarded := 0
	for dec.More() {
		var m *message
		if err := dec.Decode(&m); err != nil {
			discarded++
			return out, discarded, true
		}
		if m == nil {
			discarded++
			continue
		}
		out = append(out, *m)
	}
	return out, discarded, partial
}

func decodeNDJSON(body []byte, partial bool) ([]message, int, bool) {
	var out []message
	discarded := 0
	truncated := false
	reader := bufio.NewReader(bytes.NewReader(body))
	for {
		line, readErr := reader.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			var m *message
			if err := json.Unmarshal(line, &m); err != nil || m == nil {
				discarded++
				if readErr != nil || partial {
					truncated = true
				}
			} else {
				out = append(out, *m)
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				truncated = true
			}
			break
		}
	}
	return out, discarded, truncated
}

func (c *Client) itemFrom(serviceID, topic string, m message, ts, now time.Time, maxLineBytes int) evidence.Item {
	cleanTopic, topicRedactions, topicCut := redaction.ApplyAndTruncate(c.Redact, topic, 64)
	text := m.Message
	if m.Title != "" {
		text = m.Title + ": " + m.Message
	}
	text = redaction.SanitizeControl(text)
	cleaned, rn := c.redact(text)
	rn += topicRedactions
	// Free-form notifications are untrusted data, like Loki lines.
	cleaned = redaction.NeutralizeInstructionLike(cleaned)
	cleaned, trunc := redaction.TruncateBytes(cleaned, maxLineBytes)
	priority, priorityValid := effectivePriority(m.Priority)
	sev, validMapping := severityForPriority(priority)
	if !priorityValid || !validMapping {
		sev = evidence.SeverityUnknown
	}
	sum := cleaned
	if len([]rune(sum)) > 200 {
		var summaryCut bool
		sum, summaryCut = redaction.Truncate(sum, 200)
		trunc = trunc || summaryCut
	}
	attrs := map[string]any{
		"topic":    cleanTopic,
		"priority": priority,
		"message":  cleaned,
	}
	if !priorityValid {
		attrs["priority_valid"] = false
	}
	if len(m.Tags) > 0 {
		tagCount := len(m.Tags)
		if tagCount > 32 {
			tagCount = 32
			trunc = true
		}
		tags := make([]string, 0, tagCount)
		for _, tag := range m.Tags[:tagCount] {
			cleanTag, n := c.redact(redaction.SanitizeControl(tag))
			rn += n
			cleanTag, cut := redaction.Truncate(cleanTag, 64)
			trunc = trunc || cut
			tags = append(tags, cleanTag)
		}
		attrs["tags"] = tags
	}
	sid := m.ID
	if sid == "" {
		sid = cleanTopic
	} else {
		var sidRedactions int
		var sidCut bool
		sid, sidRedactions, sidCut = redaction.ApplyAndTruncate(c.Redact, sid, 256)
		rn += sidRedactions
		trunc = trunc || sidCut
	}
	trunc = trunc || topicCut
	return evidence.Item{
		ID:                evidence.NewOpaqueID("ev"),
		ServiceID:         serviceID,
		Source:            evidence.SourceNtfy,
		SourceID:          sid,
		Kind:              evidence.KindNotification,
		ObservedAt:        ts,
		RetrievedAt:       now,
		Summary:           sum,
		Severity:          sev,
		Attributes:        attrs,
		Truncated:         trunc,
		RedactionsApplied: rn,
		Freshness:         evidence.ComputeFreshness(ts, now),
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
