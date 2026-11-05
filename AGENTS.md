# homelab-evidence-mcp

## What this is

Read-only evidence collection for a homelab. One Go binary speaks MCP over stdio
and joins six read-only sources into a timestamped, redacted timeline per
`service_id`. It serves operators during an incident with facts and limits, not
a root cause. State: v0.1.3, released 2026-10-23, active development.

## Stack

- Go 1.26.10 is the minimum version. The value agrees with `go.mod`, CI, and the
  release workflow. The repository has one module and one binary from
  `cmd/homelab-evidence-mcp`.
- The project uses MCP Go SDK 1.8.0 and `gopkg.in/yaml.v3`. The build matrix uses
  static binaries with `CGO_ENABLED=0`.
- There is no database, container, or daemon. The external services are the six
  HTTP APIs in the YAML configuration: Gatus, Docker Engine through a
  read-limited socket proxy, Loki, Healthchecks, Beszel, and ntfy.
- `make lint` needs golangci-lint 2.15.0 on `PATH`. `make lint-docs` needs
  Node.js 22 or later with `npx`. CI uses Node 24.

## Commands

The recipes come from the `Makefile` and `scripts/`. Run them from the
repository root. Check the local tool versions before you compare results:

```bash
go version
golangci-lint --version
node --version
```

```bash
make build                  # bin/homelab-evidence-mcp with the source version
make build-matrix           # five release targets in dist/
make print-version          # resolved Makefile version
./scripts/check-version.sh "v$(make -s print-version)"
make test                   # race tests, then the executable stdio E2E suite
make test-quick             # unit tests only: no race detection and no stdio E2E
make test-e2e               # real binary against six local httptest fixtures
make lint                   # go vet, gofmt check, golangci-lint
make lint-docs              # npx markdownlint-cli2 0.23.2
make lint-private           # rejects Tailscale addresses and *.ts.net
make demo                   # same as go run ./demo, in-memory MCP
make coverage               # coverage.out, no race detection and no stdio E2E
GOMAXPROCS=2 GOFLAGS=-p=1 make test
hem_artifact_dir=/var/tmp/hem-e2e
HEM_E2E_ARTIFACT_DIR="$hem_artifact_dir" make test-e2e
HEM_E2E_BINARY="$PWD/bin/homelab-evidence-mcp" make test-e2e
go test ./internal/mcpserver -run TestToolsViaMCPSession -count=1
```

`make test` runs the race-enabled package tests with `-skip '^TestStdio'`, then
runs `scripts/test-e2e.sh`. That script runs the `TestStdio` tests against the
real executable. `make test-quick` runs the package tests without race detection
and without the stdio E2E suite. `make coverage` also skips the stdio E2E suite.

No command needs a secret. All commands use local fixtures, so no live homelab
is necessary. `make lint-docs` downloads markdownlint-cli2 on first use. The E2E
runner writes its log and metadata outside the repository.

Rebuild the binary for the current platform before you execute it:

```bash
make build
./bin/homelab-evidence-mcp --version
```

## Layout

- `cmd/homelab-evidence-mcp/main.go`: flags, config load, stdio wiring.
- `internal/mcpserver/`: the seven MCP tools, budgets, and collectors.
  `tools.go` holds the tool definitions.
- `internal/adapters/<source>/`: one GET-only adapter for each source.
- `internal/httpx/client.go`: the only HTTP path. It locks destinations, refuses
  redirects, and caches responses.
- `internal/evidence/`: item model, sort order, freshness, opaque identifiers.
- `internal/version/version.txt`: authoritative release version. `version.go`
  embeds it and accepts a linker override.
- `internal/correlation/`, `internal/redaction/`, `internal/registry/`,
  `internal/config/`, and `internal/audit/` hold the remaining logic.
- `scripts/test-e2e.sh` and `scripts/check-private-addresses.sh`: the E2E runner
  and the private-address scan.
- `bin/`, `dist/`, `coverage.out`, and `./homelab-evidence-mcp`: generated and
  ignored. Do not edit them.
- `docs/`: evidence model, API compatibility, incident playbook, and MCP host
  notes. `config.example.yaml` is the template.

## Invariants

- Write evidence artifacts outside the repository. `scripts/test-e2e.sh` exits 2
  for an in-tree path, because an artifact in the tree can be committed.
- Record `source_revision`, `tracked_diff_sha256`,
  `working_tree_identity_sha256`, environment, fixtures, and result in each
  artifact. A result without revision identity cannot be tied to code.
- Keep `observed_at` as the source time and `retrieved_at` as the read time.
  Freshness is their difference, so a filled `observed_at` hides the age.
