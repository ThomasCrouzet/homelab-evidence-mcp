package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/healthchecks"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/loki"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/config"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
)

func (a *App) collectGatusStatus(ctx context.Context, serviceID string, ref *config.GatusRef) (evidence.SourceOutcome, []evidence.Item) {
	outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceGatus, func(ctx context.Context) ([]evidence.Item, error) {
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
	if len(items) == 1 && isMissingItem(items[0]) {
		outcome.Reason = "unmatched_binding"
		outcome.Complete = false
	}
	markEmpty(&outcome)
	return outcome, items
}

func (a *App) collectGatusWindow(ctx context.Context, serviceID string, ref *config.GatusRef, start, end time.Time, max int) (evidence.SourceOutcome, []evidence.Item) {
	localTruncated := false
	outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceGatus, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.gatus[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("gatus client missing")
		}
		if max <= 0 || max > a.Cfg.Limits.MaxEvidenceItems {
			max = a.Cfg.Limits.MaxEvidenceItems
		}
		var err error
		var result []evidence.Item
		result, localTruncated, err = cli.EvidenceInWindow(ctx, serviceID, ref.EndpointKey, start, end, max)
		return result, err
	})
	for _, item := range items {
		if isMissingItem(item) {
			outcome.Reason = "unmatched_binding"
			outcome.Complete = false
		}
	}
	markTruncated(&outcome, false, localTruncated)
	markEmpty(&outcome)
	return outcome, items
}

func (a *App) collectDockerStatus(ctx context.Context, serviceID string, ref *config.DockerRef) (evidence.SourceOutcome, []evidence.Item) {
	outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceDocker, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.dock[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("docker client missing")
		}
		return cli.Evidence(ctx, serviceID, ref.ContainerName)
	})
	if len(items) == 1 && isMissingItem(items[0]) {
		outcome.Reason = "unmatched_binding"
		outcome.Complete = false
	}
	markEmpty(&outcome)
	return outcome, items
}

func (a *App) collectDockerWindow(ctx context.Context, serviceID string, ref *config.DockerRef, start, end time.Time) (evidence.SourceOutcome, []evidence.Item) {
	outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceDocker, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.dock[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("docker client missing")
		}
		current, err := cli.Evidence(ctx, serviceID, ref.ContainerName)
		return filterCurrentSnapshots(current, start, end), err
	})
	markSnapshotHistory(&outcome, start, end)
	markEmpty(&outcome)
	return outcome, items
}

func (a *App) collectLoki(ctx context.Context, serviceID string, ref *config.LokiRef, start, end time.Time, text, rx string, limit int) (evidence.SourceOutcome, []evidence.Item) {
	stats := loki.QueryStats{}
	localTruncated := false
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
			Stats:     &stats,
		})
		localTruncated = cut
		return result, err
	})
	markTruncated(&outcome, false, localTruncated)
	markDiscarded(&outcome, stats.Discarded())
	markEmpty(&outcome)
	return outcome, items
}

func (a *App) collectHCStatus(ctx context.Context, serviceID string, ref *config.HealthchecksRef) (evidence.SourceOutcome, []evidence.Item) {
	outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceHealthchecks, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.hc[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("healthchecks client missing")
		}
		return cli.Status(ctx, serviceID, healthchecks.FilterFromRef(ref))
	})
	markEmpty(&outcome)
	return outcome, items
}

