# API compatibility

Project support covers the data structures in local `httptest` fixtures.
The executable E2E suite connects the real stdio binary to all six adapters.
It uses local fixtures and does not contact live upstream services.
Other versions can be compatible without project support.

The versioned corpus and its verification levels are in
[upstream fixtures](upstream-fixtures.md). No combination is live verified.
The read-only credential scope is an operator responsibility and a documented
gap, not a result that HTTP GET alone provides.

The adapter gives an error when a JSON collection is `null`. It also gives an
error when an envelope does not contain the expected array. For the two cases,
the adapter does not give an empty array without an error.

The shared HTTP client decodes a gzip response body and applies the body limit
to the decoded size. It rejects a configured
`X-Loki-Response-Encoding-Flags` header because the adapter does not decode the
categorized 3-tuple response. A snapshot-only adapter skips a wholly historical
window before a GET and reports `unsupported_history`.

## Gatus

The Gatus adapter has this behavior:

- The adapter sends a `GET /api/v1/endpoints/statuses` request.
- The response body is an array of objects with `name`, `group`, `key`, and
  `results`.
- Each result contains `success`, `status`, `timestamp`, `errors`, and
  `duration`.
- The adapter accepts `duration` as a nanosecond number (stock Gatus) or a
  string.
- The adapter selects the most recent state by the greatest correct timestamp.
- Endpoint resolution uses exact `key` values across the complete collection
  before compatibility aliases. A `name` or `group_name` alias is used only
  when no exact key exists.
- Duplicate exact keys and multiple matching aliases give an explicit ambiguity
  source error. The adapter does not select an arbitrary row.
- Row order does not change the selected endpoint.
- A configured endpoint that is absent from the status list gives
  `unmatched_binding` and `complete: false` in every window, including a
  historical window. The adapter does not report matched empty history.
- Each result must have a usable timestamp and an explicit `success` field. An
  invalid record is counted in `discarded` and the source reports
  `partial_invalid_records`. An absent `success` is not a failure value.
- An explicit `success: false` with no `status` remains a valid failure. The
  adapter omits `http_status` rather than inventing zero.
- Gatus keeps a bounded result count for each endpoint. The default is 100
  results, and the default memory storage loses history on restart. A windowed
  result therefore reports `retention_limited: true`, `reason:
  retention_limited` (or a more specific coverage reason), and `complete:
  false`. A short returned list does not prove full window coverage.
- If no result has a valid timestamp, the adapter reports `freshness: unknown`
  and marks the collection-derived time in `attributes`. A historical window
  does not include the undated result.
- Tests include empty arrays, empty results, unknown fields, and unordered
  history.

## Docker Engine

The Docker adapter has this behavior:

- The adapter sends a `GET /containers/json?all=true` request.
- A docker source can set `api_version: v1.56` to send
  `GET /v1.56/containers/json?all=true`. Unversioned Engine API routes are
  deprecated. Do not put a version in both `base_url` and `api_version`.
- The adapter reads `Names`, `Image`, `State`, `Status`, some `Labels`, and
  `Created`.
- A canonical `Names[0]` match wins over an alias. An exact-case match wins
  over a case-folded match. More than one distinct container in the winning
  stage gives an `ambiguous_binding` source error. Row order cannot select a
  container.
- A missing container gives a missing item, not an empty list.
- A network error or invalid JSON does not give an empty array without an error.
- The adapter sends no request to `inspect`, `create`, `start`, `stop`, or
  `exec` endpoints.
- `observed_at` contains the collection time.
- `attributes.snapshot` is `true`.
- A historical window does not include the current snapshot.

## Loki

The Loki adapter has this behavior:

- The adapter sends a `GET /loki/api/v1/query_range` request.
- The request contains `query`, `start`, `end`, `limit`, and
  `direction=backward`. `start` and `end` use RFC3339Nano, so distant years do
  not wrap through the undefined `UnixNano` range.