- Keep the initial `observed_at` on a source cache hit. Only a refresh reports a
  new snapshot time.
- Use HTTP `GET` only, to destinations fixed at startup. An MCP call cannot
  supply a URL, selector, container, or topic, so a caller cannot retarget the
  server.
- Keep adapter HTTP inside `internal/httpx`. It refuses redirects and ignores
  `HTTP_PROXY` and `HTTPS_PROXY`, so a proxy cannot divert a read.
- Treat log lines, notification bodies, and upstream identifiers as hostile
  data. Redact and size-limit them, because they reach the client as text.
- Never emit tokens, ping URLs, unique keys, or `Config.Env`. Emit
  `redactions_applied` instead, because evidence is copied into tickets and
  chat.
- Keep `severity` as presentation, not diagnosis. Keep it and `factual_summary`
  causal-free.
- Keep missing data missing. Report `ok`, `absent`, `skipped`, `error`,
  `timeout`, or `canceled` per source, because an empty list must not look
  healthy.
- Do not add start, stop, restart, exec, write, or delete operations. The tool
  must stay unable to change the homelab.
- Keep config and token files owner-only (mode `0600`, user ACL on Windows) and
  take secrets from `token_env` or `token_file`. A `base_url` cannot hold a
  query, fragment, or userinfo.
- Keep the tag, the Makefile `VERSION` default,
  `internal/version/version.txt`, and the `CHANGELOG.md` section equal. The
  release workflow rejects a mismatch.
- Do not use U+2014 in new content.
- Keep audit and review reports outside the repository. Do not commit them.

## Pitfalls

- A binary in `bin/` can target another operating system. If
  `./bin/homelab-evidence-mcp --version` reports `cannot execute binary file:
  Exec format error`, run `make build` again for the current platform.
- `make test-e2e` exits 2 with `HEM_E2E_ARTIFACT_DIR must be outside the
  repository.` The runner refuses in-tree paths. Point the variable at a
  directory such as `/var/tmp/hem-e2e`.
- `make lint` stops with `Install golangci-lint 2.15.0`. The binary is absent
  from `PATH`. Install that version. The target pins `GOTOOLCHAIN=go1.26.10`.
  Override `LINT_GOTOOLCHAIN` only if a compatible toolchain is necessary.
- `--validate` rejects unknown YAML keys and a `base_url` with a query or
  fragment. The parser is strict. Correct the config. Do not relax the check.
- `TestToolsViaMCPSession` can pass while the stdio transport stays broken. It
  uses in-memory MCP and local fixtures. Run `make test-e2e` to cover the
  executable, its flags, and the stdout/stderr split.
- A cached response can look newer than it is. Only `get_evidence` returns
  `freshness=cached`. `observed_at` keeps the first collection time. Read
  `retrieved_at` for the current read.

## Conventions

- Branch from `main`. Use short, imperative commits with a prefix (`feat:`,
  `fix:`, `ci:`, `docs:`, or `chore:`). Do not rewrite history. Do not add
  attribution lines.
- One pull request has one purpose and includes its tests. Update the docs when
  a tool, the config, or the security model changes. Give the reason for each
  new dependency.
- Write documentation, comments, and commit messages in English. Use ASD-STE100
  Issue 9 as a descriptive style guide: one instruction per sentence, active
  voice, 20 words for a procedure sentence, and 25 words for a descriptive
  sentence. Do not claim certified conformance.
- Read `CONTRIBUTING.md` first. Keep its code, test, security, and pull request
  rules.
- Write the test before the code. Prefer E2E tests with observable results.
  Document failure checks for decoding, freshness, truncation, budgets, cache
  isolation, config, redaction, and destinations. Never test a coverage
  percentage, a type contract, or a mock call sequence alone. A skipped E2E test
  is not coverage. A simulated boundary is not coverage either. Keep isolated
  modes before an isolated test, and use one only for a failure E2E cannot
  detect.
- CI (`ci.yml`) runs on push and pull request to `main`. It checks Markdown,
  gofmt, private addresses, the `go mod tidy` diff, golangci-lint, race and
  coverage, the demo, the executable E2E, govulncheck, and gitleaks. The E2E
  artifact directory is under `$RUNNER_TEMP`. Do not upload CI reports or scan
  results as artifacts.
- Release on a tag `v*.*.*`. The tag must equal the Makefile version, the source
  default, and the changelog heading. The release workflow rechecks them, builds
  five targets, and publishes checksums. Dependabot raises routine updates and
  the auto-merge workflow handles them.
