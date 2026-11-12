package gatus

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

func setup(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	fixed := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	hc := httpx.NewLockedClient(httpx.Options{Timeout: 2 * time.Second, Now: func() time.Time { return fixed }})
	if err := hc.RegisterDestination("gatus", ts.URL); err != nil {
		t.Fatal(err)
	}
	eng, _ := redaction.New(nil)
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
		t.Fatalf("want error with maximum timestamp, got %s attrs=%v", item.Severity, item.Attributes)
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
	window, err := cli.EvidenceInWindow(context.Background(), "x", "a", start, end, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !window.Matched || len(window.Items) != 1 {
		t.Fatalf("matched=%v items=%d", window.Matched, len(window.Items))
	}
	if window.Truncated {
		t.Fatal("history unexpectedly truncated")
	}
	if window.Items[0].WindowStart == nil || window.Items[0].WindowEnd == nil {
		t.Fatal("window missing")
	}
}

func TestStatus_NumericDuration(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{
			"name":"a","key":"a",
			"results":[{"success":true,"status":200,"duration":15000000,"timestamp":"2026-07-25T11:00:00Z"}]
		}]`))
	})
	item, err := cli.Status(context.Background(), "x", "a")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := item.Attributes["duration"].(string)
	if got == "" {
		t.Fatalf("numeric duration not decoded: %+v", item.Attributes)
	}
	if !strings.Contains(got, "15ms") && got != (15*time.Millisecond).String() {
		t.Fatalf("duration=%q attrs=%v", got, item.Attributes)
	}
}

func TestEvidenceInWindow_MissingEndpointIsAbsence(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"a","key":"a","results":[{"success":true,"status":200,"timestamp":"2026-07-25T11:00:00Z"}]}]`))
	})
	start := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	window, err := cli.EvidenceInWindow(context.Background(), "x", "missing", start, end, 10)
	if err != nil {
		t.Fatal(err)
	}
	if window.Matched || window.Truncated || len(window.Items) != 1 {
		t.Fatalf("matched=%v items=%d truncated=%v", window.Matched, len(window.Items), window.Truncated)
	}
	if window.Items[0].Freshness != evidence.FreshnessMissing || window.Items[0].Attributes["found"] != false {
		t.Fatalf("want a missing item, got %+v", window.Items[0])
	}
}

func TestEvidenceInWindow_KeepsNewestWhenTruncating(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{
			"name":"a","key":"a",
			"results":[
				{"success":false,"status":500,"timestamp":"2026-07-25T11:10:00Z"},
				{"success":false,"status":501,"timestamp":"2026-07-25T11:20:00Z"}
			]
		}]`))
	})
	start := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	window, err := cli.EvidenceInWindow(context.Background(), "x", "a", start, end, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !window.Truncated || len(window.Items) != 1 {
		t.Fatalf("items=%d truncated=%v", len(window.Items), window.Truncated)
	}
	if window.Items[0].Attributes["http_status"] != 501 {
		t.Fatalf("kept oldest instead of newest: %+v", window.Items[0].Attributes)
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

func TestStatus_ExactKeyBeatsEarlierAlias(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"name":"prod_api","group":"dev","key":"dev_prod_api","results":[{"success":true,"status":200,"timestamp":"2026-07-25T11:00:00Z"}]},
			{"name":"api","group":"prod","key":"prod_api","results":[{"success":false,"status":503,"timestamp":"2026-07-25T11:00:00Z"}]}
		]`))
	})
	item, err := cli.Status(context.Background(), "x", "prod_api")
	if err != nil {
		t.Fatal(err)
	}
	if item.Attributes["http_status"] != 503 || item.Attributes["endpoint_key"] != "prod_api" {
		t.Fatalf("alias masked the exact key: %+v", item.Attributes)
	}
}

