package mcpserver

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

func benchmarkBundle(count int) evidence.Bundle {
	base := time.Unix(1_800_000_000, 0).UTC()
	bundle := evidence.Bundle{ServiceID: "svc"}
	for index := 0; index < count; index++ {
		bundle.Items = append(bundle.Items, evidence.Item{
			ID:         fmt.Sprintf("ev-%04d", index),
			ServiceID:  "svc",
			Source:     evidence.SourceKind([]string{"gatus", "loki", "docker"}[index%3]),
			SourceID:   fmt.Sprintf("source-%d", index%10),
			ObservedAt: base.Add(time.Duration(index) * time.Second),
			Summary:    "line",
			Attributes: map[string]any{"line": strings.Repeat("x", 200)},
			Order:      index,
		})
	}
	bundle.Sources = []evidence.SourceOutcome{
		{Kind: evidence.SourceGatus, Status: "ok", ItemCount: count / 3, Complete: true},
		{Kind: evidence.SourceLoki, Status: "ok", ItemCount: count / 3, Complete: true},
		{Kind: evidence.SourceDocker, Status: "ok", ItemCount: count / 3, Complete: true},
	}
	return bundle
}

func BenchmarkCompactIncident200(b *testing.B) {
	bundle := benchmarkBundle(200)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = compactIncident(bundle, 50, 64<<10)
	}
}

func BenchmarkIncidentWireSize200(b *testing.B) {
	bundle := benchmarkBundle(200)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = incidentWireSize(bundle)
	}
}
