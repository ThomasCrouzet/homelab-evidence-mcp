package version

import (
	"regexp"
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

func TestVersionComesFromTheSourceFile(t *testing.T) {
	if got := strings.TrimSpace(versionFile); Version != got {
		t.Fatalf("Version=%q, source file=%q", Version, got)
	}
	semver := regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)
	if !semver.MatchString(Version) {
		t.Fatalf("Version=%q is not a supported release form", Version)
	}
}
