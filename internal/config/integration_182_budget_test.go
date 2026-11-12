package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"gopkg.in/yaml.v3"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
)

func cacheBudgetConfig(value *int64) []byte {
	limits := "limits: {}\n"
	if value != nil {
		limits = fmt.Sprintf("limits:\n  source_cache_max_bytes: %d\n", *value)
	}
	return []byte("version: 1\n" + limits + `sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
services: []
`)
}

func cacheBudgetValue(value int64) *int64 {
	return &value
}

func TestParse_SourceCacheMaxBytes(t *testing.T) {
	cases := []struct {
		name    string
		value   *int64
		want    int64
		wantErr bool
	}{
		{"missing", nil, httpx.DefaultSourceCacheMaxBytes, false},
		{"zero uses default", cacheBudgetValue(0), httpx.DefaultSourceCacheMaxBytes, false},
		{"minimum", cacheBudgetValue(httpx.MinSourceCacheMaxBytes), httpx.MinSourceCacheMaxBytes, false},
		{"maximum", cacheBudgetValue(httpx.MaxSourceCacheMaxBytes), httpx.MaxSourceCacheMaxBytes, false},
		{"one is rejected", cacheBudgetValue(1), 0, true},
		{"below minimum is rejected", cacheBudgetValue(httpx.MinSourceCacheMaxBytes - 1), 0, true},
		{"negative is rejected", cacheBudgetValue(-1), 0, true},
		{"above maximum is rejected", cacheBudgetValue(httpx.MaxSourceCacheMaxBytes + 1), 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse(cacheBudgetConfig(tc.value))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected a validation error")
				}
				if !strings.Contains(err.Error(), "limits.source_cache_max_bytes") {
					t.Fatalf("error does not name the field: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if cfg.Limits.SourceCacheMaxBytes != tc.want {
				t.Fatalf("effective limit=%d, want %d", cfg.Limits.SourceCacheMaxBytes, tc.want)
			}
		})
	}
}

// TestConfigSchema_SourceCacheMaxBytesParity detects drift between the schema
// acceptance and the parser acceptance at every boundary value.
func TestConfigSchema_SourceCacheMaxBytesParity(t *testing.T) {
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
	validate := func(raw []byte) error {
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
	values := []int64{
		0,
		1,
		httpx.MinSourceCacheMaxBytes - 1,
		httpx.MinSourceCacheMaxBytes,
		httpx.MaxSourceCacheMaxBytes,
		httpx.MaxSourceCacheMaxBytes + 1,
		-1,
	}
	for _, value := range values {
		raw := cacheBudgetConfig(cacheBudgetValue(value))
		_, parseErr := Parse(raw)
		schemaErr := validate(raw)
		if (parseErr == nil) != (schemaErr == nil) {
			t.Fatalf("value %d: parser error=%v, schema error=%v", value, parseErr, schemaErr)
		}
	}
	// A missing field is accepted by both layers.
	missing := cacheBudgetConfig(nil)
	if _, err := Parse(missing); err != nil {
		t.Fatalf("parser rejected a missing field: %v", err)
	}
	if err := validate(missing); err != nil {
		t.Fatalf("schema rejected a missing field: %v", err)
	}
}

func TestConfigExample_SourceCacheMaxBytes(t *testing.T) {
	example, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(example, []byte("source_cache_max_bytes: 33554432")) {
		t.Fatal("config.example.yaml does not show the 32 MiB default")
	}
	cfg, err := Parse(example)
	if err != nil {
		t.Fatalf("runtime rejected the shipped example: %v", err)
	}
	if cfg.Limits.SourceCacheMaxBytes != httpx.DefaultSourceCacheMaxBytes {
		t.Fatalf("example effective limit=%d, want %d", cfg.Limits.SourceCacheMaxBytes, httpx.DefaultSourceCacheMaxBytes)
	}
}
