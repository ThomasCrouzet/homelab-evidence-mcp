package audit

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

type auditFailureWriter struct {
	fail  bool
	short bool
}

func (w *auditFailureWriter) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	if w.fail {
		return 0, errors.New("sensitive-path/should-not-appear")
	}
	return len(p), nil
}

func TestLogJSONLine(t *testing.T) {
	var out bytes.Buffer
	l := New(&out)
	l.Log(Event{Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Action: "tool", Tool: "list_services", Status: "ok"})
	s := out.String()
	if !strings.Contains(s, `"action":"tool"`) || !strings.HasSuffix(s, "\n") {
		t.Fatal(s)
	}
}

func TestLogReportsOneGenericDiagnosticPerFailurePeriod(t *testing.T) {
	out := &auditFailureWriter{fail: true}
	var diagnostic bytes.Buffer
	l := New(out, &diagnostic)
	event := Event{Action: "tool", Tool: "list_services", Status: "ok"}

	l.Log(event)
	l.Log(event)
	if got := diagnostic.String(); got != "audit write failed\n" {
		t.Fatalf("diagnostic=%q", got)
	}
	if strings.Contains(diagnostic.String(), "sensitive-path") {
		t.Fatal("diagnostic contains writer error text")
	}

	out.fail = false
	l.Log(event)
	out.fail = true
	l.Log(event)
	if got := diagnostic.String(); got != "audit write failed\naudit write failed\n" {
		t.Fatalf("diagnostic after recovery=%q", got)
	}
}

func TestLogReportsShortWrite(t *testing.T) {
	out := &auditFailureWriter{short: true}
	var diagnostic bytes.Buffer
	l := New(out, &diagnostic)

	l.Log(Event{Action: "tool", Status: "ok"})
	if got := diagnostic.String(); got != "audit write failed\n" {
		t.Fatalf("diagnostic=%q", got)
	}
}
