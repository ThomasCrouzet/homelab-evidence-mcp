// Package version holds the version shared by the CLI, MCP, and User-Agent.
package version

import (
	_ "embed"
	"strings"
)

// version.txt is the authoritative source version. It works with go install
// from a tagged module and with local builds. The linker can set Version for an
// explicit build override.
//
//go:embed version.txt
var versionFile string

// Version is the resolved release version. The linker can set it for an
// explicit build. The embedded file supplies the default.
var Version string

func init() {
	if Version == "" {
		Version = strings.TrimSpace(versionFile)
	}
}

// UserAgent gives the HTTP User-Agent used by adapters.
func UserAgent() string {
	return "homelab-evidence-mcp/" + Version
}