func TestStatus_RowOrderDoesNotChangeExactSelection(t *testing.T) {
	rows := []string{
		`{"name":"prod_api","group":"dev","key":"dev_prod_api","results":[{"success":true,"status":200,"timestamp":"2026-07-25T11:00:00Z"}]}`,
		`{"name":"api","group":"prod","key":"prod_api","results":[{"success":false,"status":503,"timestamp":"2026-07-25T11:00:00Z"}]}`,
	}
	for _, order := range [][]string{{rows[0], rows[1]}, {rows[1], rows[0]}} {
		body := "[" + strings.Join(order, ",") + "]"
		cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		})
		item, err := cli.Status(context.Background(), "x", "prod_api")
		if err != nil {
			t.Fatal(err)
		}
		if item.Attributes["http_status"] != 503 {
			t.Fatalf("order changed the selected endpoint: %+v", item.Attributes)
		}
	}
}

func TestStatus_LegacyAliasResolves(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"api","group":"prod","key":"dev_prod_api","results":[{"success":false,"status":503,"timestamp":"2026-07-25T11:00:00Z"}]}]`))
	})
	item, err := cli.Status(context.Background(), "x", "prod_api")
	if err != nil {
		t.Fatal(err)
	}
	if item.Attributes["endpoint_key"] != "dev_prod_api" || item.Attributes["http_status"] != 503 {
		t.Fatalf("group/name alias did not resolve: %+v", item.Attributes)
	}
}

func TestStatus_DuplicateExactKeyIsAmbiguous(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"name":"one","key":"api","results":[{"success":true,"status":200,"timestamp":"2026-07-25T11:00:00Z"}]},
			{"name":"two","key":"api","results":[{"success":false,"status":503,"timestamp":"2026-07-25T11:00:00Z"}]}
		]`))
	})
	if _, err := cli.Status(context.Background(), "x", "api"); !errors.Is(err, ErrEndpointAmbiguous) {
		t.Fatalf("err=%v, want ambiguity", err)
	}
}

func TestStatus_MultipleAliasesAreAmbiguous(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"name":"api","key":"first","results":[{"success":true,"status":200,"timestamp":"2026-07-25T11:00:00Z"}]},
			{"name":"api","key":"second","results":[{"success":false,"status":503,"timestamp":"2026-07-25T11:00:00Z"}]}
		]`))
	})
	if _, err := cli.Status(context.Background(), "x", "api"); !errors.Is(err, ErrEndpointAmbiguous) {
		t.Fatalf("err=%v, want ambiguity", err)
	}
}

func TestEvidenceInWindow_ExactKeyBeatsEarlierAlias(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"name":"prod_api","group":"dev","key":"dev_prod_api","results":[{"success":true,"status":200,"timestamp":"2026-07-25T11:00:00Z"}]},
			{"name":"api","group":"prod","key":"prod_api","results":[{"success":false,"status":503,"timestamp":"2026-07-25T11:00:00Z"}]}
		]`))
	})
	start := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	window, err := cli.EvidenceInWindow(context.Background(), "x", "prod_api", start, end, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Items) != 1 || window.Items[0].Attributes["http_status"] != 503 {
		t.Fatalf("window used an alias: %+v", window.Items)
	}
}

func TestStatus_UnknownSourceTimeIsExplicit(t *testing.T) {
	for name, timestamp := range map[string]string{
		"invalid": `"timestamp":"invalid"`,
		"missing": "",
	} {
		t.Run(name, func(t *testing.T) {
			field := ""
			if timestamp != "" {
				field = "," + timestamp
			}
			cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`[{"name":"api","key":"api","results":[{"success":true,"status":200` + field + `}]}]`))
			})
			item, err := cli.Status(context.Background(), "x", "api")
			if err != nil {
				t.Fatal(err)
			}
			if item.Freshness != evidence.FreshnessUnknown {
				t.Fatalf("freshness=%s, want unknown", item.Freshness)
			}
			if item.Attributes["source_time_known"] != false || item.Attributes["collection_time_derived"] != true {
				t.Fatalf("unknown source time is not explicit: %+v", item.Attributes)
			}
			if !item.ObservedAt.Equal(time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)) {
				t.Fatalf("observed_at=%s, want collection time", item.ObservedAt)
			}
		})
	}
}

