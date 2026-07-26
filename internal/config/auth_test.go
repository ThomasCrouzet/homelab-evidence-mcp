package config

import "testing"

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
	if h["X-Scope-OrgID"] != "lab" {
		t.Fatalf("%+v", h)
	}
}

func TestAuthHeaders_CustomHeader(t *testing.T) {
	h := AuthHeaders(Source{Kind: "gatus", TokenHeader: "X-Api-Key"}, "tok")
	if h["X-Api-Key"] != "tok" {
		t.Fatalf("%+v", h)
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
