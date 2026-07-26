package version

import (
	"strings"
	"testing"
)

func TestVersionNonEmpty(t *testing.T) {
	if strings.TrimSpace(Version) == "" {
		t.Fatal("empty Version")
	}
}

func TestUserAgentContainsVersion(t *testing.T) {
	ua := UserAgent()
	if !strings.Contains(ua, Version) {
		t.Fatalf("%q missing %q", ua, Version)
	}
	if !strings.HasPrefix(ua, "homelab-evidence-mcp/") {
		t.Fatal(ua)
	}
}
