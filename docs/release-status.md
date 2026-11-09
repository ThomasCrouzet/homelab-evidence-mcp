# Release status

The default branch and the latest public release are different code.

## Latest public release

The latest public release is `v0.1.3`, published 2026-10-23 from commit
`ad610ab2bd74df78c8cf951d0afde0521574478e`. This release is mutable and does
not contain the post-audit work on `main`.

Use the release when you need a tagged, published version:

```bash
go install github.com/ThomasCrouzet/homelab-evidence-mcp/cmd/homelab-evidence-mcp@v0.1.3
```

`@latest` resolves to the newest published module tag. It does not install the
default branch.

## Default branch

`main` contains unreleased work: the audit fixes, `--doctor`, typed output
schemas, compact incident context, Healthchecks transitions, and the newer
source-coverage fields. The source version still reports `0.1.3` until a release
bump.

A development build of `main` is available from a clone:

```bash
git clone https://github.com/ThomasCrouzet/homelab-evidence-mcp.git
cd homelab-evidence-mcp
make build
./bin/homelab-evidence-mcp --version
```

A development build is not a release candidate and is not covered by release
checksums or provenance attestations.

## Feature availability

| Capability | v0.1.3 | Main |
| --- | --- | --- |
| Seven MCP tools over stdio | Yes | Yes |
| `--doctor` and `--doctor --json` | No | Yes |
| Typed output schemas | No | Yes |
| Compact `incident_context` | No | Yes |
| Healthchecks `history_mode: transitions` | No | Yes |
| Source coverage and omission fields | Partial | Yes |
| Evidence cache byte budget | No | Yes |
| Docker `api_version` | No | Yes |
| Healthchecks `status_filter` distinction | No | Yes |

Check the installed binary instead of assuming:

```bash
homelab-evidence-mcp --version
homelab-evidence-mcp --config /path/config.yaml --validate
```

The MCP `evidence_capabilities` tool reports the tool list, limits, and
features of the running binary.

## Release gate

A release requires a matching tag, `Makefile` version, source version, and
changelog section. The release workflow checks them before publication. Do not
describe an unreleased capability as part of the latest tag.
