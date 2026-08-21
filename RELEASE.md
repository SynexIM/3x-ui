# Release procedure

This fork has two release channels:

1. Stable releases are created from a protected `main` commit with a tag such as
   `v3.6.0`.
2. Release candidates use a tag such as `v3.6.0-rc.1` and are not promoted as
   the latest stable release.
3. `dev-latest` is force-moved by CI for development builds only.

## Required order

1. Release the matching SynexIM `xray-core` build first.
2. Set the Xray release tag in the 3x-ui release workflow.
3. Run the local verification gate and the real-node smoke journey.
4. Push the release tag from the release branch.
5. Confirm the GitHub Release contains every platform archive, a SHA256 file,
   and the generated build provenance/SBOM metadata.
6. Verify a clean install and an in-place update from the previous release.

The panel updater must resolve this repository's Release API and raw files. A
release is not complete when only a Git tag exists: the tag, Release assets,
container image, checksums, and install/update path must all point at the same
commit.

## Rollback

Rollback means selecting the previous immutable GitHub Release tag and its
matching archive. Never replace an existing stable tag or silently rebuild a
published asset.
