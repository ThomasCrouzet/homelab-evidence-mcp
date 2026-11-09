package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/docker"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/gatus"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/healthchecks"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/loki"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/adapters/ntfy"
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
	if len(items) == 1 && !isMissingItem(items[0]) {
		if known, ok := items[0].Attributes["success_known"].(bool); ok && !known {
			outcome.Discarded = 1
			outcome.Complete = false
			outcome.Reason = "partial_invalid_records"
		}
	}
	markEmpty(&outcome)
	return outcome, items
}

func (a *App) collectGatusWindow(ctx context.Context, serviceID string, ref *config.GatusRef, start, end time.Time, max int) (evidence.SourceOutcome, []evidence.Item) {
	var window gatus.WindowResult
	outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceGatus, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.gatus[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("gatus client missing")
		}
		if max <= 0 || max > a.Cfg.Limits.MaxEvidenceItems {
			max = a.Cfg.Limits.MaxEvidenceItems
		}
		var err error
		window, err = cli.EvidenceInWindow(ctx, serviceID, ref.EndpointKey, start, end, max)
		return window.Items, err
	})
	if outcome.Status == "ok" {
		if !window.Matched {
			outcome.Reason = "unmatched_binding"
			outcome.Complete = false
		} else {
			// Gatus keeps a bounded result count per endpoint. The adapter
			// cannot prove that a window is fully retained.
			outcome.RetentionLimited = true
			outcome.Complete = false
			switch {
			case window.Discarded > 0:
				outcome.Discarded = window.Discarded
				outcome.Reason = "partial_invalid_records"
			case window.Truncated:
				outcome.Reason = "truncated"
			default:
				outcome.Reason = "retention_limited"
			}
		}
		markTruncated(&outcome, false, window.Truncated)
	}
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
	if windowIsHistorical(end) {
		return unsupportedHistory(ref.Source, evidence.SourceDocker), nil
	}
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
	filtered := false
	outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceHealthchecks, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.hc[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("healthchecks client missing")
		}
		result, err := cli.Status(ctx, serviceID, healthchecks.FilterFromRef(ref))
		if errors.Is(err, healthchecks.ErrStatusFilterNoMatch) {
			filtered = true
			return nil, nil
		}
		return result, err
	})
	if filtered {
		outcome.Status = "ok"
		outcome.Complete = true
		outcome.Reason = "status_filter_no_match"
		outcome.Error = ""
		outcome.ItemCount = 0
	}
	markHealthchecksIdentity(&outcome, items)
	markEmpty(&outcome)
	return outcome, items
}

// markHealthchecksIdentity reports a selected check without a unique identity.
func markHealthchecksIdentity(outcome *evidence.SourceOutcome, items []evidence.Item) {
	if outcome.Status != "ok" {
		return
	}
	for _, item := range items {
		if item.IdentityUnverified {
			outcome.Complete = false
			outcome.Reason = "identity_unverified"
			return
		}
	}
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
		a.applyHCTransition(&outcome, transition, items)
		return outcome, items
	}
	if windowIsHistorical(end) {
		return unsupportedHistory(ref.Source, evidence.SourceHealthchecks), nil
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
	markHealthchecksIdentity(&outcome, items)
	markEmpty(&outcome)
	return outcome, items
}

func (a *App) applyHCTransition(outcome *evidence.SourceOutcome, transition healthchecks.TransitionResult, items []evidence.Item) {
	outcome.QueriedChecks = transition.Requests
	outcome.FailedChecks = transition.Failures
	outcome.UnauthorizedChecks = transition.Unauthorized
	outcome.MissingIdentityChecks = transition.MissingIdentity
	outcome.Omitted = transition.Omitted
	outcome.Discarded = transition.Discarded
	outcome.LocalTruncated = transition.Truncated
	outcome.Truncated = transition.Truncated
	// The upstream history endpoint does not document complete retention.
	outcome.Complete = false
	if outcome.Status == "ok" {
		outcome.Reason = "unknown_history_coverage"
		switch {
		case transition.Budget > 0:
			outcome.Reason = "request_budget_exceeded"
			outcome.Error = fmt.Sprintf("%d healthcheck history request(s) skipped by the source request budget", transition.Budget)
		case transition.Unauthorized > 0:
			outcome.Reason = "authentication_failed"
			outcome.Error = fmt.Sprintf("%d healthcheck history request(s) returned 401 or 403", transition.Unauthorized)
		case transition.Failures > 0:
			outcome.Reason = "partial_fetch"
			outcome.Error = fmt.Sprintf("%d healthcheck history request(s) failed", transition.Failures)
		case transition.MissingIdentity > 0:
			outcome.Reason = "missing_identity"
			outcome.Error = fmt.Sprintf("%d selected check(s) expose no unique_key for history", transition.MissingIdentity)
		case transition.Omitted > 0:
			outcome.Reason = "omitted_checks"
		case transition.Truncated:
			outcome.Reason = "truncated"
		}
	}
	markHealthchecksIdentity(outcome, items)
}

