package mcpserver

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// warningByteSizes returns every byte_size value quoted in warnings.
func warningByteSizes(t *testing.T, warnings []string) []int {
	t.Helper()
	values := []int{}
	for _, warning := range warnings {
		index := strings.Index(warning, "byte_size=")
		if index < 0 {
			continue
		}
		field := warning[index+len("byte_size="):]
		if end := strings.IndexAny(field, " "); end >= 0 {
			field = field[:end]
		}
		value, err := strconv.Atoi(field)
		if err != nil {
			t.Fatalf("warning %q: %v", warning, err)
		}
		values = append(values, value)
	}
	return values
}

// TestIncidentWarningsMatchFinalByteSize detects a warning that quotes a stale
// byte_size. The warning text is part of the measured payload, so a stale value
// contradicts the accounting contract even when the byte_size field is correct.
func TestIncidentWarningsMatchFinalByteSize(t *testing.T) {
	app := budgetApp(t, lokiBulkServer(t).URL)
	for _, compact := range []bool{false, true} {
		_, out, err := app.toolIncidentContext(context.Background(), nil, incidentIn{
			ServiceID: "logs", Compact: compact,
		})
		if err != nil {
			t.Fatal(err)
		}
		incident := out.(IncidentContextOut)
		if !incident.ByteBudgetTruncated {
			t.Fatalf("compact=%v: fixture did not exercise byte trimming: items=%d size=%d budget=%d",
				compact, len(incident.Items), incident.ByteSize, incident.ByteBudget)
		}
		found := false
		for _, warning := range incident.Warnings {
			if strings.Contains(warning, "byte budget omitted") {
				found = true
			}
		}
		if !found {
			t.Fatalf("compact=%v: no byte budget warning: %v", compact, incident.Warnings)
		}
		for _, value := range warningByteSizes(t, incident.Warnings) {
			if value != incident.ByteSize {
				t.Fatalf("compact=%v: warning byte_size=%d, final byte_size=%d: %v",
					compact, value, incident.ByteSize, incident.Warnings)
			}
		}
	}
}
