package config

import (
	"strings"
	"testing"
)

// TestParse_LokiSelectorDelimiters detects a label-set scan that ends at a
// brace inside a quoted value. The trailing duplicate key then escapes the
// duplicate check.
func TestParse_LokiSelectorDelimiters(t *testing.T) {
	rejected := []struct {
		name     string
		selector string
		leaks    []string
	}{
		{
			name:     "conflicting duplicate after quoted brace",
			selector: `{container="frontend",note="x}y",container="backend"}`,
			leaks:    []string{"frontend", "backend", "x}y"},
		},
		{
			name:     "equal duplicate after quoted brace",
			selector: `{container="frontend",note="x}y",container="frontend"}`,
			leaks:    []string{"frontend", "x}y"},
		},
		{
			name:     "duplicate after escaped quote and brace",
			selector: `{container="frontend",note="x\"}y",container="backend"}`,
			leaks:    []string{"frontend", "backend"},
		},
		{
			name:     "duplicate after backslash value",
			selector: `{container="frontend",note="x\\",container="backend"}`,
			leaks:    []string{"frontend", "backend"},
		},
		{
			name:     "trailing content after label set",
			selector: `{container="frontend"} trailing`,
		},
		{
			name:     "non-equality filter",
			selector: `{container="frontend"} | json`,
		},
		{
			name:     "unterminated label set",
			selector: `{container="frontend",note="x}y"`,
		},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(lokiSelectorConfig(tc.selector))
			if err == nil {
				t.Fatalf("Parse accepted %s", tc.selector)
			}
			if !strings.Contains(err.Error(), "services[0].sources.loki.selector") {
				t.Fatalf("error %q does not name the selector field", err)
			}
			for _, leaked := range tc.leaks {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("error %q exposes selector content %q", err, leaked)
				}
			}
		})
	}

	accepted := []string{
		`{container="frontend",note="x}y"}`,
		`{container="frontend",note="x\"}y"}`,
		`{container="frontend",note="x\\y"}`,
		`{container="frontend",note="x}y"} |= "error"`,
		`{container="frontend",note="x}y"} |~ "warn"`,
	}
	for _, selector := range accepted {
		t.Run(selector, func(t *testing.T) {
			if _, err := Parse(lokiSelectorConfig(selector)); err != nil {
				t.Fatalf("Parse rejected %s: %v", selector, err)
			}
		})
	}
}
