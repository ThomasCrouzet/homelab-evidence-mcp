# Release verification

## Current release

The latest public release `v0.1.3` is mutable. It has no immutable-release
attestation. A `SHA256SUMS` file from the same mutable release does not
independently authenticate the assets.

## Candidate release process

The release workflow:

1. Validates the tag, version, changelog, source version, tests, and native
   smoke checks.
2. Builds the five targets and stage artifact.
3. Creates a draft release.
4. Uploads every asset and verifies that the draft asset list equals `dist/`.
5. Publishes only after the full set exists.

Attestations use the pinned GitHub build-provenance action, with `id-token`
and `attestations` permissions scoped to the publish job.

## Consumer commands

For a future immutable release:

```bash
sha256sum -c SHA256SUMS
gh release verify v0.2.0
gh release verify-asset v0.2.0 homelab-evidence-mcp_linux_amd64
```

Immutability applies only to releases published after a maintainer enables
**Settings > Code security > Immutable releases**. It is not retroactive.
