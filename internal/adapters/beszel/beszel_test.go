package beszel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
)

func TestStatus_PocketBaseRecordsPath(t *testing.T) {
	var sawOfficial bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method %s", r.Method)
		}
		if r.URL.Path != "/api/collections/systems/records" {
			http.NotFound(w, r)
			return
		}
		sawOfficial = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"page": 1, "perPage": 30, "totalItems": 1,
			"items": []map[string]any{
				{"name": "pb-host", "status": "up", "cpu": 3.0, "host": "pb-host"},
			},
		})
	}))
	defer ts.Close()
	hc := httpx.NewLockedClient(httpx.Options{Timeout: time.Second})
	_ = hc.RegisterDestination("bz", ts.URL)
	cli := &Client{HTTP: hc, DestName: "bz"}
	it, err := cli.Status(context.Background(), "media", "pb-host")
	if err != nil {
		t.Fatal(err)
	}
	if !sawOfficial {
		t.Fatal("PocketBase records path was not requested")
	}
	if it.Attributes["found"] != true || it.SourceID != "pb-host" {
		t.Fatalf("%+v", it)
	}
}

func TestStatus_Found(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method %s", r.Method)
		}
		if r.Header.Get("X-Scope-OrgID") != "lab" {
			t.Errorf("missing header")
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": "media-host", "status": "up", "cpu": 12.5},
		})
	}))
	defer ts.Close()
	hc := httpx.NewLockedClient(httpx.Options{Timeout: time.Second})
	_ = hc.RegisterDestination("bz", ts.URL)
	eng, _ := redaction.New(nil)
	cli := &Client{HTTP: hc, DestName: "bz", Headers: map[string]string{"X-Scope-OrgID": "lab"}, Redact: eng}
	it, err := cli.Status(context.Background(), "media", "media-host")
	if err != nil {
		t.Fatal(err)
	}
	if it.Attributes["found"] != true {
		t.Fatalf("%+v", it)
	}
	if it.Source != "beszel" {
		t.Fatal(it.Source)
	}
}

func TestStatus_NotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer ts.Close()
	hc := httpx.NewLockedClient(httpx.Options{Timeout: time.Second})
	_ = hc.RegisterDestination("bz", ts.URL)
	cli := &Client{HTTP: hc, DestName: "bz"}
	it, err := cli.Status(context.Background(), "media", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if it.Attributes["found"] != false {
		t.Fatal(it)
	}
}

func TestStatus_EmptyWrappedListIsNotAnAdapterError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"systems": []map[string]any{}})
	}))
	defer ts.Close()
	hc := httpx.NewLockedClient(httpx.Options{Timeout: time.Second})
	_ = hc.RegisterDestination("bz", ts.URL)
	cli := &Client{HTTP: hc, DestName: "bz"}
	it, err := cli.Status(context.Background(), "media", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if it.Attributes["found"] != false {
		t.Fatal(it)
	}
}

func TestStatus_NullListIsAnAdapterError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`null`))
	}))
	defer ts.Close()
	hc := httpx.NewLockedClient(httpx.Options{Timeout: time.Second})
	_ = hc.RegisterDestination("bz", ts.URL)
	cli := &Client{HTTP: hc, DestName: "bz"}
	if _, err := cli.Status(context.Background(), "media", "missing"); err == nil {
		t.Fatal("a null list must not become a valid not-found result")
	}
}

func TestStatus_PreservesZeroMetrics(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": "idle", "status": "up", "cpu": 0, "mem": 0},
		})
	}))
	defer ts.Close()
	hc := httpx.NewLockedClient(httpx.Options{Timeout: time.Second})
	_ = hc.RegisterDestination("bz", ts.URL)
	cli := &Client{HTTP: hc, DestName: "bz"}
	it, err := cli.Status(context.Background(), "idle", "idle")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := it.Attributes["cpu"]; !ok {
		t.Fatal("zero cpu metric omitted")
	}
	if _, ok := it.Attributes["mem"]; !ok {
		t.Fatal("zero memory metric omitted")
	}
}

