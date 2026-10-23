// Package beszel contains the read-only Beszel status adapter.
// Compatible routes give a JSON array or wrapper.
package beszel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
)

const (
	beszelPageSize   = 200
	maxBeszelPages   = 32
	maxBeszelRecords = beszelPageSize * maxBeszelPages
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
		Hostname string   `json:"h"`
		CPU      *float64 `json:"cpu"`
		Mem      *float64 `json:"mp"`
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
		if p == "/api/collections/systems/records" {
			list, observedAt, tryAlternate, err := c.fetchPocketBaseSystems(ctx, p, fallback)
			if tryAlternate {
				lastErr = err
				continue
			}
			if err != nil {
				return nil, time.Time{}, err
			}
			return list, observedAt, nil
		}
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

func (c *Client) fetchPocketBaseSystems(ctx context.Context, path string, fallback time.Time) ([]systemRow, time.Time, bool, error) {
	var all []systemRow
	var observedAt time.Time
	expectedPages := 1
	expectedTotalItems := 0
	expectedHasTotalItems := false
	for pageNumber := 1; pageNumber <= expectedPages; pageNumber++ {
		query := url.Values{}
		query.Set("page", strconv.Itoa(pageNumber))
		query.Set("perPage", strconv.Itoa(beszelPageSize))
		requestPath := path + "?" + query.Encode()
		// LockedClient.Get closes the body before it gives the response.
		resp, body, err := c.HTTP.Get(ctx, c.DestName, requestPath, c.Headers) //nolint:bodyclose
		if err != nil {
			return nil, time.Time{}, pageNumber == 1, err
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, time.Time{}, pageNumber == 1, fmt.Errorf("beszel returned 404")
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, time.Time{}, false, fmt.Errorf("beszel authentication failed")
		}
		if resp.StatusCode != http.StatusOK {
			return nil, time.Time{}, false, fmt.Errorf("beszel HTTP %d", resp.StatusCode)
		}
		if observedAt.IsZero() {
			observedAt = httpx.CollectionTime(resp, fallback)
		}
		result, err := decodeSystemPage(body)
		if err != nil {
			return nil, time.Time{}, false, err
		}
		if result.HasTotalItems && result.TotalItems > maxBeszelRecords {
			return nil, time.Time{}, false, fmt.Errorf("beszel collection exceeds %d records", maxBeszelRecords)
		}
		if len(result.Items) > maxBeszelRecords-len(all) {
			return nil, time.Time{}, false, fmt.Errorf("beszel collection exceeds %d records", maxBeszelRecords)
		}
		all = append(all, result.Items...)
		if !result.Paginated {
			return all, observedAt, false, nil
		}
		if result.Page < 1 || result.TotalPages < 1 {
			return nil, time.Time{}, false, fmt.Errorf("beszel pagination metadata is incomplete")
		}
		if pageNumber == 1 {
			expectedPages = result.TotalPages
			expectedTotalItems = result.TotalItems
			expectedHasTotalItems = result.HasTotalItems
			if expectedPages > maxBeszelPages {
				return nil, time.Time{}, false, fmt.Errorf("beszel collection exceeds %d pages", maxBeszelPages)
			}
		}
		if result.Page != pageNumber || result.TotalPages != expectedPages ||
			result.HasTotalItems != expectedHasTotalItems ||
			(result.HasTotalItems && result.TotalItems != expectedTotalItems) {
			return nil, time.Time{}, false, fmt.Errorf("beszel pagination changed during collection")
		}
	}
	if expectedHasTotalItems && len(all) != expectedTotalItems {
		return nil, time.Time{}, false, fmt.Errorf("beszel collection item count does not match totalItems")
	}
	return all, observedAt, false, nil
}

type systemPage struct {
	Items         []systemRow
	Page          int
	TotalItems    int
	TotalPages    int
	Paginated     bool
	HasTotalItems bool
}

func decodeSystemPage(body []byte) (systemPage, error) {
	var arr []systemRow
	if err := json.Unmarshal(body, &arr); err == nil && arr != nil {
		return systemPage{Items: arr}, nil
	}
	var wrap struct {
		Items      *[]systemRow `json:"items"`
		Systems    *[]systemRow `json:"systems"`
		Data       *[]systemRow `json:"data"`
		Page       *int         `json:"page"`
		PerPage    *int         `json:"perPage"`
		TotalItems *int         `json:"totalItems"`
		TotalPages *int         `json:"totalPages"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return systemPage{}, fmt.Errorf("beszel decode: invalid JSON")
	}
	switch {
	case wrap.Systems != nil:
		return systemPage{Items: *wrap.Systems}, nil
	case wrap.Data != nil:
		return systemPage{Items: *wrap.Data}, nil
	case wrap.Items != nil:
		paginated := wrap.Page != nil || wrap.PerPage != nil || wrap.TotalItems != nil || wrap.TotalPages != nil
		if !paginated {
			return systemPage{Items: *wrap.Items}, nil
		}
		if wrap.Page == nil || wrap.PerPage == nil || *wrap.Page < 1 || *wrap.PerPage < 1 {
			return systemPage{}, fmt.Errorf("beszel pagination metadata is incomplete")
		}
		if wrap.TotalItems != nil && *wrap.TotalItems < 0 {
			return systemPage{}, fmt.Errorf("beszel pagination metadata is invalid")
		}
		if wrap.TotalPages != nil && *wrap.TotalPages < 0 {
			return systemPage{}, fmt.Errorf("beszel pagination metadata is invalid")
		}
		totalPages := 0
		switch {
		case wrap.TotalPages != nil && *wrap.TotalPages > 0:
			totalPages = *wrap.TotalPages
		case wrap.TotalPages != nil && *wrap.TotalPages == 0:
			if wrap.TotalItems == nil || *wrap.TotalItems != 0 || len(*wrap.Items) != 0 {
				return systemPage{}, fmt.Errorf("beszel pagination metadata is incomplete")
			}
			totalPages = 1
		case wrap.TotalItems != nil:
			totalPages = *wrap.TotalItems / *wrap.PerPage
			if *wrap.TotalItems%*wrap.PerPage != 0 {
				totalPages++
			}
			if totalPages == 0 {
				totalPages = 1
			}
		default:
			return systemPage{}, fmt.Errorf("beszel pagination metadata is incomplete")
		}
		if *wrap.Page > totalPages {
			return systemPage{}, fmt.Errorf("beszel pagination metadata is invalid")
		}
		totalItems := 0
		if wrap.TotalItems != nil {
			totalItems = *wrap.TotalItems
			if totalItems < len(*wrap.Items) {
				return systemPage{}, fmt.Errorf("beszel pagination metadata is invalid")
			}
		}
		return systemPage{
			Items: *wrap.Items, Page: *wrap.Page, TotalItems: totalItems,
			TotalPages: totalPages, Paginated: true, HasTotalItems: wrap.TotalItems != nil,
		}, nil
	}
	return systemPage{}, fmt.Errorf("beszel decode: empty systems list")
}

func decodeSystems(body []byte) ([]systemRow, error) {
	result, err := decodeSystemPage(body)
	return result.Items, err
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
	cpu := row.Info.CPU
	if cpu == nil {
		cpu = row.CPU
	}
	if cpu != nil {
		attrs["cpu"] = *cpu
	}
	mem := row.Info.Mem
	if mem == nil {
		mem = row.Mem
	}
	if mem != nil {
		attrs["mem"] = *mem
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
