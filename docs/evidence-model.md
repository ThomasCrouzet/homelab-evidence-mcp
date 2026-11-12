# Evidence model

## Item fields

Each evidence item has these fields:

- `id` is an opaque, process-local identifier.
- `service_id` is the canonical service identifier.
- `source` is `gatus`, `docker`, `loki`, `healthchecks`, `beszel`, or `ntfy`.
- `source_id` is the source-specific identity.
- `kind` is `endpoint_check`, `container_state`, `log_line`, `cron_check`,
  `metric_sample`, or `notification`.
- `observed_at` is the observation time at the source.
- `retrieved_at` is the time when the server made the response.
- `window_start` and `window_end` give the requested window when applicable.
- `summary` is a short readable fact.
- `severity` is `info`, `warning`, `error`, `critical`, or `unknown`.
- `attributes` contains permitted fields with a defined structure.
- `truncated` shows that the server truncated the item.
- `redactions_applied` gives the number of redacted text regions.
  Overlapping or adjacent rule matches form one region.
- `untrusted` is `true` for evidence whose text and attributes come from an
  upstream source. The marker is structural; it does not depend on keyword
  detection. It does not guarantee that every MCP host ignores hostile text.
- `freshness` is `live`, `recent`, `stale`, `cached`, `missing`, or `unknown`.
  For a `get_evidence` result, `freshness` is `cached` and `source_freshness`
  keeps the original source value.
- `source_freshness` is the source-derived freshness at collection. It does
  not change when the evidence cache serves the item.
- `cache_read_at` is the time of a `get_evidence` cache read. It does not
  replace the source collection time in `retrieved_at`.

## Timeline order

Items sort by `observed_at`, then source family, then a stable source position,
then source identity. The random opaque `id` never decides the order of two
items with equal time. When a limit keeps the newest items, the same rules
decide the boundary. ntfy same-second messages keep the source receipt order.

Before emission, the server cleans and redacts source identifiers, summaries,
and text attributes. It uses size limits for these values. Numeric fields and
closed vocabularies keep their structure. The server derives the Loki
`source_id` from a bounded process-local HMAC of the complete stream label map.
Allowlisted labels stay in readable attributes. ntfy keeps the message body
with a size limit in
`attributes.message`.

## Meaning of `observed_at`

The sources set `observed_at` as follows:

- **Gatus**: `observed_at` contains the result timestamp. If no result has a
  valid timestamp, `observed_at` contains the snapshot collection time,
  `attributes.source_time_known` is `false`,
  `attributes.collection_time_derived` is `true`, and `freshness` is `unknown`.
  A collection time does not prove that an undated event is recent. Historical
  windows do not include undated results.
- **Loki**: `observed_at` contains the nanosecond line timestamp.
- **Healthchecks**: `observed_at` contains the current-state collection time in
  snapshot mode. In transition mode it contains the source transition time.
  The `last_ping` value stays in an attribute.
- **Docker**: `observed_at` contains the collection time. The Docker list API
  does not give the last state change.
- **Beszel**: `observed_at` contains the snapshot collection time.
- **ntfy**: `observed_at` contains the message `time`. A message without a
  usable source time is not evidence: the adapter counts it in
  `unknown_time_records` and does not put it in a timeline. Collection time is
  never a substitute source time.
  The ntfy replay endpoint is a short-lived cache. Every ntfy source reports
  `retention_limited: true` and `complete: false`.

When the source response cache supplies a current snapshot, `observed_at`
keeps its initial time. `retrieved_at` contains the time the source response
was obtained and decoded, not the time the request started. Thus, a slow
response cannot report `retrieved_at` before `observed_at`. A later
`get_evidence` call keeps the original `retrieved_at`, sets `freshness` to
`cached`, and records the current read in `cache_read_at`. The original source
age stays in `source_freshness`.
The server assigns one collection time after it reads a successful response
body. The direct response and every cache hit use that same time. A refresh
after the TTL assigns a new collection time.

