# Tool output schemas

Every MCP tool publishes an `outputSchema`. A successful `tools/call` returns
`structuredContent` that validates against that schema. A text content block
carries the same facts in pretty-printed JSON.

## Schema rules

- Collections are arrays. `items`, `sources`, `services`, `adapters_active`,
  and similar lists are `[]` when empty, never `null`.
- `observed_at` and window fields are RFC3339 timestamps with timezone.
- `severity` is `info`, `warning`, `error`, `critical`, or `unknown`.
- `freshness` is `live`, `recent`, `stale`, `unknown`, `cached`, or `missing`.
- `source` and source outcome `kind` are `gatus`, `docker`, `loki`,
  `healthchecks`, `beszel`, or `ntfy`.
- Evidence `kind` is `endpoint_check`, `container_state`, `log_line`,
  `cron_check`, `metric_sample`, or `notification`.
- Source `status` is `ok`, `error`, `skipped`, `timeout`, `absent`, or
  `canceled`.
- Source `reason` is a coverage code. See the
  [evidence model](evidence-model.md#outcome-and-coverage).
- `complete` is required on every evidence result. It keeps the same value as
  `source_complete` in every evidence tool. `delivery_complete` reports local
  output loss instead. `truncated` combines `upstream_truncated` and
  `local_truncated`.
- `byte_size` and `byte_budget` are required on every result that has a
  response budget. `byte_size` measures the final text and structured payloads,
  so it includes the digits of `byte_size` itself, plus a documented 512-byte
  MCP envelope allowance. A result does not exceed its budget.
- `service_id` is optional and identifies a per-binding source outcome.
- `discarded` counts unusable source records. `cache_state` is `hit`, `miss`,
  `mixed`, or `disabled`; `cache_age_seconds` is optional.
- Compact `incident_context` adds `compact`, `byte_budget`, `byte_size`,
  `selection_truncated`, `byte_budget_truncated`, `omitted_items`, and
  `omitted_sources`. See [compact incident context](incident-context.md).
- Fields without `omitempty` are required. Optional fields can be absent.

`attributes` stays an open object. Each adapter documents its allowlisted keys.
Numeric fields keep their JSON number type. Text fields are size-limited and
redacted before emission.

## Error results

A failed tool call sets `isError: true` and returns a text error envelope. It
does not return `structuredContent`, so a client cannot treat an error as a
successful typed result. The envelope can contain a stable `reason` code.

A multi-source collection failure is different: the call succeeds and the
result has `complete: false` with per-source `status` and `reason`. A mixed
failure keeps the evidence from successful sources. An all-source failure has
no `ok` source. A single-source `search_logs` failure uses `isError: true`.

## Empty results

An empty list is a valid successful result when the source response is valid.
It is different from an unmatched binding, unsupported history, timeout, or
source error. Later source outcomes carry those reasons. Do not infer health
from an empty list.

## Compatibility policy

- Additive fields are backward compatible within a minor release.
- Removing or renaming a field, changing a field type, or removing an enum value
  is a breaking change and needs a major release.
- Adding an enum value is a minor release change. Clients must tolerate unknown
  values unless they opt into a fixed vocabulary.
- The schema version follows the server version. Clients can call
  `evidence_capabilities` for the running version.
- Text and structured content stay equivalent. A client can use either.

## Migration note

Before this contract, an empty `items` list could appear as `null` after a
failed or empty source response. Current responses always use `[]`. Clients that
tested `items == null` must test `items.length == 0` instead.

## Coverage

The stdio regression reads `tools/list`, resolves every advertised schema, calls
all seven tools, and validates the observed wire result. It also checks that the
text and structured payloads match.
