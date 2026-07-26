package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
)

func setup(t *testing.T, body any, status int) *Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method %s", r.Method)
		}
		if !strings.HasPrefix(r.URL.Path, "/containers/json") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		if status == 200 {
			_ = json.NewEncoder(w).Encode(body)
		}
	}))
	t.Cleanup(ts.Close)
	hc := httpx.NewLockedClient(httpx.Options{Timeout: 2 * time.Second})
	_ = hc.RegisterDestination("docker", ts.URL)
	eng, _ := redaction.New(nil)
	return &Client{HTTP: hc, DestName: "docker", Redact: eng, Now: func() time.Time {
		return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	}}
}

func TestRunningHealthy(t *testing.T) {
	cli := setup(t, []map[string]any{{
		"Id": "abc123", "Names": []string{"/jellyfin"}, "Image": "jellyfin:latest",
		"State": "running", "Status": "Up 2 hours (healthy)", "Created": 1700000000,
		"Labels": map[string]string{"com.docker.compose.service": "jellyfin"},
	}}, 200)
	item, err := cli.Status(context.Background(), "jellyfin", "jellyfin")
	if err != nil {
		t.Fatal(err)
	}
	if item.Severity != evidence.SeverityInfo {
		t.Fatal(item.Severity)
	}
	if item.Attributes["health"] != "healthy" {
		t.Fatal(item.Attributes)
	}
	if item.Attributes["found"] != true {
		t.Fatal("found")
	}
}

func TestRestarting(t *testing.T) {
	cli := setup(t, []map[string]any{{
		"Id": "x", "Names": []string{"/app"}, "State": "restarting", "Status": "Restarting (1) 5 seconds ago",
	}}, 200)
	item, err := cli.Status(context.Background(), "app", "app")
	if err != nil {
		t.Fatal(err)
	}
	if item.Severity != evidence.SeverityWarning {
		t.Fatal(item.Severity)
	}
}

func TestExitedUnhealthy(t *testing.T) {
	cli := setup(t, []map[string]any{{
		"Id": "x", "Names": []string{"/app"}, "State": "running", "Status": "Up 1 minute (unhealthy)",
	}}, 200)
	item, err := cli.Status(context.Background(), "app", "app")
	if err != nil {
		t.Fatal(err)
	}
	if item.Severity != evidence.SeverityError {
		t.Fatal(item.Severity)
	}
}

func TestNameWithSlashAndMultipleNames(t *testing.T) {
	cli := setup(t, []map[string]any{{
		"Id": "x", "Names": []string{"/foo", "/bar"}, "State": "running", "Status": "Up",
	}}, 200)
	item, err := cli.Status(context.Background(), "s", "FOO")
	if err != nil {
		t.Fatal(err)
	}
	if item.Attributes["found"] != true {
		t.Fatal(item)
	}
}

func TestNotFound(t *testing.T) {
	cli := setup(t, []map[string]any{}, 200)
	item, err := cli.Status(context.Background(), "s", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if item.Freshness != evidence.FreshnessMissing {
		t.Fatal(item.Freshness)
	}
}

func TestProxyErrorNotEmptyList(t *testing.T) {
	cli := setup(t, nil, 500)
	_, err := cli.Status(context.Background(), "s", "x")
	if err == nil {
		t.Fatal("expected error, not empty success")
	}
}

func TestInvalidJSONNotEmptyList(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer ts.Close()
	hc := httpx.NewLockedClient(httpx.Options{Timeout: time.Second})
	_ = hc.RegisterDestination("docker", ts.URL)
	cli := &Client{HTTP: hc, DestName: "docker"}
	_, err := cli.Status(context.Background(), "s", "x")
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("got %v", err)
	}
}

func TestNullNotEmptyList(t *testing.T) {
	cli := setup(t, nil, 200)
	if _, err := cli.Status(context.Background(), "s", "x"); err == nil {
		t.Fatal("a null list must not become a valid not-found result")
	}
}

func TestStatus_RedactsAllTextAttributes(t *testing.T) {
	cli := setup(t, []map[string]any{{
		"Id":     "x",
		"Names":  []string{"/password=supersecret"},
		"Image":  "token=anothersecret",
		"State":  "running",
		"Status": "Up",
	}}, 200)
	item, err := cli.Status(context.Background(), "s", "password=supersecret")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(item)
	if strings.Contains(string(raw), "supersecret") || strings.Contains(string(raw), "anothersecret") {
		t.Fatalf("attribute not redacted: %s", raw)
	}
	if item.RedactionsApplied == 0 {
		t.Fatal("no redaction reported")
	}
}

func TestStatus_CachePreservesSnapshotTime(t *testing.T) {
	var calls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"Id": "x", "Names": []string{"/app"}, "State": "running", "Status": "Up",
		}})
	}))
	defer ts.Close()

	hc := httpx.NewLockedClient(httpx.Options{
		Timeout:  time.Second,
		CacheTTL: time.Hour,
	})
	if err := hc.RegisterDestination("docker", ts.URL); err != nil {
		t.Fatal(err)
	}
	retrievedAt := time.Now().UTC()
	cli := &Client{
		HTTP: hc, DestName: "docker",
		Now: func() time.Time { return retrievedAt },
	}
	if _, err := cli.Status(context.Background(), "app", "app"); err != nil {
		t.Fatal(err)
	}

	retrievedAt = retrievedAt.Add(20 * time.Minute)
	item, err := cli.Status(context.Background(), "app", "app")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("upstream calls=%d, cache not used", calls)
	}
	if !item.ObservedAt.Before(item.RetrievedAt) {
		t.Fatalf("inconsistent timestamps: observed=%s retrieved=%s", item.ObservedAt, item.RetrievedAt)
	}
	if item.Freshness != evidence.FreshnessStale {
		t.Fatalf("freshness=%s, want=%s", item.Freshness, evidence.FreshnessStale)
	}
}
