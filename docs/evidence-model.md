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
- `redactions_applied` gives the number of redaction replacements.
- `freshness` is `live`, `recent`, `stale`, `cached`, `missing`, or `unknown`.

Before emission, the server cleans and redacts source identifiers, summaries,
and text attributes. It uses size limits for these values. Numeric fields and
closed vocabularies keep their structure. The server derives the Loki
`source_id` from allowlisted stream labels. ntfy keeps the message body with
a size limit in
`attributes.message`.

## Meaning of `observed_at`

The sources set `observed_at` as follows:

- **Gatus**: `observed_at` contains the result timestamp.
- **Loki**: `observed_at` contains the nanosecond line timestamp.
- **Healthchecks**: `observed_at` contains the current-state collection time.
  The `last_ping` value stays in an attribute.
- **Docker**: `observed_at` contains the collection time. The Docker list API
  does not give the last state change.
- **Beszel**: `observed_at` contains the snapshot collection time.
- **ntfy**: If the response has `time`, `observed_at` contains that value.
  If the response has no `time`, `observed_at` contains the collection time.

When the source response cache supplies a current snapshot, `observed_at`
keeps its initial time. `retrieved_at` contains the time of the current read.
Thus, the freshness continues to show the age of the observation.

`incident_context` includes a current Docker, Healthchecks, or Beszel snapshot
only when its collection time is in the requested window. The check allows a
30-second tolerance for the request duration. Therefore, a historical window
does not present the current state as historical data.

## Multi-source envelope

A grouped response contains:

- The requested and effective windows
- Items sorted by date, source, identity, and identifier
- The state and truncation of each source: `ok`, `error`, `timeout`, `skipped`,
  or `absent`
- The truncation indicator and warnings
- A factual summary without causal claims

## Honesty rules

Use these interpretation rules:

1. Missing data stays missing.
2. Failure of one source does not hide the others.
3. Contradictions stay in the response.
4. Severity is for presentation, not diagnosis.
5. The response gives each limit that the server used.
