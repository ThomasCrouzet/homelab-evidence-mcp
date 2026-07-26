# Security policy

## Supported versions

| Version | Supported |
|---|---|
| 0.1.x | yes |

## Reporting a vulnerability

Open a private security advisory on the GitHub repository or write to the
maintainer address listed on their profile. Do not open a public issue for a
secret leak or an unpatched vulnerability.

## Threat model

### Protected assets

- service inventory and health signals;
- log excerpts that may contain access secrets or personal data;
- Healthchecks API tokens;
- filtered Docker metadata;
- temporary caches held in process.

### Trust boundaries

| Input or output | Trust level |
|---|---|
| YAML configuration and environment at startup | operator input, considered trusted |
| MCP tool arguments | untrusted |
| HTTP responses from sources | untrusted |
| log and notification text | hostile data |
| stdout | JSON-RPC protocol only |
| stderr and audit file | redacted operational data |

### Structural read-only

- No start, stop, restart, exec, write, or delete tools.
- The internal HTTP client exposes only `GET`.
- Redirects are refused.
- Destinations are registered from configuration at startup.
- Path prefixes of `base_url` are preserved.
- Absolute paths, directory traversal, and host changes are refused.
- Loki selectors and ntfy topics come exclusively from configuration.
- Docker responses exclude `Config.Env`, raw mounts, and unauthorized labels.
- Healthchecks responses exclude UUID, ping, pause, update, and badge URLs.
- TLS verification stays enabled; no public option disables it.
- Budgets limit call count, concurrency, and response volume.
- The direct Docker Unix socket is not supported.

### SSRF and network resolution

A request may only target a destination name registered at startup. Schemes
are limited to `http` and `https`, redirects are refused, and MCP arguments
cannot supply a URL.

Private, CGNAT, and Tailscale addresses are allowed when they appear
explicitly in configuration: they are normal destinations for a homelab.

The hostname is locked, but its DNS resolution is still performed by the system
at connection time. A compromised DNS can therefore change the resolved
address. Use stable internal names, a controlled DNS, or fixed IP addresses for
sensitive destinations.

### Secrets

- Tokens come from `token_env` or a regular `token_file` in mode `0600` on
  Unix. On Windows, apply a user-only ACL; POSIX bits are not interpreted there.
- `token_header` chooses the authentication header; static headers whose name
  suggests an access secret are refused.
- Query strings, fragments, and userinfo are refused in `base_url`.
- Errors and capabilities show neither tokens, URLs, nor destination hostnames;
  they use the logical source name.
- Built-in redaction covers passwords, bearer tokens, cookies, common keys,
  email addresses, and private-key headers, among others.
- Redaction and bounds also apply to identifiers and textual attributes from
  sources, not only to summaries.
- HTTP cache keys use a process-local HMAC fingerprint for all header values;
  no secret or reusable digest is stored in clear text.
- Paths and query strings are also replaced by an HMAC fingerprint in cache
  keys.
- The HTTP cache retains no response headers, including `Set-Cookie`.
- The YAML configuration itself must also be owner-restricted.
- The audit log is created in mode `0600` on Unix. On Windows, the operator
  must protect its path with the same user ACL.

### Hostile text

Loki lines and ntfy notifications are cleaned, redacted, bounded, and prefixed
with `[UNTRUSTED_LOG_DATA]` when they look like an instruction. The original
content remains evidence to treat as untrusted data.

### Cardinality and denial of service

- maximum configuration and HTTP body size;
- bounded configuration cardinality for sources, services, headers, and
  redaction rules;
- limits on lines, evidence items, windows, and timeouts;
- global per-minute and concurrency limits;
- at most eight concurrent source requests, including during fan-out;
- source response cache bounded to 128 entries, 32 MiB, and a TTL;
- evidence cache bounded to 1,000 entries and a TTL, with individual texts
  limited to 16 KiB;
- explicit partial responses when a source fails;
- limits reapplied client-side even if a remote source ignores them.

The source response cache can be disabled with `limits.source_cache_ttl: 0`.
A short TTL reduces the risk of showing a snapshot that has become stale. On a
hit, `observed_at` keeps the original collection time and freshness continues
to reflect the real age of the data.

### Misleading results

- Absence of evidence is never presented as proof of healthy operation.
- Failure of one source does not remove results from the others.
- Contradictions are kept in the timeline.
- Network or decode errors never become a successful empty list.
- Gatus picks the latest state by timestamp, not by position.
- The Healthchecks list API cannot assert a past incident or a recovery.
- A current Healthchecks state is dated at collection time; `last_ping` remains
  a separate attribute.

### Residual risks

- A compromised configuration can point to attacker-controlled services.
- System DNS remains a trust dependency.
- A read-only Docker proxy must be correctly restricted by the operator.
- Redaction is defense in depth, not a DLP system.
- Clock skew can disorder events across multiple hosts.

## Operator checklist

1. Use a Docker proxy limited to `GET /containers/json`.
2. Use a strictly read-only Healthchecks key.
3. Store configuration and tokens outside the repository in mode `0600` on
   Unix, or with an equivalent user ACL on Windows.
4. Run the binary with a low-privilege account.
5. Prefer HTTPS with a valid internal PKI.
6. Adapt `redact` rules to local secret formats.