- The adapter applies the documented half-open interval `[start, end)`.
- A window outside the RFC3339 year range gives a source error before any GET.
- The request asks for one more line than the requested limit.
- The adapter sorts lines chronologically and keeps the newest requested lines.
- Equal timestamps in one stream keep their response position. Across streams,
  the stream identity is the stable tie-break. Opaque evidence ids do not
  decide order or truncation.
- The adapter parses the equality labels in the configured selector before the
  request. A returned stream that does not contain every configured label
  value is discarded. It cannot become evidence for the service.
- `source_id` is a bounded process-local HMAC of the complete label map before
  allowlisting and redaction. Different full label maps cannot collapse into
  one identity, and labels outside the allowlist never appear in output. The
  identity is stable for the process lifetime and can change after a restart.
- Severity prefers an allowlisted `level` or `severity` label, then a bounded
  `level=`/`severity=` field or JSON field. A negated phrase such as "no
  errors detected" and a token such as `failure_count=0` is not an error. The
  `attributes.severity_source` value shows `label`, `structured`, or `text`.
  Severity is presentation only and does not prove an incident.
- Duplicate lines are preserved.
- The additional line sets `truncated` when the result reaches the limit.
- The response contains streams with `[nanosecond_timestamp, line]` pairs.
- If the response includes a result type, its value must be `streams`.
- The adapter filters and redacts labels. It uses size limits for the labels.
- The adapter ignores timestamps that are not in the window.
- The adapter uses the limit again if the server exceeds it.
- Status 429 gives an explicit error.

## Healthchecks

The Healthchecks adapter has this behavior:

- The adapter sends a `GET /api/v3/checks/` request.
- The adapter uses the authentication header in the configuration. The default
  header is
  `X-Api-Key`.
- The adapter reads `name`, `slug`, `tags`, `status`, `grace`, `last_ping`,
  `next_ping`, `uuid`, and `unique_key`.
- The adapter does not include ping URLs, UUIDs, or unique keys in its output.
- The adapter accepts wrapped responses and raw arrays.
- A read-only API key omits `uuid` and supplies `unique_key`. Bind the service
  with `check_unique_key` in that case.
- A full-access key can supply `uuid`. Existing `check_uuid` configurations keep
  their behavior. The adapter does not derive one identity from the other.
- `check_uuid` and `check_unique_key` cannot be set together. `check_name`,
  `check_tags`, and one identity field combine with AND semantics.
- A binding that selects no check gives a source error. It does not look like a
  successful empty result.
- A configured check that exists but does not match `status_filter` reports
  `status_filter_no_match` with `complete: true` and no items. It is not
  `unmatched_binding`.
- `source_id` is a bounded process-local HMAC of the check unique identity when
  one exists. Two checks that share a slug keep distinct provenance, and raw
  unique keys or UUIDs never appear in output. A check without a unique
  identity sets `identity_unverified` and makes coverage incomplete.
- The adapter keeps one checks inventory per source instance for a global
  `failed_crons` call. A sliding request budget of
  `limits.max_source_requests_per_minute` (default 100) bounds each source.
  An exhausted budget gives `request_budget_exceeded` with partial evidence.
- A `check_name` that matches more than one check, by name or by slug, gives an
  `ambiguous_binding` source error. A name does not identify one check across
  collisions. Use `check_unique_key` or `check_uuid` for a single check, or
  `check_tags` for deliberate aggregation.
- `failed_crons` without `service_id` reads only the Healthchecks bindings in
  the service registry. It never enumerates unbound checks. A source without a
  registered binding reports `outside_registry_scope` and makes the result
  incomplete.
- `history_mode: snapshot` is the default. It keeps the current list behavior.
- `history_mode: transitions` reads
  `GET /api/v3/checks/<unique_key>/flips/?start=<unix>&end=<unix>` for
  windowed `failed_crons` and `incident_context`. Both bounds are Unix
  seconds. The adapter widens each exclusive upstream bound by one second,
  then applies the requested inclusive window to the returned flips locally.
  As a result, a flip exactly on `start` or `end` stays in the result.
- Transition mode keeps both failure and recovery events, with source
  timestamps, even when the current state is healthy.