func TestStatus_UsesInfoHostnameAsPublicIdentity(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"status": "up", "info": map[string]any{"h": "nested-host"}},
		})
	}))
	defer ts.Close()
	hc := httpx.NewLockedClient(httpx.Options{Timeout: time.Second})
	_ = hc.RegisterDestination("bz", ts.URL)
	cli := &Client{HTTP: hc, DestName: "bz"}
	item, err := cli.Status(context.Background(), "s", "nested-host")
	if err != nil {
		t.Fatal(err)
	}
	if item.SourceID != "nested-host" || item.Attributes["system_name"] != "nested-host" {
		t.Fatalf("item=%+v", item)
	}
}

func TestAuthFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer ts.Close()
	hc := httpx.NewLockedClient(httpx.Options{Timeout: time.Second})
	_ = hc.RegisterDestination("bz", ts.URL)
	cli := &Client{HTTP: hc, DestName: "bz"}
	_, err := cli.Status(context.Background(), "media", "x")
	if err == nil {
		t.Fatal("expected auth error")
	}
}

func TestStatus_RedactsAllTextAttributes(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": "password=supersecret", "status": "up"},
		})
	}))
	defer ts.Close()
	hc := httpx.NewLockedClient(httpx.Options{Timeout: time.Second})
	_ = hc.RegisterDestination("bz", ts.URL)
	eng, _ := redaction.New(nil)
	cli := &Client{HTTP: hc, DestName: "bz", Redact: eng}
	item, err := cli.Status(context.Background(), "s", "password=supersecret")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(item)
	if strings.Contains(string(raw), "supersecret") {
		t.Fatalf("attribute not redacted: %s", raw)
	}
	if item.RedactionsApplied == 0 {
		t.Fatal("redaction data is missing")
	}
}

func TestFindSystem_CanonicalPrecedenceAndAmbiguity(t *testing.T) {
	rows := []systemRow{
		{ID: "1", Name: "agent-a", Host: "prod"},
		{ID: "2", Name: "prod", Host: "node-b"},
	}
	got, ok, err := findSystem(rows, "prod")
	if err != nil || !ok || got.ID != "2" {
		t.Fatalf("canonical precedence: %+v ok=%v err=%v", got, ok, err)
	}
	// Reversing the rows must not change the result.
	rows[0], rows[1] = rows[1], rows[0]
	if got, ok, err := findSystem(rows, "prod"); err != nil || !ok || got.ID != "2" {
		t.Fatalf("row order changed identity: %+v ok=%v err=%v", got, ok, err)
	}
	if _, _, err := findSystem([]systemRow{
		{ID: "1", Name: "same"},
		{ID: "2", Name: "same"},
	}, "same"); !errors.Is(err, ErrSystemAmbiguous) {
		t.Fatalf("duplicate canonical name err=%v", err)
	}
	// A unique host alias remains compatible.
	if got, ok, err := findSystem(rows, "node-b"); err != nil || !ok || got.ID != "2" {
		t.Fatalf("alias lookup: %+v ok=%v err=%v", got, ok, err)
	}
}

func TestStatus_MixedPageAgeIsConservative(t *testing.T) {
	page1At := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	page2At := page1At.Add(-20 * time.Second)
	times := []time.Time{page1At, page2At}
	call := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "1" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"id": "a", "name": "one"}},
				"page":  1, "perPage": 200, "totalItems": 2, "totalPages": 2,
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{"id": "b", "name": "two"}},
			"page":  2, "perPage": 200, "totalItems": 2, "totalPages": 2,
		})
	}))
	defer upstream.Close()
	hc := httpx.NewLockedClient(httpx.Options{Timeout: time.Second, Now: func() time.Time {
		value := times[call]
		if call < len(times)-1 {
			call++
		}
		return value
	}})
	_ = hc.RegisterDestination("beszel", upstream.URL)
	cli := &Client{HTTP: hc, DestName: "beszel"}
	item, err := cli.Status(context.Background(), "svc", "two")
	if err != nil {
		t.Fatal(err)
	}
	if item.Attributes["mixed_page_age"] != true || item.Attributes["pages"] != 2 {
		t.Fatalf("mixed age metadata=%+v", item.Attributes)
	}
	if !item.ObservedAt.Equal(page2At) {
		t.Fatalf("observed_at=%s, want oldest page %s", item.ObservedAt, page2At)
	}
}

