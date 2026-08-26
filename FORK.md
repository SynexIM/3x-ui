# SynexIM 3x-ui fork

This repository is an open-source fork of [MHSanaei/3x-ui](https://github.com/MHSanaei/3x-ui), maintained by SynexIM.

## What is different

- Mixed clients and native Xray hot reload
- Outbounds and routing rules as single, addressable objects
- Namespace-scoped API tokens, so an automation and an operator share one panel
- Per-client and node-level traffic shaping
- Declarative node configuration and bounded reconciliation
- Panel-side delivery links and runtime readback for API-driven fleet management

The upstream module path is intentionally preserved for source compatibility. The
release artifacts, updater, container images, and issue tracker belong to this
fork and must not be mixed with the upstream release channel.

## Would upstream want these? A per-feature assessment

The three changes below are not specific to how we deploy this panel. Every
3x-ui user runs into the same problem, so each is written up here with what an
upstream pull request would have to argue and what would make it hard.

### 1. Outbounds and routing rules as objects — worth proposing

**The problem it fixes, for anyone.** Today the only way to add one outbound is
`POST /panel/api/xray/update` with the whole config template. That means an API
caller has to reproduce every other object byte for byte, and any diff that
touches a section with no runtime reload API restarts the core and drops every
connection on the server. Xray-core has had `AddOutbound` / `RemoveOutbound` /
`AddRule` / `RemoveRule` for years; the gap has always been that the panel never
exposed them.

**Shape of the PR.** Eight endpoints (`/panel/api/outbounds`,
`/panel/api/routing/rules`), one service that treats the stored template as the
authority and reconciles the running core after the write, and a rollback when
the core refuses. It is additive: `POST /panel/api/xray/update` keeps working
unchanged, which is what makes it proposable at all.

**What a reviewer will push on.** (a) The persistence-versus-runtime authority
rule needs to be stated in the docs, not just in code comments — "saved" and "in
effect" are genuinely two different states and the response says which happened.
(b) The escaped-colon route (`/routing/rules\:batch`) is a gin-ism; upstream may
prefer `/routing/rules/batch`. (c) We would have to carry the acceptance test,
which needs a real xray binary, and upstream CI has no such job today.

**Verdict: propose it.** The value to a plain 3x-ui user is immediate — editing
one outbound stops costing every connection on the box.

### 2. Namespace-scoped API tokens — worth proposing, needs a smaller first cut

**The problem it fixes, for anyone.** Any 3x-ui token is a full-admin
credential. The moment a user points a bot, a billing hook or a CI job at their
panel, that thing can delete every inbound on it. Declaring the prefixes a token
owns confines it, and it is what lets a person keep editing a panel some
automation also writes to — instead of the panel locking itself.

**What a reviewer will push on.** The enforcement middleware identifies objects
by walking the JSON body for `tag`, `ruleTag` and `email`. That is deliberately
broad, and its most arguable rule is that a mutating request naming no object at
all is refused: correct, but it means a scoped token cannot call
`/panel/api/setting/*` or the backup endpoints. A first PR might scope only the
inbound/client/outbound/routing surface and leave everything else unrestricted,
then tighten later.

**Verdict: propose it,** starting from the smaller surface.

### 3. Read-only runtime page — worth proposing, smallest of the three

**The problem it fixes, for anyone.** Every page in the panel shows what was
saved. Nothing showed what the core actually loaded, so an inbound that is
enabled, stored and simply absent from the running core looks perfectly healthy.
`GET /panel/api/runtime` asks the core and shows the gap.

**What a reviewer will push on.** Very little. It is one read-only endpoint and
one page; the only judgement call is that a core that is up but does not answer
surfaces its gRPC error instead of rendering an empty list.

**Verdict: propose it.** Probably the easiest of the three to land.

## Known trade-offs carried in this fork

- **`PATCH /panel/api/outbounds/:tag` replaces rather than alters.** Xray's
  `AlterOutbound` takes an operation message, and the xray-core this repo pins
  ships no operation type that can change an outbound's server or protocol —
  only the shared rate limiter added by our own fork, which is not released yet.
  A patch is therefore `RemoveOutbound` + `AddOutbound`: still hot, still no
  process restart, but connections through that one outbound do end. Exit
  condition: an `AlterOutbound` operation that can replace a handler's settings
  in place.
- **Runtime rules are listed with `ListRule`, not `ListRuleFull`.** On the
  pinned xray-core, `ListRuleFull` **kills the core**: `ListRule` builds each
  `Route` with a nil embedded `routing.Context`, and `ListRuleFull` then calls
  the promoted `GetUser()` on it. Verified here against a real process — the
  pinned binary answers the call with a connection EOF because it has just
  segfaulted. Fixed in our xray-core fork at `7300d185` and verified against a
  binary built from it (the call then returns each rule's `user` and
  `inboundTag` correctly), but that commit has no released module version, so
  the panel keeps using `ListRule` until the dependency is bumped. Exit
  condition: bump `github.com/xtls/xray-core` past `7300d185`, then switch
  `XrayAPI.ListRules` back to `ListRuleFull` and widen `RuntimeRule`.

## Licensing and attribution

3x-ui remains licensed under GPL-3.0. Upstream copyright and license notices are
retained. Changes made by SynexIM are documented in the repository history and
release notes.

## Release channels

- Stable releases use a `vMAJOR.MINOR.PATCH` tag and a GitHub Release.
- Candidate releases use a `-rc.N` suffix and are never marked as the latest stable release.
- `dev-latest` is a rolling development channel and is not suitable for production.

The complete release procedure is documented in [RELEASE.md](RELEASE.md).
