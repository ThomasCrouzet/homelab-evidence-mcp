package loki

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestParseEqualitySelector_RejectsDuplicateLabels detects a duplicate
// equality key that is silently collapsed to a single matcher.
func TestParseEqualitySelector_RejectsDuplicateLabels(t *testing.T) {
	rejected := []string{
		`{container="frontend",container="backend"}`,
		`{container="frontend",container="frontend"}`,
		`{app="one",container="two",app="three"}`,
		`{app="one", app="two"} |= "error"`,
	}
	for _, selector := range rejected {
		t.Run(selector, func(t *testing.T) {
			got, err := parseEqualitySelector(selector)
			if err == nil {
				t.Fatalf("duplicate selector accepted: %v", got)
			}
			for _, leaked := range []string{"frontend", "backend", "container", "app", "one", "two", "three"} {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("error %q exposes selector content %q", err, leaked)
				}
			}
		})
	}
	got, err := parseEqualitySelector(`{app="api", namespace="prod"} |= "error"`)
	if err != nil {
		t.Fatalf("distinct labels rejected: %v", err)
	}
	if got["app"] != "api" || got["namespace"] != "prod" {
		t.Fatalf("selector=%v", got)
	}
}

// TestSearch_DuplicateSelectorNeverAttributes detects an invalid selector
// whose fixture stream could become attributed evidence.
func TestSearch_DuplicateSelectorNeverAttributes(t *testing.T) {
	var calls atomic.Int32
	start := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	lineAt := start.Add(10 * time.Minute)
	cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"streams","result":[`+
			`{"stream":{"container":"backend"},"values":[["%s","foreign line"]]}]}}`, nano(lineAt))
	})
	items, _, err := cli.Search(context.Background(), QueryOptions{
		ServiceID: "svc",
		Selector:  `{container="frontend",container="backend"}`,
		Start:     start,
		End:       start.Add(time.Hour),
		Limit:     10,
	})
	if err == nil {
		t.Fatalf("duplicate selector produced %d item(s)", len(items))
	}
	if calls.Load() != 0 {
		t.Fatalf("duplicate selector sent %d upstream request(s)", calls.Load())
	}
	if len(items) != 0 {
		t.Fatalf("duplicate selector returned evidence: %+v", items)
	}
}
