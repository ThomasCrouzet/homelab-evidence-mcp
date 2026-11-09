# Platform validation matrix

The release workflow builds five targets and runs the native smoke suite on
each advertised target. The last verified column records the most recent
release with evidence. Source revisions after that release are not verified
until the next release run.

- **Linux amd64** (`ubuntu-latest`): native release smoke and executable E2E.
  Last verified: v0.2.1.
- **Linux arm64** (`ubuntu-24.04-arm`): native release smoke. Last verified:
  v0.2.1.
- **macOS amd64** (`macos-15-intel`): native release smoke. Last verified:
  v0.2.1.
- **macOS arm64** (`macos-15`): native release smoke. Last verified: v0.2.1.
- **Windows amd64** (`windows-latest`): native release smoke. Last verified:
  v0.2.1.

The matrix does not list a target without a build in the release workflow.
Linux arm64 has a GitHub-hosted arm64 runner. macOS Intel uses the remaining
`macos-15-intel` label, which GitHub documents until 2027. The matrix must be
updated if a label is retired.

## What native smoke executes

The suite runs the actual candidate binary from the verified release artifact.
It does not rebuild a substitute. It covers:

- `--version` with the expected release version.
- A valid and an invalid `--validate`.
- MCP initialization and tool discovery.
- One fixture-backed `service_status` collection.
- Clean MCP shutdown.
- A binary and configuration path that contain spaces.
- stdout JSON-RPC separation from stderr diagnostics.
- On Unix, rejection of a group-accessible configuration file.

The suite uses loopback fixtures and synthetic credentials. It needs no live
homelab and no model provider.

## Release gate

The verification job builds and tests the release candidates. The native smoke
jobs then execute each candidate. The publication job needs both jobs and
verifies the transferred checksums before it creates the release. A failing
native smoke job blocks publication for every target, including targets that
passed.

## Compile-only policy

A target can be published as compile-only only when no native runner is
available, and the matrix must say so before the release. A known execution
failure is not relabeled as missing coverage. If a native runner disappears,
update the matrix and the workflow before the next release.

## Evidence location

Native smoke logs stay in the GitHub Actions run. Do not upload test reports or
raw transcripts as artifacts. Release binaries, checksums, licenses, and release
notes remain allowed artifacts.
