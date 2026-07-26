package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/healthchecks"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/loki"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

func (a *App) collectGatusStatus(ctx context.Context, serviceID string, ref *config.GatusRef) (evidence.SourceOutcome, []evidence.Item) {
	return a.withSourceTimeout(ctx, ref.Source, evidence.SourceGatus, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.gatus[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("gatus client missing")
		}
		it, err := cli.Status(ctx, serviceID, ref.EndpointKey)
		if err != nil {
			return nil, err
		}
		return []evidence.Item{it}, nil
	})
}

func (a *App) collectGatusWindow(ctx context.Context, serviceID string, ref *config.GatusRef, start, end time.Time, max int) (evidence.SourceOutcome, []evidence.Item) {
	truncated := false
	outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceGatus, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.gatus[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("gatus client missing")
		}
		if max <= 0 || max > a.Cfg.Limits.MaxEvidenceItems {
			max = a.Cfg.Limits.MaxEvidenceItems
		}
		if max > 100 {
			max = 100
		}
		var err error
		var result []evidence.Item
		result, truncated, err = cli.EvidenceInWindow(ctx, serviceID, ref.EndpointKey, start, end, max)
		return result, err
	})
	outcome.Truncated = truncated
	return outcome, items
}

func (a *App) collectDockerStatus(ctx context.Context, serviceID string, ref *config.DockerRef) (evidence.SourceOutcome, []evidence.Item) {
	return a.withSourceTimeout(ctx, ref.Source, evidence.SourceDocker, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.dock[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("docker client missing")
		}
		return cli.Evidence(ctx, serviceID, ref.ContainerName)
	})
}

func (a *App) collectDockerWindow(ctx context.Context, serviceID string, ref *config.DockerRef, start, end time.Time) (evidence.SourceOutcome, []evidence.Item) {
	return a.withSourceTimeout(ctx, ref.Source, evidence.SourceDocker, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.dock[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("docker client missing")
		}
		items, err := cli.Evidence(ctx, serviceID, ref.ContainerName)
		return filterCurrentSnapshots(items, start, end), err
	})
}

func (a *App) collectLoki(ctx context.Context, serviceID string, ref *config.LokiRef, start, end time.Time, text, rx string, limit int) (evidence.SourceOutcome, []evidence.Item) {
	truncated := false
	outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceLoki, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.loki[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("loki client missing")
		}
		result, cut, err := cli.Search(ctx, loki.QueryOptions{
			ServiceID: serviceID,
			Selector:  ref.Selector,
			Start:     start,
			End:       end,
			Limit:     limit,
			Text:      text,
			Regex:     rx,
		})
		truncated = cut
		return result, err
	})
	outcome.Truncated = truncated
	return outcome, items
}

func (a *App) collectHCStatus(ctx context.Context, serviceID string, ref *config.HealthchecksRef) (evidence.SourceOutcome, []evidence.Item) {
	return a.withSourceTimeout(ctx, ref.Source, evidence.SourceHealthchecks, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.hc[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("healthchecks client missing")
		}
		return cli.Status(ctx, serviceID, healthchecks.FilterFromRef(ref))
	})
}

func (a *App) collectHCWindow(ctx context.Context, serviceID string, ref *config.HealthchecksRef, start, end time.Time) (evidence.SourceOutcome, []evidence.Item) {
	return a.withSourceTimeout(ctx, ref.Source, evidence.SourceHealthchecks, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.hc[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("healthchecks client missing")
		}
		return cli.FailedInWindow(ctx, serviceID, healthchecks.FilterFromRef(ref), start, end)
	})
}

func (a *App) collectHCAllFailed(ctx context.Context, name string, cli *healthchecks.Client, start, end time.Time) (evidence.SourceOutcome, []evidence.Item) {
	return a.withSourceTimeout(ctx, name, evidence.SourceHealthchecks, func(ctx context.Context) ([]evidence.Item, error) {
		return cli.FailedInWindow(ctx, "", healthchecks.Filter{}, start, end)
	})
}

func (a *App) collectBeszel(ctx context.Context, serviceID string, ref *config.BeszelRef) (evidence.SourceOutcome, []evidence.Item) {
	return a.withSourceTimeout(ctx, ref.Source, evidence.SourceBeszel, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.beszel[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("beszel client missing")
		}
		return cli.Evidence(ctx, serviceID, ref.SystemName)
	})
}

