package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

// runtimeWriteConfig writes one fixture configuration outside the repository.
func runtimeWriteConfig(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write fixture configuration: %v", err)
	}
	return path
}

// runtimeConnect starts one executable stdio session and closes it after the test.
func runtimeConnect(t *testing.T, ctx context.Context, binary, configPath string) *mcp.ClientSession {
	t.Helper()
	// The process lifetime is independent of the test deadline, so a session
	// close can still finish after the test function returns.
	processCtx, stopProcess := context.WithCancel(context.Background())
	var stderr bytes.Buffer
	command := exec.CommandContext(processCtx, binary, "--config", configPath)
	command.Env = os.Environ()
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "runtime-campaign", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{
		Command:           command,
		TerminateDuration: stdioShutdownTimeout,
	}, nil)
	if err != nil {
		stopProcess()
		t.Fatalf("connect to executable: %v\nstderr:\n%s", err, stderr.String())
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close MCP session: %v", err)
		}
		stopProcess()
	})
	return session
}

// runtimeSources decodes the source outcomes of one MCP tool result.
func runtimeSources(t *testing.T, raw []byte) []evidence.SourceOutcome {
	t.Helper()
	var parsed struct {
		Sources []evidence.SourceOutcome `json:"sources"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("decode source outcomes: %v\n%s", err, raw)
	}
	return parsed.Sources
}

// runtimeFindSource returns the outcome for one source name and kind.
func runtimeFindSource(t *testing.T, sources []evidence.SourceOutcome, name string, kind evidence.SourceKind) evidence.SourceOutcome {
	t.Helper()
	for _, source := range sources {
		if source.SourceName == name && source.Kind == kind {
			return source
		}
	}
	t.Fatalf("source %s/%s is missing: %+v", name, kind, sources)
	return evidence.SourceOutcome{}
}
