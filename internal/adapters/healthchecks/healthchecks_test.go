package healthchecks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	fixed := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	hc := httpx.NewLockedClient(httpx.Options{Timeout: 2 * time.Second, Now: func() time.Time { return fixed }})
	_ = hc.RegisterDestination("hc", ts.URL)
	eng, _ := redaction.New(nil)
	return &Client{HTTP: hc, DestName: "hc", Headers: map[string]string{"X-Api-Key": "readonly-test-token"}, Redact: eng, Now: func() time.Time {
		return fixed
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
	// Only current down, grace, and paused states prove a problem.
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
		t.Fatal("an incomplete object must not become an empty list")
	}
}

func TestStatus_ReadOnlyUniqueKey(t *testing.T) {
	const key = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cli := setup(t, 200, map[string]any{
		"checks": []map[string]any{
			{
				"name": "backup", "slug": "backup", "status": "down",
				"unique_key": key,
			},
			{
				"name": "other", "slug": "other", "status": "up",
				"unique_key": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			},
		},
	})
	items, err := cli.Status(context.Background(), "backup", Filter{UniqueKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Severity != "error" {
		t.Fatalf("items=%+v", items)
	}
	raw, _ := json.Marshal(items)
	if strings.Contains(string(raw), key) {
		t.Fatalf("unique key must stay internal: %s", raw)
	}
}

func TestStatus_UnknownUniqueKeyIsExplicit(t *testing.T) {
	cli := setup(t, 200, map[string]any{
		"checks": []map[string]any{{
			"name": "backup", "slug": "backup", "status": "down",
			"unique_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
	})
	_, err := cli.Status(context.Background(), "backup", Filter{UniqueKey: "cccccccccccccccccccccccccccccccc"})
	if !errors.Is(err, ErrNoMatch) {
		t.Fatalf("err=%v, want ErrNoMatch", err)
	}
	if strings.Contains(err.Error(), "cccccccc") {
		t.Fatalf("diagnostic disclosed the identity: %v", err)
	}
}

func TestStatus_LegacyUUIDStillSelects(t *testing.T) {
	const uuid = "11111111-1111-1111-1111-111111111111"
	cli := setup(t, 200, map[string]any{
		"checks": []map[string]any{{
			"name": "backup", "slug": "backup", "status": "down", "uuid": uuid,
		}},
	})
	items, err := cli.Status(context.Background(), "backup", Filter{UUID: uuid})
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
}

func TestFailedInWindow_UnknownBindingIsExplicit(t *testing.T) {
	cli := setup(t, 200, map[string]any{
		"checks": []map[string]any{{"name": "backup", "status": "up", "tags": "ops"}},
	})
	start := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	_, err := cli.FailedInWindow(context.Background(), "s", Filter{Tags: []string{"absent"}}, start, end)
	if !errors.Is(err, ErrNoMatch) {
		t.Fatalf("err=%v, want ErrNoMatch", err)
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
			t.Fatalf("attribute not redacted: %s", raw)
		}
	}
	if len(items) != 1 || items[0].RedactionsApplied == 0 {
		t.Fatalf("items=%+v", items)
	}
}

func transitionsClient(t *testing.T, checks []map[string]any, flips func(key string) (int, string)) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/checks/" {
			_ = json.NewEncoder(w).Encode(map[string]any{"checks": checks})
			return
		}
		const prefix = "/api/v3/checks/"
		if strings.HasPrefix(r.URL.Path, prefix) && strings.HasSuffix(r.URL.Path, "/flips/") {
			key := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/flips/")
			// The upstream API documents start and end as Unix seconds and
			// rejects any other format with HTTP 400.
			for _, name := range []string{"start", "end"} {
				value := r.URL.Query().Get(name)
				if value == "" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if _, err := strconv.ParseInt(value, 10, 64); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
			}
			status, body := flips(key)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	hc := httpx.NewLockedClient(httpx.Options{Timeout: 2 * time.Second})
	_ = hc.RegisterDestination("hc", server.URL)
	eng, _ := redaction.New(nil)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	return &Client{
		HTTP: hc, DestName: "hc", Redact: eng,
		Headers: map[string]string{"X-Api-Key": "synthetic"},
		Now:     func() time.Time { return now },
	}
}

func TestTransitions_RecoveredIncident(t *testing.T) {
	cli := transitionsClient(t, []map[string]any{{
		"name": "backup", "slug": "backup", "status": "up",
		"unique_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}, func(string) (int, string) {
		return http.StatusOK, `{"flips":[
			{"timestamp":"2026-07-25T10:00:00Z","up":0},
			{"timestamp":"2026-07-25T10:05:00Z","up":1}
		]}`
	})
	start := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	result, err := cli.Transitions(context.Background(), "svc", Filter{Name: "backup"}, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Requests != 1 || result.Failures != 0 {
		t.Fatalf("result=%+v", result)
	}
	if result.Items[0].Severity != "error" || result.Items[1].Severity != "info" {
		t.Fatalf("severities=%s,%s", result.Items[0].Severity, result.Items[1].Severity)
	}
	if !result.Items[0].ObservedAt.Equal(time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)) ||
		!result.Items[1].ObservedAt.Equal(time.Date(2026, 7, 25, 10, 5, 0, 0, time.UTC)) {
		t.Fatalf("timestamps=%s,%s", result.Items[0].ObservedAt, result.Items[1].ObservedAt)
	}
	if result.Items[0].Attributes["transition"] != "down" || result.Items[1].Attributes["transition"] != "up" {
		t.Fatalf("attributes=%+v", result.Items[0].Attributes)
	}
}

func TestTransitions_EmptyAndInvalid(t *testing.T) {
	cli := transitionsClient(t, []map[string]any{{
		"name": "backup", "status": "up", "unique_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}, func(string) (int, string) {
		return http.StatusOK, `{"flips":[
			{"timestamp":"invalid","up":0},
			{"timestamp":"2026-07-25T10:00:00Z","up":7}
		]}`
	})
	start := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	result, err := cli.Transitions(context.Background(), "svc", Filter{Name: "backup"}, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 || result.Discarded != 2 {
		t.Fatalf("result=%+v", result)
	}
}

func TestTransitions_MissingIdentity(t *testing.T) {
	cli := transitionsClient(t, []map[string]any{{
		"name": "backup", "status": "up",
	}}, func(string) (int, string) { return http.StatusOK, `{"flips":[]}` })
	start := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	result, err := cli.Transitions(context.Background(), "svc", Filter{Name: "backup"}, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if result.MissingIdentity != 1 || result.Requests != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestTransitions_UsesUnixSecondWindow(t *testing.T) {
	var gotStart, gotEnd int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/checks/" {
			_ = json.NewEncoder(w).Encode(map[string]any{"checks": []map[string]any{{
				"name": "backup", "status": "up", "unique_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			}}})
			return
		}
		start, err := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		end, err := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		gotStart, gotEnd = start, end
		_ = json.NewEncoder(w).Encode(map[string]any{"flips": []map[string]any{
			{"timestamp": "2026-07-25T10:00:00Z", "up": 0},
		}})
	}))
	t.Cleanup(server.Close)
	hc := httpx.NewLockedClient(httpx.Options{Timeout: 2 * time.Second})
	_ = hc.RegisterDestination("hc", server.URL)
	eng, _ := redaction.New(nil)
	cli := &Client{
		HTTP: hc, DestName: "hc", Redact: eng,
		Headers: map[string]string{"X-Api-Key": "synthetic"},
	}
	// Non-UTC input must still produce Unix seconds for the same instant.
	start := time.Date(2026, 7, 25, 11, 0, 0, 500, time.FixedZone("CEST", 2*60*60))
	end := time.Date(2026, 7, 25, 13, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	if _, err := cli.Transitions(context.Background(), "svc", Filter{Name: "backup"}, start, end); err != nil {
		t.Fatal(err)
	}
	if gotStart != start.UTC().Unix()-1 || gotEnd != end.UTC().Unix()+1 {
		t.Fatalf("start=%d end=%d, want %d and %d", gotStart, gotEnd, start.UTC().Unix()-1, end.UTC().Unix()+1)
	}
}

func TestTransitions_InclusiveLocalBoundaries(t *testing.T) {
	cli := transitionsClient(t, []map[string]any{{
		"name": "backup", "status": "up", "unique_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}, func(string) (int, string) {
		// The fixture answers with the widened upstream window. A boundary flip
		// at exactly start or end must survive the local inclusive filter.
		return http.StatusOK, `{"flips":[
			{"timestamp":"2026-07-25T09:00:00Z","up":0},
			{"timestamp":"2026-07-25T11:00:00Z","up":1},
			{"timestamp":"2026-07-25T11:00:01Z","up":0}
		]}`
	})
	start := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	result, err := cli.Transitions(context.Background(), "svc", Filter{Name: "backup"}, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("items=%d, want 2: %+v", len(result.Items), result.Items)
	}
}

func TestSelectChecks_RejectsNameSlugCollision(t *testing.T) {
	cli := transitionsClient(t, []map[string]any{
		{"name": "database", "slug": "db-primary", "status": "down", "unique_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"name": "nightly backup", "slug": "database", "status": "up", "unique_key": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}, func(string) (int, string) { return http.StatusOK, `{"flips":[]}` })
	if _, err := cli.Status(context.Background(), "svc", Filter{Name: "database"}); !errors.Is(err, ErrAmbiguousBinding) {
		t.Fatalf("err=%v, want ErrAmbiguousBinding", err)
	}
}

func TestSelectChecks_ExplicitIdentityWins(t *testing.T) {
	cli := transitionsClient(t, []map[string]any{
		{"name": "database", "slug": "db-primary", "status": "down", "unique_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"name": "database", "slug": "db-replica", "status": "up", "unique_key": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}, func(string) (int, string) { return http.StatusOK, `{"flips":[]}` })
	items, err := cli.Status(context.Background(), "svc", Filter{UniqueKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Attributes["slug"] != "db-primary" {
		t.Fatalf("items=%+v", items)
	}
}

func TestSelectChecks_TagAggregationStillAllowed(t *testing.T) {
	cli := transitionsClient(t, []map[string]any{
		{"name": "one", "slug": "one", "tags": "ops", "status": "down", "unique_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"name": "two", "slug": "two", "tags": "ops", "status": "up", "unique_key": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}, func(string) (int, string) { return http.StatusOK, `{"flips":[]}` })
	items, err := cli.Status(context.Background(), "svc", Filter{Tags: []string{"ops"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items=%d, want 2", len(items))
	}
}

func TestTransitions_PartialAuthKeepsEvidence(t *testing.T) {
	cli := transitionsClient(t, []map[string]any{
		{"name": "one", "slug": "one", "tags": "ops", "status": "up", "unique_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"name": "two", "slug": "two", "tags": "ops", "status": "up", "unique_key": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}, func(key string) (int, string) {
		if key == "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
			return http.StatusForbidden, ""
		}
		return http.StatusOK, `{"flips":[{"timestamp":"2026-07-25T10:00:00Z","up":0}]}`
	})
	start := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	result, err := cli.Transitions(context.Background(), "svc", Filter{Tags: []string{"ops"}}, start, end)
	if err != nil {
		t.Fatalf("partial evidence must not be an error: %v", err)
	}
	if len(result.Items) != 1 || result.Unauthorized != 1 || result.Requests != 2 {
		t.Fatalf("result=%+v", result)
	}
}

func TestTransitions_AllAuthFailIsError(t *testing.T) {
	cli := transitionsClient(t, []map[string]any{{
		"name": "backup", "slug": "backup", "status": "up", "unique_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}, func(string) (int, string) { return http.StatusForbidden, "" })
	start := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	if _, err := cli.Transitions(context.Background(), "svc", Filter{Name: "backup"}, start, end); err == nil {
		t.Fatal("expected an authentication error")
	}
}

func TestTransitions_PartialFailure(t *testing.T) {
	cli := transitionsClient(t, []map[string]any{
		{"name": "one", "status": "up", "tags": "ops", "unique_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"name": "two", "status": "up", "tags": "ops", "unique_key": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}, func(key string) (int, string) {
		if key == "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
			return http.StatusInternalServerError, ""
		}
		return http.StatusOK, `{"flips":[{"timestamp":"2026-07-25T10:00:00Z","up":0}]}`
	})
	start := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	result, err := cli.Transitions(context.Background(), "svc", Filter{Tags: []string{"ops"}}, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failures != 1 || len(result.Items) != 1 || result.Requests != 2 {
		t.Fatalf("result=%+v", result)
	}
}

func TestTransitions_AuthFailure(t *testing.T) {
	cli := transitionsClient(t, []map[string]any{{
		"name": "backup", "status": "up", "unique_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}, func(string) (int, string) { return http.StatusUnauthorized, "" })
	start := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	if _, err := cli.Transitions(context.Background(), "svc", Filter{Name: "backup"}, start, end); err == nil {
		t.Fatal("expected authentication error")
	}
}

func TestTransitions_BoundsChecksAndFlips(t *testing.T) {
	checks := make([]map[string]any, 0, maxTransitionChecks+1)
	for index := 0; index <= maxTransitionChecks; index++ {
		checks = append(checks, map[string]any{
			"name":       fmt.Sprintf("check-%02d", index),
			"status":     "up",
			"tags":       "ops",
			"unique_key": fmt.Sprintf("%032d", index),
		})
	}
	var flips strings.Builder
	flips.WriteString(`{"flips":[`)
	base := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	for index := 0; index <= maxFlipsPerCheck; index++ {
		if index > 0 {
			flips.WriteString(",")
		}
		_, _ = fmt.Fprintf(&flips, `{"timestamp":%q,"up":0}`,
			base.Add(time.Duration(index)*time.Second).Format(time.RFC3339))
	}
	flips.WriteString(`]}`)
	cli := transitionsClient(t, checks, func(key string) (int, string) {
		if key == fmt.Sprintf("%032d", 0) {
			return http.StatusOK, flips.String()
		}
		return http.StatusOK, `{"flips":[]}`
	})
	start := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	result, err := cli.Transitions(context.Background(), "svc", Filter{Tags: []string{"ops"}}, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if result.Omitted != 1 || result.Requests != maxTransitionChecks {
		t.Fatalf("bounds=%+v", result)
	}
	if !result.Truncated {
		t.Fatal("flip limit did not report truncation")
	}
	if got := len(result.Items); got != maxFlipsPerCheck {
		t.Fatalf("items=%d, want=%d", got, maxFlipsPerCheck)
	}
}

func TestStatus_StatusFilterNoMatchIsNotUnmatchedBinding(t *testing.T) {
	cli := transitionsClient(t, []map[string]any{{
		"name": "backup", "slug": "backup", "status": "up", "unique_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}, func(string) (int, string) { return http.StatusOK, `{"flips":[]}` })
	if _, err := cli.Status(context.Background(), "svc", Filter{Name: "backup", Status: "down"}); !errors.Is(err, ErrStatusFilterNoMatch) {
		t.Fatalf("err=%v, want ErrStatusFilterNoMatch", err)
	}
	// A truly unknown check remains unmatched.
	if _, err := cli.Status(context.Background(), "svc", Filter{Name: "missing", Status: "down"}); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("err=%v, want ErrNoMatch", err)
	}
}

func TestStatus_DuplicateSlugsHaveDistinctProvenance(t *testing.T) {
	cli := transitionsClient(t, []map[string]any{
		{"name": "one", "slug": "backups", "tags": "ops", "status": "up", "unique_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"name": "two", "slug": "backups", "tags": "ops", "status": "down", "unique_key": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}, func(string) (int, string) { return http.StatusOK, `{"flips":[]}` })
	items, err := cli.Status(context.Background(), "svc", Filter{Tags: []string{"ops"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].SourceID == items[1].SourceID {
		t.Fatalf("duplicate slug collapsed provenance: %+v", items)
	}
	for _, item := range items {
		if strings.Contains(item.SourceID, "aaaaaaaa") || strings.Contains(item.SourceID, "bbbb") {
			t.Fatalf("raw unique key leaked in source id: %s", item.SourceID)
		}
	}
}

func TestStatus_LegacyDuplicateSlugIsUnverified(t *testing.T) {
	cli := transitionsClient(t, []map[string]any{
		{"name": "one", "slug": "backups", "tags": "ops", "status": "up"},
		{"name": "two", "slug": "backups", "tags": "ops", "status": "up"},
	}, func(string) (int, string) { return http.StatusOK, `{"flips":[]}` })
	items, err := cli.Status(context.Background(), "svc", Filter{Tags: []string{"ops"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || !items[0].IdentityUnverified || !items[1].IdentityUnverified {
		t.Fatalf("legacy duplicate identity was not marked: %+v", items)
	}
}

func TestList_RequestBudget(t *testing.T) {
	cli := transitionsClient(t, []map[string]any{{
		"name": "backup", "slug": "backup", "status": "up", "unique_key": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}, func(string) (int, string) { return http.StatusOK, `{"flips":[]}` })
	cli.RequestLimit = 1
	if _, err := cli.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Fetch(context.Background()); !errors.Is(err, ErrRequestBudget) {
		t.Fatalf("err=%v, want ErrRequestBudget", err)
	}
}
