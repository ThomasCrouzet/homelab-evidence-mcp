// Package ntfy implémente l’historique d’un sujet ntfy en lecture seule.
// La route compatible renvoie du NDJSON ou un tableau JSON.
// Les sujets proviennent uniquement de la configuration.
package ntfy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
)

// Client consulte l’historique d’un sujet prédéfini.
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
	Time     int64    `json:"time"`
	Event    string   `json:"event"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags"`
}

// History renvoie les notifications entre start et end, bornées par limit.
func (c *Client) History(ctx context.Context, serviceID, topic string, start, end time.Time, limit int) ([]evidence.Item, bool, error) {
	now := c.now()
	if topic == "" {
		return nil, false, fmt.Errorf("ntfy topic is required")
	}
	if limit <= 0 {
		limit = 50
	}
	if !start.Before(end) {
		return nil, false, fmt.Errorf("invalid time window")
	}
	// poll=1 renvoie les messages conservés sans ouvrir de flux.
	q := url.Values{}
	q.Set("poll", "1")
	q.Set("since", strconv.FormatInt(start.Unix(), 10))
	path := "/" + url.PathEscape(topic) + "/json?" + q.Encode()
	// LockedClient.Get ferme le corps avant de retourner la réponse.
	resp, body, err := c.HTTP.Get(ctx, c.DestName, path, c.Headers) //nolint:bodyclose
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, false, fmt.Errorf("ntfy authentication failed")
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, fmt.Errorf("ntfy topic not found")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("ntfy HTTP %d", resp.StatusCode)
	}
	snapshotAt := httpx.CollectionTime(resp, now)
	msgs, err := decodeMessages(body)
	if err != nil {
		return nil, false, err
	}
	maxLineBytes := c.MaxLineBytes
	if maxLineBytes <= 0 {
		maxLineBytes = 2048
	}
	sort.SliceStable(msgs, func(i, j int) bool {
		if msgs[i].Time != msgs[j].Time {
			return msgs[i].Time < msgs[j].Time
		}
		return msgs[i].ID < msgs[j].ID
	})
	var items []evidence.Item
	truncated := false
	for _, m := range msgs {
		if m.Event != "" && m.Event != "message" {
			continue
		}
		ts := time.Unix(m.Time, 0).UTC()
		if m.Time <= 0 {
			ts = snapshotAt
			if ts.Before(start) || ts.After(end.Add(30*time.Second)) {
				continue
			}
		} else if ts.Before(start) || ts.After(end) {
			continue
		}
		if len(items) >= limit {
			truncated = true
			continue
		}
		item := c.itemFrom(serviceID, topic, m, ts, now, maxLineBytes)
		item.WindowStart = timePtr(start)
		item.WindowEnd = timePtr(end)
		items = append(items, item)
	}
	evidence.SortItems(items)
	return items, truncated, nil
}

func decodeMessages(body []byte) ([]message, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, nil
	}
	// Tableau JSON.
	if body[0] == '[' {
		var decoded []*message
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, fmt.Errorf("ntfy decode: invalid JSON array")
		}
		arr := make([]message, 0, len(decoded))
		for _, m := range decoded {
			if m == nil {
				return nil, fmt.Errorf("ntfy decode: null message")
			}
			arr = append(arr, *m)
		}
		return arr, nil
	}
	// Flux NDJSON.
	var out []message
	sc := bufio.NewScanner(bytes.NewReader(body))
	// Augmenter la limite du scanner tout en restant borné par le corps HTTP.
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m *message
		if err := json.Unmarshal([]byte(line), &m); err != nil || m == nil {
			return nil, fmt.Errorf("ntfy decode: invalid NDJSON line")
		}
		out = append(out, *m)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("ntfy decode: %w", err)
	}
	return out, nil
}

func (c *Client) itemFrom(serviceID, topic string, m message, ts, now time.Time, maxLineBytes int) evidence.Item {
	text := m.Message
	if m.Title != "" {
		text = m.Title + ": " + m.Message
	}
	text = redaction.SanitizeControl(text)
	cleaned, rn := c.redact(text)
	// Les notifications libres sont des données non fiables, comme les lignes Loki.
	cleaned = redaction.NeutralizeInstructionLike(cleaned)
	cleaned, trunc := redaction.TruncateBytes(cleaned, maxLineBytes)
	sev := evidence.SeverityInfo
	switch {
	case m.Priority >= 5:
		sev = evidence.SeverityCritical
	case m.Priority == 4:
		sev = evidence.SeverityError
	case m.Priority == 3:
		sev = evidence.SeverityWarning
	}
	sum := cleaned
	if len([]rune(sum)) > 200 {
		var summaryCut bool
		sum, summaryCut = redaction.Truncate(sum, 200)
		trunc = trunc || summaryCut
	}
	attrs := map[string]any{
		"topic":    topic,
		"priority": m.Priority,
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
		sid = topic
	}
	sid, sidRedactions, sidCut := redaction.ApplyAndTruncate(c.Redact, sid, 256)
	rn += sidRedactions
	trunc = trunc || sidCut
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
