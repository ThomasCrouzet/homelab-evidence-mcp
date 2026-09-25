# Security policy

## Versions

The project supplies security updates for version 0.1.x.

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
- Budgets set maximum call counts, concurrency, and response volumes.
- The Docker Unix socket is not compatible with the server.

### SSRF and network resolution

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
- On Windows, use a user-only ACL. The binary does not examine Windows ACLs.
- `token_header` selects the authentication header. The server does not accept static
  headers with secret-related names.
- The server does not accept query strings, fragments, and userinfo in `base_url`.
- Errors and capabilities do not show tokens, URLs, or destination hostnames.
  They use the logical source name.
- Built-in redaction includes passwords, bearer tokens, cookies, usual keys,
  email addresses, and private-key headers. It also includes other secret forms.
- Configured authentication tokens are exact redaction rules, including values
  echoed without a label in source responses and cached evidence.
- The server redacts source identifiers, text attributes, and summaries. It
  uses text size limits for all of them.
- Source response cache keys use a process-local HMAC fingerprint for all
  header values.
  The keys contain no clear-text secret or digest that an attacker can use
  again.
- An HMAC fingerprint also replaces paths and query strings in cache keys.
- The source response cache does not keep response headers. This includes
  `Set-Cookie`.
- Make sure that only the owner can access the YAML configuration.
- On Unix, the server creates the audit file in mode `0600`.
- On Windows, use a user-only ACL for the audit file path. The binary does not
  examine Windows ACLs.

### Hostile text

The server cleans and redacts Loki lines and ntfy notifications. It uses
text size limits. It adds `[UNTRUSTED_LOG_DATA]` when the text contains an
instruction pattern.
The initial content is hostile data.

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
- An evidence cache limit of 1,000 entries and a TTL.
- The value `evidence_cache_max: 0` deactivates the evidence cache.
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

1. Use a Docker proxy with access only to `GET /containers/json`.
2. Use a read-only Healthchecks key.
3. Store configuration and tokens in a directory that is not in the repository.
4. On Unix, use mode `0600`.
5. On Windows, use a user-only ACL. The binary does not examine Windows ACLs.
6. Run the binary with a low-privilege account.
7. If possible, use HTTPS with a correct internal PKI.
8. Adapt `redact` rules to local secret formats.