// collectHCWindowFrom uses one shared checks inventory for one binding.
func (a *App) collectHCWindowFrom(ctx context.Context, snapshot healthchecks.Snapshot, serviceID string, ref *config.HealthchecksRef, start, end time.Time) (evidence.SourceOutcome, []evidence.Item) {
	if ref.HistoryMode == "transitions" {
		transition := healthchecks.TransitionResult{}
		outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceHealthchecks, func(ctx context.Context) ([]evidence.Item, error) {
			cli := a.hc[ref.Source]
			if cli == nil {
				return nil, fmt.Errorf("healthchecks client missing")
			}
			f := healthchecks.FilterFromRef(ref)
			f.Status = ""
			var err error
			transition, err = cli.TransitionsFrom(ctx, snapshot, serviceID, f, start, end)
			return transition.Items, err
		})
		a.applyHCTransition(&outcome, transition, items)
		return outcome, items
	}
	outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceHealthchecks, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.hc[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("healthchecks client missing")
		}
		f := healthchecks.FilterFromRef(ref)
		f.Status = ""
		return cli.FailedInWindowFrom(snapshot, serviceID, f, start, end)
	})
	markSnapshotHistory(&outcome, start, end)
	markHealthchecksIdentity(&outcome, items)
	markEmpty(&outcome)
	return outcome, items
}

// collectRegisteredFailedCrons queries only the Healthchecks bindings declared
// in the service registry. It does not enumerate unbound checks in a source.
func (a *App) collectRegisteredFailedCrons(ctx context.Context, start, end time.Time) ([]evidence.SourceOutcome, []evidence.Item, bool) {
	type binding struct {
		service config.Service
		ref     *config.HealthchecksRef
	}
	bindings := make([]binding, 0)
	bound := map[string]bool{}
	for _, svc := range a.Registry.All() {
		if svc.Sources.Healthchecks == nil {
			continue
		}
		bindings = append(bindings, binding{service: svc, ref: svc.Sources.Healthchecks})
		bound[svc.Sources.Healthchecks.Source] = true
	}
	// Collect one checks inventory per source instance. The number of active
	// workers is bounded, independent of the service count.
	snapshots := map[string]healthchecks.Snapshot{}
	snapshotErr := map[string]error{}
	sourceNames := make([]string, 0, len(bound))
	for name := range bound {
		sourceNames = append(sourceNames, name)
	}
	sort.Strings(sourceNames)
	if len(sourceNames) > 0 {
		jobs := make(chan string)
		var wg sync.WaitGroup
		var mu sync.Mutex
		workers := len(sourceNames)
		if workers > maxConcurrentSourceRequests {
			workers = maxConcurrentSourceRequests
		}
		for worker := 0; worker < workers; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for name := range jobs {
					cli := a.hc[name]
					if cli == nil {
						mu.Lock()
						snapshotErr[name] = fmt.Errorf("healthchecks client missing")
						mu.Unlock()
						continue
					}
					snapshot, err := cli.Fetch(ctx)
					mu.Lock()
					if err != nil {
						snapshotErr[name] = err
					} else {
						snapshots[name] = snapshot
					}
					mu.Unlock()
				}
			}()
		}
		for _, name := range sourceNames {
			jobs <- name
		}
		close(jobs)
		wg.Wait()
	}

	outcomes := make([]evidence.SourceOutcome, 0, len(bindings)+len(a.hc))
	items := make([]evidence.Item, 0)
	if len(bindings) > 0 {
		jobs := make(chan binding)
		var wg sync.WaitGroup
		var mu sync.Mutex
		workers := len(bindings)
		if workers > maxConcurrentSourceRequests {
			workers = maxConcurrentSourceRequests
		}
		for worker := 0; worker < workers; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for job := range jobs {
					var outcome evidence.SourceOutcome
					var its []evidence.Item
					if err := snapshotErr[job.ref.Source]; err != nil {
						outcome = a.sourceErrorOutcome(ctx, ctx, job.ref.Source, evidence.SourceHealthchecks, err, time.Now())
					} else {
						outcome, its = a.collectHCWindowFrom(ctx, snapshots[job.ref.Source], job.service.ID, job.ref, start, end)
					}
					outcome.ServiceID = job.service.ID
					mu.Lock()
					outcomes = append(outcomes, outcome)
					items = append(items, its...)
					mu.Unlock()
				}
			}()
		}
		for _, job := range bindings {
			jobs <- job
		}
		close(jobs)
		wg.Wait()
	}
	excluded := false
	for name := range a.hc {
		if bound[name] {
			continue
		}
		excluded = true
		outcomes = append(outcomes, evidence.SourceOutcome{
			SourceName: name,
			Kind:       evidence.SourceHealthchecks,
			Status:     "skipped",
			Reason:     "outside_registry_scope",
			Complete:   false,
		})
	}
	return outcomes, items, excluded
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
	markBeszelCollection(&outcome, items)
	markEmpty(&outcome)
	return outcome, items
}

