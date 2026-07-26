// Package docker implémente l’adaptateur Docker Engine limité à GET.
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/redaction"
)

// Client liste les conteneurs avec GET /containers/json.
type Client struct {
	HTTP     *httpx.LockedClient
	DestName string
	Headers  map[string]string
	Redact   *redaction.Engine
	Now      func() time.Time
}

// containerSummary contient uniquement les champs Docker utilisés.
type containerSummary struct {
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Labels  map[string]string `json:"Labels"`
	Created int64             `json:"Created"`
}

// Status renvoie l’état filtré de containerName.
func (c *Client) Status(ctx context.Context, serviceID, containerName string) (evidence.Item, error) {
	retrievedAt := c.now()
	list, observedAt, err := c.list(ctx, retrievedAt)
	if err != nil {
		return evidence.Item{}, err
	}
	ct, ok := findContainer(list, containerName)
	if !ok {
		cleanName, redactions, truncated := redaction.ApplyAndTruncate(c.Redact, containerName, 256)
		return evidence.Item{
			ID:          evidence.NewOpaqueID("ev"),
			ServiceID:   serviceID,
			Source:      evidence.SourceDocker,
			SourceID:    cleanName,
			Kind:        evidence.KindContainer,
			ObservedAt:  observedAt,
			RetrievedAt: retrievedAt,
			Summary:     "docker container not found in list",
			Severity:    evidence.SeverityUnknown,
			Attributes: map[string]any{
				"container_name": cleanName,
				"found":          false,
			},
			Truncated:         truncated,
			RedactionsApplied: redactions,
			Freshness:         evidence.FreshnessMissing,
		}, nil
	}
	return c.itemFrom(serviceID, ct, observedAt, retrievedAt), nil
}

// Evidence renvoie l’état courant ; l’API de liste ne fournit aucun historique.
func (c *Client) Evidence(ctx context.Context, serviceID, containerName string) ([]evidence.Item, error) {
	item, err := c.Status(ctx, serviceID, containerName)
	if err != nil {
		return nil, err
	}
	return []evidence.Item{item}, nil
}