func TestStatus_MixedTimestampsSelectLatestValid(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"api","key":"api","results":[
			{"success":true,"status":200,"timestamp":"invalid"},
			{"success":false,"status":503,"timestamp":"2026-07-25T11:00:00Z"},
			{"success":true,"status":200}
		]}]`))
	})
	item, err := cli.Status(context.Background(), "x", "api")
	if err != nil {
		t.Fatal(err)
	}
	if item.Attributes["http_status"] != 503 || item.Attributes["source_time_known"] != true {
		t.Fatalf("latest valid result was not selected: %+v", item.Attributes)
	}
	if !item.ObservedAt.Equal(time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("observed_at=%s", item.ObservedAt)
	}
}

func TestStatusSelectionDiagnostics(t *testing.T) {
	cases := []struct {
		name            string
		body            string
		key             string
		matched         bool
		records         int
		discarded       int
		httpStatus      int
		successKnown    bool
		sourceTimeKnown bool
	}{
		{
			name: "valid success plus undated failure",
			body: `[{"name":"api","key":"api","results":[
				{"success":true,"status":200,"timestamp":"2026-07-25T11:00:00Z"},
				{"success":false,"status":503,"timestamp":"malformed"}
			]}]`,
			key: "api", matched: true, records: 2, discarded: 1,
			httpStatus: 200, successKnown: true, sourceTimeKnown: true,
		},
		{
			name: "reversed rows keep the selection and the count",
			body: `[{"name":"api","key":"api","results":[
				{"success":false,"status":503,"timestamp":"malformed"},
				{"success":true,"status":200,"timestamp":"2026-07-25T11:00:00Z"}
			]}]`,
			key: "api", matched: true, records: 2, discarded: 1,
			httpStatus: 200, successKnown: true, sourceTimeKnown: true,
		},
		{
			name: "missing success sibling is discarded",
			body: `[{"name":"api","key":"api","results":[
				{"success":true,"status":200,"timestamp":"2026-07-25T11:00:00Z"},
				{"status":503,"timestamp":"2026-07-25T10:00:00Z"}
			]}]`,
			key: "api", matched: true, records: 2, discarded: 1,
			httpStatus: 200, successKnown: true, sourceTimeKnown: true,
		},
		{
			name: "fully valid snapshot stays complete",
			body: `[{"name":"api","key":"api","results":[
				{"success":true,"status":200,"timestamp":"2026-07-25T10:00:00Z"},
				{"success":false,"status":503,"timestamp":"2026-07-25T11:00:00Z"},
				{"success":true,"status":200,"timestamp":"2026-07-25T09:00:00Z"}
			]}]`,
			key: "api", matched: true, records: 3, discarded: 0,
			httpStatus: 503, successKnown: true, sourceTimeKnown: true,
		},
		{
			name: "wholly undated snapshot counts every row",
			body: `[{"name":"api","key":"api","results":[
				{"success":true,"status":200,"timestamp":"malformed"},
				{"success":false,"status":503}
			]}]`,
			key: "api", matched: true, records: 2, discarded: 2,
			httpStatus: 200, successKnown: true, sourceTimeKnown: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			})
			status, err := cli.Status(context.Background(), "x", tc.key)
			if err != nil {
				t.Fatal(err)
			}
			if status.Matched != tc.matched || status.Records != tc.records || status.Discarded != tc.discarded {
				t.Fatalf("matched=%v records=%d discarded=%d", status.Matched, status.Records, status.Discarded)
			}
			item := status.Item
			if item.Attributes["http_status"] != tc.httpStatus ||
				item.Attributes["success_known"] != tc.successKnown ||
				item.Attributes["source_time_known"] != tc.sourceTimeKnown {
				t.Fatalf("selected item=%+v", item.Attributes)
			}
			if !tc.sourceTimeKnown && item.Freshness != evidence.FreshnessUnknown {
				t.Fatalf("undated selection freshness=%s, want unknown", item.Freshness)
			}
		})
	}

	t.Run("empty results keep an unknown state", func(t *testing.T) {
		cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`[{"name":"api","key":"api","results":[]}]`))
		})
		status, err := cli.Status(context.Background(), "x", "api")
		if err != nil {
			t.Fatal(err)
		}
		if !status.Matched || status.Records != 0 || status.Discarded != 0 {
			t.Fatalf("matched=%v records=%d discarded=%d", status.Matched, status.Records, status.Discarded)
		}
		if status.Severity != evidence.SeverityUnknown ||
			status.Attributes["results"] != 0 ||
			status.Freshness != evidence.FreshnessUnknown {
			t.Fatalf("empty endpoint gained a state: %+v", status.Item)
		}
	})

	t.Run("missing endpoint is not a matched empty snapshot", func(t *testing.T) {
		cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`[{"name":"other","key":"other","results":[{"success":true,"status":200,"timestamp":"2026-07-25T11:00:00Z"}]}]`))
		})
		status, err := cli.Status(context.Background(), "x", "api")
		if err != nil {
			t.Fatal(err)
		}
		if status.Matched || status.Records != 0 || status.Discarded != 0 ||
			status.Freshness != evidence.FreshnessMissing {
			t.Fatalf("missing endpoint status=%+v", status)
		}
	})
}

func TestEvidenceInWindow_DoesNotInventUndatedHistory(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"api","key":"api","results":[{"success":true,"status":200,"timestamp":"invalid"}]}]`))
	})
	start := time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	window, err := cli.EvidenceInWindow(context.Background(), "x", "api", start, end, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Items) != 0 || window.Discarded != 1 {
		t.Fatalf("undated result became history: items=%+v discarded=%d", window.Items, window.Discarded)
	}
}

