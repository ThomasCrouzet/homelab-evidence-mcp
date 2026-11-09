# Changelog

This file records project changes. Its format uses
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Add a serialized-payload byte budget to the evidence cache, with counters in
  `evidence_capabilities`. The default is 8 MiB and the maximum is 64 MiB.
- Add a versioned upstream fixture corpus with provenance, offline replay tests,
  auth-failure checks, and a compatibility matrix that distinguishes replay from
  live verification.
- Publish typed output schemas for all seven tools, keep empty collections as
  arrays, and document the compatibility policy.
- Add source coverage fields (`reason`, `complete`, truncation split,
  `discarded`, cache state and age) to every evidence result.
- Return an MCP error for a failed single-source log search, including a stable
  reason code. Valid empty searches remain successful.
- Add `--doctor` for bounded read-only source diagnostics with human and JSON
  output, stable exit codes, and binding validation.

### Changed

- Publish releases through a verified draft, add build-provenance attestations,
  and document immutable-release verification without claiming the current
  mutable release is protected.
- Keep one authoritative version in `internal/version/version.txt`, embed it for
  `go install`, and use the same Makefile build path in CI and release.
- Pin external GitHub Actions by commit, split release validation from
  publication, and verify transferred release checksums before publishing.

### Fixed

- Distinguish a Healthchecks `status_filter` mismatch from an unmatched check
  binding with `status_filter_no_match`.
- Give tag-aggregated Healthchecks checks a bounded per-check provenance token,
  and mark a check without a stable unique identity as unverified.
- Reuse one Healthchecks inventory per source for global `failed_crons`, bound
  the worker count, and add a per-source sliding request budget.
- Redact a configured service display name in `list_services` and
  `service_status` before MCP emission.
- Reject the unsupported Loki categorized-encoding header, decode gzip response
  bodies with a decoded-size limit, and skip a snapshot GET for a wholly
  historical window with `unsupported_history`.
- Replace raw YAML substring bans with parser-aware validation, so comments and
  quoted literals are allowed while unsupported tags still fail.
- Add a schema/runtime parity test for the shipped example and unknown keys.
- Extend the tracked-file privacy check to RFC1918, IPv6 ULA, private DNS, and
  the existing Tailscale patterns, with safe documentation ranges excluded.
- Make the release matrix recipe use POSIX `sh` without `pipefail`.
- Document the latest release versus `main`, the feature availability table,
  the development build path, and Healthchecks flip retention.
- Add an optional `service_id` to a per-binding source outcome, and use the
  binding and service identity as stable sort tie-breakers before opaque ids.
- Keep a concurrent-tool slot until its handler exits, including a canceled
  handler, so `max_concurrent_tools` bounds active callbacks.
- Enforce `max_tool_calls_per_minute` over a sliding minute instead of a fixed
  window that allows a boundary burst.
- Validate every returned Loki stream against the configured equality labels
  and discard a mismatched stream with an incomplete source outcome.
- Derive a bounded process-local Loki stream identity from the complete label
  map, so distinct streams cannot collide after label allowlisting.
- Prefer a Loki `level`/`severity` label and structured level field over
  keyword guesses, and do not report negated or zero-count phrases as errors.
- Rebuild the incident `factual_summary` and item warnings from the retained
  items after compact selection, while source coverage warnings stay visible.
- Enforce the compact byte budget after the summary rebuild. A metadata-only
  overflow returns an explicit tool error instead of an oversized success.
- Add `source_complete`, `delivery_complete`, and separate selection and byte
  omission fields. Top-level `truncated` now includes source truncation.
- Select a Docker container by canonical name and exact case before aliases or
  case folding, and reject more than one matching container with
  `ambiguous_binding`.
- Add an optional Docker `api_version` source setting for explicit versioned
  Engine API read routes, with a configuration error for a duplicate version
  in `base_url`.
- Sample `retrieved_at` after the source response is decoded in all adapters,
  so a slow request cannot report retrieval before collection.
- Keep the source freshness in `source_freshness` and the cache read time in
  `cache_read_at` when `get_evidence` serves an item from memory.
- Store and return defensive copies of cached evidence attributes so caller
  mutation cannot change retained evidence or byte accounting.
- Reject conflicting `start` and `duration`, future-start windows, and
  negative limits or offsets with a stable `invalid_arguments` tool error.
- Encode Loki query bounds as RFC3339Nano, apply the half-open `[start, end)`
  window, and reject years outside the RFC3339 range before any GET.
- Check caller cancellation before local-only tool work and before the tool
  budget, and never report a canceled source callback as a successful result.
- Validate ntfy message `id`, `event`, and `topic`, reject cross-topic rows,
  collapse duplicate ids, and count invalid or undated records without losing
  valid notifications.
- Never use ntfy collection time for a message with a missing or unusable
  `time`. Such a message is not evidence and is counted in
  `unknown_time_records`.
- Report every ntfy replay as retention-limited and incomplete, including an
  empty replay, and keep complete records from an oversized body with
  `local_truncated: true`.
- Make equal-timestamp ordering use a stable source position before source
  identity and the opaque item id. ntfy same-second messages keep receipt order.
- Send Healthchecks `flips` time filters as Unix seconds, widen the exclusive
  upstream bounds, and keep the requested inclusive window locally.
- Reject an ambiguous Healthchecks `check_name` that matches more than one
  check by name or slug, and keep tag aggregation for deliberate multi-check
  bindings.
- Keep validated Healthchecks transitions when a later check returns 401 or
  403, and report query, failure, unauthorized, and missing-identity counts.
- Scope `failed_crons` without `service_id` to registered bindings and report
  `outside_registry_scope` for a configured source with no registered binding.
- Report a missing Gatus endpoint as `unmatched_binding` in every window, and
  stop an absent endpoint from looking like matched empty history.
- Count Gatus records with an invalid timestamp or no `success` field as
  `discarded`, and keep explicit failures without a status.
- Mark windowed Gatus results as retention-limited and incomplete because the
  upstream result count is bounded and memory storage loses history on restart.
- Redact complete `Cookie` and `Set-Cookie` header values instead of the first
  cookie pair only.
- Check the caller context before a source cache read or network request, and
  report a canceled source outcome instead of a successful cached collection.
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
- Make Loki equal-timestamp order and truncation depend on stable source
  position instead of opaque evidence identifiers.

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
