package upstream_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/beszel"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/docker"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/gatus"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/healthchecks"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/loki"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/ntfy"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func serve(t *testing.T, handler http.HandlerFunc) (*httpx.LockedClient, *redaction.Engine) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := httpx.NewLockedClient(httpx.Options{Timeout: 2 * time.Second})
	if err := client.RegisterDestination("upstream", server.URL); err != nil {
		t.Fatal(err)
	}
	engine, err := redaction.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return client, engine
}

func serveFixture(t *testing.T, path, name string) (*httpx.LockedClient, *redaction.Engine) {
	t.Helper()
	body := fixture(t, name)
	return serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	})
}

func TestReplayGatusV5(t *testing.T) {
	client, engine := serveFixture(t, "/api/v1/endpoints/statuses", "gatus/statuses-v5.38.0.json")
	cli := &gatus.Client{HTTP: client, DestName: "upstream", Redact: engine}
	item, err := cli.Status(context.Background(), "svc", "prod_api")
	if err != nil {
		t.Fatal(err)
	}
	if item.Severity != evidence.SeverityError || item.Attributes["http_status"] != 503 {
		t.Fatalf("item=%+v", item)
	}
	empty, err := cli.Status(context.Background(), "svc", "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if empty.Attributes["results"] != 0 {
		t.Fatalf("empty results=%+v", empty.Attributes)
	}

	client, engine = serveFixture(t, "/api/v1/endpoints/statuses", "gatus/statuses-empty.json")
	cli = &gatus.Client{HTTP: client, DestName: "upstream", Redact: engine}
	missing, err := cli.Status(context.Background(), "svc", "prod_api")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Freshness != evidence.FreshnessMissing {
		t.Fatalf("freshness=%s", missing.Freshness)
	}
}

func TestReplayDockerV1_56(t *testing.T) {
	client, engine := serveFixture(t, "/containers/json", "docker/containers-v1.56.json")
	cli := &docker.Client{HTTP: client, DestName: "upstream", Redact: engine}
	item, err := cli.Status(context.Background(), "svc", "api")
	if err != nil {
		t.Fatal(err)
	}
	if item.Attributes["state"] != "running" || item.Attributes["health"] != "healthy" {
		t.Fatalf("item=%+v", item.Attributes)
	}

	client, engine = serveFixture(t, "/containers/json", "docker/containers-empty.json")
	cli = &docker.Client{HTTP: client, DestName: "upstream", Redact: engine}
	missing, err := cli.Status(context.Background(), "svc", "api")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Freshness != evidence.FreshnessMissing {
		t.Fatalf("freshness=%s", missing.Freshness)
	}
}

func TestReplayLokiV3_7(t *testing.T) {
	client, engine := serveFixture(t, "/loki/api/v1/query_range", "loki/query-range-v3.7.9.json")
	cli := &loki.Client{HTTP: client, DestName: "upstream", Redact: engine}
	start := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 20, 11, 0, 0, 0, time.UTC)
	items, truncated, err := cli.Search(context.Background(), loki.QueryOptions{
		ServiceID: "svc", Selector: `{container="api"}`, Start: start, End: end, Limit: 10,
	})
	if err != nil || truncated || len(items) != 2 {
		t.Fatalf("items=%d truncated=%v err=%v", len(items), truncated, err)
	}
	if items[1].Severity != evidence.SeverityError {
		t.Fatalf("severity=%s", items[1].Severity)
	}

	client, engine = serveFixture(t, "/loki/api/v1/query_range", "loki/query-range-invalid-timestamp.json")
	cli = &loki.Client{HTTP: client, DestName: "upstream", Redact: engine}
	items, _, err = cli.Search(context.Background(), loki.QueryOptions{
		ServiceID: "svc", Selector: `{container="api"}`, Start: start, End: end, Limit: 10,
	})
	if err != nil || len(items) != 0 {
		t.Fatalf("invalid timestamp was not discarded: items=%d err=%v", len(items), err)
	}
}

