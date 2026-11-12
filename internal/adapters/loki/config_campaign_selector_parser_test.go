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

// TestParseEqualitySelector_Delimiters detects a label-set scan that ends at
// a brace inside a quoted value, and any content that the parser does not
// consume.
func TestParseEqualitySelector_Delimiters(t *testing.T) {
	rejected := []string{
		`{container="frontend",note="x}y",container="backend"}`,
		`{container="frontend",note="x}y",container="frontend"}`,
		`{container="frontend",note="x\"}y",container="backend"}`,
		`{container="frontend",note="x\\",container="backend"}`,
		`{container="frontend"} trailing`,
		`{container="frontend"} | json`,
		`{container="frontend",note="x}y"`,
	}
	for _, selector := range rejected {
		t.Run(selector, func(t *testing.T) {
			got, err := parseEqualitySelector(selector)
			if err == nil {
				t.Fatalf("selector accepted with %d matcher(s): %v", len(got), got)
			}
			for _, leaked := range []string{"frontend", "backend", "x}y"} {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("error %q exposes selector content %q", err, leaked)
				}
			}
		})
	}

	cases := []struct {
		selector string
		want     map[string]string
	}{
		{
			selector: `{container="frontend",note="x}y"}`,
			want:     map[string]string{"container": "frontend", "note": "x}y"},
		},
		{
			selector: `{container="frontend",note="x\"}y"}`,
			want:     map[string]string{"container": "frontend", "note": `x"}y`},
		},
		{
			selector: `{container="frontend",note="x\\y"}`,
			want:     map[string]string{"container": "frontend", "note": `x\y`},
		},
		{
			selector: `{container="frontend",note="x}y"} |= "error"`,
			want:     map[string]string{"container": "frontend", "note": "x}y"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.selector, func(t *testing.T) {
			got, err := parseEqualitySelector(tc.selector)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("matchers=%v, want %v", got, tc.want)
			}
			for key, want := range tc.want {
				if got[key] != want {
					t.Fatalf("matcher %q=%q, want %q", key, got[key], want)
				}
			}
		})
	}
}

// TestSearch_QuotedBraceSelector detects a local verifier that compares a
// truncated set of equality labels, so a stream that contradicts a quoted
// brace value becomes attributed evidence.
func TestSearch_QuotedBraceSelector(t *testing.T) {
	start := time.Date(2026, 7, 25, 11, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	lineAt := start.Add(10 * time.Minute)

	t.Run("mismatched quoted brace is not attributed", func(t *testing.T) {
		var calls atomic.Int32
		cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"streams","result":[`+
				`{"stream":{"container":"frontend","note":"wrong"},"values":[["%s","foreign line"]]},`+
				`{"stream":{"container":"frontend","note":"x"},"values":[["%s","truncated line"]]},`+
				`{"stream":{"container":"frontend","note":"x}y"},"values":[["%s","owned line"]]}]}}`,
				nano(lineAt), nano(lineAt), nano(lineAt))
		})
		items, _, err := cli.Search(context.Background(), QueryOptions{
			ServiceID: "svc",
			Selector:  `{container="frontend",note="x}y"}`,
			Start:     start,
			End:       end,
			Limit:     10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 1 {
			t.Fatalf("requests=%d, want 1", calls.Load())
		}
		if len(items) != 1 || items[0].Summary != "owned line" {
			t.Fatalf("mismatched stream attributed: %+v", items)
		}
	})

	t.Run("duplicate hidden after quoted brace sends no request", func(t *testing.T) {
		var calls atomic.Int32
		cli := setup(t, func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"streams","result":[`+
				`{"stream":{"container":"frontend"},"values":[["%s","foreign line"]]}]}}`, nano(lineAt))
		})
		items, _, err := cli.Search(context.Background(), QueryOptions{
			ServiceID: "svc",
			Selector:  `{container="frontend",note="x}y",container="backend"}`,
			Start:     start,
			End:       end,
			Limit:     10,
		})
		if err == nil {
			t.Fatalf("duplicate selector produced %d item(s)", len(items))
		}
		if calls.Load() != 0 || len(items) != 0 {
			t.Fatalf("duplicate selector sent %d request(s) and returned %d item(s)", calls.Load(), len(items))
		}
	})
}
