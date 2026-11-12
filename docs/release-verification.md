# Release verification

## Current release

The latest public release `v0.2.1` is mutable because repository immutability
is disabled. Its assets have build-provenance attestations from the release
workflow. A `SHA256SUMS` file from the same mutable release does not
independently authenticate the assets.

Verify an asset against its attestation:

```bash
sha256sum -c SHA256SUMS
gh attestation verify homelab-evidence-mcp_linux_amd64 \
  --repo ThomasCrouzet/homelab-evidence-mcp
```

## Candidate release process

The release workflow:

1. Validates the tag, version, changelog, source version, static checks, race
   tests, executable E2E suite, and independent Python MCP client
   interoperability.
2. Builds the five targets and stage artifact.
3. Runs the native smoke suite on each target.
4. Creates a draft release.
5. Uploads every asset and verifies that the draft asset list equals `dist/`.
6. Publishes only after the full set exists.

The `verify` job runs the interoperability check before it uploads the release
artifact. A failure stops `verify`, so `native-smoke` and `publish` do not run.
Only `publish` has write and attestation permissions. It downloads the staged
artifacts and runs no repository code.

A version tag starts only the release workflow. The `CI` workflow runs on pull
requests and branch pushes, not on tags.

Attestations use the pinned GitHub build-provenance action, with `id-token`
and `attestations` permissions scoped to the publish job.

## Consumer commands

For a future immutable release:

```bash
gh release verify v0.2.1
gh release verify-asset v0.2.1 homelab-evidence-mcp_linux_amd64
```

Immutability applies only to releases published after a maintainer enables
**Settings > Code security > Immutable releases**. It is not retroactive.
