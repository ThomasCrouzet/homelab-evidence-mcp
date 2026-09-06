# homelab-evidence-mcp

[![CI](https://github.com/ThomasCrouzet/homelab-evidence-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/ThomasCrouzet/homelab-evidence-mcp/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

This local MCP server uses **stdio** transport. It collects incident evidence
from a homelab. The server is a static Go binary. It has no mutation operations.

## Why

When a service has a failure, each source contains different signals:

- Gatus gives an error.
- Docker shows a container in the `restarting` state.
- Loki contains a timeout from some moments before the failure.
- Healthchecks gives a scheduled job failure.

Each source tool gives data from one source. The tools do not use the same
service identities, evidence formats, or timelines. This project correlates
the data. It does not identify a cause.

## Principles

The project uses these principles:

1. **Canonical service registry**: A `service_id` connects Gatus, Docker, Loki,
   Healthchecks, Beszel, and ntfy identities.
2. **Common evidence model**: The model adds a source and two timestamps to
   each item. It also redacts data and uses limits. It shows truncation.
3. **Deterministic correlation**: The timeline presents facts. It does not identify
   a root cause.
4. **Read-only operation**: The server sends only HTTP `GET` requests.
   Destinations cannot change after startup.

Short example:

```text
02:12 Loki  upstream timeout [REDACTED]
02:13 Docker container media is restarting
02:14 Gatus media/app failure status=503
02:15 Healthchecks "media-cron" is down

The timeline uses observed_at order. Correlation does not identify a root cause.
```

This server is not a dashboard, an HTTP proxy for general use, or a control
plane. It does not start containers again or identify root causes.

## Installation

Prerequisite: Go 1.25 is the minimum version.

```bash
go install github.com/ThomasCrouzet/homelab-evidence-mcp/cmd/homelab-evidence-mcp@latest
```

To build from the repository:

```bash
make build
./bin/homelab-evidence-mcp --version
```

## Quick start

Do these steps:

1. Copy `config.example.yaml` to a directory that is not in the repository.
2. Make sure that only its owner can access the file. On Unix, use
   `chmod 600 /path/config.yaml`. On Windows, use a user-only ACL.
3. Add URLs for the internal network and some pilot services.
4. Set the necessary tokens as environment variables, for example
   `HEALTHCHECKS_API_TOKEN`.
5. Validate the configuration before you add it to the MCP client.

```bash
homelab-evidence-mcp --config /path/config.yaml --validate
```

Validation loads tokens from the YAML settings. Destinations cannot change
after this step. Validation sends no HTTP requests to these destinations.
Validation gives an error for each unknown YAML key. A `base_url` cannot
contain query strings, fragments, or userinfo.
Authentication uses `token_env` or `token_file`.

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

The server uses standard output only for the JSON-RPC protocol. It writes logs
and audit events to standard error. See
[MCP client configuration](docs/mcp-hosts.md) and the
[integration example](docs/configuration-example.md).

## Source adapters

The server has these source adapters:

- **Gatus**: The adapter uses `GET /api/v1/endpoints/statuses`. It selects the
  result with the greatest timestamp.
- **Docker Engine**: The adapter uses `GET /containers/json?all=true`. It
  filters fields and does not include `Config.Env`.
- **Loki**: The adapter uses `GET /loki/api/v1/query_range`. The configuration
  supplies the selector.
- **Healthchecks**: The adapter uses `GET /api/v3/checks/`. It uses a read-only
  key and does not include ping URLs in its output.
- **Beszel**: The adapter uses `GET /api/systems` and compatible routes. It can
  give a host snapshot.
- **ntfy**: The adapter uses `GET /{topic}/json?poll=1&since=<unix>`. The
  configuration supplies the topic.

Destinations use only `http` and `https`. For Docker, use a read-limited socket
proxy. The Docker Unix socket is not compatible with the server. The server
ignores `HTTP_PROXY` and `HTTPS_PROXY`.

You can use a path prefix in `base_url`. For example, use
`https://proxy.example/gatus` for Gatus. The server keeps `/gatus` before each
Gatus API route.

The source response cache keeps responses to the same requests for a short
time.
Set the cache TTL with `limits.source_cache_ttl`. The default is `15s`. A value
of `0` deactivates the cache.

The cache does not store clear-text authentication values in its keys. A
process-local HMAC fingerprint identifies each access secret. On a cache hit,
`observed_at` keeps the initial collection time. The `retrieved_at` field gives
the time of this read. Thus, freshness shows the age of the snapshot.

## MCP tools

The server has these MCP tools:

- `evidence_capabilities` gives the version, active sources, limits, and
  statistics.
- `list_services` gives services in the canonical service registry.
- `service_status` gives a Gatus, Docker, Healthchecks, and Beszel snapshot.
- `incident_context` gives a multi-source timeline with configuration limits.
- `search_logs` examines Loki with the selector in the configuration.
- `failed_crons` gives checks in the `down`, `grace`, or `paused` state.
- `get_evidence` reads temporary evidence again by its opaque identifier.

All tool annotations identify the tools as read-only. Global limits control
windows, HTTP body sizes, evidence counts, and concurrency.

## Evidence model

Each evidence item contains its source and its observation and collection
timestamps. It also contains severity, freshness state, truncation, and the
redaction count. Responses give the `ok`, `absent`, `skipped`, `error`, and
`timeout` state of each source.

See the [evidence model](docs/evidence-model.md) and
[tested API contracts](docs/api-compatibility.md).

## Security

Read [SECURITY.md](SECURITY.md) for guarantees and remaining risks. The primary
security controls are:

- Destinations cannot change after startup.
- The server does not accept HTTP redirects.
- MCP calls cannot supply a URL or stream selector.
- Log content is hostile data.
- The server uses built-in redaction and optional local rules.
- On Unix, give only the owner access to configuration and token files. Use
  mode `0600`.
- On Windows, use a user-only ACL. The binary does not examine Windows ACLs.

## Local demo

```bash
go run ./demo
```

The demo starts test HTTP servers and opens an in-memory MCP session. It
examines results when sources have errors and examines redaction. It also
makes sure that the server sends only `GET` requests. A homelab is not necessary.

## Development

```bash
make test          # tests with race detection
make test-quick    # fast tests
make lint-docs     # Markdown checks
make lint          # formatting, go vet, and golangci-lint
make coverage
make build
```

CI builds Linux, macOS, and Windows binaries. The primary targets are headless
Linux and macOS systems.

## License

MIT. See [LICENSE](LICENSE).
