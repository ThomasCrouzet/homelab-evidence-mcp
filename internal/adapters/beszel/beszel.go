// Package beszel contains the read-only Beszel status adapter.
// Compatible routes give a JSON array or wrapper.
package beszel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
)

// Client gets data from a Beszel-compatible API with GET only.
type Client struct {
	HTTP     *httpx.LockedClient
	DestName string
	Headers  map[string]string
	Redact   *redaction.Engine
	Now      func() time.Time
}

type systemRow struct {
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Host   string   `json:"host"`
	CPU    *float64 `json:"cpu"`
	Mem    *float64 `json:"mem"`
	// Some API variants use these alternative fields.
	System string `json:"system"`
	Info   struct {
		Hostname string `json:"h"`
	} `json:"info"`
}

// Status gives a snapshot for systemName.
func (c *Client) Status(ctx context.Context, serviceID, systemName string) (evidence.Item, error) {
	retrievedAt := c.now()
	list, observedAt, err := c.fetchSystems(ctx, retrievedAt)
	if err != nil {
		return evidence.Item{}, err
	}
	row, ok := findSystem(list, systemName)
	if !ok {
		cleanName, redactions, truncated := redaction.ApplyAndTruncate(c.Redact, systemName, 256)
		return evidence.Item{
			ID:                evidence.NewOpaqueID("ev"),
			ServiceID:         serviceID,
			Source:            evidence.SourceBeszel,
			SourceID:          cleanName,
			Kind:              evidence.KindMetricSample,
			ObservedAt:        observedAt,
			RetrievedAt:       retrievedAt,
			Summary:           "beszel system not found",
			Severity:          evidence.SeverityUnknown,
			Attributes:        map[string]any{"system_name": cleanName, "found": false},
			Truncated:         truncated,
			RedactionsApplied: redactions,
			Freshness:         evidence.FreshnessMissing,
		}, nil
	}
	return c.itemFrom(serviceID, row, observedAt, retrievedAt), nil
}

// Evidence gives Status in a list.
func (c *Client) Evidence(ctx context.Context, serviceID, systemName string) ([]evidence.Item, error) {
	it, err := c.Status(ctx, serviceID, systemName)
	if err != nil {
		return nil, err
	}
	return []evidence.Item{it}, nil
}

func (c *Client) fetchSystems(ctx context.Context, fallback time.Time) ([]systemRow, time.Time, error) {
	paths := []string{
		"/api/collections/systems/records",
		"/api/systems",
		"/api/beszel/systems",
		"/api/systems/",
	}
	var lastErr error
	for _, p := range paths {
		// LockedClient.Get closes the body before it gives the response.
		resp, body, err := c.HTTP.Get(ctx, c.DestName, p, c.Headers) //nolint:bodyclose
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusNotFound {
			lastErr = fmt.Errorf("beszel returned 404")
			continue
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, time.Time{}, fmt.Errorf("beszel authentication failed")
		}
		if resp.StatusCode != http.StatusOK {
			return nil, time.Time{}, fmt.Errorf("beszel HTTP %d", resp.StatusCode)
		}
		list, err := decodeSystems(body)
		if err != nil {
			return nil, time.Time{}, err
		}
		return list, httpx.CollectionTime(resp, fallback), nil
	}
	if lastErr != nil {
		return nil, time.Time{}, lastErr
	}
	return nil, time.Time{}, fmt.Errorf("beszel: no systems endpoint available")
}

func decodeSystems(body []byte) ([]systemRow, error) {
	var arr []systemRow
	if err := json.Unmarshal(body, &arr); err == nil && arr != nil {
		return arr, nil
	}
	var wrap struct {
		Items   *[]systemRow `json:"items"`
		Systems *[]systemRow `json:"systems"`
		Data    *[]systemRow `json:"data"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return nil, fmt.Errorf("beszel decode: invalid JSON")
	}
	switch {
	case wrap.Systems != nil:
		return *wrap.Systems, nil
	case wrap.Items != nil:
		return *wrap.Items, nil
	case wrap.Data != nil:
		return *wrap.Data, nil
	}
	return nil, fmt.Errorf("beszel decode: empty systems list")
}

func findSystem(list []systemRow, want string) (systemRow, bool) {
	want = strings.ToLower(strings.TrimSpace(want))
	for _, r := range list {
		names := []string{r.Name, r.System, r.Host, r.Info.Hostname}
		for _, n := range names {
			if strings.ToLower(strings.TrimSpace(n)) == want {
				return r, true
			}
		}
	}
	return systemRow{}, false
}

func (c *Client) itemFrom(serviceID string, row systemRow, observedAt, retrievedAt time.Time) evidence.Item {
	name := row.Name
	if name == "" {
		name = row.System
	}
	if name == "" {
		name = row.Host
	}
	if name == "" {
		name = row.Info.Hostname
	}
	st := strings.ToLower(strings.TrimSpace(row.Status))
	var sev evidence.Severity
	rawSummary := fmt.Sprintf("beszel system %s status=%s", name, st)
	switch st {
	case "", "up", "online", "ok":
		sev = evidence.SeverityInfo
		if st == "" {
			st = "unknown"
			sev = evidence.SeverityUnknown
			rawSummary = fmt.Sprintf("beszel system %s snapshot", name)
		}
	case "down", "offline", "error":
		sev = evidence.SeverityError
		rawSummary = fmt.Sprintf("beszel system %s is %s", name, st)
	case "paused", "pending", "warn", "warning":
		sev = evidence.SeverityWarning
	default:
		sev = evidence.SeverityUnknown
	}
	cleanName, nameRedactions, nameCut := redaction.ApplyAndTruncate(c.Redact, name, 256)
	cleanStatus, statusRedactions, statusCut := redaction.ApplyAndTruncate(c.Redact, st, 64)
	attrs := map[string]any{
		"system_name": cleanName,
		"status":      cleanStatus,
		"found":       true,
		"snapshot":    true,
	}
	if row.CPU != nil {
		attrs["cpu"] = *row.CPU
	}
	if row.Mem != nil {
		attrs["mem"] = *row.Mem
	}
	sum, summaryRedactions, summaryCut := redaction.ApplyAndTruncate(c.Redact, rawSummary, 512)
	return evidence.Item{
		ID:                evidence.NewOpaqueID("ev"),
		ServiceID:         serviceID,
		Source:            evidence.SourceBeszel,
		SourceID:          cleanName,
		Kind:              evidence.KindMetricSample,
		ObservedAt:        observedAt,
		RetrievedAt:       retrievedAt,
		Summary:           sum,
		Severity:          sev,
		Attributes:        attrs,
		Truncated:         nameCut || statusCut || summaryCut,
		RedactionsApplied: nameRedactions + statusRedactions + summaryRedactions,
		Freshness:         evidence.ComputeFreshness(observedAt, retrievedAt),
	}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}
