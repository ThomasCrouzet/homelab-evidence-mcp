# Evidence model

## Item fields

| Field | Meaning |
|---|---|
| `id` | opaque, process-local identifier |
| `service_id` | canonical service identifier |
| `source` | `gatus`, `docker`, `loki`, `healthchecks`, `beszel`, or `ntfy` |
| `source_id` | source-specific identity |
| `kind` | closed type: endpoint, container, log, job, metric, or notification |
| `observed_at` | observation time at the source |
| `retrieved_at` | time the server produced the response |
| `window_start`, `window_end` | requested window when applicable |
| `summary` | short readable fact |
| `severity` | `info`, `warning`, `error`, `critical`, or `unknown` |
| `attributes` | allowed structured fields |
| `truncated` | indicates truncation |
| `redactions_applied` | number of redaction replacements |
| `freshness` | `live`, `recent`, `stale`, `cached`, `missing`, or `unknown` |

Identifiers, summaries, and textual attributes from a source are cleaned,
redacted, then bounded before emission. Numeric fields and closed vocabularies
remain structured.

## Meaning of `observed_at`

| Source | Timestamp |
|---|---|
| Gatus | result timestamp |
| Loki | nanosecond line timestamp |
| Healthchecks | collection time of the current state; `last_ping` remains an attribute |
| Docker | collection time; the list API does not expose the last state change |
| Beszel | snapshot collection time |
| ntfy | `time` field, otherwise collection time |

When a current snapshot comes from the source cache, its original time stays in
`observed_at` and the current re-read is dated by `retrieved_at`. Its freshness
therefore continues to reflect the age of the observation.

A current Docker, Healthchecks, or Beszel snapshot is included in
`incident_context` only if its collection time falls within the requested
window, with a 30-second tolerance for request duration. A historical window
therefore does not receive a current state presented as historical.

## Multi-source envelope

A grouped response contains:

- requested and effective windows;
- items sorted by date, source, identity, and identifier;
- state and truncation of each source: `ok`, `error`, `timeout`, `skipped`, or `absent`;
- truncation indicator and warnings;
- a factual summary without causal claims.

## Honesty rules

1. Missing data stays missing.
2. Failure of one source does not hide the others.
3. Contradictions remain visible.
4. Severity is for presentation, not diagnosis.
5. Every applied limit is reported.
