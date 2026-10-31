# homelab-evidence-mcp

## What this is

Read-only evidence collection for a homelab. One Go binary speaks MCP over stdio
and joins six read-only sources into a timestamped, redacted timeline per
`service_id`. It serves operators during an incident with facts and limits, not
a root cause. State: v0.1.3, released 2026-10-23, active development.

## Stack

- Go 1.26.10 minimum, the version in `go.mod`, CI, and the release workflow. One
  module, one binary
from `cmd/homelab-evidence-mcp`.
- MCP Go SDK 1.8.0 and `gopkg.in/yaml.v3`. Static binaries, `CGO_ENABLED=0` in
  the build matrix.
- No database, no container, no daemon. The external services are the six HTTP
  APIs in the YAML
config: Gatus, Docker Engine through a read-limited socket proxy, Loki,
Healthchecks, Beszel, ntfy.
- `make lint` needs golangci-lint 2.15.0 on `PATH`. `make lint-docs` needs
  Node.js 22 or later with
`npx`; CI uses Node 24.

## Verified commands

The recipes come from the `Makefile` and the scripts, checked with `make -n`
from the repository root. Go 1.27.3 is installed; `go build ./...` and `go vet
./...` pass from the root.

```bash
make build                  # bin/homelab-evidence-mcp, version from VERSION
make test                   # race tests, then the executable stdio E2E suite
make test-quick             # same tests, no race detection
make test-e2e               # real binary against six local httptest fixtures
GOMAXPROCS=2 GOFLAGS=-p=1 make test
HEM_E2E_ARTIFACT_DIR=/var/tmp/hem-e2e make test-e2e   # outside the repository
HEM_E2E_BINARY=/abs/path/homelab-evidence-mcp make test-e2e
make lint                   # go vet, gofmt check, golangci-lint
make lint-docs              # npx markdownlint-cli2 0.23.2
make lint-private           # rejects Tailscale addresses and *.ts.net
make demo                   # same as go run ./demo, in-memory MCP
make coverage               # coverage.out, ignored by Git
go test ./internal/mcpserver -run TestToolsViaMCPSession -count=1
```

No command needs a secret, and all of them use local fixtures, so no live
homelab is necessary. `make lint-docs` downloads markdownlint-cli2 on first use.

## Layout

- `cmd/homelab-evidence-mcp/main.go`: flags, config load, stdio wiring.
- `internal/mcpserver/`: the seven MCP tools, budgets, collectors. `tools.go`
  holds the definitions.
- `internal/adapters/<source>/`: one GET-only adapter for each source.
- `internal/httpx/client.go`: the only HTTP path. It locks destinations, refuses
  redirects, caches.
- `internal/evidence/`: item model, sort order, freshness, opaque identifiers.
  `internal/correlation/`,
`internal/redaction/`, `internal/registry/`, `internal/config/`,
`internal/audit/` hold the rest.
- `scripts/test-e2e.sh`, `scripts/check-private-addresses.sh`: the E2E runner
  and the private scan.
- `bin/`, `dist/`, `coverage.out`, `./homelab-evidence-mcp`: generated and
  ignored. Do not edit them.
- `docs/`: evidence model, API compatibility, playbook, MCP hosts;
  `config.example.yaml` template.

## Invariants

- Write evidence artifacts outside the repository. `scripts/test-e2e.sh` exits 2
  for an in-tree path,
because an artifact in the tree can be committed.
- Record `source_revision`, `tracked_diff_sha256`,
  `working_tree_identity_sha256`, environment,
fixtures, and result in each artifact. A result without revision identity cannot
be tied to code.
- Keep `observed_at` as the source time and `retrieved_at` as the read time.
  Freshness is their
difference, so a filled `observed_at` hides the age.
- Keep the initial `observed_at` on a source cache hit; a refresh reports a
  snapshot as `live`.
- Use HTTP `GET` only, to destinations fixed at startup. An MCP call cannot
  supply a URL, selector,
container, or topic, so a caller cannot retarget the server.
- Keep adapter HTTP inside `internal/httpx`. It refuses redirects and ignores
  `HTTP_PROXY` and
