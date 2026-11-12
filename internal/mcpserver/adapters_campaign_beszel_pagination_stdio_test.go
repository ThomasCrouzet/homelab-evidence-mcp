package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestStdioBeszelPaginationWrappers checks issue 191 through the executable.
// It covers paginated systems/data compatibility envelopes, an impossible
// first page, malformed counters, a failed later page, and unpaginated routes.
func TestStdioBeszelPaginationWrappers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pagedWrapper := func(key, first, second string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/collections/systems/records" {
				http.NotFound(w, r)
				return
			}
			if r.URL.Path != "/api/systems" {
				http.NotFound(w, r)
				return
			}
			if r.URL.Query().Get("page") == "2" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					key:    []map[string]any{{"id": "two", "name": second}},
					"page": 2, "perPage": 1, "totalItems": 2, "totalPages": 2,
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				key:    []map[string]any{{"id": "one", "name": first}},
				"page": 1, "perPage": 1, "totalItems": 2, "totalPages": 2,
			})
		}
	}

	systemsServer := httptest.NewServer(pagedWrapper("systems", "systems-page-one", "systems-page-two"))
	defer systemsServer.Close()
	dataServer := httptest.NewServer(pagedWrapper("data", "data-page-one", "data-page-two"))
	defer dataServer.Close()

	badFirstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"systems": []map[string]any{{"id": "two", "name": "page-two-only"}},
			"page":    2, "perPage": 1, "totalItems": 2, "totalPages": 2,
		})
	}))
	defer badFirstServer.Close()

	malformedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"systems": []map[string]any{{"id": "one", "name": "malformed-host"}},
			"page":    1, "perPage": 0, "totalItems": 2, "totalPages": 2,
		})
	}))
	defer malformedServer.Close()

	lateFailServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"systems": []map[string]any{{"id": "one", "name": "first-page-only"}},
			"page":    1, "perPage": 1, "totalItems": 2, "totalPages": 2,
		})
	}))
	defer lateFailServer.Close()

	plainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/collections/systems/records" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "plain", "name": "plain-host"}},
		})
	}))
	defer plainServer.Close()

	binary := stdioTestBinary(t, ctx)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configText := fmt.Sprintf(`version: 1
limits:
  max_incident_window: 168h
  max_log_lines: 10
  max_evidence_items: 100
  per_source_timeout: 5s
  total_timeout: 20s
sources:
  beszel-systems:
    kind: beszel
    base_url: %s
  beszel-data:
    kind: beszel
    base_url: %s
  beszel-bad-first:
    kind: beszel
    base_url: %s
  beszel-malformed:
    kind: beszel
    base_url: %s
  beszel-late-fail:
    kind: beszel
    base_url: %s
  beszel-plain:
    kind: beszel
    base_url: %s
services:
  - id: systems-page-two
    display_name: Systems page two
    sources:
      beszel: { source: beszel-systems, system_name: systems-page-two }
  - id: systems-missing
    display_name: Systems missing
    sources:
      beszel: { source: beszel-systems, system_name: absent-host }
  - id: data-page-two
    display_name: Data page two
    sources:
      beszel: { source: beszel-data, system_name: data-page-two }
  - id: bad-first
    display_name: Bad first page
    sources:
      beszel: { source: beszel-bad-first, system_name: page-two-only }
  - id: malformed
    display_name: Malformed counters
    sources:
      beszel: { source: beszel-malformed, system_name: malformed-host }
  - id: late-fail
    display_name: Late page failure
    sources:
      beszel: { source: beszel-late-fail, system_name: absent-host }
  - id: plain
    display_name: Plain data wrapper
    sources:
      beszel: { source: beszel-plain, system_name: plain-host }
`, systemsServer.URL, dataServer.URL, badFirstServer.URL, malformedServer.URL, lateFailServer.URL, plainServer.URL)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(ctx, binary, "--config", configPath)
	client := mcp.NewClient(&mcp.Implementation{Name: "beszel-pagination-fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: stdioShutdownTimeout}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	type statusSource struct {
		Kind     string `json:"kind"`
		Status   string `json:"status"`
		Reason   string `json:"reason"`
		Complete bool   `json:"complete"`
		Items    int    `json:"item_count"`
	}
	type statusBody struct {
		Items []struct {
			Source     string         `json:"source"`
			Attributes map[string]any `json:"attributes"`
		} `json:"items"`
		Sources        []statusSource `json:"sources"`
		Complete       bool           `json:"complete"`
		SourceComplete bool           `json:"source_complete"`
	}
	status := func(serviceID string) statusBody {
		t.Helper()
		result := adapterCallTool(t, ctx, session, "service_status", map[string]any{"service_id": serviceID})
		var body statusBody
		adapterDecodeContent(t, result, &body)
		return body
	}
	beszelSource := func(body statusBody) statusSource {
		t.Helper()
		for _, source := range body.Sources {
			if source.Kind == "beszel" {
				return source
			}
		}
		t.Fatalf("no beszel source outcome: %+v", body.Sources)
		return statusSource{}
	}
	foundName := func(body statusBody) (string, bool) {
		t.Helper()
		for _, item := range body.Items {
			if item.Source != "beszel" {
				continue
			}
			if found, _ := item.Attributes["found"].(bool); found {
				name, _ := item.Attributes["system_name"].(string)
				return name, true
			}
		}
		return "", false
	}

	for _, tc := range []struct {
		name      string
		serviceID string
		wantName  string
	}{
		{"systems wrapper fetches page two", "systems-page-two", "systems-page-two"},
		{"data wrapper fetches page two", "data-page-two", "data-page-two"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := status(tc.serviceID)
			name, found := foundName(body)
			if !found || name != tc.wantName {
				t.Fatalf("target on page 2 not found: %+v", body.Items)
			}
			if len(body.Items) != 1 || body.Items[0].Attributes["pages"] != float64(2) {
				t.Fatalf("pages=%v items=%d", body.Items[0].Attributes["pages"], len(body.Items))
			}
			source := beszelSource(body)
			if source.Status != "ok" || !source.Complete || !body.SourceComplete {
				t.Fatalf("full inventory not reported complete: source=%+v body=%+v", source, body)
			}
		})
	}

	t.Run("missing target scans both pages", func(t *testing.T) {
		missing := status("systems-missing")
		if _, found := foundName(missing); found {
			t.Fatalf("absent system reported as found: %+v", missing.Items)
		}
		if len(missing.Items) != 1 || missing.Items[0].Attributes["pages"] != float64(2) {
			t.Fatalf("absent system did not scan both pages: %+v", missing.Items)
		}
		// An unmatched binding stays incomplete by design. The pages count
		// proves the adapter read the full inventory before it reported this.
		if source := beszelSource(missing); source.Status != "ok" || source.Reason != "unmatched_binding" {
			t.Fatalf("complete two-page scan did not stay a binding result: source=%+v", source)
		}
	})

	for _, tc := range []struct {
		name      string
		serviceID string
	}{
		{"impossible first page is an error", "bad-first"},
		{"malformed counters are an error", "malformed"},
		{"failed later page is not complete", "late-fail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := status(tc.serviceID)
			if _, found := foundName(body); found {
				t.Fatalf("partial inventory produced a found item: %+v", body.Items)
			}
			for _, item := range body.Items {
				if item.Source == "beszel" && item.Attributes["found"] == false {
					t.Fatalf("partial inventory produced a not-found item: %+v", body.Items)
				}
			}
			source := beszelSource(body)
			if source.Status != "error" || source.Complete || body.SourceComplete || body.Complete {
				t.Fatalf("partial inventory stayed complete: source=%+v body=%+v", source, body)
			}
		})
	}

	t.Run("unpaginated data envelope works", func(t *testing.T) {
		plain := status("plain")
		name, found := foundName(plain)
		if !found || name != "plain-host" || plain.Items[0].Attributes["pages"] != float64(1) {
			t.Fatalf("unpaginated data envelope regressed: %+v", plain.Items)
		}
		if source := beszelSource(plain); source.Status != "ok" || !source.Complete || !plain.SourceComplete {
			t.Fatalf("unpaginated data envelope not complete: source=%+v", source)
		}
	})
}