`incident_context` includes a current Docker, Healthchecks, or Beszel snapshot
only when its collection time is in the requested window. The check allows a
30-second tolerance for the request duration. Therefore, a historical window
does not present the current state as historical data.
The same rule applies when Gatus reports a missing endpoint. A missing Gatus
endpoint reports `unmatched_binding` in every window. It never reports a
matched empty history.

Gatus keeps a bounded result count for each endpoint. A windowed Gatus source
reports `retention_limited: true` and `complete: false`, because the adapter
cannot prove that the source retained the complete window. The API also cannot
prove a restart gap in the default memory storage.

## Multi-source envelope

A grouped response contains:

- The requested and effective windows
- Items sorted by date, source, stable source position, identity, and
  identifier
- The state and truncation of each source: `ok`, `error`, `timeout`,
  `canceled`, `skipped`, or `absent`
- `source_complete` reports whether every queried source answered with
  complete coverage. A service with no bound source reports
  `reason: no_bound_sources` and `complete: false`; it does not claim a
  successful investigation. `complete` keeps the same value for older clients.
  Every evidence tool keeps `complete` aligned with `source_complete`,
  including `search_logs`.
- `delivery_complete` reports whether the response includes every retrieved
  item. It is false when item selection or the response byte budget omitted a
  retrieved item. A local delivery trim does not change `source_complete`,
  because the source coverage stayed complete.
- `selection_truncated`, `byte_budget_truncated`, and `omitted_items` separate
  an item limit from the response byte limit. `truncated` is true when either
  applies, or when any source reports truncation.
- The truncation indicator and warnings. Warnings describe retained evidence
  and source coverage, not omitted item details.
- A factual summary without causal claims. Item counts describe returned
  evidence, not total upstream records.
- `byte_size` and `byte_budget` for the serialized result. Every tool with a
  byte budget reports them. `byte_size` measures the final text and structured
  payloads, so it includes its own decimal digits, plus the documented 512-byte
  MCP envelope allowance. A result does not exceed its budget. When the source
  metadata alone is too large, the tool returns an explicit error.

A canceled request is not a successful collection, even when a warm source
cache could answer it. The server checks the caller context before a cache read
and before it starts network work. Cancellation releases the tool and source
slots.

## Outcome and coverage

Execution status and evidence coverage are separate:

- `status` is `ok`, `error`, `timeout`, `canceled`, `skipped`, or `absent`.
- `reason` is a stable coverage code:
  `empty_result`, `no_binding`, `unmatched_binding`, `unsupported_history`,
  `partial_invalid_records`, `truncated`, `timeout`, `canceled`,
  `authentication_failed`, `upstream_error`, or `not_queried_by_tool`.
- `service_id` identifies the service binding when the outcome belongs to one
  binding. A global source-only outcome omits it.
- `complete` is true only when the source answered the requested coverage.
- `truncated` combines `upstream_truncated` and `local_truncated`.
- `discarded` counts unusable source records, such as a Loki line with an
  invalid timestamp.
- `cache_state` is `hit`, `miss`, `mixed`, or `disabled`.
  `cache_age_seconds` gives the age of the oldest cached snapshot used. These
  fields do not change `observed_at`.

A multi-source result sets `complete` for the whole result. An `ok` source with
`complete: false` means the source answered only part of the request. A
snapshot-only source in a past window reports `unsupported_history`. An empty
valid response reports `empty_result`, not an error.

A mixed-source failure keeps the successful evidence and sets
`complete: false`. An all-source failure also stays a successful MCP call, with
`complete: false` and no `ok` source. A single-source `search_logs` failure
uses MCP `isError: true` with a text envelope that contains `reason`. A valid
empty log search stays successful.

`absent` and `skipped` sources are outside request coverage. `absent` means the
service has no binding. `skipped` means the tool does not query that source.

## Honesty rules

Use these interpretation rules:

1. Missing data stays missing.
2. Failure of one source does not hide the others.
3. Contradictions stay in the response.
4. Severity is for presentation, not diagnosis.
5. The response gives each limit that the server used.
