<!-- README standard: filmcraft-inspired/v4; profile: backend -->
<!-- markdownlint-configure-file
{"MD033":{"allowed_elements":["h1","p","b","a"]}}
-->

<h1 align="center">homelab-evidence-mcp</h1>

<p align="center"><b>Read-only homelab incident evidence through MCP.</b></p>

<p align="center">
  <a href="#get-started">Get started</a> ·
  <a href="#mcp-tools">Tools</a> ·
  <a href="#mcp-integration">Connect a client</a> ·
  <a href="#documentation">Docs</a>
</p>

<!-- readme:section positioning -->
## Why homelab-evidence-mcp?

For homelab operators investigating an incident, this Go binary collects
evidence from Gatus, Docker Engine, Loki, Healthchecks, Beszel, and ntfy.
A configured `service_id` connects each service's identities across sources.
The MCP client receives one redacted timeline with timestamps, source states,
and collection limits.

The server presents facts without identifying a root cause.
It uses stdio and sends only HTTP `GET` requests to configured destinations.
It has no tools to restart containers, change hosts, or acknowledge alerts.

**Accounts:** No product account is required.
Configured sources may require credentials; Healthchecks requires a read-only
API key. Your MCP client has its own account requirements.

**Telemetry:** The server has no telemetry or crash reporting.
It writes operational logs and audit events to stderr, with an optional local
audit file. The MCP client's telemetry follows that client's policy.

**Data:** Caches hold API responses and evidence in process memory.
Source requests send credentials and query parameters to configured APIs.
Redacted results go to the MCP client, which may send them to its model provider.
The optional audit file persists operation metadata locally.

![Six GET-only adapters feed a service registry and a redacted evidence timeline.](docs/diagrams/evidence-flow.svg)

<!-- readme:section capabilities -->
## Source adapters

Configure only the sources and service bindings you need.
All routes below use `GET`.

| Source | Read path | Evidence |
| --- | --- | --- |
| Gatus | `/api/v1/endpoints/statuses` | Endpoint checks |
| Docker Engine | `/containers/json?all=true` | Container snapshot |
| Loki | `/loki/api/v1/query_range` | Log lines |
| Healthchecks | `/api/v3/checks/` | Current check states |
| Beszel | `/api/collections/systems/records` | Host snapshot |
| ntfy | `/{topic}/json?poll=1&since=<unix>` | Notifications |

Gatus selects the latest status by the greatest valid timestamp.
Docker requires a read-limited HTTP socket proxy; the Unix socket is unsupported.
Docker evidence excludes `Config.Env` and raw mounts.
Healthchecks evidence excludes ping URLs and UUIDs.
Beszel also supports `/api/systems` and compatible fallback routes.
Loki selectors and ntfy topics come from configuration, not tool arguments.
See [tested API contracts](docs/api-compatibility.md) for formats and limits.

### Evidence model

Each item includes its source, opaque `id`, `observed_at`, `retrieved_at`,
severity, freshness, `redactions_applied`, and truncation state.
Timelines use observation order and retain contradictory evidence.
Responses report `ok`, `absent`, `skipped`, `error`, or `timeout` per source.
A source failure does not hide results from other sources.
An empty result does not prove health.

For source timestamps and snapshot rules, read the
[evidence model](docs/evidence-model.md).

<!-- readme:section interface -->
## MCP tools

All seven tools declare read-only behavior.
Collection tools query their configured sources and cache returned evidence.
Every tool uses call budgets and emits audit events.

| Tool | Input | Result |
| --- | --- | --- |
| `evidence_capabilities` | None | Version, sources, limits, cache statistics |
| `list_services` | Optional `prefix`, `offset`, `limit` | IDs and coverage |
| `service_status` | `service_id` | Status snapshot |
| `incident_context` | `service_id`, optional window, `max_items` | Timeline |
| `search_logs` | `service_id`, optional filters and window | Loki lines |
| `failed_crons` | Optional `service_id`, window | Current failing checks |
| `get_evidence` | Opaque `id` from a previous result | Temporary cached item |

A window uses `duration`, such as `1h`, or RFC3339 `start` and `end` values.
Server limits bound windows, evidence counts, body sizes, and concurrency.
Invalid arguments and exhausted budgets produce tool errors.
`service_status` marks Loki and ntfy as `skipped`.
It collects Gatus, Docker, Healthchecks, and Beszel snapshots.
`search_logs` accepts optional `text`, `regex`, and `limit` arguments.
`failed_crons` selects current `down`, `grace`, and `paused` states.
`get_evidence` reports an error when its item expires or is evicted.

Follow the [incident playbook](docs/playbook.md) to choose tools and interpret
partial results.

<a id="quick-start"></a>
<!-- readme:section get-started -->
## Get started

### Local demo

**Requirements:** Git and Go 1.26.10 or later for the source demo and builds.
The demo needs no credentials or live homelab.

```bash
git clone https://github.com/ThomasCrouzet/homelab-evidence-mcp.git
cd homelab-evidence-mcp
go run ./demo
```

