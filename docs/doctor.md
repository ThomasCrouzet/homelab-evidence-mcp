# Doctor command

`--doctor` runs bounded, read-only source diagnostics without starting an MCP
session. It reuses the locked HTTP client, adapter request paths, per-source
timeouts, and the source concurrency limit.

## Syntax

```bash
homelab-evidence-mcp --config /path/config.yaml --doctor
homelab-evidence-mcp --config /path/config.yaml --doctor --json
```

`--validate` stays offline. It resolves credentials, destination rules, and
configuration only. `--doctor` performs the network checks that `--validate`
cannot:

- Source reachability and TLS handshake.
- Authentication and authorization responses.
- Response decoding.
- Configured binding matches.
- Adapter capabilities.

Use `--validate` before every start. Use `--doctor` when setup fails or a
binding looks wrong.

## Output

The human output gives one line per source and one line per service binding.
Each line includes status, reason, completeness, capabilities, and item count.
The JSON output has this shape:

```json
{
  "version": "0.2.1",
  "transport": "stdio",
  "readonly": true,
  "services_count": 2,
  "complete": false,
  "sources": [
    {
      "name": "gatus",
      "kind": "gatus",
      "status": "ok",
      "complete": true,
      "capabilities": ["service_status", "incident_context"],
      "bindings": [
        {
          "service_id": "media",
          "status": "ok",
          "complete": true,
          "item_count": 1
        }
      ]
    }
  ],
  "warnings": []
}
```

The report contains logical source names and sanitized errors. It does not
contain base URLs, tokens, selectors, UUIDs, or notification bodies.

## Status and reasons

A source status is `ok`, `partial`, `timeout`, `canceled`, `error`, or `unbound`.
A binding uses the source outcome status from the
[evidence model](evidence-model.md#outcome-and-coverage).

- `unbound` means the source has no service binding. No request is made.
- `partial` means at least one binding matched only part of the request, or one
  binding failed while another succeeded.
- A responsive HTTP server is not enough. A binding that selects no object is
  `partial` or `error` with `unmatched_binding`.

## Deadlines

`--doctor` uses the configured `limits.per_source_timeout` for each adapter call
and the configured source concurrency limit. The total doctor deadline is
`limits.total_timeout`, with a 30-second minimum. A source that reaches its
deadline reports `timeout`.

## Exit codes

- `0`: every configured source is reachable and every binding is complete.
  Unbound sources produce a warning and do not fail the command.
- `1`: a source failed or a binding is incomplete. The report still lists all
  sources that answered.
- `2`: usage or configuration error, including a bad flag combination, a
  configuration load failure, or a startup prerequisite failure. The doctor
  performed no source diagnostics in this case.

Under `--doctor --json`, an early configuration or startup failure writes one
JSON object to stdout, with `error` and `reason` fields, and no report. The
message is sanitized: it does not contain a local path or a secret. Human mode
writes the sanitized message to stderr and keeps stdout empty.
`--validate` also returns `2` for a configuration or prerequisite failure.

The command exits before an MCP session starts. It needs no daemon.

## Limits

The doctor runs at most eight service bindings at once. It reports at most 100
bindings per source and sets `omitted_bindings` with a warning above that
limit. The source status still summarizes every checked binding.

`--doctor` converts snapshots and logs like the MCP tools. It does not prove that
a credential is read-only at the upstream service. It does not replace the
versioned fixture matrix or a live integration lane. See
[upstream fixtures](upstream-fixtures.md).
