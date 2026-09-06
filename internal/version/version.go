// Package version holds the version shared by the CLI, MCP, and User-Agent.
package version

// The linker sets Version at link time through -ldflags.
// The default value stays in sync with the changelog and Makefile.
var Version = "0.1.1"

// UserAgent gives the HTTP User-Agent used by adapters.
func UserAgent() string {
	return "homelab-evidence-mcp/" + Version
}