The bundled Go SDK client connects through an in-memory MCP session.
It discovers seven tools, lists the fixture service `media`, and collects
status, incident, log, and cron evidence.
It checks redaction, hostile instruction markers, partial results, and GET-only
requests.

Selected expected output from the bundled fixtures:

```text
initialize: ok (in-memory MCP session)
tools/list: 7 tools
non-GET HTTP calls observed: 0
DEMO OK
```

The full output includes JSON results, varying timestamps, and opaque IDs.
The demo does not exercise the stdio executable.

### Installation

Install the command with Go:

```bash
go install github.com/ThomasCrouzet/homelab-evidence-mcp/cmd/homelab-evidence-mcp@latest
```

Add the Go binary directory to `PATH`, or use the installed binary's absolute
path. Alternatively, build from the cloned repository:

```bash
make build
./bin/homelab-evidence-mcp --version
```

Prebuilt binaries are available in
[GitHub Releases](https://github.com/ThomasCrouzet/homelab-evidence-mcp/releases).
They target Linux and macOS on amd64/arm64, and Windows on amd64.
Go is unnecessary when you use a prebuilt binary.

### Configure your sources

1. Copy [config.example.yaml](config.example.yaml) outside the repository.
2. On Unix, restrict configuration access with `chmod 600 /path/config.yaml`.
3. On Windows, set a user-only ACL. The binary does not inspect Windows ACLs.
4. Set source URLs and service bindings for your environment.
5. Supply tokens through `token_env` or an owner-only `token_file`.
6. For Healthchecks, supply a read-only API key.
7. Validate the configuration:

```bash
homelab-evidence-mcp --config /path/config.yaml --validate
```

Success prints `config ok`.
Validation resolves credentials and checks configuration and destination
settings. It sends no HTTP requests and does not prove source connectivity.

### Collect your first evidence

Register the binary with a client using [MCP integration](#mcp-integration).
After connection, inspect its tool list and call `evidence_capabilities` with
empty arguments. Confirm the configured sources and service count.
Call `list_services` to find a configured `service_id`.

For a service named `media`, ask the client:

```text
Call incident_context with service_id "media" and duration "1h".
Show the factual_summary, source states, warnings, and evidence timestamps.
```

Use an ID from your registry if `media` is absent.
The result contains a time-ordered `items` array and per-source `sources`
outcomes. Available data depends on your bindings and source availability.

<!-- readme:section configuration -->
## Configuration

`--config` selects one YAML file loaded at startup.
The parser rejects unknown keys and multiple YAML documents.
Tokens come from the environment or files; `.env` files are not loaded.
Set only one of `token_env` and `token_file` per source.

- **Registry:** `version` must be `1`. `sources` requires at least one entry.
  Each source requires `kind` and `base_url`, without defaults.
  `services` defaults to an empty registry.
- **Credentials:** Healthchecks requires `token_env` or `token_file`.
  Other sources use them when authentication is needed. Neither has a default.
  `token_header` defaults to `X-Api-Key` for Healthchecks and `Authorization`
  for other sources.
- **Time windows:** `limits.default_window` defaults to `1h`.
  `limits.max_incident_window` defaults to `24h`;
  `limits.max_cron_window` defaults to `168h`.
- **Volume:** `limits.max_log_lines` defaults to `100`;
  `limits.max_evidence_items` defaults to `200`.
- **Timeouts:** `limits.per_source_timeout` defaults to `3s`;
  `limits.total_timeout` defaults to `10s`.
- **Caches:** `limits.source_cache_ttl` defaults to `15s`; `0` disables it.
  `limits.evidence_cache_ttl` defaults to `5m`.
  `limits.evidence_cache_max` defaults to `256`; `0` disables storage.
- **Optional files:** `audit.file` enables audit persistence and defaults to none.
  `tls.ca_file` adds a CA bundle; its default uses only system CAs.

Each source defines its API kind and base URL.
Each service maps a canonical ID to source-specific identities.
See the [YAML template](config.example.yaml), [schema](config.schema.json), and
[integration example](docs/configuration-example.md) for all settings.

`base_url` accepts only HTTP or HTTPS, without userinfo, queries, or fragments.
Path prefixes remain intact: `https://proxy.example/gatus` keeps `/gatus`
before each Gatus route.
Use `headers` for non-secret static headers and `token_header` for authentication.
The default `Authorization` header adds `Bearer` unless the token already
starts with `Bearer` or `Basic`.

### Security

Destinations stay fixed after startup. Redirects are refused.
The HTTP client ignores `HTTP_PROXY` and `HTTPS_PROXY`.
Tool calls cannot supply URLs, container names, selectors, or topics.
TLS verification remains active; `tls.ca_file` adds trusted CA certificates.

Use owner-only configuration and token files, with mode `0600` on Unix.
Use user-only ACLs on Windows.
The server rejects symbolic links for configuration and token files.
Log lines, notification bodies, and upstream identifiers receive redaction and
size limits. Built-in redaction remains active with optional `redact` rules.
Redaction cannot guarantee removal of every sensitive value.
Read [SECURITY.md](SECURITY.md) for the threat model and remaining risks.

<!-- readme:section mcp -->
## MCP integration

The client launches the binary as a child process with `--config` and an
absolute configuration path. The implemented transport is **stdio only**.
The server exposes seven tools; it registers no resources or prompts.
It has no HTTP listener or remote MCP endpoint.

### Claude Desktop

Open **Settings > Developer > Edit Config**, as described in the
[official local-server guide](https://modelcontextprotocol.io/docs/develop/connect-local-servers).
The configuration file is:

- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`
- Linux: `~/.config/Claude/claude_desktop_config.json`

Merge this entry into `mcpServers`. Replace both paths with absolute paths.
The example assumes tokens use private files or the client process environment.

```json
{
  "mcpServers": {
    "homelab-evidence": {
      "command": "/path/homelab-evidence-mcp",
      "args": ["--config", "/path/config.yaml"]
    }
  }
}
```

If you use `token_env`, ensure the launched process receives those variables.
On Windows, use forward slashes or escaped backslashes in JSON paths.
Include the binary's `.exe` extension.
Save the file and fully restart Claude Desktop.
Inspect the server's tools, then follow
[Collect your first evidence](#collect-your-first-evidence).
The fixture suites exercise the Go SDK client; they do not certify Claude
Desktop compatibility. See [client configuration notes](docs/mcp-hosts.md).

<!-- readme:section operations -->
## Operations

The client owns the stdio process. Configuration changes require a new process.
During MCP operation, stdout carries JSON-RPC only.
Logs and audit events go to stderr; `--log-level` defaults to `info`.
An optional `audit.file` adds a rotating JSONL file.
Audit write failures produce a bounded stderr diagnostic while tool calls
continue. Audit storage can be incomplete after a failure.

The server has no database. Both caches are process-local and disappear on exit.
Source cache keys use process-local HMAC fingerprints for authentication,
paths, and query strings.
A source cache hit keeps the initial `observed_at`; `retrieved_at` records the
current collection read. Freshness continues to reflect observation age.
Only `get_evidence` returns `freshness=cached`; it rereads stored evidence
without refreshing the source.

### Development

Run these commands from the repository root:

```bash
make test          # Race tests, then executable stdio E2E tests
make test-e2e      # Real binary against six local HTTP fixtures
make test-quick    # Tests without race detection or executable E2E
make lint-docs     # Markdown checks
make lint-private  # Private Tailscale address check
make lint          # gofmt check, go vet, and golangci-lint
make coverage
make build
```

`make lint-docs` needs Node.js 22 or later and `npx`.
It downloads markdownlint-cli2 on first use.
`make lint` needs golangci-lint 2.15.0 and defaults to Go toolchain 1.26.10.
Tests use local fixtures without live homelab credentials.
The executable E2E suite covers all seven tools and six adapters.
It saves logs and revision, working-tree, fixture, environment, and result
metadata outside the repository.
Set `HEM_E2E_ARTIFACT_DIR` to select an external artifact directory.
Set `HEM_E2E_BINARY` to an absolute binary path to test a release build.
See [Contributing](CONTRIBUTING.md) for the development and test rules.

<!-- readme:section documentation -->
## Documentation

- [Configuration example](docs/configuration-example.md),
  [YAML template](config.example.yaml), and [JSON schema](config.schema.json)
- [MCP clients](docs/mcp-hosts.md) and [incident playbook](docs/playbook.md)
- [Evidence model](docs/evidence-model.md) and
  [API compatibility](docs/api-compatibility.md)
- [Contributing](CONTRIBUTING.md) and [security policy](SECURITY.md)
- [Releases](https://github.com/ThomasCrouzet/homelab-evidence-mcp/releases)
  and [Changelog](CHANGELOG.md)

<!-- readme:section status -->
## Status

**v0.1.3, active development.** Released on 2026-10-23.
The implemented scope includes six source families and seven stdio MCP tools.
API compatibility claims cover local fixture structures, not every deployed
upstream version.

Current limitations:

- **Snapshots:** Docker, Healthchecks, and Beszel provide current state.
  Historical windows exclude current snapshots outside their time window.
  Healthchecks cannot establish past outages or recoveries.
- **Collection limits:** Responses can be truncated. Inspect source outcomes,
  `truncated`, and warnings before drawing conclusions.
- **Hostile content:** Log and notification text remains untrusted after
  redaction. Clock skew can affect timeline order.

The [roadmap](ROADMAP.md) lists possible directions without release commitments.
Report bugs through
[GitHub Issues](https://github.com/ThomasCrouzet/homelab-evidence-mcp/issues).
Include the version, affected tool, and a redacted reproduction.
Use the [private reporting process](SECURITY.md#vulnerability-reports) for
unpatched vulnerabilities or secret leaks.

<a id="license"></a>
<!-- readme:section license -->
## License and credits

[MIT](LICENSE). Copyright (c) 2026 Thomas Crouzet.
See [third-party notices](THIRD_PARTY_NOTICES.md) for dependency attribution.
