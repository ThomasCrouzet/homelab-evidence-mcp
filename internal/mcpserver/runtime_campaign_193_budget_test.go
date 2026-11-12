package mcpserver

import (
	"errors"
	"strings"
	"testing"
)

// This test detects tool budget refusals that cannot be classified by identity.
// The readable phrases stay stable for existing callers and tests.
func TestToolBudgetRefusalsAreTyped(t *testing.T) {
	t.Run("concurrent", func(t *testing.T) {
		budget := newToolBudget(1000, 1)
		if err := budget.acquire(); err != nil {
			t.Fatalf("first acquire failed: %v", err)
		}
		defer budget.release()
		err := budget.acquire()
		if err == nil {
			t.Fatal("a second concurrent call was accepted")
		}
		if !errors.Is(err, errToolBudgetConcurrent) {
			t.Fatalf("concurrent refusal is not typed: %v", err)
		}
		if !strings.Contains(err.Error(), "too many concurrent tool calls (budget)") {
			t.Fatalf("concurrent phrase changed: %q", err.Error())
		}
		if errors.Is(err, errToolBudgetRate) {
			t.Fatalf("concurrent refusal also matches the rate sentinel: %v", err)
		}
	})

	t.Run("rate", func(t *testing.T) {
		budget := newToolBudget(1, 4)
		if err := budget.acquire(); err != nil {
			t.Fatalf("first acquire failed: %v", err)
		}
		defer budget.release()
		err := budget.acquire()
		if err == nil {
			t.Fatal("a call above the per-minute budget was accepted")
		}
		if !errors.Is(err, errToolBudgetRate) {
			t.Fatalf("rate refusal is not typed: %v", err)
		}
		if !strings.Contains(err.Error(), "tool call rate limit exceeded (1/min)") {
			t.Fatalf("rate phrase changed: %q", err.Error())
		}
		if errors.Is(err, errToolBudgetConcurrent) {
			t.Fatalf("rate refusal also matches the concurrent sentinel: %v", err)
		}
	})
}
