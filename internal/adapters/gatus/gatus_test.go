package gatus

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

func setup(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	hc := httpx.NewLockedClient(httpx.Options{Timeout: 2 * time.Second})
	if err := hc.RegisterDestination("gatus", ts.URL); err != nil {
		t.Fatal(err)
	}
	eng, _ := redaction.New(nil)
	fixed := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	return &Client{HTTP: hc, DestName: "gatus", Redact: eng, Now: func() time.Time { return fixed }}
}

func TestStatus_LatestByTimestampNotOrder(t *testing.T) {
	payload := []map[string]any{
		{
			"name": "jellyfin", "group": "media", "key": "media_jellyfin",
			"results": []map[string]any{
				{"success": true, "status": 200, "timestamp": "2026-07-25T10:00:00Z"},
				{"success": false, "status": 503, "timestamp": "2026-07-25T11:00:00Z"},
				{"success": true, "status": 200, "timestamp": "2026-07-25T09:00:00Z"},
			},
		},
	}
	b, _ := json.Marshal(payload)
	cli := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/endpoints/statuses" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	})
	item, err := cli.Status(context.Background(), "jellyfin", "media_jellyfin")
	if err != nil {
		t.Fatal(err)
	}
	if item.Severity != evidence.SeverityError {
		t.Fatalf("want error latest, got %s attrs=%v", item.Severity, item.Attributes)
	}
	if item.Attributes["http_status"] != 503 {
		t.Fatalf("%v", item.Attributes)
	}
}

func TestStatus_InvalidFirstTimestampDoesNotHideValidLatest(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{
			"name":"a","key":"a",
			"results":[
				{"success":true,"status":200,"timestamp":"invalid"},
				{"success":false,"status":503,"timestamp":"2026-07-25T11:00:00Z"}
			]
		}]`))
	})
	item, err := cli.Status(context.Background(), "x", "a")
	if err != nil {
		t.Fatal(err)
	}
	if item.Severity != evidence.SeverityError {
		t.Fatalf("got %s", item.Severity)
	}
}

func TestStatus_EmptyList(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	item, err := cli.Status(context.Background(), "x", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if item.Freshness != evidence.FreshnessMissing {
		t.Fatal(item.Freshness)
	}
}

func TestStatus_RejectsNullList(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`null`))
	})
	if _, err := cli.Status(context.Background(), "x", "missing"); err == nil {
		t.Fatal("a null list must not become a valid not-found result")
	}
}

func TestStatus_EmptyResults(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"a","key":"a","results":[]}]`))
	})
	item, err := cli.Status(context.Background(), "x", "a")
	if err != nil {
		t.Fatal(err)
	}
	if item.Attributes["results"] != 0 {
		t.Fatal(item.Attributes)
	}
}

func TestEvidenceInWindow(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{
			"name":"a","key":"a",
			"results":[
				{"success":false,"status":500,"timestamp":"2026-07-25T11:30:00Z"},
				{"success":true,"status":200,"timestamp":"2026-07-25T08:00:00Z"}
			]
		}]`))
	})
	start := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	items, truncated, err := cli.EvidenceInWindow(context.Background(), "x", "a", start, end, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d", len(items))
	}
	if truncated {
		t.Fatal("history unexpectedly truncated")
	}
	if items[0].WindowStart == nil || items[0].WindowEnd == nil {
		t.Fatal("window missing")
	}
}

func TestEvidenceInWindow_ReportsTruncation(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{
			"name":"a","key":"a",
			"results":[
				{"success":false,"status":500,"timestamp":"2026-07-25T11:10:00Z"},
				{"success":false,"status":500,"timestamp":"2026-07-25T11:20:00Z"}
			]
		}]`))
	})
	start := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	items, truncated, err := cli.EvidenceInWindow(context.Background(), "x", "a", start, end, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !truncated {
		t.Fatalf("items=%d truncated=%v", len(items), truncated)
	}
}

func TestPartialUnknownFields(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"a","key":"a","extra":{"nested":true},"results":[{"success":true,"status":200,"timestamp":"2026-07-25T11:00:00Z","weird":1}]}]`))
	})
	item, err := cli.Status(context.Background(), "x", "a")
	if err != nil {
		t.Fatal(err)
	}
	if item.Severity != evidence.SeverityInfo {
		t.Fatal(item.Severity)
	}
}

func TestStatus_RedactsAllTextAttributes(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{
			"name":"password=supersecret","group":"ops","key":"endpoint",
			"results":[{"success":false,"status":500,"duration":"token=anothersecret","timestamp":"2026-07-25T11:00:00Z"}]
		}]`))
	})
	item, err := cli.Status(context.Background(), "x", "endpoint")
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
