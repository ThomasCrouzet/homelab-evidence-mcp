// Package beszel contains the read-only Beszel status adapter.
// Compatible routes give a JSON array or wrapper.
package beszel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	ID     string   `json:"id"`
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

// ErrSystemAmbiguous reports more than one system at the same identity tier.
var ErrSystemAmbiguous = errors.New("beszel system selection is ambiguous")

// fetchResult gives the complete inventory and its collection-age facts.
type fetchResult struct {
	Rows       []systemRow
	ObservedAt time.Time
	PageCount  int
	MixedAge   bool
	Overlap    bool
	Unverified bool
}

// Status gives a snapshot for systemName.
func (c *Client) Status(ctx context.Context, serviceID, systemName string) (evidence.Item, error) {
	requestedAt := c.now()
	result, err := c.fetchSystems(ctx, requestedAt)
	if err != nil {
		return evidence.Item{}, err
	}
	retrievedAt := c.now()
	row, ok, err := findSystem(result.Rows, systemName)
	if err != nil {
		return evidence.Item{}, err
	}
	if !ok {
		cleanName, redactions, truncated := redaction.ApplyAndTruncate(c.Redact, systemName, 256)
		return evidence.Item{
			ID:                evidence.NewOpaqueID("ev"),
			ServiceID:         serviceID,
			Source:            evidence.SourceBeszel,
			SourceID:          cleanName,
			Kind:              evidence.KindMetricSample,
			ObservedAt:        result.ObservedAt,
			RetrievedAt:       retrievedAt,
			Summary:           "beszel system not found",
			Severity:          evidence.SeverityUnknown,
			Attributes:        c.collectionAttributes(result, map[string]any{"system_name": cleanName, "found": false}),
			Truncated:         truncated,
			RedactionsApplied: redactions,
			Freshness:         evidence.FreshnessMissing,
		}, nil
	}
	item := c.itemFrom(serviceID, row, result.ObservedAt, retrievedAt)
	item.Attributes = c.collectionAttributes(result, item.Attributes)
	return item, nil
}

func (c *Client) collectionAttributes(result fetchResult, attrs map[string]any) map[string]any {
	if attrs == nil {
		attrs = map[string]any{}
	}
	attrs["pages"] = result.PageCount
	if result.MixedAge {
		attrs["mixed_page_age"] = true
	}
	if result.Overlap {
		attrs["pagination_overlap"] = true
	}
	if result.Unverified {
		attrs["pagination_unverified"] = true
	}
	return attrs
}

// Evidence gives Status in a list.
func (c *Client) Evidence(ctx context.Context, serviceID, systemName string) ([]evidence.Item, error) {
	it, err := c.Status(ctx, serviceID, systemName)
	if err != nil {
		return nil, err
	}
	return []evidence.Item{it}, nil
}

func (c *Client) fetchSystems(ctx context.Context, fallback time.Time) (fetchResult, error) {
	paths := []string{
		"/api/collections/systems/records",
		"/api/systems",
		"/api/beszel/systems",
		"/api/systems/",
	}
	var lastErr error
	for _, path := range paths {
		if path == "/api/collections/systems/records" {
			page, collectedAt, err := c.fetchPage(ctx, path, 1, beszelPageSize, fallback)
			if err != nil {
				lastErr = err
				continue
			}
			result, err := c.collectPages(ctx, path, page, collectedAt, fallback)
			if err != nil {
				return fetchResult{}, err
			}
			return result, nil
		}
		// LockedClient.Get closes the body before it gives the response.
		resp, body, err := c.HTTP.Get(ctx, c.DestName, path, c.Headers) //nolint:bodyclose
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusNotFound {
			lastErr = fmt.Errorf("beszel returned 404")
			continue
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fetchResult{}, fmt.Errorf("beszel authentication failed")
		}
		if resp.StatusCode != http.StatusOK {
			return fetchResult{}, fmt.Errorf("beszel HTTP %d", resp.StatusCode)
		}
		page, err := decodeSystemPage(body)
		if err != nil {
			return fetchResult{}, err
		}
		collectedAt := httpx.CollectionTime(resp, fallback)
		result, err := c.collectPages(ctx, path, page, collectedAt, fallback)
		if err != nil {
			return fetchResult{}, err
		}
		return result, nil
	}
	if lastErr != nil {
		return fetchResult{}, lastErr
	}
	return fetchResult{}, fmt.Errorf("beszel: no systems endpoint available")
}