func (a *App) collectBeszelWindow(ctx context.Context, serviceID string, ref *config.BeszelRef, start, end time.Time) (evidence.SourceOutcome, []evidence.Item) {
	if windowIsHistorical(end) {
		return unsupportedHistory(ref.Source, evidence.SourceBeszel), nil
	}
	outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceBeszel, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.beszel[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("beszel client missing")
		}
		current, err := cli.Evidence(ctx, serviceID, ref.SystemName)
		return filterCurrentSnapshots(current, start, end), err
	})
	markSnapshotHistory(&outcome, start, end)
	markBeszelCollection(&outcome, items)
	markEmpty(&outcome)
	return outcome, items
}

// markBeszelCollection reports page-age or pagination uncertainty.
func markBeszelCollection(outcome *evidence.SourceOutcome, items []evidence.Item) {
	if outcome.Status != "ok" {
		return
	}
	for _, item := range items {
		if overlap, _ := item.Attributes["pagination_overlap"].(bool); overlap {
			outcome.Reason = "pagination_overlap"
			outcome.Complete = false
			return
		}
	}
	for _, item := range items {
		if unverified, _ := item.Attributes["pagination_unverified"].(bool); unverified {
			outcome.Reason = "pagination_unverified"
			outcome.Complete = false
			return
		}
	}
	for _, item := range items {
		if mixed, _ := item.Attributes["mixed_page_age"].(bool); mixed {
			outcome.Reason = "mixed_page_age"
			outcome.Complete = false
			return
		}
	}
}

func (a *App) collectNtfy(ctx context.Context, serviceID string, ref *config.NtfyRef, start, end time.Time, limit int) (evidence.SourceOutcome, []evidence.Item) {
	result := ntfy.Result{}
	outcome, items := a.withSourceTimeout(ctx, ref.Source, evidence.SourceNtfy, func(ctx context.Context) ([]evidence.Item, error) {
		cli := a.ntfy[ref.Source]
		if cli == nil {
			return nil, fmt.Errorf("ntfy client missing")
		}
		var err error
		result, err = cli.HistoryDetail(ctx, serviceID, ref.Topic, start, end, limit)
		return result.Items, err
	})
	if outcome.Status == "ok" {
		// The replay endpoint is a short-lived cache, not an archive. A
		// successful empty replay cannot prove that no notification existed.
		outcome.RetentionLimited = true
		outcome.Complete = false
		outcome.Discarded = result.Discarded
		outcome.UnknownTimeRecords = result.UnknownTime
		markTruncated(&outcome, result.UpstreamTruncated, result.LocalTruncated)
		switch {
		case result.Discarded > 0:
			outcome.Reason = "partial_invalid_records"
		case outcome.Reason == "":
			outcome.Reason = "retention_limited"
		}
	}
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
	for index := range items {
		items[index].Untrusted = true
	}
	evidence.MarkSourceFreshness(items)
	cacheState, cacheAge := info.Snapshot()
	if err == nil {
		cancelErr := ctx.Err()
		if cancelErr == nil {
			cancelErr = sctx.Err()
		}
		if cancelErr != nil {
			// Keep validated partial evidence but never report a canceled or
			// timed-out collection as a successful one.
			outcome := a.sourceErrorOutcome(ctx, sctx, sourceName, kind, cancelErr, t0)
			outcome.ItemCount = len(items)
			outcome.CacheState, outcome.CacheAgeS = cacheState, cacheAge
			return outcome, items
		}
	}
	if err != nil {
		outcome := a.sourceErrorOutcome(ctx, sctx, sourceName, kind, err, t0)
		outcome.ItemCount = len(items)
		outcome.CacheState, outcome.CacheAgeS = cacheState, cacheAge
		return outcome, items
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
	if errors.Is(err, healthchecks.ErrAmbiguousBinding) {
		return "ambiguous_binding"
	}
	if errors.Is(err, gatus.ErrEndpointAmbiguous) {
		return "ambiguous_binding"
	}
	if errors.Is(err, docker.ErrAmbiguousContainer) {
		return "ambiguous_binding"
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
func windowIsHistorical(end time.Time) bool {
	return end.Before(time.Now().UTC().Add(-30 * time.Second))
}

func unsupportedHistory(sourceName string, kind evidence.SourceKind) evidence.SourceOutcome {
	return evidence.SourceOutcome{
		SourceName: sourceName,
		Kind:       kind,
		Status:     "ok",
		Reason:     "unsupported_history",
		Complete:   false,
	}
}

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

func anySourceTruncated(sources []evidence.SourceOutcome) bool {
	for _, source := range sources {
		if source.Truncated {
			return true
		}
	}
	return false
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

func coverageReason(configured int) string {
	if configured == 0 {
		return "no_bound_sources"
	}
	return ""
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
	if startS != "" && durS != "" {
		return time.Time{}, time.Time{}, fmt.Errorf("set only one of start or duration")
	}
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
	// Allow the snapshot grace and ordinary clock skew. A window that starts
	// later cannot describe an incident that has already happened.
	if start.After(now.Add(30 * time.Second)) {
		return time.Time{}, time.Time{}, fmt.Errorf("window starts in the future")
	}
	return start, end, nil
}
