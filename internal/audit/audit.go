// Package audit écrit des événements structurés sur stderr ou dans un fichier.
package audit

import (
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// Event représente une entrée d’audit sans secret ni corps de journal brut.
type Event struct {
	Time       time.Time `json:"time"`
	Action     string    `json:"action"`
	Tool       string    `json:"tool,omitempty"`
	ServiceID  string    `json:"service_id,omitempty"`
	Status     string    `json:"status"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	Detail     string    `json:"detail,omitempty"`
}

// Logger sérialise les écritures concurrentes.
type Logger struct {
	mu  sync.Mutex
	out io.Writer
}

// New crée un journal JSONL vers w, ou stderr par défaut.
func New(w io.Writer) *Logger {
	if w == nil {
		w = os.Stderr
	}
	return &Logger{out: w}
}

// Log écrit un événement.
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