`HTTPS_PROXY`, so a proxy cannot divert a read.
- Treat log lines, notification bodies, and upstream identifiers as hostile
  data. Redact and
size-limit them, because they reach the client as text.
- Never emit tokens, ping URLs, or `Config.Env`. Emit `redactions_applied`
  instead, because evidence
is copied into tickets and chat.
- `severity` is presentation, not diagnosis; keep it and `factual_summary`
  causal-free.
- Keep missing data missing. Report `ok`, `absent`, `skipped`, `error`, or
  `timeout` per source,
because an empty list must not look healthy.
- Do not add start, stop, restart, exec, write, or delete operations. The tool
  must stay unable to
change the homelab.
- Keep config and token files owner-only (mode `0600`, user ACL on Windows) and
  take secrets from
`token_env` or `token_file`. A `base_url` cannot hold a query, fragment, or
userinfo.
- Keep the tag, `Makefile` `VERSION`, `internal/version.Version`, and the
  `CHANGELOG.md` section
equal. The release workflow rejects a mismatch.
- Do not use U+2014 in new content.
- Keep audit and review reports outside the repository. Do not commit them.

## Pitfalls

- `./bin/homelab-evidence-mcp --version` fails with `cannot execute binary file:
  Exec format error`.
The file is a darwin/arm64 Mach-O build that `.gitignore` hides. Run `make
build` for this platform.
- `make test-e2e` exits 2 with `HEM_E2E_ARTIFACT_DIR must be outside the
  repository.` The runner
refuses in-tree paths. Point the variable at a directory such as
`/var/tmp/hem-e2e`.
- `make lint` stops with `Install golangci-lint 2.15.0`. The binary is absent
  from `PATH`. Install
that version. The target pins `GOTOOLCHAIN=go1.26.10`; override
`LINT_GOTOOLCHAIN` only if needed.
- `--validate` rejects unknown YAML keys and a `base_url` with a query or
  fragment. The parser is
strict. Correct the config; do not relax the check.
- `TestToolsViaMCPSession` passes while the stdio transport stays broken. It
  uses in-memory MCP and
local fixtures. Run `make test-e2e` to cover the executable, its flags, and the
stdout/stderr split.
- A cached response looks newer than it is. Only `get_evidence` returns
  `freshness=cached`, and
`observed_at` keeps the first collection time. Read `retrieved_at` for the
current read.

## Conventions

- Branch `main`. Commits short, imperative, with a prefix (`feat:`, `fix:`,
  `ci:`, `docs:`, `chore:`).
Do not rewrite history or add attribution lines.
- One pull request, one purpose, with its tests. Update the docs when a tool,
  the config, or the
security model changes. Give the reason for each new dependency.
- Write documentation, comments, and commit messages in English. Use ASD-STE100
  Issue 9 as a style descriptive one. Do not claim certified conformance.
guide: one instruction per sentence, active voice, 20 words for a procedure
sentence and 25 for a
- Read `CONTRIBUTING.md` first; keep its code, test, security, and pull request
  rules.
- Tests: write the test before the code. Prefer E2E tests with observable
  results. Document failure checks for decoding, freshness, truncation, budgets,
  cache isolation, config, redaction, and destinations. Never test a coverage
  percentage, a type contract, or a mock call sequence alone. A skipped E2E test
  is not coverage, and a simulated boundary is not either.
modes before an isolated test, and use one only for a failure E2E cannot detect.
Keep isolated
- CI (`ci.yml`) on push and pull request to `main`: markdownlint, gofmt, private
  address scan, govulncheck, gitleaks. Do not upload CI reports or scan results
  as artifacts.
`go mod tidy` diff, golangci-lint, race and coverage, demo, executable E2E into
`$RUNNER_TEMP`,
- Release: push a tag `v*.*.*` equal to the three version locations and the
  changelog heading. The updates and the auto-merge workflow handles them.
workflow rechecks them, builds five targets, and publishes checksums. Dependabot
raises routine
