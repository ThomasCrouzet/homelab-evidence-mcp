package config

import (
	"strings"
	"testing"
)

func TestAuthHeaders_HealthchecksDefault(t *testing.T) {
	h := AuthHeaders(Source{Kind: "healthchecks"}, "ro-token")
	if h["X-Api-Key"] != "ro-token" {
		t.Fatalf("%+v", h)
	}
}

func TestAuthHeaders_BearerDefault(t *testing.T) {
	h := AuthHeaders(Source{Kind: "loki", Headers: map[string]string{"X-Scope-OrgID": "lab"}}, "abc")
	if h["Authorization"] != "Bearer abc" {
		t.Fatalf("%+v", h)
	}
	if h["X-Scope-Orgid"] != "lab" {
		t.Fatalf("%+v", h)
	}
}

func TestAuthHeaders_CustomHeader(t *testing.T) {
	h := AuthHeaders(Source{Kind: "gatus", TokenHeader: "X-Api-Key"}, "tok")
	if h["X-Api-Key"] != "tok" {
		t.Fatalf("%+v", h)
	}
}

func TestParse_RejectsAuthHeaderCollision(t *testing.T) {
	raw := []byte(`
version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
    token_env: REVIEW_TOKEN
    token_header: X-Access
    headers:
      x-access: fallback-value
services: []
`)
	_, err := Parse(raw)
	if err == nil {
		t.Fatal("expected a collision error")
	}
	message := err.Error()
	if !strings.Contains(message, "X-Access") || !strings.Contains(message, "conflicts") {
		t.Fatalf("error does not identify the header: %v", err)
	}
	if strings.Contains(message, "fallback-value") {
		t.Fatalf("error disclosed the static value: %v", err)
	}
}

func TestParse_RejectsDefaultAuthHeaderCollision(t *testing.T) {
	for _, kind := range []string{"gatus", "healthchecks"} {
		raw := []byte(`
version: 1
sources:
  src:
    kind: ` + kind + `
    base_url: https://src.example.internal
    token_env: REVIEW_TOKEN
    headers:
      AUTHORIZATION: fallback-value
services: []
`)
		if _, err := Parse(raw); err == nil {
			t.Fatalf("%s accepted a static Authorization header with a token", kind)
		}
	}
}

func TestAuthHeaders_CanonicalizesNames(t *testing.T) {
	h := AuthHeaders(Source{
		Kind: "loki",
		Headers: map[string]string{
			"x-scope-orgid": "lab",
			"X-Tenant":      "media",
		},
	}, "token-value")
	if h["X-Scope-Orgid"] != "lab" || h["X-Tenant"] != "media" {
		t.Fatalf("%+v", h)
	}
	if h["Authorization"] != "Bearer token-value" {
		t.Fatalf("%+v", h)
	}
	for key := range h {
		if key != "X-Scope-Orgid" && key != "X-Tenant" && key != "Authorization" {
			t.Fatalf("unexpected key spelling %q", key)
		}
	}
}

func TestHasAnyBinding(t *testing.T) {
	if HasAnyBinding(Service{}) {
		t.Fatal("expected false")
	}
	if !HasAnyBinding(Service{Sources: ServiceSources{Gatus: &GatusRef{Source: "g"}}}) {
		t.Fatal("expected true")
	}
}
