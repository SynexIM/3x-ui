# Release procedure

This fork has two release channels:

1. Stable releases are created from a protected `main` commit with a tag such as
   `v3.6.0`.
2. Release candidates use a tag such as `v3.6.0-rc.1` and are not promoted as
   the latest stable release.
3. `dev-latest` is force-moved by CI for development builds only.

## Required order

1. Release the matching SynexIM `xray-core` build first, and confirm that
   Release actually carries its `Xray-*.zip` assets.
2. Write that tag into `.xray-version`. It is the single source of truth: both
   the Linux and the Windows build job read the file, and nothing else sets it.
3. Run `make check-xray-pin`. It fails unless the `.xray-version` tag resolves
   to exactly the commit `go.mod` replaces `github.com/xtls/xray-core` with, and
   unless that Release carries all eight archives the build downloads. CI runs
   the same script in the `verify-xray-pin` job, which gates every build job.
4. Run the local verification gate and the real-node smoke journey.
5. Push the release tag from the release branch.
6. Confirm the GitHub Release contains every platform archive and its
   `.sha256` companion.
7. Verify a clean install and an in-place update from the previous release.

## Why step 3 exists

The panel is *compiled* against the xray-core commit in `go.mod` and *ships*
the binary from the `.xray-version` Release tag. When those drift, the build is
green, the install succeeds, and the panel only fails later over gRPC against a
core that lacks the symbols it calls. Nothing else in the pipeline notices.

The panel updater must resolve this repository's Release API and raw files. A
release is not complete when only a Git tag exists: the tag, Release assets,
container image, checksums, and install/update path must all point at the same
commit.

## Rollback

Rollback means selecting the previous immutable GitHub Release tag and its
matching archive. Never replace an existing stable tag or silently rebuild a
published asset.
