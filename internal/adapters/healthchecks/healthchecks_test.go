package healthchecks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
)

func setup(t *testing.T, status int, body any) *Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") == "" {
			t.Error("missing api key header")
		}
		if r.URL.Path != "/api/v3/checks/" {
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
	_ = hc.RegisterDestination("hc", ts.URL)
	eng, _ := redaction.New(nil)
	return &Client{HTTP: hc, DestName: "hc", Headers: map[string]string{"X-Api-Key": "readonly-test-token"}, Redact: eng, Now: func() time.Time {
		return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	}}
}

func TestStatus_ByTag(t *testing.T) {
	cli := setup(t, 200, map[string]any{
		"checks": []map[string]any{
			{
				"name": "backup", "slug": "backup", "tags": "docker backup", "status": "up",
				"grace": 3600, "last_ping": "2026-07-25T11:00:00Z",
				"ping_url": "https://hc.example/ping/SECRET-SHOULD-NOT-APPEAR",
				"uuid":     "11111111-1111-1111-1111-111111111111",
			},
			{
				"name": "other", "slug": "other", "tags": "misc", "status": "down",
				"grace": 60, "last_ping": "2026-07-25T10:00:00Z",
			},
		},
	})
	items, err := cli.Status(context.Background(), "backup", Filter{Tags: []string{"backup"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("%d", len(items))
	}
	raw, _ := json.Marshal(items[0])
	if strings.Contains(string(raw), "SECRET-SHOULD-NOT-APPEAR") {
		t.Fatal("ping url leaked")
	}
	if strings.Contains(string(raw), "11111111") {
		t.Fatal("uuid should not appear in attributes by default")
	}
}

func TestFailedInWindow_DownAndGrace(t *testing.T) {
	cli := setup(t, 200, map[string]any{
		"checks": []map[string]any{
			{"name": "a", "slug": "a", "status": "down", "tags": "x", "grace": 60, "last_ping": "2026-07-25T11:50:00Z"},
			{"name": "b", "slug": "b", "status": "grace", "tags": "x", "grace": 60, "last_ping": "2026-07-25T11:55:00Z"},
			{"name": "c", "slug": "c", "status": "up", "tags": "x", "grace": 60, "last_ping": "2026-07-25T09:00:00Z"},
			{"name": "d", "slug": "d", "status": "paused", "tags": "x", "grace": 60},
			{"name": "e", "slug": "e", "status": "new", "tags": "x", "grace": 60},
		},
	})
	start := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	items, err := cli.FailedInWindow(context.Background(), "", Filter{}, start, end)
	if err != nil {
		t.Fatal(err)
	}
	// Seuls les états actuels down, grace et paused prouvent un problème.
	if len(items) != 3 {
		t.Fatalf("got %d items %#v", len(items), items)
	}
	for _, item := range items {
		if item.SourceID == "c" || item.SourceID == "e" {
			t.Fatalf("healthy/new check included: %+v", item)
		}
		if item.WindowStart == nil || item.WindowEnd == nil {
			t.Fatalf("window missing: %+v", item)
		}
	}
}

func TestAuthFailure(t *testing.T) {
	cli := setup(t, 401, nil)
	_, err := cli.Status(context.Background(), "s", Filter{Name: "a"})
	if err == nil || !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("%v", err)
	}
}

func TestBareArrayResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": "a", "slug": "a", "status": "up", "tags": "t"},
		})
	}))
	defer ts.Close()
	hc := httpx.NewLockedClient(httpx.Options{Timeout: time.Second})
	_ = hc.RegisterDestination("hc", ts.URL)
	cli := &Client{HTTP: hc, DestName: "hc", Headers: map[string]string{"X-Api-Key": "t"}}
	items, err := cli.Status(context.Background(), "s", Filter{Tags: []string{"t"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatal(len(items))
	}
}

func TestMissingChecksArrayIsAnError(t *testing.T) {
	cli := setup(t, 200, map[string]any{})
	if _, err := cli.Status(context.Background(), "s", Filter{Name: "a"}); err == nil {
		t.Fatal("un objet incomplet ne doit pas devenir une liste vide")
	}
}

func TestStatus_RedactsAllTextAttributes(t *testing.T) {
	cli := setup(t, 200, map[string]any{
		"checks": []map[string]any{{
			"name":   "password=supersecret",
			"slug":   "token=anothersecret",
			"tags":   "ops secret=thirdsecret",
			"status": "down",
		}},
	})
	items, err := cli.Status(context.Background(), "s", Filter{Tags: []string{"ops"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(items)
	for _, secret := range []string{"supersecret", "anothersecret", "thirdsecret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("attribut non expurgé : %s", raw)
		}
	}
	if len(items) != 1 || items[0].RedactionsApplied == 0 {
		t.Fatalf("items=%+v", items)
	}
}
