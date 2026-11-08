package mcpserver

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
)

func TestToolBudget_RateLimit(t *testing.T) {
	b := newToolBudget(3, 8)
	for i := 0; i < 3; i++ {
		if err := b.acquire(); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		b.release()
	}
	err := b.acquire()
	if err == nil {
		t.Fatal("expected per-minute rate limit error")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestToolBudget_ConcurrentLimit(t *testing.T) {
	b := newToolBudget(1000, 2)
	if err := b.acquire(); err != nil {
		t.Fatal(err)
	}
	if err := b.acquire(); err != nil {
		t.Fatal(err)
	}
	err := b.acquire()
	if err == nil {
		t.Fatal("expected concurrent budget error")
	}
	if !strings.Contains(err.Error(), "concurrent") {
		t.Fatalf("unexpected error: %v", err)
	}
	b.release()
	if err := b.acquire(); err != nil {
		t.Fatalf("after release: %v", err)
	}
	b.release()
	b.release()
}

func TestWithBudget_DeniesWhenConcurrentExceeded(t *testing.T) {
	app := &App{budget: newToolBudget(1000, 1)}
	started := make(chan struct{})
	releaseHold := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, _ = app.withBudget(context.Background(), "service_status", func() (*mcp.CallToolResult, any, error) {
			close(started)
			<-releaseHold
			return textResult(map[string]string{"ok": "held"}), map[string]string{"ok": "held"}, nil
		})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("holder did not start")
	}
	res, _, err := app.withBudget(context.Background(), "service_status", func() (*mcp.CallToolResult, any, error) {
		t.Fatal("second call must not run under concurrent budget of 1")
		return nil, nil, nil
	})
	if err != nil {
		t.Fatalf("withBudget should give a tool error result, not a Go error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError budget result, got %+v", res)
	}
	txt, ok := res.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(txt.Text, "concurrent") {
		t.Fatalf("expected concurrent budget message, got %+v", res.Content)
	}
	close(releaseHold)
	wg.Wait()
}

func TestWithBudget_DeniesWhenRateExceeded(t *testing.T) {
	app := &App{budget: newToolBudget(2, 8)}
	for i := 0; i < 2; i++ {
		res, _, err := app.withBudget(context.Background(), "service_status", func() (*mcp.CallToolResult, any, error) {
			return textResult(map[string]string{"ok": "yes"}), nil, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if res == nil || res.IsError {
			t.Fatalf("call %d should succeed: %+v", i, res)
		}
	}
	res, _, err := app.withBudget(context.Background(), "service_status", func() (*mcp.CallToolResult, any, error) {
		t.Fatal("third call must not run")
		return nil, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatal("expected rate limit error result")
	}
	txt := res.Content[0].(*mcp.TextContent)
	if !strings.Contains(txt.Text, "rate limit") {
		t.Fatalf("got %s", txt.Text)
	}
}

func TestSanitizeErr_UsesRedaction(t *testing.T) {
	eng, err := redaction.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	msg := sanitizeErr(errString("upstream password=supersecret-token-value failed"), eng)
	if strings.Contains(msg, "supersecret-token-value") {
		t.Fatalf("secret leaked: %s", msg)
	}
	if !strings.Contains(msg, "[REDACTED]") {
		t.Fatalf("expected redaction marker: %s", msg)
	}
	// The path without an engine must redact built-in secret patterns.
	msg2 := sanitizeErr(errString("request failed token=aaaaaaaaaaaaaaaaaaaa"), nil)
	if strings.Contains(msg2, "aaaaaaaaaaaaaaaaaaaa") {
		t.Fatalf("token leaked: %s", msg2)
	}
	if !strings.Contains(msg2, "[REDACTED]") {
		t.Fatalf("expected redaction: %s", msg2)
	}
	msg3 := sanitizeErr(errString(strings.Repeat("é", 301)), nil)
	if !utf8.ValidString(msg3) || utf8.RuneCountInString(msg3) != 300 {
		t.Fatalf("invalid UTF-8 truncation: %q", msg3)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestWithBudget_CanceledHandlerKeepsSlotUntilExit(t *testing.T) {
	app := &App{budget: newToolBudget(1000, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = app.withBudget(ctx, "service_status", func() (*mcp.CallToolResult, any, error) {
			close(started)
			<-release
			return textResult(map[string]string{"ok": "held"}), nil, nil
		})
	}()
	<-started
	cancel()
	ran := false
	res, _, err := app.withBudget(context.Background(), "service_status", func() (*mcp.CallToolResult, any, error) {
		ran = true
		return textResult(map[string]string{"ok": "second"}), nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if ran || res == nil || !res.IsError {
		t.Fatalf("a canceled handler released its slot early: ran=%v res=%+v", ran, res)
	}
	close(release)
	<-done
	if res, _, err := app.withBudget(context.Background(), "service_status", func() (*mcp.CallToolResult, any, error) {
		return textResult(map[string]string{"ok": "after"}), nil, nil
	}); err != nil || res.IsError {
		t.Fatalf("slot was not released after the handler exited: %v %+v", err, res)
	}
}

func TestToolBudget_SlidingMinuteWindow(t *testing.T) {
	b := newToolBudget(2, 8)
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	b.clock = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		if err := b.acquire(); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		b.release()
	}
	now = now.Add(59 * time.Second)
	if err := b.acquire(); err == nil {
		t.Fatal("a burst at the next fixed minute bypassed the limit")
		b.release()
	}
	now = now.Add(2 * time.Second)
	if err := b.acquire(); err != nil {
		t.Fatalf("expired calls still count: %v", err)
	}
	b.release()
}