func (c *Client) list(ctx context.Context, fallback time.Time) ([]containerSummary, time.Time, error) {
	// all=true inclut les conteneurs arrêtés ; le filtrage par nom reste local.
	path := "/containers/json?" + url.Values{"all": {"true"}}.Encode()
	resp, body, err := c.HTTP.Get(ctx, c.DestName, path, c.Headers)
	if err != nil {
		return nil, time.Time{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, time.Time{}, fmt.Errorf("docker HTTP %d", resp.StatusCode)
	}
	var list []containerSummary
	if err := json.Unmarshal(body, &list); err != nil {
		// Une erreur de décodage ne doit jamais devenir une liste vide réussie.
		return nil, time.Time{}, fmt.Errorf("docker decode: invalid JSON")
	}
	if list == nil {
		return nil, time.Time{}, fmt.Errorf("docker decode: expected container array")
	}
	return list, httpx.CollectionTime(resp, fallback), nil
}

func findContainer(list []containerSummary, want string) (containerSummary, bool) {
	want = normalizeName(want)
	for _, c := range list {
		for _, n := range c.Names {
			if normalizeName(n) == want {
				return c, true
			}
		}
	}
	return containerSummary{}, false
}

func normalizeName(n string) string {
	n = strings.TrimSpace(n)
	n = strings.TrimPrefix(n, "/")
	return strings.ToLower(n)
}

func (c *Client) itemFrom(serviceID string, ct containerSummary, observedAt, retrievedAt time.Time) evidence.Item {
	state := strings.ToLower(strings.TrimSpace(ct.State))
	sev := evidence.SeverityInfo
	rawSummary := fmt.Sprintf("docker container %s state=%s", primaryName(ct), state)
	switch state {
	case "running":
		sev = evidence.SeverityInfo
	case "restarting":
		sev = evidence.SeverityWarning
		rawSummary = fmt.Sprintf("docker container %s is restarting (%s)", primaryName(ct), ct.Status)
	case "exited", "dead":
		sev = evidence.SeverityError
		rawSummary = fmt.Sprintf("docker container %s state=%s (%s)", primaryName(ct), state, ct.Status)
	case "paused", "created":
		sev = evidence.SeverityWarning
	default:
		sev = evidence.SeverityUnknown
	}

	health := healthFromStatus(ct.Status)
	if health == "unhealthy" {
		sev = evidence.SeverityError
	}

	name, nameRedactions, nameCut := redaction.ApplyAndTruncate(c.Redact, primaryName(ct), 256)
	stateClean, stateRedactions, stateCut := redaction.ApplyAndTruncate(c.Redact, state, 64)
	statusClean, statusRedactions, statusCut := redaction.ApplyAndTruncate(c.Redact, ct.Status, 256)
	imageClean, imageRedactions, imageCut := redaction.ApplyAndTruncate(c.Redact, ct.Image, 256)
	names, namesRedactions, namesCut := c.cleanNames(ct.Names)
	redactions := nameRedactions + stateRedactions + statusRedactions + imageRedactions + namesRedactions
	truncated := nameCut || stateCut || statusCut || imageCut || namesCut

	attrs := map[string]any{
		"container_name": name,
		"names":          names,
		"state":          stateClean,
		"status":         statusClean,
		"image":          imageClean,
		"found":          true,
		// L’API de liste n’expose pas le dernier changement ; observed_at est la collecte.
		"snapshot": true,
	}
	if health != "" {
		attrs["health"] = health
	}
	// Ne jamais renvoyer tous les labels, seulement les clés explicitement autorisées.
	if v := safeLabel(ct.Labels, "com.docker.compose.service"); v != "" {
		clean, n, cut := redaction.ApplyAndTruncate(c.Redact, v, 128)
		redactions += n
		truncated = truncated || cut
		attrs["compose_service"] = clean
	}
	if v := safeLabel(ct.Labels, "com.docker.compose.project"); v != "" {
		clean, n, cut := redaction.ApplyAndTruncate(c.Redact, v, 128)
		redactions += n
		truncated = truncated || cut
		attrs["compose_project"] = clean
	}

	// Created représente la création du conteneur et non son dernier changement.
	// Conserver cette date comme attribut et dater l’instantané à la collecte.
	if ct.Created > 0 {
		attrs["created_at"] = time.Unix(ct.Created, 0).UTC().Format(time.RFC3339)
	}
	sum, summaryRedactions, summaryCut := redaction.ApplyAndTruncate(c.Redact, rawSummary, 512)
	redactions += summaryRedactions
	truncated = truncated || summaryCut
	return evidence.Item{
		ID:                evidence.NewOpaqueID("ev"),
		ServiceID:         serviceID,
		Source:            evidence.SourceDocker,
		SourceID:          name,
		Kind:              evidence.KindContainer,
		ObservedAt:        observedAt,
		RetrievedAt:       retrievedAt,
		Summary:           sum,
		Severity:          sev,
		Attributes:        attrs,
		Truncated:         truncated,
		RedactionsApplied: redactions,
		Freshness:         evidence.ComputeFreshness(observedAt, retrievedAt),
	}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func primaryName(ct containerSummary) string {
	if len(ct.Names) == 0 {
		return ""
	}
	return normalizeName(ct.Names[0])
}

func (c *Client) cleanNames(names []string) ([]string, int, bool) {
	count := len(names)
	truncated := false
	if count > 32 {
		count = 32
		truncated = true
	}
	out := make([]string, 0, count)
	redactions := 0
	for _, name := range names[:count] {
		clean, n, cut := redaction.ApplyAndTruncate(c.Redact, normalizeName(name), 128)
		redactions += n
		truncated = truncated || cut
		out = append(out, clean)
	}
	return out, redactions, truncated
}

func healthFromStatus(status string) string {
	ls := strings.ToLower(status)
	switch {
	case strings.Contains(ls, "(healthy)"):
		return "healthy"
	case strings.Contains(ls, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(ls, "(health: starting)"):
		return "starting"
	}
	return ""
}

func safeLabel(labels map[string]string, key string) string {
	if labels == nil {
		return ""
	}
	return labels[key]
}
