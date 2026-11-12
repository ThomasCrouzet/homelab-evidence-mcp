package config

import (
	"fmt"
	"strings"
	"testing"
)

func lokiSelectorConfig(selector string) []byte {
	return []byte(fmt.Sprintf(`version: 1
sources:
  loki:
    kind: loki
    base_url: https://loki.example.internal
services:
  - id: svc
    sources:
      loki: { source: loki, selector: '%s' }
`, selector))
}

// TestParse_RejectsDuplicateLokiEqualityLabels detects configuration
// validation that accepts two equality matchers for the same label.
func TestParse_RejectsDuplicateLokiEqualityLabels(t *testing.T) {
	rejected := []string{
		`{container="frontend",container="backend"}`,
		`{container="frontend",container="frontend"}`,
		`{app="one",container="two",app="three"}`,
	}
	for _, selector := range rejected {
		t.Run(selector, func(t *testing.T) {
			_, err := Parse(lokiSelectorConfig(selector))
			if err == nil {
				t.Fatalf("Parse accepted %s", selector)
			}
			if !strings.Contains(err.Error(), "services[0].sources.loki.selector") {
				t.Fatalf("error %q does not name the selector field", err)
			}
			for _, leaked := range []string{"frontend", "backend", "container", "app", "one", "two", "three"} {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("error %q exposes selector content %q", err, leaked)
				}
			}
		})
	}
	accepted := []string{
		`{app="x",host="y"} |= "error"`,
		`{container="api"}`,
	}
	for _, selector := range accepted {
		t.Run(selector, func(t *testing.T) {
			if _, err := Parse(lokiSelectorConfig(selector)); err != nil {
				t.Fatalf("Parse rejected %s: %v", selector, err)
			}
		})
	}
}
