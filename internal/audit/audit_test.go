package audit

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestLogJSONLine(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	l.Log(Event{Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Action: "tool", Tool: "list_services", Status: "ok"})
	s := buf.String()
	if !strings.Contains(s, `"action":"tool"`) || !strings.HasSuffix(s, "\n") {
		t.Fatal(s)
	}
}
