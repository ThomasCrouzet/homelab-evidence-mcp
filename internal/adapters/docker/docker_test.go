package docker

import (
	"context"
	"encoding/json"
	"errors"
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

func TestStatus_RetrievedAfterCollection(t *testing.T) {
	requestAt := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	collectedAt := requestAt.Add(2 * time.Second)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/containers/json") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"Id": "abc", "Names": []string{"/app"}, "State": "running",
		}})
	}))
	defer ts.Close()
	// The HTTP client clock marks the response body read. The adapter clock
	// gives the pre-request value on the first call and the post-request value
	// on the second.
	hc := httpx.NewLockedClient(httpx.Options{
		Timeout: time.Second,
		Now:     func() time.Time { return collectedAt },
	})
	_ = hc.RegisterDestination("docker", ts.URL)
	calls := 0
	cli := &Client{HTTP: hc, DestName: "docker", Now: func() time.Time {
		calls++
		if calls == 1 {
			return requestAt
		}
		return collectedAt
	}}
	item, err := cli.Status(context.Background(), "svc", "app")
	if err != nil {
		t.Fatal(err)
	}
	if !item.RetrievedAt.Equal(collectedAt) {
		t.Fatalf("retrieved_at=%s, want %s", item.RetrievedAt, collectedAt)
	}
	if item.RetrievedAt.Before(item.ObservedAt) {
		t.Fatalf("retrieved_at %s is before observed_at %s", item.RetrievedAt, item.ObservedAt)
	}
}

func TestFindContainer_CanonicalPrecedenceAndAmbiguity(t *testing.T) {
	list := []containerSummary{
		{ID: "1", Names: []string{"/alias", "/shared"}},
		{ID: "2", Names: []string{"/app", "/shared"}},
	}
	got, ok, err := findContainer(list, "app")
	if err != nil || !ok || got.ID != "2" {
		t.Fatalf("canonical exact: %+v ok=%v err=%v", got, ok, err)
	}
	if _, _, err := findContainer(list, "shared"); !errors.Is(err, ErrAmbiguousContainer) {
		t.Fatalf("shared alias err=%v", err)
	}
	// The same container listed twice with one ID is not ambiguous.
	duplicate := []containerSummary{
		{ID: "same", Names: []string{"/app"}},
		{ID: "same", Names: []string{"/app"}},
	}
	if got, ok, err := findContainer(duplicate, "app"); err != nil || !ok || got.ID != "same" {
		t.Fatalf("duplicate row: %+v ok=%v err=%v", got, ok, err)
	}
	// Exact case wins over a case-folded match.
	caseList := []containerSummary{
		{ID: "upper", Names: []string{"/App"}},
		{ID: "lower", Names: []string{"/app"}},
	}
	got, ok, err = findContainer(caseList, "App")
	if err != nil || !ok || got.ID != "upper" {
		t.Fatalf("case precedence: %+v ok=%v err=%v", got, ok, err)
	}
	// Two case-only canonical matches are ambiguous when no exact case exists.
	if _, _, err := findContainer(caseList, "APP"); !errors.Is(err, ErrAmbiguousContainer) {
		t.Fatalf("case-only collision err=%v", err)
	}
}

func TestStatus_AmbiguousContainerIsError(t *testing.T) {
	cli := setup(t, []map[string]any{
		{"Id": "1", "Names": []string{"/app"}, "State": "running"},
		{"Id": "2", "Names": []string{"/app"}, "State": "exited"},
	}, http.StatusOK)
	if _, err := cli.Status(context.Background(), "svc", "app"); !errors.Is(err, ErrAmbiguousContainer) {
		t.Fatalf("err=%v, want ErrAmbiguousContainer", err)
	}
}

func TestStatus_ExplicitAPIVersion(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.Query().Get("all") != "true" {
			t.Errorf("query=%s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"Id": "abc", "Names": []string{"/app"}, "State": "running",
		}})
	}))
	defer ts.Close()
	hc := httpx.NewLockedClient(httpx.Options{Timeout: time.Second})
	_ = hc.RegisterDestination("docker", ts.URL)
	cli := &Client{HTTP: hc, DestName: "docker", APIVersion: "v1.56"}
	if _, err := cli.Status(context.Background(), "svc", "app"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1.56/containers/json" {
		t.Fatalf("path=%s", gotPath)
	}
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
		t.Fatal("redaction data is missing")
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

func TestStatus_HealthReadiness(t *testing.T) {
	cases := []struct {
		name      string
		status    string
		severity  evidence.Severity
		readiness string
	}{
		{name: "healthy", status: "Up 5 minutes (healthy)", severity: evidence.SeverityInfo, readiness: "ready"},
		{name: "starting", status: "Up 5 seconds (health: starting)", severity: evidence.SeverityWarning, readiness: "starting"},
		{name: "unhealthy", status: "Up 5 minutes (unhealthy)", severity: evidence.SeverityError, readiness: "unhealthy"},
		{name: "no healthcheck", status: "Up 5 minutes", severity: evidence.SeverityInfo, readiness: "unproven"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cli := setup(t, []map[string]any{{
				"Id": "abc", "Names": []string{"/api"}, "State": "running", "Status": tc.status,
			}}, http.StatusOK)
			item, err := cli.Status(context.Background(), "svc", "api")
			if err != nil {
				t.Fatal(err)
			}
			if item.Severity != tc.severity || item.Attributes["health_readiness"] != tc.readiness {
				t.Fatalf("severity=%s readiness=%v summary=%q", item.Severity, item.Attributes["health_readiness"], item.Summary)
			}
			if tc.readiness == "starting" && !strings.Contains(item.Summary, "health=starting") {
				t.Fatalf("starting health is not in the summary: %q", item.Summary)
			}
		})
	}
}