func (a *App) collectBeszelWindow(ctx context.Context, serviceID string, ref *config.BeszelRef, start, end time.Time) (evidence.SourceOutcome, []evidence.Item) {
	return a.withSourceTimeout(ctx, ref.Source, evidence.SourceBeszel, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.beszel[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("beszel client missing")
		}
		items, err := cli.Evidence(ctx, serviceID, ref.SystemName)
		return filterCurrentSnapshots(items, start, end), err
	})
}

func (a *App) collectNtfy(ctx context.Context, serviceID string, ref *config.NtfyRef, start, end time.Time, limit int) (evidence.SourceOutcome, []evidence.Item) {
	truncated := false
	outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceNtfy, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.ntfy[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("ntfy client missing")
		}
		var err error
		var result []evidence.Item
		result, truncated, err = cli.History(ctx, serviceID, ref.Topic, start, end, limit)
		return result, err
	})
	outcome.Truncated = truncated
	return outcome, items
}

func (a *App) withSourceTimeout(ctx context.Context, sourceName string, kind evidence.SourceKind, fn func(context.Context) ([]evidence.Item, error)) (evidence.SourceOutcome, []evidence.Item) {
	t0 := time.Now()
	sctx, cancel := context.WithTimeout(ctx, a.Cfg.Limits.PerSourceTimeout)
	defer cancel()
	if a.sourceSem != nil {
		select {
		case a.sourceSem <- struct{}{}:
			defer func() { <-a.sourceSem }()
		case <-sctx.Done():
			return a.sourceErrorOutcome(ctx, sctx, sourceName, kind, sctx.Err(), t0), nil
		}
	}
	items, err := fn(sctx)
	if err != nil {
		return a.sourceErrorOutcome(ctx, sctx, sourceName, kind, err, t0), nil
	}
	return evidence.SourceOutcome{
		SourceName: sourceName,
		Kind:       kind,
		Status:     "ok",
		ItemCount:  len(items),
		DurationMS: time.Since(t0).Milliseconds(),
	}, items
}

func (a *App) sourceErrorOutcome(ctx, sourceCtx context.Context, sourceName string, kind evidence.SourceKind, err error, started time.Time) evidence.SourceOutcome {
	status := "error"
	if errors.Is(sourceCtx.Err(), context.DeadlineExceeded) ||
		errors.Is(ctx.Err(), context.DeadlineExceeded) {
		status = "timeout"
	}
	return evidence.SourceOutcome{
		SourceName: sourceName,
		Kind:       kind,
		Status:     status,
		Error:      sanitizeErr(err, a.Redact),
		DurationMS: time.Since(started).Milliseconds(),
	}
}

func filterCurrentSnapshots(items []evidence.Item, start, end time.Time) []evidence.Item {
	out := make([]evidence.Item, 0, len(items))
	for _, item := range items {
		if item.ObservedAt.Before(start) || item.ObservedAt.After(end.Add(30*time.Second)) {
			continue
		}
		item.WindowStart = utcTimePtr(start)
		item.WindowEnd = utcTimePtr(end)
		out = append(out, item)
	}
	return out
}

func utcTimePtr(t time.Time) *time.Time {
	t = t.UTC()
	return &t
}

func (a *App) parseWindow(startS, endS, durS string, def, max time.Duration) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	var start, end time.Time
	if endS != "" {
		t, err := time.Parse(time.RFC3339, endS)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid end: want RFC3339")
		}
		end = t.UTC()
	} else {
		end = now
	}
	switch {
	case startS != "":
		t, err := time.Parse(time.RFC3339, startS)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid start: want RFC3339")
		}
		start = t.UTC()
	case durS != "":
		d, err := time.ParseDuration(durS)
		if err != nil || d <= 0 {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid duration")
		}
		if d > max {
			return time.Time{}, time.Time{}, fmt.Errorf("duration exceeds max window %s", max)
		}
		start = end.Add(-d)
	default:
		start = end.Add(-def)
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, fmt.Errorf("start must be before end")
	}
	if end.Sub(start) > max {
		return time.Time{}, time.Time{}, fmt.Errorf("window exceeds max %s", max)
	}
	return start, end, nil
}
