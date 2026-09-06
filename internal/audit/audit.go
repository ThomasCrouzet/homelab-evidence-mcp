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
	mu  sync.Mutex
	out io.Writer
}

// New makes a JSONL log to w, or stderr by default.
func New(w io.Writer) *Logger {
	if w == nil {
		w = os.Stderr
	}
	return &Logger{out: w}
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
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.out.Write(b)
	_, _ = l.out.Write([]byte("\n"))
}
