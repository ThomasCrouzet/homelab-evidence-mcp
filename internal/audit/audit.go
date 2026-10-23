// Package audit writes structured events to stderr or a file.
package audit

import (
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// Event contains an audit entry without secrets or raw log bodies.
type Event struct {
	Time       time.Time `json:"time"`
	Action     string    `json:"action"`
	Tool       string    `json:"tool,omitempty"`
	ServiceID  string    `json:"service_id,omitempty"`
	Status     string    `json:"status"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	Detail     string    `json:"detail,omitempty"`
}

// Logger serializes concurrent writes.
type Logger struct {
	mu              sync.Mutex
	out             io.Writer
	diagnostic      io.Writer
	failureReported bool
}

// New makes a JSONL log to w, or stderr by default.
// The optional writer receives a fixed diagnostic after an audit write failure.
func New(w io.Writer, diagnostics ...io.Writer) *Logger {
	if w == nil {
		w = os.Stderr
	}
	diagnostic := io.Discard
	if len(diagnostics) > 0 && diagnostics[0] != nil {
		diagnostic = diagnostics[0]
	}
	return &Logger{out: w, diagnostic: diagnostic}
}

// Log writes an event.
func (l *Logger) Log(e Event) {
	if l == nil {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	b = append(b, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.out.Write(b)
	if err == nil && n == len(b) {
		l.failureReported = false
		return
	}
	if l.failureReported {
		return
	}
	l.failureReported = true
	_, _ = io.WriteString(l.diagnostic, "audit write failed\n")
}
