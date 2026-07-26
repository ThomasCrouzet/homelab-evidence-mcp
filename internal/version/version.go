// Package version conserve la version partagée par la CLI, MCP et le User-Agent.
package version

// Version est injectée à l’édition des liens avec -ldflags.
// La valeur par défaut reste synchronisée avec le journal et le Makefile.
var Version = "0.1.0"

// UserAgent renvoie le User-Agent HTTP des adaptateurs.
func UserAgent() string {
	return "homelab-evidence-mcp/" + Version
}