- A selection reads at most 32 checks and 200 flips per check. It reports
  `omitted` and `truncated` when it applies those bounds.
- A check without a validated `unique_key` is skipped and reported as
  `missing_identity`.
- Per-check history failures are reported as `partial_fetch`; successful
  transitions from other checks remain available.
- Hosted Healthchecks documents flip retention for the current month and the
  two complete preceding months, subject to periodic cleanup. Self-hosted
  deployments and older versions can differ. A transition window that starts
  before that horizon is not fully provable. Transition results therefore
  report `unknown_history_coverage`, set `retention_limited: true` when the
  window reaches before the horizon, and keep `complete: false`. An empty
  result never proves that no incident occurred.
- `service_status` remains a current snapshot. A historical window in a
  snapshot-only mode gives `unsupported_history` before any GET and does not
  include the current state.

## Beszel

The Beszel adapter has this behavior:

- The adapter first sends a request to
  `/api/collections/systems/records` (PocketBase).
- A PocketBase request uses `perPage=200` and a page number.
- The adapter reads at most 32 consistent pages and 6,400 records.
- Incomplete, changing, or excessive pagination causes a source error.
- It then tries `/api/systems`, `/api/beszel/systems`, and the variants with a
  trailing slash.
- The adapter accepts arrays and `systems`, `items`, or `data` envelopes. It
  also accepts empty arrays and envelopes.
- An empty PocketBase collection is a complete result.
- The adapter gets the identity from `name`, `system`, `host`, or `info.h`.
- The adapter reads current `info.cpu` and `info.mp` values.
- It also reads the legacy top-level `cpu` and `mem` values.
- CPU and memory values are optional. Their value can be zero.
- The response contains only a snapshot.
- A historical window does not include the current snapshot.

## ntfy

The ntfy adapter has this behavior:

- The adapter sends a `GET /{topic}/json?poll=1&since=<unix>` request.
- The adapter stores the message body with a size limit in
  `attributes.message`. The summary contains a shorter view.
- The adapter reads the topic only from the configuration.
- The adapter accepts NDJSON and JSON arrays.
- The adapter reads `id`, `time`, `event`, `topic`, `message`, `title`,
  `priority`, and `tags`.
- A message record requires `id`, `event`, and `topic`. The source `topic` must
  equal the configured topic exactly. A cross-topic message, a missing event,
  a missing id, or a malformed row is discarded and counted. It cannot receive
  this service's identity.
- Control events such as `open`, `keepalive`, `poll_request`, `message_clear`,
  and `message_delete` are skipped. They are not evidence and are not
  discarded records.
- A missing, zero, negative, or out-of-range `time` is not usable evidence. The
  adapter counts it in `unknown_time_records` and does not place the message in
  a timeline. It never uses collection time as a substitute source time.
- A repeated `topic` and `id` inside one replay is collapsed. An identical
  repeat counts as a duplicate. A conflicting repeat counts as discarded.
- Same-second messages keep the source receipt order. The order does not depend
  on the random upstream `id`.
- An omitted priority uses the ntfy default of `3`. Priorities `1`, `2`, and `3`
  map to `info`, `4` maps to `error`, and `5` maps to `critical`. These values
  are presentation severity only and do not prove an incident.
- An explicit priority outside `1` through `5` maps to `unknown` and sets
  `attributes.priority_valid` to `false`.
- The replay endpoint is a short-lived cache, not an archive. Every ntfy source
  reports `retention_limited: true` and `complete: false`, including an empty
  replay. A successful empty replay cannot prove that no notification existed.
- When a valid replay is larger than `limits.max_body_bytes`, the adapter keeps
  complete records before the cutoff, sets `local_truncated: true`, and reports
  the partial result. It does not discard the whole response.
- `X-Messages-Truncated: 1` marks the source and grouped result as incomplete,
  even when the local item limit is not reached. The signal survives a source
  cache hit. Other response headers are not retained.
- The adapter sorts messages before it uses the limit.
- Local item truncation remains visible with or without the upstream signal.
