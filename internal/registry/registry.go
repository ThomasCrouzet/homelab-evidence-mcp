// Package registry holds canonical service identities.
package registry

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
)

// Coverage indicates which source families are bound to a service.
type Coverage struct {
	Gatus        bool `json:"gatus"`
	Docker       bool `json:"docker"`
	Loki         bool `json:"loki"`
	Healthchecks bool `json:"healthchecks"`
	Beszel       bool `json:"beszel"`
	Ntfy         bool `json:"ntfy"`
}

// ServiceView is the public view of a registry entry.
type ServiceView struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Coverage    Coverage `json:"coverage"`
}

// Registry is an immutable table built from configuration.
type Registry struct {
	byID  map[string]config.Service
	order []string
}

// New builds a registry from a validated configuration.
func New(cfg *config.Config) *Registry {
	r := &Registry{
		byID: make(map[string]config.Service, len(cfg.Services)),
	}
	for _, s := range cfg.Services {
		r.byID[s.ID] = s
		r.order = append(r.order, s.ID)
	}
	sort.Strings(r.order)
	return r
}

func (r *Registry) get(id string) (config.Service, bool) {
	s, ok := r.byID[id]
	return s, ok
}

// List returns a bounded page of services.
func (r *Registry) List(prefix string, offset, limit int) ([]ServiceView, int) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	var matched []string
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	for _, id := range r.order {
		display := strings.ToLower(r.byID[id].DisplayName)
		if prefix != "" && !strings.HasPrefix(id, prefix) && !strings.HasPrefix(display, prefix) {
			continue
		}
		matched = append(matched, id)
	}
	total := len(matched)
	if offset >= total {
		return []ServiceView{}, total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	out := make([]ServiceView, 0, end-offset)
	for _, id := range matched[offset:end] {
		out = append(out, r.view(id))
	}
	return out, total
}

func (r *Registry) view(id string) ServiceView {
	s := r.byID[id]
	return ServiceView{
		ID:          s.ID,
		DisplayName: s.DisplayName,
		Coverage: Coverage{
			Gatus:        s.Sources.Gatus != nil,
			Docker:       s.Sources.Docker != nil,
			Loki:         s.Sources.Loki != nil,
			Healthchecks: s.Sources.Healthchecks != nil,
			Beszel:       s.Sources.Beszel != nil,
			Ntfy:         s.Sources.Ntfy != nil,
		},
	}
}

// Len returns the number of services.
func (r *Registry) Len() int { return len(r.order) }

// Require returns the service or an error.
func (r *Registry) Require(id string) (config.Service, error) {
	s, ok := r.get(id)
	if !ok {
		return config.Service{}, fmt.Errorf("unknown service_id %q", id)
	}
	return s, nil
}
