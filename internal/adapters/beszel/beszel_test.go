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
