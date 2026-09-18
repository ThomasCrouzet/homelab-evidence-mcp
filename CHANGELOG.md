# Changelog

This file records project changes. Its format uses
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
[Semantic Versioning](https://semver.org/).

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
