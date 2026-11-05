# Changelog

This file records project changes. Its format uses
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Add a serialized-payload byte budget to the evidence cache, with counters in
  `evidence_capabilities`. The default is 8 MiB and the maximum is 64 MiB.

### Changed

- Keep one authoritative version in `internal/version/version.txt`, embed it for
  `go install`, and use the same Makefile build path in CI and release.

### Fixed

- Redact complete `Cookie` and `Set-Cookie` header values instead of the first
  cookie pair only.
- Support Healthchecks `unique_key` bindings with read-only API keys, and
  report an unmatched binding as a source error instead of an empty success.
- Resolve exact Gatus endpoint keys before compatibility aliases and report an
  ambiguous selection instead of choosing the first row.
- Treat an omitted ntfy priority as the default `3` and map explicit
  out-of-range priorities to `unknown`.
- Mark Gatus evidence with no valid source timestamp as `freshness: unknown`
  instead of presenting the collection time as a recent observation.
- Reopen the audit file after a recoverable rotation failure instead of leaving
  the writer closed until restart.
- Keep one snapshot collection time across the direct response and cache hits
  for the same upstream response.
- Reject a static header that collides with the effective authentication header
  in any letter case, and canonicalize outbound header names.
- Preserve the ntfy `X-Messages-Truncated` signal on source cache hits without
  retaining other response headers.

## [0.1.3] - 2026-10-23

### Fixed

- Get the newest Loki lines before the server applies its result limit.
- Use an additional Loki line to identify truncated results.
- Read multiple Beszel PocketBase pages with page and record limits.
- Read current Beszel CPU and memory fields, including zero values.
- Keep current missing Gatus endpoints out of historical timelines.
- Redact ntfy topics, configured tokens, and MCP error audit fields.
- Remove raw caller identifiers from unknown-service errors.
- Redact complete JSON credentials that contain escaped quotes.
- Apply redaction rules to the original text to prevent recursive output growth.
- Keep complete JSONL audit records together during rotation.
- Give a bounded diagnostic when an audit write fails.
- Refuse audit records that exceed the file size limit.

### Changed

- Require Go 1.26.10 and update compatible Go dependencies.
- Use MCP Go SDK 1.8.0 and current Go lint and vulnerability tools.
- Align the configuration schema with parser defaults.
- Use the same Markdown and private-address checks before CI and release builds.
- Require equal tag, source, Makefile, and changelog versions before publication.
- Use changelog entries as release notes.
- Automate routine dependency updates behind required CI checks.
- Keep test reports outside the repository and GitHub artifacts.

### Added

- Executable stdio checks for all seven MCP tools and six source adapters.
- Executable checks for cancellation, partial results, redaction, and adapter regressions.
- Repeatable E2E logs with source, fixture, environment, and binary identities.
- E2E checks of the release binary before publication.

## [0.1.2] - 2026-09-06

### Fixed

- Use Gitleaks action 3.0.0 with Node.js 24 in CI and release checks.
- Remove the Node.js 20 deprecation warning from these checks.

## [0.1.1] - 2026-09-06

### Changed

- Use consistent English in documentation, comments, and error messages.
- Add repository writing rules.
- Add Markdown checks to the Makefile and CI.
- Require golangci-lint for the local Go lint check.

## [0.1.0] - 2026-07-26

### Added

Version 0.1.0 added these features:

- A stdio MCP server with seven read-only tools.
- A canonical service registry in YAML that connects six source families.
- Gatus, Docker Engine, Loki, Healthchecks, Beszel, and ntfy adapters.
- A `GET`-only HTTP client that does not accept redirects.
- Destinations that cannot change after startup.
- An optional source response cache.
- Authentication through `token_env`, `token_file`, and `token_header`.
- A timeline with deterministic correlation and source error results.
- Built-in redaction and optional local rules.
- A temporary evidence cache for `get_evidence`.
- Concurrency, throughput, and cardinality budgets.
- The CLI options `--version`, `--validate`, `--config`, and `--log-level`.
- An optional JSONL audit file with rotation.
- A configuration JSON schema and integration examples.
- Contract tests, race detection, short fuzzing, and an end-to-end demo.
- CI and automatic dependency tracking.
- Multi-architecture binaries for `darwin/amd64`,
  `linux/{amd64,arm64}`, `darwin/arm64`, and `windows/amd64`.

### Security

Version 0.1.0 added these security and compatibility features:

- YAML validation does not accept unknown keys or multi-document files.
- Configuration cardinality limits control all global collections.
- On Unix, group and other accounts cannot access configuration and token
  files.
- Capabilities, errors, and audit events contain no secrets.
- The server redacts invalid YAML values in decode errors.
- Redaction occurs before the server truncates hostile text.
- Redaction removes the full value of Bearer and Basic credentials.
- The server uses redaction and limits for all source text attributes and
  identifiers.
- Process-local HMAC fingerprints replace secrets in cache keys.
- The source response cache has limits of 128 entries and 32 MiB.
- The evidence cache has a limit of 1,000 entries. Text has a limit of 16 KiB.
- Fan-out has a limit of eight concurrent source requests.
- The client uses result limits again.
- Historical windows do not include Docker, Healthchecks, or Beszel snapshots.
- The server does not accept absolute paths, directory traversal, fragments,
  or query strings.
- The server does not accept symbolic links for configuration, token, CA, and
  audit files.
- `tls.ca_file` adds a CA bundle to the system CAs.
- No CLI option deactivates TLS verification.
- Audit events for MCP tool calls with errors contain no secrets.
- On Unix, group and other accounts cannot access configuration and token
  files.
- On Windows, the operator must set a user-only ACL. The binary does not
  examine Windows ACLs.
- The server does not accept a symbolic link for an audit file. It sets mode
  `0600` when it opens an audit file.
- Timeline and log limits keep the most recent items.
- Gatus accepts numeric `duration` values from the Gatus API.
- Beszel collects from PocketBase `/api/collections/systems/records`.
- `evidence_cache_max: 0` deactivates the evidence cache.
