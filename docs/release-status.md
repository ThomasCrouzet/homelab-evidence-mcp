# Release status

The default branch and the latest public release can differ by documentation
commits.

## Latest public release

The latest public release is `v0.2.0`, published 2026-11-09 from the `v0.2.0`
tag. This release is mutable and its assets have build-provenance attestations.

Use the release when you need a tagged, published version:

```bash
go install github.com/ThomasCrouzet/homelab-evidence-mcp/cmd/homelab-evidence-mcp@v0.2.0
```

`@latest` resolves to the newest published module tag. It does not install the
default branch.

## Default branch

`main` contains the release and later documentation updates. The source version
reports `0.2.0`.

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

| Capability | v0.1.3 | v0.2.0 |
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
| Explicit Beszel metric units and pagination coverage | No | Yes |
| Upstream cache-directive handling | No | Yes |
| Fuzz targets, benchmarks, and opt-in live lane | No | Yes |

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
