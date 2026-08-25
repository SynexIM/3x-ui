# SynexIM 3x-ui fork

This repository is an open-source fork of [MHSanaei/3x-ui](https://github.com/MHSanaei/3x-ui), maintained by SynexIM.

## What is different

- Mixed clients and native Xray hot reload
- Per-client and node-level traffic shaping
- Declarative node configuration and bounded reconciliation
- Panel-side delivery links and runtime readback for API-driven fleet management

The upstream module path is intentionally preserved for source compatibility. The
release artifacts, updater, container images, and issue tracker belong to this
fork and must not be mixed with the upstream release channel.

## Licensing and attribution

3x-ui remains licensed under GPL-3.0. Upstream copyright and license notices are
retained. Changes made by SynexIM are documented in the repository history and
release notes.

## Release channels

- Stable releases use a `vMAJOR.MINOR.PATCH` tag and a GitHub Release.
- Candidate releases use a `-rc.N` suffix and are never marked as the latest stable release.
- `dev-latest` is a rolling development channel and is not suitable for production.

The complete release procedure is documented in [RELEASE.md](RELEASE.md).
