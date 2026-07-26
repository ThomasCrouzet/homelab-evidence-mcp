# Changelog

Notable changes are recorded in this file. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
[Semantic Versioning](https://semver.org/).

## [0.1.0] - 2026-07-26

### Added

- Stdio MCP server with seven read-only tools.
- Canonical YAML registry linking six source families.
- Gatus, Docker Engine, Loki, Healthchecks, Beszel, and ntfy adapters.
- `GET`-only HTTP client, locked destinations, refused redirects, and optional
  short-lived cache.
- Generic authentication via `token_env`, `token_file`, and `token_header`.
- Deterministic timeline with explicit partial results.
- Built-in redaction and optional local rules.
- Temporary evidence cache for `get_evidence`.
- Concurrency, throughput, and cardinality budgets.
- CLI `--version`, `--validate`, `--config`, and `--log-level`.
- Optional rotating JSONL audit file.
- Configuration JSON schema and integration examples.
- Contract tests, race detection, short fuzzing, and end-to-end demo.
- CI, binary publishing, and automated dependency tracking.

### Security

- Strict YAML validation: unknown keys and multi-document files refused.
- Bounded configuration cardinality for all global collections.
- Configuration and token files limited to mode `0600` on Unix.
- No secrets in capabilities, errors, or audit events.
- Invalid YAML values redacted from decode errors.
- Redaction applied before truncation of hostile text.
- Bearer and Basic credentials redacted with their full value.
- Redaction and bounds applied to all textual attributes from sources and their
  public identifiers.
- Process-local HMAC fingerprints for secrets in cache keys.
- Source response cache capped at 128 entries and 32 MiB.
- Evidence cache capped at 1,000 entries and texts at 16 KiB.
- Fan-out capped at eight concurrent source requests.
- Result limits reapplied client-side.
- Docker, Healthchecks, and Beszel snapshots excluded from historical windows.
- Absolute paths, traversal, fragments, and query strings refused.
- Symbolic audit files refused and permissions tightened on open.
