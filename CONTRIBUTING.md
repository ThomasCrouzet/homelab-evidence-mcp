# Contributing

## Rules

Use these contribution rules:

- Write code, comments, commits, and documentation in English.
- Keep variable, function, type, and file names in English.
- Do not add attribution or promotional lines to commit messages.
- Do not decrease coverage, remove tests, or add convenience `nolint`
  directives.
- Do not add start, stop, restart, exec, write, or delete operations.
- The pull request must give the cause for each new dependency.
- Obey the repository-wide writing rules in [AGENTS.md](AGENTS.md).

## Setup

Use these commands for setup:

```bash
git clone https://github.com/ThomasCrouzet/homelab-evidence-mcp.git
cd homelab-evidence-mcp
go test ./...
go test -race ./...
go run ./demo
```

Go 1.25 is the minimum version.

Node.js 22 is the minimum version for the Markdown check. `npx` must be
available. The Go lint check uses golangci-lint 2.12.2. By default, it uses Go
toolchain 1.25.14. Override `LINT_GOTOOLCHAIN` only when a compatible toolchain
is necessary.

Use `make lint-docs` to examine Markdown. Use `make lint` to examine Go source.

## Style

Use these code style rules:

- Run `gofmt`.
- Make sure that error text helps the operator correct the problem.
- Do not include secrets in error messages.
- If possible, use small packages in `internal/`.
- Keep all adapter HTTP access behind `internal/httpx`.

## Tests

Add contract tests with `httptest` for each adapter change. Do not use a
homelab for standard tests. Add hostile test cases when you change redaction,
SSRF handling, or logs.

## Pull requests

Use these pull request rules:

1. Limit each pull request to one change with one purpose.
2. Include applicable tests.
3. Update documentation if tools, configuration, or the security model change.
4. Use short, imperative commit messages.

For an unpatched vulnerability, refer to [SECURITY.md](SECURITY.md). Do not open
a public issue.
