package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"gopkg.in/yaml.v3"
)

func sourceRequestRateConfig(limitLine string) []byte {
	limits := ""
	if limitLine != "" {
		limits = fmt.Sprintf("limits:\n%s\n", limitLine)
	}
	return []byte(fmt.Sprintf(`version: 1
%ssources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
services:
  - id: app
    sources:
      gatus: { source: gatus, endpoint_key: app }
`, limits))
}

// TestParse_SourceRequestRateEffectiveLimit detects a raw
// max_source_requests_per_minute value that reaches callers without the
// default and range that the JSON Schema documents.
func TestParse_SourceRequestRateEffectiveLimit(t *testing.T) {
	cases := []struct {
		name  string
		line  string
		want  int
		field string
	}{
		{name: "omitted", line: "", want: 100},
		{name: "explicit zero", line: "  max_source_requests_per_minute: 0", want: 100},
		{name: "explicit one", line: "  max_source_requests_per_minute: 1", want: 1},
		{name: "explicit maximum", line: "  max_source_requests_per_minute: 1000", want: 1000},
		{name: "negative", line: "  max_source_requests_per_minute: -1", field: "limits.max_source_requests_per_minute"},
		{name: "above maximum", line: "  max_source_requests_per_minute: 1001", field: "limits.max_source_requests_per_minute"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse(sourceRequestRateConfig(tc.line))
			if tc.field != "" {
				if err == nil {
					t.Fatalf("Parse accepted %s", tc.line)
				}
				if !strings.Contains(err.Error(), tc.field) {
					t.Fatalf("error %q does not name %s", err, tc.field)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if cfg.Limits.MaxSourceRequestsPerMinute != tc.want {
				t.Fatalf("effective limit=%d, want %d", cfg.Limits.MaxSourceRequestsPerMinute, tc.want)
			}
		})
	}
}

// TestConfigSchema_SourceRequestRateLimits detects drift between the schema
// range and the parser range for the source request rate.
func TestConfigSchema_SourceRequestRateLimits(t *testing.T) {
	schemaRaw, err := os.ReadFile("../../config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatalf("schema does not resolve: %v", err)
	}
	validate := func(line string) error {
		raw := sourceRequestRateConfig(line)
		var doc any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		asJSON, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(asJSON, &value); err != nil {
			t.Fatal(err)
		}
		return resolved.Validate(value)
	}
	for _, line := range []string{
		"  max_source_requests_per_minute: 0",
		"  max_source_requests_per_minute: 1",
		"  max_source_requests_per_minute: 1000",
	} {
		if err := validate(line); err != nil {
			t.Fatalf("schema rejected %q: %v", line, err)
		}
	}
	for _, line := range []string{
		"  max_source_requests_per_minute: -1",
		"  max_source_requests_per_minute: 1001",
	} {
		if err := validate(line); err == nil {
			t.Fatalf("schema accepted %q", line)
		}
	}
}
