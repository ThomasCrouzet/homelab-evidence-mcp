// Package registry conserve les identités canoniques des services.
package registry

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
)

// Coverage indique les familles de sources reliées à un service.
type Coverage struct {
	Gatus        bool `json:"gatus"`
	Docker       bool `json:"docker"`
	Loki         bool `json:"loki"`
	Healthchecks bool `json:"healthchecks"`
	Beszel       bool `json:"beszel"`
	Ntfy         bool `json:"ntfy"`
}

// ServiceView est la vue publique d’une entrée du registre.
type ServiceView struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Coverage    Coverage `json:"coverage"`
}

// Registry est une table immuable construite depuis la configuration.
type Registry struct {
	byID  map[string]config.Service
	order []string
}

// New construit un registre depuis une configuration validée.
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

// List renvoie une page bornée de services.
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
		if prefix != "" && !strings.HasPrefix(id, prefix) && !strings.Contains(strings.ToLower(r.byID[id].DisplayName), prefix) {
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

// Len renvoie le nombre de services.
func (r *Registry) Len() int { return len(r.order) }

// Require renvoie le service ou une erreur.
func (r *Registry) Require(id string) (config.Service, error) {
	s, ok := r.get(id)
	if !ok {
		return config.Service{}, fmt.Errorf("unknown service_id %q", id)
	}
	return s, nil
}
