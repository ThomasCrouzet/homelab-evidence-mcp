# homelab-evidence-mcp

[![CI](https://github.com/ThomasCrouzet/homelab-evidence-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/ThomasCrouzet/homelab-evidence-mcp/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Local MCP server over **stdio** transport that gathers incident evidence from a
homelab. It ships as a static Go binary and offers no mutation operations.

## Why

When a service fails, the signals are scattered:

- Gatus reports an error;
- Docker shows a container restarting;
- Loki contains a timeout a few moments earlier;
- Healthchecks shows a scheduled job failing.

Specialized tools correctly expose their own source, but they share neither a
service identity, nor a common evidence format, nor a shared timeline. This
project provides that correlation layer without inventing causality.

## Principles

1. **Canonical registry**: an explicit `service_id` links Gatus, Docker, Loki,
   Healthchecks, Beszel, and ntfy identities.
2. **Common evidence model**: every item is bounded, timestamped, attributed,
   redacted, and marked when truncated.
3. **Deterministic correlation**: the timeline presents facts; it does not claim
   to establish a root cause.
4. **Structural read-only**: only HTTP `GET` requests to destinations locked at
   startup are allowed.

Short example:

```text
02:12 Loki  upstream timeout [REDACTED]
02:13 Docker container media is restarting
02:14 Gatus media/app failure status=503
02:15 Healthchecks "media-cron" is down

Timeline is ordered by observed_at; correlation does not establish root cause.
```

This server is neither a dashboard, nor a generic HTTP proxy, nor a control
plane. It restarts no container and produces no root-cause analysis.

## Installation

Prerequisite: Go 1.25 or later.

```bash
go install github.com/ThomasCrouzet/homelab-evidence-mcp/cmd/homelab-evidence-mcp@latest
```

To build from the repository:

```bash
make build
./bin/homelab-evidence-mcp --version
```

## Quick start

1. Copy `config.example.yaml` to a private location.
2. Restrict the file to the owner: `chmod 600 /path/config.yaml` on Unix, or a
   user-only ACL on Windows.
3. Fill in internal URLs and a few pilot services.
4. Export required tokens, for example `HEALTHCHECKS_API_TOKEN`.
5. Validate the configuration before wiring it into the MCP client.

```bash
homelab-evidence-mcp --config /path/config.yaml --validate
```

Validation loads tokens, checks destinations, and fails on any unknown YAML
key. Query strings and fragments are forbidden in `base_url`; authentication
uses `token_env` or `token_file`.

Registration with an MCP client:

```json
{
  "mcpServers": {
    "homelab-evidence": {
      "command": "/path/homelab-evidence-mcp",
      "args": ["--config", "/path/config.yaml"],
      "env": {
        "HEALTHCHECKS_API_TOKEN": "readonly-key"
      }
    }
  }
}
```

Standard output is reserved for the JSON-RPC protocol. Logs and audit events
are written to standard error. See
[MCP client configuration](docs/mcp-hosts.md) and the
[integration example](docs/configuration-example.md).

## Supported sources

| Source | Surface used | Main guarantees |
|---|---|---|
| Gatus | `GET /api/v1/endpoints/statuses` | latest result chosen by timestamp |
| Docker Engine | `GET /containers/json?all=true` | filtered fields, never `Config.Env` |
| Loki | `GET /loki/api/v1/query_range` | selector fixed in configuration |
| Healthchecks | `GET /api/v3/checks/` | read-only key, no ping URL |
| Beszel | `GET /api/systems` and compatible variants | optional host snapshot |
| ntfy | `GET /{topic}/json?poll=1` | topic fixed in configuration |

Destinations accept only `http` and `https`. For Docker, use a read-limited
socket proxy; the direct Unix socket is not supported. `HTTP_PROXY` and
`HTTPS_PROXY` are ignored.

A path prefix is allowed in `base_url`:
`https://proxy.example/gatus` is correctly combined with Gatus routes.

The source response cache (`limits.source_cache_ttl`, default `15s`, `0` to
disable) briefly memorizes identical requests. Authentication values are never
stored in clear text in its keys; a process-local HMAC fingerprint distinguishes
access secrets. On a hit, `observed_at` keeps the original collection time and
`retrieved_at` marks the current read; freshness therefore reflects the real age
of the snapshot.

## MCP tools

| Tool | Usage |
|---|---|
| `evidence_capabilities` | version, active sources, limits, and statistics |
| `list_services` | canonical services and coverage |
| `service_status` | Gatus, Docker, Healthchecks, and Beszel snapshot |
| `incident_context` | bounded multi-source timeline |
| `search_logs` | Loki search on the configured selector |
| `failed_crons` | checks currently `down`, `grace`, or `paused` |
| `get_evidence` | temporary re-read of evidence by opaque identifier |

All tools are annotated as read-only. Global limits bound windows, HTTP bodies,
evidence counts, and concurrency.

## Evidence model

Each evidence item includes its source, observation timestamp, collection
timestamp, severity, freshness state, any truncation, and the number of
redactions applied. Responses also report sources that succeeded, were absent,
skipped, errored, or timed out.

See the [evidence model](docs/evidence-model.md) and
[tested API contracts](docs/api-compatibility.md).

## Security

Guarantees and residual risks are detailed in [SECURITY.md](SECURITY.md).
Key points:

- destinations locked at startup;
- HTTP redirects refused;
- no URL or stream selector supplied by an MCP call;
- log content treated as hostile data;
- built-in redaction and optional local rules;
- configuration and token files limited to mode `0600` on Unix, or a user ACL
  on Windows.

## Local demo

```bash
go run ./demo
```

The demo starts test HTTP servers, opens an in-memory MCP session, verifies
partial results, redaction, and the complete absence of non-`GET` requests. No
real homelab is required.

## Development

```bash
make test          # tests with race detection
make test-quick    # fast tests
make lint          # formatting, go vet, and golangci-lint if available
make coverage
make build
```

The project builds Linux, macOS, and Windows binaries in CI. Primary targets
remain headless Linux and macOS systems.

## License

MIT. See [LICENSE](LICENSE).