func (a *App) collectHCWindow(ctx context.Context, serviceID string, ref *config.HealthchecksRef, start, end time.Time) (evidence.SourceOutcome, []evidence.Item) {
	if ref.HistoryMode == "transitions" {
		transition := healthchecks.TransitionResult{}
		outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceHealthchecks, func(ctx context.Context) ([]evidence.Item, error) {
			cli := a.hc[ref.Source]
			if cli == nil {
				return nil, fmt.Errorf("healthchecks client missing")
			}
			f := healthchecks.FilterFromRef(ref)
			// Historical selection must not depend on the current state.
			f.Status = ""
			var err error
			transition, err = cli.Transitions(ctx, serviceID, f, start, end)
			return transition.Items, err
		})
		if outcome.Status != "ok" {
			return outcome, items
		}
		outcome.Omitted = transition.Omitted
		outcome.Discarded = transition.Discarded
		outcome.LocalTruncated = transition.Truncated
		outcome.Truncated = transition.Truncated
		// The upstream history endpoint does not document complete retention.
		outcome.Complete = false
		outcome.Reason = "unknown_history_coverage"
		switch {
		case transition.Failures > 0:
			outcome.Reason = "partial_fetch"
			outcome.Error = fmt.Sprintf("%d healthcheck history request(s) failed", transition.Failures)
		case transition.MissingIdentity > 0 && len(items) == 0:
			outcome.Reason = "missing_identity"
			outcome.Error = "no selected check exposes a unique_key for history"
		case transition.Omitted > 0:
			outcome.Reason = "omitted_checks"
		case transition.Truncated:
			outcome.Reason = "truncated"
		}
		return outcome, items
	}
	outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceHealthchecks, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.hc[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("healthchecks client missing")
		}
		f := healthchecks.FilterFromRef(ref)
		// Incident paths and cron failure paths always select current down/grace/paused
		// states. A binding status_filter of "up" must not hide failures.
		f.Status = ""
		return cli.FailedInWindow(ctx, serviceID, f, start, end)
	})
	markSnapshotHistory(&outcome, start, end)
	markEmpty(&outcome)
	return outcome, items
}

func (a *App) collectHCAllFailed(ctx context.Context, name string, cli *healthchecks.Client, start, end time.Time) (evidence.SourceOutcome, []evidence.Item) {
	outcome, items := a.withSourceTimeout(ctx, name, evidence.SourceHealthchecks, func(ctx context.Context) ([]evidence.Item, error) {
		return cli.FailedInWindow(ctx, "", healthchecks.Filter{}, start, end)
	})
	markSnapshotHistory(&outcome, start, end)
	markEmpty(&outcome)
	return outcome, items
}

func (a *App) collectBeszel(ctx context.Context, serviceID string, ref *config.BeszelRef) (evidence.SourceOutcome, []evidence.Item) {
	outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceBeszel, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.beszel[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("beszel client missing")
		}
		return cli.Evidence(ctx, serviceID, ref.SystemName)
	})
	if len(items) == 1 && isMissingItem(items[0]) {
		outcome.Reason = "unmatched_binding"
		outcome.Complete = false
	}
	markEmpty(&outcome)
	return outcome, items
}

func (a *App) collectBeszelWindow(ctx context.Context, serviceID string, ref *config.BeszelRef, start, end time.Time) (evidence.SourceOutcome, []evidence.Item) {
	outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceBeszel, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.beszel[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("beszel client missing")
		}
		current, err := cli.Evidence(ctx, serviceID, ref.SystemName)
		return filterCurrentSnapshots(current, start, end), err
	})
	markSnapshotHistory(&outcome, start, end)
	markEmpty(&outcome)
	return outcome, items
}

func (a *App) collectNtfy(ctx context.Context, serviceID string, ref *config.NtfyRef, start, end time.Time, limit int) (evidence.SourceOutcome, []evidence.Item) {
	upstreamTruncated := false
	localTruncated := false
	outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceNtfy, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.ntfy[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("ntfy client missing")
		}
		result, upstream, local, err := cli.HistoryDetail(ctx, serviceID, ref.Topic, start, end, limit)
		upstreamTruncated = upstream
		localTruncated = local
		return result, err
	})
	markTruncated(&outcome, upstreamTruncated, localTruncated)
	markEmpty(&outcome)
	return outcome, items
}

func (a *App) withSourceTimeout(ctx context.Context, sourceName string, kind evidence.SourceKind, fn func(context.Context) ([]evidence.Item, error)) (evidence.SourceOutcome, []evidence.Item) {
	t0 := time.Now()
	requestCtx, info := httpx.WithRequestInfo(ctx)
	if err := ctx.Err(); err != nil {
		outcome := a.sourceErrorOutcome(ctx, ctx, sourceName, kind, err, t0)
		outcome.CacheState, outcome.CacheAgeS = info.Snapshot()
		return outcome, nil
	}
	if a.sourceSem != nil {
		select {
		case a.sourceSem <- struct{}{}:
			defer func() { <-a.sourceSem }()
		case <-ctx.Done():
			outcome := a.sourceErrorOutcome(ctx, ctx, sourceName, kind, ctx.Err(), t0)
			outcome.CacheState, outcome.CacheAgeS = info.Snapshot()
			return outcome, nil
		}
	}
	sctx, cancel := context.WithTimeout(requestCtx, a.Cfg.Limits.PerSourceTimeout)
	defer cancel()
	items, err := fn(sctx)
	cacheState, cacheAge := info.Snapshot()
	if err != nil {
		outcome := a.sourceErrorOutcome(ctx, sctx, sourceName, kind, err, t0)
		outcome.CacheState, outcome.CacheAgeS = cacheState, cacheAge
		return outcome, nil
	}
	return evidence.SourceOutcome{
		SourceName: sourceName,
		Kind:       kind,
		Status:     "ok",
		ItemCount:  len(items),
		Complete:   true,
		CacheState: cacheState,
		CacheAgeS:  cacheAge,
		DurationMS: time.Since(t0).Milliseconds(),
	}, items
}

