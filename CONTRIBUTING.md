# Contributing

Thanks for your interest in the project.

## Rules

- Code, comments, commits, and documentation are written in English.
- Variable, function, type, and file names stay in English.
- Do not add attribution or promotional lines to commit messages.
- Do not reduce coverage, remove tests, or add convenience `nolint` directives.
- Do not introduce mutation: start, stop, restart, exec, write, or delete.
- Any new dependency must be justified in the pull request.

## Setup

```bash
git clone https://github.com/ThomasCrouzet/homelab-evidence-mcp.git
cd homelab-evidence-mcp
go test ./...
go test -race ./...
go run ./demo
```

Go 1.25 or later is required.

## Style

- Apply `gofmt`.
- Produce actionable errors without including secrets.
- Prefer small packages under `internal/`.
- Keep all adapter HTTP access behind `internal/httpx`.

## Tests

Any adapter change must include contract tests with `httptest`. No standard test
should depend on a real homelab. Add hostile cases when changing redaction,
SSRF handling, or logs.

## Pull requests

1. Limit each pull request to one coherent change.
2. Include appropriate tests.
3. Update documentation if tools, configuration, or the security model change.
4. Use short, imperative commit messages.

For an unpatched vulnerability, follow [SECURITY.md](SECURITY.md) instead of
opening a public issue.
