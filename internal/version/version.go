// Package version holds the version shared by the CLI, MCP, and User-Agent.
package version

// Version is injected at link time with -ldflags.
// The default value stays in sync with the changelog and Makefile.
var Version = "0.1.0"

// UserAgent returns the HTTP User-Agent used by adapters.
func UserAgent() string {
	return "homelab-evidence-mcp/" + Version
}
