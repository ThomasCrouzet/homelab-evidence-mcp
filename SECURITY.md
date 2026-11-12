# Security policy

## Versions

The project supplies security updates for version 0.2.x.

## Vulnerability reports

Open a private security advisory on the GitHub repository. You can also write
to the email address on the maintainer GitHub profile. Do not open a public
issue for a secret leak or an unpatched vulnerability.

## Threat model

### Protected assets

The protected assets are:

- Service inventory and health signals.
- Log excerpts that can contain access secrets or personal data.
- Healthchecks API tokens.
- Filtered Docker metadata.
- Temporary in-process caches.

### Trust boundaries

These are the trust boundaries:

- The YAML configuration and startup environment are trusted operator input.
- MCP tool arguments are untrusted input.
- HTTP source responses are untrusted input.
- Log and notification text is hostile data.
- Standard output contains only JSON-RPC protocol data.
- Standard error and the audit file contain redacted operation data.

### Read-only operation

Read-only operation has these controls:

- The server has no start, stop, restart, exec, write, or delete tools.
- The HTTP client sends only `GET` requests.
- The server does not accept redirects.
- The server gets destinations from the startup configuration.
- The server keeps path prefixes from `base_url`.
- The server does not accept absolute paths, directory traversal, and host changes.
- The configuration supplies all Loki selectors and ntfy topics.
- The server removes `Config.Env` and raw mounts from Docker responses.
- The server also removes labels that are not in the allowlist.
- The server removes UUID, ping, pause, update, and badge URLs from
  Healthchecks responses.
- TLS verification stays active. No user option deactivates it.
- An optional `tls.ca_file` adds CA certificates to the system trust store.
- An optional per-source `ca_file` replaces the global pool for that source
  only. It does not broaden trust for other destinations.
- Budgets set maximum call counts, concurrency, and response volumes.
- The Docker Unix socket is not compatible with the server.

### SSRF and network resolution

An optional per-source `allowed_cidrs` policy checks every resolved address at
dial time. A host with no allowed address fails before a connection. If several
resolved addresses are allowed, the server tries them in order until one accepts
the connection. It never dials a disallowed address, even when every allowed
address fails. A canceled or expired request stops the fallback. TLS continues to
verify the configured hostname, because the dial target is an address and the
certificate name stays the destination host. This mitigates DNS rebinding; it is
opt-in and does not replace fixed destinations.
A request can use only a destination name from the startup configuration.
The server uses only `http` and `https`. It does not accept redirects. MCP arguments
cannot supply a URL.

The server can use private, CGNAT, and Tailscale addresses from the
configuration. These addresses are usual homelab destinations.

The hostname cannot change after startup. The system resolves DNS for each
connection. Thus, compromised DNS can change the resolved address. For
sensitive destinations, use stable names from the internal network, controlled
DNS, or fixed IP addresses.

### Secrets

The server and the operator use these controls for secrets:

- The server gets tokens from `token_env` or a regular `token_file`.
- On Unix, the binary does not accept token files with access for a group or other
  users. Use mode `0600`.
- The binary does not accept a symbolic link as `token_file`.
- On Windows, use a user-only ACL. Token file ACLs are checked for the owner,
  SYSTEM, and Administrators; other identities are rejected. The audit file
  ACL is not examined.
- `token_header` selects the authentication header. The server does not accept static
  headers with secret-related names.
- The server rejects a static header that equals the effective authentication
  header in any letter case. This rule includes adapter default headers.
  Header names are canonical, so each logical header has one value.
- The server rejects method and URL override headers, including
  `X-HTTP-Method-Override`, `X-Method-Override`, `X-Original-URL`, and
  `X-Rewrite-URL`. It also rejects representation and conditional headers such
  as `Accept-Encoding`, `Range`, `If-None-Match`, and `If-Modified-Since`.
  A tenant header that does not change the method or target remains valid.
- The server does not accept query strings, fragments, and userinfo in `base_url`.
- Errors and capabilities do not show tokens, URLs, or destination hostnames.
  They use the logical source name.
- Built-in redaction includes passwords, bearer tokens, complete `Cookie` and
  `Set-Cookie` header values, usual keys, email addresses, and private-key
  headers. It also includes other secret forms.
  A cookie header value continues to the end of its line. Content on other
  lines is not part of the header and remains unchanged.
- Configured authentication tokens are exact redaction rules, including values
  echoed without a label in source responses and cached evidence.
- MCP error responses use the configured redaction rules.
- Audit error fields have size limits and use the same rules.
- The server redacts source identifiers, text attributes, and summaries. It
  uses text size limits for all of them.
- Source response cache keys use a process-local HMAC fingerprint for all
  header values.
  The keys contain no clear-text secret or digest that an attacker can use
  again.
- An HMAC fingerprint also replaces paths and query strings in cache keys.
- Identical in-flight GETs with the same destination, path, and authentication
  fingerprint share one upstream request. A canceled leader does not poison a
  waiter; one waiter can start a replacement request. An ordinary upstream
  failure is shared by the active waiters and does not cause one retry per
  waiter. A canceled waiter returns its own cancellation. The shared response is
  independent of the longer-lived source TTL.
- The source response cache does not keep response headers. This includes
  `Set-Cookie` and authentication challenges. It keeps only the ntfy
  `X-Messages-Truncated` signal as a boolean for cache hits.
- The source response cache checks every storage bypass before freshness. A
  `Cache-Control: no-store`, a `Cache-Control: no-cache`, or a
  `Pragma: no-cache` response is not stored, even when it also has
  `Cache-Control: max-age`. A `max-age` value caps the configured source TTL.