func (a *App) sourceErrorOutcome(ctx, sourceCtx context.Context, sourceName string, kind evidence.SourceKind, err error, started time.Time) evidence.SourceOutcome {
	status := "error"
	reason := reasonForError(err)
	switch {
	case errors.Is(ctx.Err(), context.Canceled) || errors.Is(sourceCtx.Err(), context.Canceled):
		status = "canceled"
		reason = "canceled"
	case errors.Is(sourceCtx.Err(), context.DeadlineExceeded) ||
		errors.Is(ctx.Err(), context.DeadlineExceeded):
		status = "timeout"
		reason = "timeout"
	}
	return evidence.SourceOutcome{
		SourceName: sourceName,
		Kind:       kind,
		Status:     status,
		Reason:     reason,
		Error:      sanitizeErr(err, a.Redact),
		Complete:   false,
		DurationMS: time.Since(started).Milliseconds(),
	}
}

func reasonForError(err error) string {
	if errors.Is(err, healthchecks.ErrNoMatch) {
		return "unmatched_binding"
	}
	if err == nil {
		return "upstream_error"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "authentication"),
		strings.Contains(message, " 401"), strings.Contains(message, " 403"):
		return "authentication_failed"
	case strings.Contains(message, "timeout"):
		return "timeout"
	default:
		return "upstream_error"
	}
}

func isMissingItem(item evidence.Item) bool {
	if item.Freshness == evidence.FreshnessMissing {
		return true
	}
	found, ok := item.Attributes["found"].(bool)
	return ok && !found
}

func markEmpty(outcome *evidence.SourceOutcome) {
	if outcome.Status == "ok" && outcome.Reason == "" && outcome.ItemCount == 0 {
		outcome.Reason = "empty_result"
	}
}

func markTruncated(outcome *evidence.SourceOutcome, upstream, local bool) {
	outcome.UpstreamTruncated = upstream
	outcome.LocalTruncated = local
	outcome.Truncated = upstream || local
	if outcome.Truncated {
		outcome.Complete = false
		if outcome.Reason == "" {
			outcome.Reason = "truncated"
		}
	}
}

func markDiscarded(outcome *evidence.SourceOutcome, discarded int) {
	if discarded <= 0 {
		return
	}
	outcome.Discarded = discarded
	outcome.Complete = false
	outcome.Reason = "partial_invalid_records"
}

// markSnapshotHistory reports that a snapshot-only source cannot answer a
// window that ends in the past.
func markSnapshotHistory(outcome *evidence.SourceOutcome, _, end time.Time) {
	if outcome.Status != "ok" {
		return
	}
	if end.Before(time.Now().UTC().Add(-30 * time.Second)) {
		outcome.Reason = "unsupported_history"
		outcome.Complete = false
	}
}

func normalizeOutcomes(sources []evidence.SourceOutcome) {
	for index := range sources {
		if sources[index].Reason != "" {
			continue
		}
		switch sources[index].Status {
		case "absent":
			sources[index].Reason = "no_binding"
			sources[index].Complete = true
		case "skipped":
			sources[index].Reason = "not_queried_by_tool"
			sources[index].Complete = true
		}
	}
}

// outcomeCoverage gives the overall state of a multi-source result.
// Absent and skipped sources are outside the request coverage.
func outcomeCoverage(sources []evidence.SourceOutcome) (complete, anyOK bool, configured int) {
	complete = true
	for _, source := range sources {
		switch source.Status {
		case "absent", "skipped":
			continue
		}
		configured++
		if source.Status == "ok" {
			anyOK = true
		}
		if !source.Complete {
			complete = false
		}
	}
	return complete, anyOK, configured
}

func auditStatus(complete, anyOK bool, configured int) string {
	switch {
	case complete:
		return "ok"
	case configured > 0 && !anyOK:
		return "error"
	default:
		return "partial"
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