func TestEvidenceInWindow_MissingEndpointHistoricalIsUnmatched(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"a","key":"a","results":[{"success":true,"status":200,"timestamp":"2026-07-25T11:00:00Z"}]}]`))
	})
	start := time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC)
	window, err := cli.EvidenceInWindow(context.Background(), "x", "missing", start, end, 10)
	if err != nil {
		t.Fatal(err)
	}
	if window.Matched || len(window.Items) != 0 {
		t.Fatalf("missing endpoint became matched history: %+v", window)
	}
}

func TestEvidenceInWindow_CountsInvalidRecords(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"a","key":"a","results":[
			{"success":true,"status":200,"timestamp":"2026-07-25T11:00:00Z"},
			{"status":500,"timestamp":"2026-07-25T11:10:00Z"},
			{"success":false,"status":500,"timestamp":"invalid"}
		]}]`))
	})
	start := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	window, err := cli.EvidenceInWindow(context.Background(), "x", "a", start, end, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Items) != 1 || window.Discarded != 2 {
		t.Fatalf("items=%d discarded=%d", len(window.Items), window.Discarded)
	}
	if window.Items[0].Severity != evidence.SeverityInfo {
		t.Fatalf("valid item severity=%s", window.Items[0].Severity)
	}
}

func TestEvidenceInWindow_ExplicitFalseWithoutStatusIsFailure(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"a","key":"a","results":[{"success":false,"timestamp":"2026-07-25T11:00:00Z"}]}]`))
	})
	start := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	window, err := cli.EvidenceInWindow(context.Background(), "x", "a", start, end, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(window.Items) != 1 || window.Items[0].Severity != evidence.SeverityError {
		t.Fatalf("explicit failure was not retained: %+v", window.Items)
	}
	if _, ok := window.Items[0].Attributes["http_status"]; ok {
		t.Fatalf("absent status was invented: %+v", window.Items[0].Attributes)
	}
}

func TestStatus_MissingSuccessIsUnknown(t *testing.T) {
	cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"a","key":"a","results":[{"status":500,"timestamp":"2026-07-25T11:00:00Z"}]}]`))
	})
	item, err := cli.Status(context.Background(), "x", "a")
	if err != nil {
		t.Fatal(err)
	}
	if item.Severity != evidence.SeverityUnknown || item.Attributes["success_known"] != false {
		t.Fatalf("missing success became a diagnosis: %+v", item)
	}
	if _, ok := item.Attributes["success"]; ok {
		t.Fatalf("absent success was invented: %+v", item.Attributes)
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
		t.Fatal("redaction data is missing")
	}
}
