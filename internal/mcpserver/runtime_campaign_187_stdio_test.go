package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

type runtimeBindingOutcome struct {
	serviceID string
	outcome   evidence.SourceOutcome
}

// runtimeDoctorRow aggregates binding outcomes in the given completion order.
func runtimeDoctorRow(t *testing.T, outcomes []runtimeBindingOutcome) DoctorSource {
	t.Helper()
	app := &App{}
	source := &DoctorSource{Name: "gatus", Kind: "gatus", Status: "unbound", Complete: true}
	byName := map[string]*DoctorSource{"gatus": source}
	var mu sync.Mutex
	for _, item := range outcomes {
		outcome := item.outcome
		app.doctorBinding(context.Background(), &mu, byName, "gatus", item.serviceID, func(context.Context) (evidence.SourceOutcome, []evidence.Item) {
			return outcome, nil
		})
	}
	return *source
}

// This test detects a doctor source row that depends on worker completion
// order, and a status that disagrees with the selected reason.
func TestDoctorSourceAggregateIsOrderIndependent(t *testing.T) {
	partialInvalid := evidence.SourceOutcome{
		SourceName: "gatus", Kind: evidence.SourceGatus, Status: "ok", Complete: false,
		ItemCount: 1, Reason: "partial_invalid_records",
	}
	unmatched := evidence.SourceOutcome{
		SourceName: "gatus", Kind: evidence.SourceGatus, Status: "ok", Complete: false,
		Reason: "unmatched_binding",
	}
	timedOut := evidence.SourceOutcome{
		SourceName: "gatus", Kind: evidence.SourceGatus, Status: "timeout", Complete: false,
		Reason: "timeout", Error: "source deadline exceeded",
	}
	failed := evidence.SourceOutcome{
		SourceName: "gatus", Kind: evidence.SourceGatus, Status: "error", Complete: false,
		Reason: "upstream_error", Error: "gatus HTTP 500",
	}
	failedOther := evidence.SourceOutcome{
		SourceName: "gatus", Kind: evidence.SourceGatus, Status: "error", Complete: false,
		Reason: "upstream_error", Error: "gatus decode failed",
	}

	cases := []struct {
		name    string
		first   runtimeBindingOutcome
		second  runtimeBindingOutcome
		status  string
		reason  string
		errText string
	}{
		{
			name:    "two partial reasons use the reason tie-break",
			first:   runtimeBindingOutcome{serviceID: "svc-b", outcome: unmatched},
			second:  runtimeBindingOutcome{serviceID: "svc-a", outcome: partialInvalid},
			status:  "partial",
			reason:  "partial_invalid_records",
			errText: "",
		},
		{
			name:    "timeout outranks partial in both orders",
			first:   runtimeBindingOutcome{serviceID: "svc-a", outcome: timedOut},
			second:  runtimeBindingOutcome{serviceID: "svc-b", outcome: unmatched},
			status:  "timeout",
			reason:  "timeout",
			errText: "source deadline exceeded",
		},
		{
			name:    "error outranks timeout in both orders",
			first:   runtimeBindingOutcome{serviceID: "svc-b", outcome: timedOut},
			second:  runtimeBindingOutcome{serviceID: "svc-a", outcome: failed},
			status:  "error",
			reason:  "upstream_error",
			errText: "gatus HTTP 500",
		},
		{
			name:    "equal rank uses the service ID tie-break",
			first:   runtimeBindingOutcome{serviceID: "svc-b", outcome: failedOther},
			second:  runtimeBindingOutcome{serviceID: "svc-a", outcome: failed},
			status:  "error",
			reason:  "upstream_error",
			errText: "gatus HTTP 500",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			forward := runtimeDoctorRow(t, []runtimeBindingOutcome{testCase.first, testCase.second})
			reverse := runtimeDoctorRow(t, []runtimeBindingOutcome{testCase.second, testCase.first})
			for label, row := range map[string]DoctorSource{"forward": forward, "reverse": reverse} {
				if row.Status != testCase.status || row.Reason != testCase.reason || row.Error != testCase.errText {
					t.Fatalf("%s row status=%q reason=%q error=%q, want status=%q reason=%q error=%q",
						label, row.Status, row.Reason, row.Error, testCase.status, testCase.reason, testCase.errText)
				}
				if len(row.Bindings) != 2 {
					t.Fatalf("%s row lost a binding: %+v", label, row.Bindings)
				}
			}
			if forward.Status != reverse.Status || forward.Reason != reverse.Reason || forward.Error != reverse.Error {
				t.Fatalf("completion order changed the row:\nforward=%+v\nreverse=%+v", forward, reverse)
			}
		})
	}
}

// This test detects a doctor report whose aggregated source row does not match
// the worst binding, and an unstable reason across repeated diagnostics.
func TestStdioDoctorSourceRowMatchesWorstBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	now := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/endpoints/statuses" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[
			{"name":"runtime-ok","key":"runtime-ok","results":[{"success":true,"status":200,"timestamp":"` +
			now.Add(-2*time.Minute).Format(time.RFC3339) + `"}]},
			{"name":"runtime-invalid","key":"runtime-invalid","results":[{"status":200,"timestamp":"` +
			now.Add(-2*time.Minute).Format(time.RFC3339) + `"}]}
		]`))
	}))
	defer server.Close()

	raw := `version: 1
limits:
  per_source_timeout: 2s
  total_timeout: 10s
  source_cache_ttl: 0s
sources:
  gatus-runtime:
    kind: gatus
    base_url: ` + server.URL + `
services:
  - id: runtime-ok
    sources:
      gatus: { source: gatus-runtime, endpoint_key: runtime-ok }
  - id: runtime-invalid
    sources:
      gatus: { source: gatus-runtime, endpoint_key: runtime-invalid }
  - id: runtime-missing
    sources:
      gatus: { source: gatus-runtime, endpoint_key: runtime-missing }
`
	binary := stdioTestBinary(t, ctx)
	configPath := runtimeWriteConfig(t, raw)

	var previous string
	for run := 0; run < 3; run++ {
		report, code := runtimeDoctor(t, ctx, binary, configPath)
		if code != 1 {
			t.Fatalf("doctor exit=%d, want 1: %+v", code, report)
		}
		if len(report.Sources) != 1 {
			t.Fatalf("doctor sources=%d, want 1", len(report.Sources))
		}
		source := report.Sources[0]
		if source.Status != "partial" || source.Reason != "partial_invalid_records" || source.Complete {
			t.Fatalf("run %d source status=%q reason=%q complete=%v, want partial/partial_invalid_records",
				run, source.Status, source.Reason, source.Complete)
		}
		if len(source.Bindings) != 3 {
			t.Fatalf("run %d binding count=%d, want 3: %+v", run, len(source.Bindings), source.Bindings)
		}
		reasons := map[string]string{}
		for _, binding := range source.Bindings {
			reasons[binding.ServiceID] = binding.Status + "/" + binding.Reason
		}
		if reasons["runtime-ok"] != "ok/" || reasons["runtime-invalid"] != "ok/partial_invalid_records" ||
			reasons["runtime-missing"] != "ok/unmatched_binding" {
			t.Fatalf("run %d binding outcomes=%+v", run, reasons)
		}
		encoded, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		if previous != "" && string(encoded) != previous {
			t.Fatalf("run %d changed the source row:\nbefore=%s\nafter=%s", run, previous, encoded)
		}
		previous = string(encoded)
	}
}