func (c *Client) collectPages(ctx context.Context, path string, first systemPage, firstAt, fallback time.Time) (fetchResult, error) {
	result := fetchResult{Rows: append([]systemRow(nil), first.Items...), ObservedAt: firstAt, PageCount: 1}
	ids := map[string]bool{}
	missingIDs := false
	for _, row := range first.Items {
		if row.ID == "" {
			missingIDs = true
			continue
		}
		if ids[row.ID] {
			result.Overlap = true
		}
		ids[row.ID] = true
	}
	if !first.Paginated {
		result.Unverified = false
		return result, nil
	}
	if first.Page < 1 || first.TotalPages < 1 {
		return fetchResult{}, fmt.Errorf("beszel pagination metadata is incomplete")
	}
	if first.TotalPages > maxBeszelPages {
		return fetchResult{}, fmt.Errorf("beszel collection exceeds %d pages", maxBeszelPages)
	}
	perPage := first.PerPage
	if perPage <= 0 {
		perPage = beszelPageSize
	}
	for pageNumber := 2; pageNumber <= first.TotalPages; pageNumber++ {
		page, collectedAt, err := c.fetchPage(ctx, path, pageNumber, perPage, fallback)
		if err != nil {
			return fetchResult{}, fmt.Errorf("beszel pagination failed on page %d: %w", pageNumber, err)
		}
		if !page.Paginated || page.Page != pageNumber || page.TotalPages != first.TotalPages ||
			page.HasTotalItems != first.HasTotalItems ||
			(first.HasTotalItems && page.TotalItems != first.TotalItems) {
			return fetchResult{}, fmt.Errorf("beszel pagination changed during collection")
		}
		result.PageCount++
		if collectedAt.Before(result.ObservedAt) {
			result.ObservedAt = collectedAt
		}
		if collectedAt.Sub(firstAt) > time.Second || firstAt.Sub(collectedAt) > time.Second {
			result.MixedAge = true
		}
		if len(page.Items) > maxBeszelRecords-len(result.Rows) {
			return fetchResult{}, fmt.Errorf("beszel collection exceeds %d records", maxBeszelRecords)
		}
		for _, row := range page.Items {
			if row.ID == "" {
				missingIDs = true
				continue
			}
			if ids[row.ID] {
				result.Overlap = true
			}
			ids[row.ID] = true
		}
		result.Rows = append(result.Rows, page.Items...)
	}
	if first.HasTotalItems && len(result.Rows) != first.TotalItems {
		return fetchResult{}, fmt.Errorf("beszel collection item count does not match totalItems")
	}
	if len(result.Rows) > maxBeszelRecords {
		return fetchResult{}, fmt.Errorf("beszel collection exceeds %d records", maxBeszelRecords)
	}
	result.Unverified = missingIDs
	return result, nil
}

func (c *Client) fetchPage(ctx context.Context, path string, pageNumber, perPage int, fallback time.Time) (systemPage, time.Time, error) {
	query := url.Values{}
	query.Set("page", strconv.Itoa(pageNumber))
	query.Set("perPage", strconv.Itoa(perPage))
	requestPath := path + "?" + query.Encode()
	// LockedClient.Get closes the body before it gives the response.
	resp, body, err := c.HTTP.Get(ctx, c.DestName, requestPath, c.Headers) //nolint:bodyclose
	if err != nil {
		return systemPage{}, time.Time{}, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return systemPage{}, time.Time{}, fmt.Errorf("beszel returned 404")
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return systemPage{}, time.Time{}, fmt.Errorf("beszel authentication failed")
	}
	if resp.StatusCode != http.StatusOK {
		return systemPage{}, time.Time{}, fmt.Errorf("beszel HTTP %d", resp.StatusCode)
	}
	result, err := decodeSystemPage(body)
	if err != nil {
		return systemPage{}, time.Time{}, err
	}
	return result, httpx.CollectionTime(resp, fallback), nil
}

type systemPage struct {
	Items         []systemRow
	Page          int
	PerPage       int
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
			Items: *wrap.Items, Page: *wrap.Page, PerPage: *wrap.PerPage, TotalItems: totalItems,
			TotalPages: totalPages, Paginated: true, HasTotalItems: wrap.TotalItems != nil,
		}, nil
	}
	return systemPage{}, fmt.Errorf("beszel decode: empty systems list")
}

// findSystem scans the whole collection by identity tier. Canonical names win
// over host aliases, and more than one match in the winning tier is ambiguous.
func findSystem(list []systemRow, want string) (systemRow, bool, error) {
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" {
		return systemRow{}, false, nil
	}
	tiers := [][]func(systemRow) string{
		{func(r systemRow) string { return r.Name }, func(r systemRow) string { return r.System }},
		{func(r systemRow) string { return r.Host }, func(r systemRow) string { return r.Info.Hostname }},
	}
	for _, tier := range tiers {
		matched := map[string]int{}
		for index, row := range list {
			for _, field := range tier {
				if strings.ToLower(strings.TrimSpace(field(row))) == want {
					key := row.ID
					if key == "" {
						key = fmt.Sprintf("#%d", index)
					}
					matched[key] = index
					break
				}
			}
		}
		if len(matched) == 0 {
			continue
		}
		if len(matched) > 1 {
			return systemRow{}, false, ErrSystemAmbiguous
		}
		for _, index := range matched {
			return list[index], true, nil
		}
	}
	return systemRow{}, false, nil
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
	// Info values are documented percentages. Legacy top-level values keep an
	// unknown unit because older API variants do not prove the same meaning.
	cpuInfo := validPercent(row.Info.CPU)
	cpuLegacy := validPercent(row.CPU)
	switch {
	case cpuInfo != nil:
		attrs["cpu_percent"] = *cpuInfo
		attrs["cpu_source"] = "info"
		attrs["cpu"] = *cpuInfo
	case row.Info.CPU != nil:
		attrs["cpu_invalid"] = true
	case cpuLegacy != nil:
		attrs["cpu_legacy"] = *cpuLegacy
		attrs["cpu_unit"] = "unknown"
		attrs["cpu"] = *cpuLegacy
	case row.CPU != nil:
		attrs["cpu_invalid"] = true
	}
	memInfo := validPercent(row.Info.Mem)
	memLegacy := validPercent(row.Mem)
	switch {
	case memInfo != nil:
		attrs["memory_percent"] = *memInfo
		attrs["memory_source"] = "info.mp"
		attrs["mem"] = *memInfo
	case row.Info.Mem != nil:
		attrs["memory_invalid"] = true
	case memLegacy != nil:
		attrs["memory_legacy"] = *memLegacy
		attrs["memory_unit"] = "unknown"
		attrs["mem"] = *memLegacy
	case row.Mem != nil:
		attrs["memory_invalid"] = true
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

// validPercent accepts a finite percentage from 0 through 100.
func validPercent(value *float64) *float64 {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 100 {
		return nil
	}
	v := *value
	return &v
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}
