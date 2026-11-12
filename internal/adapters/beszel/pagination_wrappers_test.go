package beszel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
)

// paginationWrapperClient starts an upstream fixture and returns a client.
func paginationWrapperClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	hc := httpx.NewLockedClient(httpx.Options{Timeout: time.Second})
	if err := hc.RegisterDestination("beszel", upstream.URL); err != nil {
		t.Fatal(err)
	}
	return &Client{HTTP: hc, DestName: "beszel"}
}

func TestPaginationWrappersDecodePaginatedEnvelopes(t *testing.T) {
	for _, key := range []string{"systems", "data"} {
		t.Run(key, func(t *testing.T) {
			raw := []byte(`{"` + key + `":[{"id":"one","name":"one"}],` +
				`"page":1,"perPage":1,"totalItems":2,"totalPages":2}`)
			page, err := decodeSystemPage(raw)
			if err != nil {
				t.Fatalf("decode %s envelope: %v", key, err)
			}
			if !page.Paginated {
				t.Fatalf("%s envelope lost the pagination marker: %+v", key, page)
			}
			if page.Page != 1 || page.PerPage != 1 || page.TotalPages != 2 || !page.HasTotalItems || page.TotalItems != 2 {
				t.Fatalf("%s envelope lost pagination metadata: %+v", key, page)
			}
		})
	}
}

func TestPaginationWrappersStatusFetchesEveryPage(t *testing.T) {
	for _, key := range []string{"systems", "data"} {
		t.Run(key, func(t *testing.T) {
			pageTwoRequests := 0
			cli := paginationWrapperClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/collections/systems/records" {
					http.NotFound(w, r)
					return
				}
				if r.URL.Path != "/api/systems" {
					http.NotFound(w, r)
					return
				}
				if r.URL.Query().Get("page") == "2" {
					pageTwoRequests++
					_ = json.NewEncoder(w).Encode(map[string]any{
						key:    []map[string]any{{"id": "two", "name": "page-two-host"}},
						"page": 2, "perPage": 1, "totalItems": 2, "totalPages": 2,
					})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					key:    []map[string]any{{"id": "one", "name": "page-one-host"}},
					"page": 1, "perPage": 1, "totalItems": 2, "totalPages": 2,
				})
			})
			item, err := cli.Status(context.Background(), "svc", "page-two-host")
			if err != nil {
				t.Fatal(err)
			}
			if item.Attributes["found"] != true || item.Attributes["system_name"] != "page-two-host" {
				t.Fatalf("%s page-2 target not found: %+v", key, item.Attributes)
			}
			if item.Attributes["pages"] != 2 || pageTwoRequests != 1 {
				t.Fatalf("%s did not read page 2 once: pages=%v requests=%d", key, item.Attributes["pages"], pageTwoRequests)
			}
		})
	}
}

func TestPaginationWrappersMissingTargetScansAllPages(t *testing.T) {
	for _, key := range []string{"systems", "data"} {
		t.Run(key, func(t *testing.T) {
			pageTwoRequests := 0
			cli := paginationWrapperClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/systems" {
					http.NotFound(w, r)
					return
				}
				if r.URL.Query().Get("page") == "2" {
					pageTwoRequests++
					_ = json.NewEncoder(w).Encode(map[string]any{
						key:    []map[string]any{{"id": "two", "name": "page-two-host"}},
						"page": 2, "perPage": 1, "totalItems": 2, "totalPages": 2,
					})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					key:    []map[string]any{{"id": "one", "name": "page-one-host"}},
					"page": 1, "perPage": 1, "totalItems": 2, "totalPages": 2,
				})
			})
			item, err := cli.Status(context.Background(), "svc", "absent-host")
			if err != nil {
				t.Fatal(err)
			}
			if item.Attributes["found"] != false {
				t.Fatalf("%s reported a missing target as found: %+v", key, item.Attributes)
			}
			if item.Attributes["pages"] != 2 || pageTwoRequests != 1 {
				t.Fatalf("%s declared the target missing before page 2: pages=%v requests=%d", key, item.Attributes["pages"], pageTwoRequests)
			}
		})
	}
}

func TestPaginationWrappersRejectsImpossibleFirstPage(t *testing.T) {
	for _, key := range []string{"items", "systems", "data"} {
		t.Run(key, func(t *testing.T) {
			requests := 0
			cli := paginationWrapperClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				_ = json.NewEncoder(w).Encode(map[string]any{
					key:    []map[string]any{{"id": "two", "name": "page-two-host"}},
					"page": 2, "perPage": 1, "totalItems": 2, "totalPages": 2,
				})
			})
			_, err := cli.Status(context.Background(), "svc", "page-two-host")
			if err == nil {
				t.Fatalf("%s first response declared page 2 and was accepted", key)
			}
			if !strings.Contains(err.Error(), "page 1") {
				t.Fatalf("%s rejection does not name page 1: %v", key, err)
			}
			if requests != 1 {
				t.Fatalf("%s continued after the impossible first page: requests=%d", key, requests)
			}
		})
	}
}

func TestPaginationWrappersRejectsMalformedCounters(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			"systems page zero",
			`{"systems":[{"id":"one","name":"one"}],"page":0,"perPage":1,"totalItems":1,"totalPages":1}`,
		},
		{
			"systems perPage zero",
			`{"systems":[{"id":"one","name":"one"}],"page":1,"perPage":0,"totalItems":1,"totalPages":1}`,
		},
		{
			"systems negative totalItems",
			`{"systems":[{"id":"one","name":"one"}],"page":1,"perPage":1,"totalItems":-1,"totalPages":1}`,
		},
		{
			"data negative totalPages",
			`{"data":[{"id":"one","name":"one"}],"page":1,"perPage":1,"totalItems":1,"totalPages":-1}`,
		},
		{
			"data page after last page",
			`{"data":[{"id":"one","name":"one"}],"page":3,"perPage":1,"totalItems":2,"totalPages":2}`,
		},
		{
			"systems empty page count with records",
			`{"systems":[{"id":"one","name":"one"}],"page":1,"perPage":1,"totalItems":0,"totalPages":0}`,
		},
		{
			"data page without totals",
			`{"data":[{"id":"one","name":"one"}],"page":1,"perPage":1}`,
		},
		{
			"systems totalItems below page size",
			`{"systems":[{"id":"one","name":"one"},{"id":"two","name":"two"}],"page":1,"perPage":2,"totalItems":1}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeSystemPage([]byte(tc.body)); err == nil {
				t.Fatalf("malformed counters were accepted: %s", tc.body)
			} else if !strings.Contains(err.Error(), "pagination metadata") {
				t.Fatalf("error does not name the pagination metadata: %v", err)
			}
		})
	}
}

func TestPaginationWrappersUnpaginatedEnvelopes(t *testing.T) {
	cases := map[string]string{
		"array":         `[{"id":"one","name":"one"}]`,
		"empty array":   `[]`,
		"systems":       `{"systems":[{"id":"one","name":"one"}]}`,
		"empty systems": `{"systems":[]}`,
		"data":          `{"data":[{"id":"one","name":"one"}]}`,
		"empty data":    `{"data":[]}`,
		"items":         `{"items":[{"id":"one","name":"one"}]}`,
		"empty items":   `{"items":[]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			page, err := decodeSystemPage([]byte(body))
			if err != nil {
				t.Fatalf("unpaginated %s rejected: %v", name, err)
			}
			if page.Paginated {
				t.Fatalf("unpaginated %s became paginated: %+v", name, page)
			}
		})
	}
}
