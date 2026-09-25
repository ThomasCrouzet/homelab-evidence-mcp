package version

import (
	"strings"
	"testing"
)

func TestUserAgentContainsVersion(t *testing.T) {
	ua := UserAgent()
	if !strings.Contains(ua, Version) {
		t.Fatalf("%q missing %q", ua, Version)
	}
	if !strings.HasPrefix(ua, "homelab-evidence-mcp/") {
		t.Fatal(ua)
	}
}
