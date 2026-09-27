# SynexIM 3x-ui fork

This repository is an open-source fork of [MHSanaei/3x-ui](https://github.com/MHSanaei/3x-ui), maintained by SynexIM.

## What is different

- Mixed clients and native Xray hot reload
- Outbounds and routing rules as single, addressable objects
- Namespace-scoped API tokens, so an automation and an operator share one panel
- Per-client and node-level traffic shaping
- Per-client fixed egress selection via Xray `egress_tag`
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
- **`ListRuleFull` once killed the core; fixed in our fork and now in use.**
  `ListRule` built each `Route` with a nil embedded `routing.Context`, and
  `ListRuleFull` then called the promoted `GetUser()` on it — a nil-interface
  method call that segfaulted the whole process, so the panel had to list
  runtime rules tag-deep with `ListRule`. Fixed in xray-core at `7300d185`
  (`Route` answers those two from the rule's own conditions when there is no
  live context) and pinned here since
  `v0.0.0-20260825234629-7300d185bb8a`. The panel now calls `ListRuleFull`, and
  a real-core test asserts a rule's `user` survives the round trip — reverting
  the call makes it fail with an empty `User`.

## Three-tier shaping, credential rotation and derived Shadowsocks keys

Panel side of the dedicated-line contract; the shaping algorithm itself lives in
the SynexIM xray-core (`common/protocol/tier_shaper.go`).

- **Client fields** (`clients` table, `/clients/add`, `/clients/runtime/:email`,
  `GET /clients/get/:email`): `upload_bandwidth_bps` / `download_bandwidth_bps`
  are the standard rate; `burst_bps`, `burst_credit_bytes`, `sustained_bps`,
  `sustained_after_seconds` add the burst and sustained tiers (0 = tier off,
  burst >= standard >= sustained). They are emitted to the core as
  `burst_bit_per_sec`, `burst_credit_bytes`, `sustained_bit_per_sec`,
  `sustained_after_seconds`; a runtime patch re-adds the user and the core swaps
  the policy under established connections.
- **`POST /clients/:email/credentials`** `{id?, password?, mixed_user?, mixed_pass?}`
  rotates credentials on every attached inbound and hot-applies them. `password`
  is also the Hysteria2 `auth`; empty `mixed_user` / `mixed_pass` fall back to
  email / password. Failures: `409 CLIENT_CREDENTIAL_CONFLICT` (another client on
  the same inbound already authenticates with that UUID, Hysteria2 auth,
  Shadowsocks password or Mixed login), `422 CLIENT_CREDENTIAL_INVALID`.
- **Mixed login**: Mixed accounts now carry `email` next to `user`/`pass`, so a
  login that differs from the email still shares shaping and stats with the
  client's other inbounds, also after a core restart.
- **Shadowsocks-2022 key derivation** (`model.ShadowsocksClientKey`): one logical
  client has one password, but a 2022 inbound needs a base64 key of the cipher's
  length. For a `2022-blake3-*` inbound the panel uses the password unchanged if
  it already decodes to exactly 16 bytes (`aes-128-gcm`) or 32 bytes (others);
  otherwise it uses
  `base64(sha256("ss2022-client/" + inboundTag + "/" + password)[:N])`.
  The core user, share links, Clash and JSON subscriptions all go through that
  one function. Legacy (non-2022) Shadowsocks keeps the raw password.

## Upstream merge 2026-09 (MHSanaei/3x-ui v3.8.5)

Merged on top of the fork's normalized client authority (the clients /
client_inbounds tables are the only source of client membership; inbound
settings JSON never holds clients) and its hot-apply red line (a client write is
applied to the running core or fails; it never schedules a core restart). The
fork's client and inbound service files were kept, and upstream v3.8.5 behaviour
was re-implemented on that model: TLS certificate completeness on inbound save,
Hysteria/TUIC client validation, AmneziaWG settings and relay-port guards, port
conflicts checked inside the serialized writer and on enable, node-eligible
protocols, MTProto custom share address as a managed host, sub-balancer cleanup
on inbound delete, fanned-out node pushes with deadlines (traffic-driven node
pushes run after commit, off the serial writer), case-insensitive email identity,
rename onto a same-identity email, TUIC copy credentials, BulkAdjust adTag/limitHwid,
EffectiveFlow on bulk attach, explicit `enable:false` kept on create, keepAlive
preserved when omitted, cross-inbound tunnel address uniqueness, tombstone
withdrawal on re-create, adopted node activation expiry latching the disable.
Tests that asserted the settings JSON as the storage location were rewritten to
read the normalized tables; the behaviour they pin is unchanged.

The core is SynexIM/xray-core on the v26.9.9 baseline, so udphop and the v26.9
migration expectations hold.

Upstream tests still skipped, each with its reason in the `t.Skip` text (12):

| Reason | Tests |
|---|---|
| Red line: client writes hot-apply or fail, never restart the core | `Test{Update,Delete,Detach}HandlerFlagsRestartOnPartialApply`, `TestManualClientDisableHonoursRestartSetting`, `TestTrafficDisableNodeRestartDoesNotBlockTrafficPoll` |
| One tunnel keypair/address per client in the clients table; upstream keeps per-inbound peer keys in settings JSON | `TestInboundLinks_PreservesPerInboundWireGuardIdentity`, `TestUpdateDoesNotBroadcastPeerCredentialsAcrossTunnelInbounds` |
| Client CRUD never writes or parses inbound settings JSON, so the injected failure / malformed blob cannot occur | `TestNodeBulkAdjustDoesNotPushBeforeFailedCommit`, `TestNodeBulkDeleteDoesNotPushBeforeFailedCommit`, `TestNodeBulkDeleteMalformedSettingsWithdrawsTombstone` |
| Membership comes from the normalized tables, not an unsaved inbound blob | `TestGetInboundClientsForUsesProvidedInbound` |
| Import succeeds only after the real core starts and answers its API; the shell stub cannot | `TestImportDBSchedulesPanelRestart` |

## Licensing and attribution

3x-ui remains licensed under GPL-3.0. Upstream copyright and license notices are
retained. Changes made by SynexIM are documented in the repository history and
release notes.

## Release channels

- Stable releases use a `vMAJOR.MINOR.PATCH` tag and a GitHub Release.
- Candidate releases use a `-rc.N` suffix and are never marked as the latest stable release.
- `dev-latest` is a rolling development channel and is not suitable for production.

The complete release procedure is documented in [RELEASE.md](RELEASE.md).
