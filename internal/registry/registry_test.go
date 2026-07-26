package registry

import (
	"testing"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
)

func TestListAndCoverage(t *testing.T) {
	cfg := &config.Config{
		Services: []config.Service{
			{ID: "alpha", DisplayName: "Alpha", Sources: config.ServiceSources{Gatus: &config.GatusRef{Source: "g", EndpointKey: "a"}}},
			{ID: "beta", DisplayName: "Beta", Sources: config.ServiceSources{Docker: &config.DockerRef{Source: "d", ContainerName: "b"}}},
		},
	}
	r := New(cfg)
	if r.Len() != 2 {
		t.Fatal(r.Len())
	}
	views, total := r.List("alp", 0, 10)
	if total != 1 || views[0].ID != "alpha" || !views[0].Coverage.Gatus {
		t.Fatalf("%+v %d", views, total)
	}
	if _, err := r.Require("nope"); err == nil {
		t.Fatal("expected error")
	}
}