- The source response cache subtracts a valid upstream `Age` value from
  `max-age` before it stores the body. For example, `max-age=60` with `Age=59`
  gives about one second. If `Age` is at least `max-age`, or if `Age` is
  malformed, signed, repeated, empty, or too large for a duration, the server
  does not store the response. A response without `Age` keeps the previous
  behavior.
- The cache cannot see time that a response spent in an upstream cache before
  the last proxy sends it. It relies on `Age` to describe all consumed freshness.
  A proxy that omits or understates `Age` can extend stale snapshot evidence. Use
  a short `source_cache_ttl` when snapshot freshness is critical.
- Make sure that only the owner can access the YAML configuration.
- On Unix, the server creates the audit file in mode `0600`.
- On Windows, use a user-only ACL for the audit file path. Token files are
  checked: an explicit allow entry for an identity other than the current
  user, SYSTEM, or Administrators is rejected. The audit file ACL is not
  examined.

### Hostile text

The server cleans and redacts Loki lines and ntfy notifications. It uses
text size limits. It adds `[UNTRUSTED_LOG_DATA]` when the text contains an
instruction pattern.
The initial content is hostile data.
Redaction rules examine the original text. They do not process replacement markers.
Overlapping or adjacent matches form one redacted region. Empty matches add no text.

### Audit storage failures

Each audit write contains one complete JSONL record.
The file writer refuses a record larger than `audit.max_bytes` before rotation.
A failed stderr write does not stop a configured audit file from receiving the
record. A partial append is rolled back, and an incomplete final JSONL record is
repaired when the file is opened again.
On Unix, one process takes an exclusive sidecar lock for an audit path. A second
process receives a clear `audit file is in use by another process` error.
If every audit destination fails, stderr receives the fixed diagnostic
`audit write failed`. The server gives one diagnostic per continuous failure
period. The diagnostic contains no writer error, path, or event data.
Tool calls continue, and the server attempts subsequent audit writes.
A rotation or reopen failure is recoverable: the next write reopens the file
with the regular-file, mode `0600`, and size checks. `Close` stops recovery.
Audit records can be missing during a storage failure, and retained backups can
lag the primary file. Audit file contents can be incomplete after a storage
failure.

### Build and release supply chain

- External GitHub Actions use full commit SHAs with a version comment.
  Dependabot updates the pins.
- The CI checkout does not persist credentials. The validation job has
  `contents: read`.
- Release validation and build run in a read-only job. A separate publication
  job receives `contents: write` only after validation, scans, the release
  binary E2E check, and checksum creation succeed.
- Native smoke jobs execute each tested candidate binary on its target runner.
  Publication needs the native smoke jobs.
- The publication job downloads the tested binaries and verifies `SHA256SUMS`.
  It runs no repository code.
- Release tags trigger publication. No pull-request event has publication
  credentials.
- CI does not upload test reports or security scan results as artifacts.

### Cardinality and denial of service

The server uses these controls:

- Maximum sizes for the configuration and HTTP bodies.
- Configuration cardinality limits for sources, services, headers, and
  redaction rules.
- Limits for lines, evidence items, windows, and timeouts.
- Global per-minute and concurrency limits.
- A maximum of eight concurrent source requests. This maximum includes fan-out
  requests.
- A source response cache limit of 128 entries, 32 MiB, and a TTL.
- An evidence cache limit of 1,000 entries, a TTL, and a serialized-payload
  byte budget. The byte budget defaults to 8 MiB and cannot exceed 64 MiB.
- The value `evidence_cache_max: 0` deactivates the evidence cache.
- An item larger than the whole evidence byte budget is not retained.
- A `max_log_line_bytes` limit for hostile log and notification text. The
  maximum value is 16 KiB.
- Responses with data from other sources when a source has an error. The
  response gives each source state.
- Client-side limits even when a remote source ignores them.

Use `limits.source_cache_ttl: 0` to deactivate the source response cache. A short
TTL reduces the risk of a stale snapshot. On a cache hit, `observed_at` keeps
the initial collection time. Freshness continues to show the age of the data.

### Misleading results

The server has these rules and limits for results:

- If there is no evidence, the server does not use this as proof of correct
  operation.
- A source with an error does not remove results from other sources.
- The timeline keeps contradictory evidence.
- Network and decode errors do not become an empty list without an error.
- Gatus selects the state with the greatest timestamp, not by position.
- The Healthchecks list API cannot show a past incident or a recovery.
- A current Healthchecks state uses the collection time. The `last_ping` value
  stays in a different attribute.

### Remaining risks

These risks remain:

- A compromised configuration can contain services that an attacker controls.
- The system DNS remains a trust dependency.
- The operator must make sure that the Docker proxy gives only read access.
- Redaction supplies defense in depth. It is not a DLP system.
- Clock skew can put events from different hosts in the incorrect order.

## Operator checklist

Do these steps:

1. Use a Docker proxy with access only to `GET /containers/json` or the
   matching versioned route, such as `GET /v1.56/containers/json`.
2. Use a read-only Healthchecks key.
3. Store configuration and tokens in a directory that is not in the repository.
4. On Unix, use mode `0600`.
5. On Windows, use a user-only ACL. Token file ACLs are checked; audit
   file ACLs are not.
6. Run the binary with a low-privilege account.
7. If possible, use HTTPS with a correct internal PKI.
8. Adapt `redact` rules to local secret formats.
