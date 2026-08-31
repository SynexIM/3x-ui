# Changelog

All notable changes to the SynexIM fork are recorded here.

## v3.7.1 — 2026-09-01

First stable release published from this fork's own channel.

### Added
- Outbounds and routing rules as individually addressable objects; editing one
  no longer rewrites the whole template or restarts the core.
- Namespace-scoped API tokens, so an automation and an operator can share one
  panel without either being able to delete the other's objects.
- Read-only runtime page: what the core actually loaded, next to what the panel
  saved.
- Per-client and node-level traffic shaping (PIR / CIR / CBS), and per-client
  fixed egress selection via Xray `egress_tag`.
- Declarative node configuration with bounded reconciliation, incremental
  delivery, and panel-side delivery links for API-driven fleet management.
- `.xray-version` plus `make check-xray-pin`: the packaged Xray binary and the
  xray-core commit `go.mod` compiles against must name the same commit, or the
  release is blocked. Drift used to build green, install fine, and only fail
  later over gRPC on a customer node.
- `install.sh` / `update.sh` now verify the Release `.sha256` companion file.

### Changed
- Xray pinned to SynexIM/xray-core `v26.7.28-synexim.2`.
- Updater, repository links, Release assets, and container namespace are the
  fork's own and are not mixed with the upstream 3x-ui release channel.

### Fixed
- `internal/config/version` was still `3.6.0` while the release was tagged
  `v3.7.0`, so the panel reported the old version and the updater saw
  `releases/latest` as permanently newer. `v3.7.0` is superseded by this tag;
  the `Release policy` workflow now fails any tag whose embedded version
  disagrees with it.

### Notes
- The tag carries no `-suffix` on purpose: `release.yml` marks any hyphenated
  tag as a pre-release, and `install.sh` resolves `releases/latest`, which skips
  pre-releases. A `-synexim.N` tag would be undiscoverable by the installer.

## Unreleased

- Separate the fork's updater, repository links, release assets, and container
  namespace from the upstream 3x-ui project.
- Add release and security procedures for public distribution.