func TestStatus_OverlappingPagesAreReported(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"id": "a", "name": "one"}, {"id": "b", "name": "two"}},
				"page":  1, "perPage": 200, "totalItems": 4, "totalPages": 2,
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{"id": "b", "name": "two"}, {"id": "c", "name": "three"}},
			"page":  2, "perPage": 200, "totalItems": 4, "totalPages": 2,
		})
	}))
	defer upstream.Close()
	hc := httpx.NewLockedClient(httpx.Options{Timeout: time.Second})
	_ = hc.RegisterDestination("beszel", upstream.URL)
	cli := &Client{HTTP: hc, DestName: "beszel"}
	item, err := cli.Status(context.Background(), "svc", "three")
	if err != nil {
		t.Fatal(err)
	}
	if item.Attributes["pagination_overlap"] != true {
		t.Fatalf("overlap not reported: %+v", item.Attributes)
	}
}

func TestStatus_AlternateRouteFollowsPagination(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/collections/systems/records":
			http.NotFound(w, r)
		case r.URL.Path == "/api/systems" && r.URL.Query().Get("page") == "":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"id": "a", "name": "one"}},
				"page":  1, "perPage": 200, "totalItems": 2, "totalPages": 2,
			})
		case r.URL.Path == "/api/systems" && r.URL.Query().Get("page") == "2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"id": "b", "name": "target"}},
				"page":  2, "perPage": 200, "totalItems": 2, "totalPages": 2,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	hc := httpx.NewLockedClient(httpx.Options{Timeout: time.Second})
	_ = hc.RegisterDestination("beszel", upstream.URL)
	cli := &Client{HTTP: hc, DestName: "beszel"}
	item, err := cli.Status(context.Background(), "svc", "target")
	if err != nil {
		t.Fatal(err)
	}
	if item.Attributes["found"] != true || item.Attributes["system_name"] != "target" {
		t.Fatalf("page-2 target not found: %+v", item.Attributes)
	}
}

func TestStatus_MetricUnitsAndBounds(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "valid", "name": "valid", "info": map[string]any{"cpu": 0, "mp": 100}},
			{"id": "invalid", "name": "invalid", "info": map[string]any{"cpu": 101, "mp": -1}},
			{"id": "legacy", "name": "legacy", "cpu": 42.0, "mem": 60.0},
		})
	}))
	defer upstream.Close()
	hc := httpx.NewLockedClient(httpx.Options{Timeout: time.Second})
	_ = hc.RegisterDestination("beszel", upstream.URL)
	cli := &Client{HTTP: hc, DestName: "beszel"}
	valid, err := cli.Status(context.Background(), "svc", "valid")
	if err != nil {
		t.Fatal(err)
	}
	if valid.Attributes["cpu_percent"] != float64(0) || valid.Attributes["memory_percent"] != float64(100) {
		t.Fatalf("valid percent metrics=%+v", valid.Attributes)
	}
	invalid, err := cli.Status(context.Background(), "svc", "invalid")
	if err != nil {
		t.Fatal(err)
	}
	if invalid.Attributes["cpu_invalid"] != true || invalid.Attributes["memory_invalid"] != true {
		t.Fatalf("out-of-range metrics were accepted: %+v", invalid.Attributes)
	}
	if _, ok := invalid.Attributes["cpu_percent"]; ok {
		t.Fatalf("invalid cpu became a percentage: %+v", invalid.Attributes)
	}
	legacy, err := cli.Status(context.Background(), "svc", "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Attributes["cpu_legacy"] != 42.0 || legacy.Attributes["cpu_unit"] != "unknown" {
		t.Fatalf("legacy metrics relabeled: %+v", legacy.Attributes)
	}
	if _, ok := legacy.Attributes["cpu_percent"]; ok {
		t.Fatalf("legacy metric became a percentage: %+v", legacy.Attributes)
	}
}