func TestReplayHealthchecksV3(t *testing.T) {
	client, engine := serveFixture(t, "/api/v3/checks/", "healthchecks/checks-v3-readonly.json")
	cli := &healthchecks.Client{
		HTTP: client, DestName: "upstream", Redact: engine,
		Headers: map[string]string{"X-Api-Key": "synthetic-read-only"},
	}
	items, err := cli.Status(context.Background(), "svc", healthchecks.Filter{UniqueKey: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if err != nil || len(items) != 1 || items[0].Severity != evidence.SeverityError {
		t.Fatalf("items=%+v err=%v", items, err)
	}

	client, engine = serveFixture(t, "/api/v3/checks/", "healthchecks/checks-v3-array.json")
	cli = &healthchecks.Client{
		HTTP: client, DestName: "upstream", Redact: engine,
		Headers: map[string]string{"X-Api-Key": "synthetic-read-only"},
	}
	items, err = cli.Status(context.Background(), "svc", healthchecks.Filter{Name: "legacy"})
	if err != nil || len(items) != 1 || items[0].Severity != evidence.SeverityInfo {
		t.Fatalf("items=%+v err=%v", items, err)
	}

	client, engine = serveFixture(t, "/api/v3/checks/", "healthchecks/checks-v3-empty.json")
	cli = &healthchecks.Client{
		HTTP: client, DestName: "upstream", Redact: engine,
		Headers: map[string]string{"X-Api-Key": "synthetic-read-only"},
	}
	if _, err := cli.Status(context.Background(), "svc", healthchecks.Filter{Name: "backup"}); !errors.Is(err, healthchecks.ErrNoMatch) {
		t.Fatalf("err=%v, want ErrNoMatch", err)
	}
}

func TestReplayBeszelV0_21(t *testing.T) {
	pageOne := fixture(t, "beszel/systems-page-1-v0.21.1.json")
	pageTwo := fixture(t, "beszel/systems-page-2-v0.21.1.json")
	client, engine := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/collections/systems/records" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("page") {
		case "", "1":
			_, _ = w.Write(pageOne)
		default:
			_, _ = w.Write(pageTwo)
		}
	})
	cli := &beszel.Client{HTTP: client, DestName: "upstream", Redact: engine}
	item, err := cli.Status(context.Background(), "svc", "host-two")
	if err != nil {
		t.Fatal(err)
	}
	if item.Severity != evidence.SeverityError || item.Attributes["status"] != "offline" {
		t.Fatalf("item=%+v", item.Attributes)
	}

	client, engine = serveFixture(t, "/api/collections/systems/records", "beszel/systems-empty.json")
	cli = &beszel.Client{HTTP: client, DestName: "upstream", Redact: engine}
	missing, err := cli.Status(context.Background(), "svc", "host-two")
	if err != nil {
		t.Fatal(err)
	}
	if missing.Freshness != evidence.FreshnessMissing {
		t.Fatalf("freshness=%s", missing.Freshness)
	}
}

func TestReplayNtfyV2_37(t *testing.T) {
	client, engine := serveFixture(t, "/alerts/json", "ntfy/replay-v2.37.2.ndjson")
	cli := &ntfy.Client{HTTP: client, DestName: "upstream", Redact: engine}
	start := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 20, 11, 0, 0, 0, time.UTC)
	items, truncated, err := cli.History(context.Background(), "svc", "alerts", start, end, 10)
	if err != nil || truncated || len(items) != 1 {
		t.Fatalf("items=%d truncated=%v err=%v", len(items), truncated, err)
	}
	if items[0].Severity != evidence.SeverityInfo || items[0].Attributes["priority"] != 3 {
		t.Fatalf("item=%+v", items[0].Attributes)
	}

	client, engine = serveFixture(t, "/alerts/json", "ntfy/replay-array-v2.37.2.json")
	cli = &ntfy.Client{HTTP: client, DestName: "upstream", Redact: engine}
	items, _, err = cli.History(context.Background(), "svc", "alerts", start, end, 10)
	if err != nil || len(items) != 1 || items[0].Severity != evidence.SeverityError {
		t.Fatalf("items=%+v err=%v", items, err)
	}
}

func TestReplayAuthFailures(t *testing.T) {
	cases := []struct {
		name string
		call func(context.Context, *httpx.LockedClient, *redaction.Engine) error
	}{
		{"gatus", func(ctx context.Context, c *httpx.LockedClient, e *redaction.Engine) error {
			_, err := (&gatus.Client{HTTP: c, DestName: "upstream", Redact: e}).Status(ctx, "svc", "api")
			return err
		}},
		{"docker", func(ctx context.Context, c *httpx.LockedClient, e *redaction.Engine) error {
			_, err := (&docker.Client{HTTP: c, DestName: "upstream", Redact: e}).Status(ctx, "svc", "api")
			return err
		}},
		{"loki", func(ctx context.Context, c *httpx.LockedClient, e *redaction.Engine) error {
			_, _, err := (&loki.Client{HTTP: c, DestName: "upstream", Redact: e}).Search(ctx, loki.QueryOptions{
				Selector: `{container="api"}`, Start: time.Now().Add(-time.Hour), End: time.Now(),
			})
			return err
		}},
		{"healthchecks", func(ctx context.Context, c *httpx.LockedClient, e *redaction.Engine) error {
			_, err := (&healthchecks.Client{
				HTTP: c, DestName: "upstream", Redact: e,
				Headers: map[string]string{"X-Api-Key": "synthetic"},
			}).Status(ctx, "svc", healthchecks.Filter{Name: "api"})
			return err
		}},
		{"beszel", func(ctx context.Context, c *httpx.LockedClient, e *redaction.Engine) error {
			_, err := (&beszel.Client{HTTP: c, DestName: "upstream", Redact: e}).Status(ctx, "svc", "api")
			return err
		}},
		{"ntfy", func(ctx context.Context, c *httpx.LockedClient, e *redaction.Engine) error {
			_, _, err := (&ntfy.Client{HTTP: c, DestName: "upstream", Redact: e}).History(
				ctx, "svc", "alerts", time.Now().Add(-time.Hour), time.Now(), 10,
			)
			return err
		}},
	}
	for _, test := range cases {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
			t.Run(test.name+"/"+http.StatusText(status), func(t *testing.T) {
				client, engine := serve(t, func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(status)
				})
				if err := test.call(context.Background(), client, engine); err == nil {
					t.Fatalf("%s accepted HTTP %d", test.name, status)
				}
			})
		}
	}
}
