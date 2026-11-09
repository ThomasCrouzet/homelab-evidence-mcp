package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/audit"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
)

// fullFixtureApp makes an App that includes six adapters for the media service.
func fullFixtureApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("HC_DEMO_TOKEN", "demo-readonly-token-not-real")
	t0 := time.Date(2026, 7, 25, 2, 12, 0, 0, time.UTC)

	gatus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"name": "app", "group": "media", "key": "media_app",
			"results": []map[string]any{
				{"success": false, "status": 503, "timestamp": t0.Format(time.RFC3339)},
			},
		}})
	}))
	t.Cleanup(gatus.Close)

	docker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"Id": "abc", "Names": []string{"/media"}, "State": "running",
			"Status": "Up 1 hour", "Image": "media:latest",
		}})
	}))
	t.Cleanup(docker.Close)

	loki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"result": []map[string]any{{
					"stream": map[string]string{"container": "media"},
					"values": [][]string{{fmt.Sprintf("%d", t0.UnixNano()), "ok"}},
				}},
			},
		})
	}))
	t.Cleanup(loki.Close)

	hc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"checks": []map[string]any{
				{
					"name": "media-cron", "slug": "media-cron", "tags": "media", "status": "down",
					"grace": 300, "last_ping": t0.Format(time.RFC3339),
					"ping_url": "https://hc.example/ping/LEAKME",
				},
				{
					"name": "other-cron", "slug": "other-cron", "tags": "backup", "status": "grace",
					"grace": 60, "last_ping": t0.Add(-time.Minute).Format(time.RFC3339),
				},
			},
		})
	}))
	t.Cleanup(hc.Close)

	beszel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/collections/systems/records", "/api/systems", "/api/systems/", "/api/beszel/systems":
		default:
			t.Errorf("beszel path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": "media-host", "status": "up", "cpu": 42.0, "mem": 60.0},
		})
	}))
	t.Cleanup(beszel.Close)

	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/media-alerts/json") {
			t.Errorf("ntfy path %s", r.URL.Path)
		}
		_, _ = fmt.Fprintf(w, `{"id":"1","time":%d,"event":"message","topic":"media-alerts","message":"disk warn","priority":3}`+"\n", t0.Unix())
	}))
	t.Cleanup(ntfy.Close)

	raw := fmt.Sprintf(`
version: 1
limits:
  default_window: 1h
  max_incident_window: 24h
  max_cron_window: 168h
  max_log_lines: 50
  max_evidence_items: 100
  per_source_timeout: 2s
  total_timeout: 5s
sources:
  gatus:
    kind: gatus
    base_url: %s
  docker-main:
    kind: docker
    base_url: %s
  loki:
    kind: loki
    base_url: %s
  healthchecks:
    kind: healthchecks
    base_url: %s
    token_env: HC_DEMO_TOKEN
  beszel:
    kind: beszel
    base_url: %s
  ntfy:
    kind: ntfy
    base_url: %s
services:
  - id: media
    display_name: Media
    sources:
      gatus:
        source: gatus
        endpoint_key: media_app
      docker:
        source: docker-main
        container_name: media
      loki:
        source: loki
        selector: '{container="media"}'
      healthchecks:
        source: healthchecks
        check_tags: [media]
        status_filter: up
      beszel:
        source: beszel
        system_name: media-host
      ntfy:
        source: ntfy
        topic: media-alerts
`, gatus.URL, docker.URL, loki.URL, hc.URL, beszel.URL, ntfy.URL)

	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := NewApp(cfg, log, audit.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestCollectBeszel_ViaServiceStatus(t *testing.T) {
	app := fullFixtureApp(t)
	res, out, err := app.toolServiceStatus(context.Background(), nil, serviceIDIn{ServiceID: "media"})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.IsError {
		t.Fatalf("tool error: %+v", res)
	}
	b, _ := json.Marshal(out)
	s := string(b)
	if !strings.Contains(s, `"source":"beszel"`) && !strings.Contains(s, "beszel") {
		t.Fatalf("beszel missing from service_status: %s", s)
	}
	if !strings.Contains(s, "media-host") && !strings.Contains(s, "metric") {
		// Attributes may include the system name.
		if !strings.Contains(s, `"kind":"beszel"`) && !strings.Contains(s, "SourceBeszel") {
			// The source outcome must show Beszel as ok.
			if !strings.Contains(s, `"kind":"beszel"`) {
				// JSON uses beszel as the SourceOutcome kind.
				if !strings.Contains(s, "beszel") {
					t.Fatal(s)
				}
			}
		}
	}
	// The Beszel result must have status ok and items.
	var parsed struct {
		Items   []map[string]any `json:"items"`
		Sources []struct {
			Kind   string `json:"kind"`
			Status string `json:"status"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, src := range parsed.Sources {
		if src.Kind == "beszel" {
			found = true
			if src.Status != "ok" {
				t.Fatalf("beszel status=%s", src.Status)
			}
		}
	}
	if !found {
		t.Fatalf("no beszel source outcome: %s", s)
	}
	beszelItem := false
	for _, it := range parsed.Items {
		if it["source"] == "beszel" {
			beszelItem = true
		}
	}
	if !beszelItem {
		t.Fatalf("no beszel evidence item: %s", s)
	}
}

func TestCollectNtfy_ViaIncidentContext(t *testing.T) {
	app := fullFixtureApp(t)
	_, out, err := app.toolIncidentContext(context.Background(), nil, incidentIn{
		ServiceID: "media",
		Start:     "2026-07-25T01:00:00Z",
		End:       "2026-07-25T04:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	var parsed struct {
		Items   []map[string]any `json:"items"`
		Sources []struct {
			Kind   string `json:"kind"`
			Status string `json:"status"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatal(err)
	}
	ntfyOK := false
	for _, src := range parsed.Sources {
		if src.Kind == "ntfy" {
			ntfyOK = src.Status == "ok"
		}
	}
	if !ntfyOK {
		t.Fatalf("ntfy source not ok: %s", b)
	}
	ntfyItem := false
	for _, it := range parsed.Items {
		if it["source"] == "ntfy" {
			ntfyItem = true
		}
	}
	if !ntfyItem {
		t.Fatalf("no ntfy item: %s", b)
	}
}

func TestFailedCrons_ServiceIgnoresUpFilter(t *testing.T) {
	app := fullFixtureApp(t)
	_, out, err := app.toolFailedCrons(context.Background(), nil, failedCronsIn{
		ServiceID: "media",
		Duration:  "24h",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), "media-cron") {
		t.Fatalf("status_filter=up hid current failures: %s", b)
	}
}

func TestIncidentContext_ExcludesCurrentSnapshotsFromHistoricalWindow(t *testing.T) {
	app := fullFixtureApp(t)
	_, out, err := app.toolIncidentContext(context.Background(), nil, incidentIn{
		ServiceID: "media",
		Start:     "2026-07-25T01:00:00Z",
		End:       "2026-07-25T04:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	var parsed struct {
		Items []struct {
			Source string `json:"source"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, item := range parsed.Items {
		if item.Source == "docker" || item.Source == "beszel" || item.Source == "healthchecks" {
			t.Fatalf("current snapshot present in historical window: %s", b)
		}
	}
}

func TestFailedCrons_GlobalRegistryScope(t *testing.T) {
	app := fullFixtureApp(t)
	// Without service_id, the global path reads only registered bindings.
	_, out, err := app.toolFailedCrons(context.Background(), nil, failedCronsIn{
		Duration: "24h",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	s := string(b)
	if strings.Contains(s, "LEAKME") {
		t.Fatal("ping url leaked")
	}
	if !strings.Contains(s, "media-cron") {
		t.Fatalf("media-cron missing: %s", s)
	}
	// other-cron is not bound by any registered service. The global path must
	// not disclose it.
	if strings.Contains(s, "other-cron") {
		t.Fatalf("unregistered check disclosed: %s", s)
	}
	var parsed struct {
		TimeBasis string `json:"time_basis"`
		Items     []struct {
			ServiceID string `json:"service_id"`
		} `json:"items"`
		Sources []struct {
			Kind      string `json:"kind"`
			ServiceID string `json:"service_id"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Items) != 1 || parsed.Items[0].ServiceID != "media" {
		t.Fatalf("global item attribution=%+v: %s", parsed.Items, s)
	}
	if parsed.TimeBasis != "current_snapshot" {
		t.Fatalf("time_basis=%q: %s", parsed.TimeBasis, s)
	}
	foundBinding := false
	for _, source := range parsed.Sources {
		if source.Kind == "healthchecks" && source.ServiceID == "media" {
			foundBinding = true
		}
	}
	if !foundBinding {
		t.Fatalf("global source outcome has no binding identity: %s", s)
	}
}

func TestTools_ReadOnlyAnnotations(t *testing.T) {
	app := fullFixtureApp(t)
	server := app.Server()
	t1, t2 := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = ss.Close() }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0"}, nil)
	session, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 7 {
		t.Fatalf("tools=%d", len(tools.Tools))
	}
	for _, tl := range tools.Tools {
		if tl.Annotations == nil {
			t.Fatalf("tool %s missing annotations", tl.Name)
		}
		if !tl.Annotations.ReadOnlyHint {
			t.Fatalf("tool %s ReadOnlyHint=false", tl.Name)
		}
		openWorld := tl.Name == "service_status" || tl.Name == "incident_context" ||
			tl.Name == "search_logs" || tl.Name == "failed_crons"
		if tl.Annotations.OpenWorldHint == nil || *tl.Annotations.OpenWorldHint != openWorld {
			t.Fatalf("tool %s OpenWorldHint=%v want %v", tl.Name, tl.Annotations.OpenWorldHint, openWorld)
		}
		// Tool names must not imply mutation.
		n := strings.ToLower(tl.Name)
		for _, bad := range []string{"restart", "delete", "write", "exec", "stop", "start", "patch"} {
			if strings.Contains(n, bad) {
				t.Fatalf("mutation-like tool name %s", tl.Name)
			}
		}
	}
}

func TestServiceStatus_StatusFilterMismatchIsNotUnmatched(t *testing.T) {
	app := fullFixtureApp(t)
	_, out, err := app.toolServiceStatus(context.Background(), nil, serviceIDIn{ServiceID: "media"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	var parsed struct {
		Items   []map[string]any `json:"items"`
		Sources []struct {
			Kind     string `json:"kind"`
			Status   string `json:"status"`
			Reason   string `json:"reason"`
			Complete bool   `json:"complete"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatal(err)
	}
	for _, source := range parsed.Sources {
		if source.Kind != "healthchecks" {
			continue
		}
		if source.Status != "ok" || source.Reason != "status_filter_no_match" || !source.Complete {
			t.Fatalf("status filter reason=%+v: %s", source, b)
		}
		for _, item := range parsed.Items {
			if item["source"] == "healthchecks" {
				t.Fatalf("filtered check became evidence: %s", b)
			}
		}
		return
	}
	t.Fatalf("healthchecks source missing: %s", b)
}

func TestFailedCrons_GlobalReusesSourceInventory(t *testing.T) {
	t.Setenv("HC_REUSE_TOKEN", "fixture-token")
	var listCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/checks/" {
			http.NotFound(w, r)
			return
		}
		listCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{"checks": []map[string]any{
			{"name": "one", "slug": "one", "status": "down", "unique_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{"name": "two", "slug": "two", "status": "down", "unique_key": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		}})
	}))
	defer server.Close()
	raw := fmt.Sprintf(`version: 1
limits:
  max_cron_window: 24h
  per_source_timeout: 2s
  total_timeout: 5s
sources:
  hc:
    kind: healthchecks
    base_url: %s
    token_env: HC_REUSE_TOKEN
services:
  - id: one
    sources:
      healthchecks: { source: hc, check_name: one }
  - id: two
    sources:
      healthchecks: { source: hc, check_name: two }
`, server.URL)
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), audit.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	_, out, err := app.toolFailedCrons(context.Background(), nil, failedCronsIn{Duration: "1h"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	if listCalls != 1 {
		t.Fatalf("global failed_crons used %d inventories: %s", listCalls, b)
	}
	var parsed struct {
		Items []struct {
			ServiceID string `json:"service_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Items) != 2 {
		t.Fatalf("per-service items=%+v", parsed.Items)
	}
}

func TestRegistryPresentation_RedactsDisplayName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"name": "api", "key": "api",
			"results": []map[string]any{{"success": true, "status": 200, "timestamp": time.Now().UTC().Format(time.RFC3339)}},
		}})
	}))
	defer server.Close()
	raw := fmt.Sprintf(`version: 1
sources:
  gatus:
    kind: gatus
    base_url: %s
services:
  - id: api
    display_name: "Ops ops@example.com"
    sources:
      gatus: { source: gatus, endpoint_key: api }
`, server.URL)
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), audit.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() (any, error){
		"list_services": func() (any, error) {
			_, out, err := app.toolListServices(context.Background(), nil, listServicesIn{})
			return out, err
		},
		"service_status": func() (any, error) {
			_, out, err := app.toolServiceStatus(context.Background(), nil, serviceIDIn{ServiceID: "api"})
			return out, err
		},
	} {
		out, err := call()
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(out)
		if bytes.Contains(raw, []byte("ops@example.com")) {
			t.Fatalf("%s disclosed the display name: %s", name, raw)
		}
		if !bytes.Contains(raw, []byte("[REDACTED]")) {
			t.Fatalf("%s did not mark the redaction: %s", name, raw)
		}
	}
}
