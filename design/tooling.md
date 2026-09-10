# Keryx — Publisher Tooling (SDK + `pub` CLI)

**Status: informative — proposal.** Normative rules live in
[`spec/`](../spec/clients.md); the product view of the publisher side is
[`products.md`](products.md) §2. This document proposes *what we build* for
the publisher lifecycle: a Go SDK (library) plus a CLI (`pub`) that consumes
it, covering every role the specification defines. The web-based publishing
app is **out of scope here**, but it will consume the same SDK later — SDK
decisions are made with that consumer in mind (§6).

Scope: **full mode** only (lite mode is Phase 2 per
[`../ROADMAP.md`](../ROADMAP.md) and not part of this proposal's v1).

---

## 1. The problem, restated

The spec defines a publisher-side contract (spec/clients.md §2): one tool
with commands in business language, deterministic output, static output
(two directories), software keys with a one-time backup, and fail-safe
defaults. The spec also leaves gaps that the tooling must fill:

- **Editor signing** — editor keys live with editors, never in CI
  ([spec/feeds.md §2](../spec/feeds.md)); the spec says `publish` *verifies*
  editor signatures, so there must be an editor-side command that produces
  them.
- **Withdrawal** — `_sig.withdrawn: true` is just an update
  ([spec/feeds.md §1.2](../spec/feeds.md)), but a dedicated command is what
  a publisher will reach for.
- **Private feeds** — the engine side (create/update/expire per-order
  capability feeds, [spec/feeds.md §3](../spec/feeds.md)) is publisher
  infrastructure; the SDK must expose it, the CLI optionally.
- **Deployment** — the spec fixes the *outputs* (well-known anchor dir +
  repo base) but no command uploads them.
- **Identity ceremony** — `custom.company_name`/`logo`/`logo_sha256`
  changes are master-signed updates ([spec/repository.md §2](../spec/repository.md)).
- **State** — there is no server-side state, so the tool must derive
  everything (versions, feed content, keyids) from the repo itself and be
  deterministic, verifiable, and single-writer.

---

## 2. Roles → tooling matrix

| Role (spec) | Key held | Where it runs | Commands | SDK surface |
|---|---|---|---|---|
| **Publisher / operator** | master (offline) | workstation, ceremony | `init`, `channel add/remove`, `editor add/revoke`, `pattern add/remove`, `company set`, `rotate-root`, `validate`, `keys backup` | `Publisher.*` master-path ops |
| **Channel publisher** | channel key | CI / pipeline | `publish`, `item withdraw`, `channel key rotate/revoke` | `Publisher.Publish` etc. |
| **Ops** | online ops key | cron / CI | `refresh-timestamp` | `Publisher.RefreshTimestamp` |
| **Editor** | editor key | editor's own machine | `item sign` | `feed.SignItem` |
| **Feed engine** | private-feed engine key | eshop backend / logistics partner | `private-feed new/update/expire` (or direct SDK call) | `privatefeed.*` |
| **Deployer** | none | CI / ops | `deploy <backend>` | `deploy.Deployer` |
| **Validator** | none | CI / anyone | `validate` | `Publisher.Validate` |

Key custody split stays as the spec mandates: master offline, editor keys
never in CI, channel key in the pipeline, ops key on a cron host. The tool
enforces the *overlaps* it can see (same key as channel role key and editor
key → refuse, [spec/feeds.md §2](../spec/feeds.md)) and never holds keys it
should not (publish never touches master; item signing never touches
channel keys).

---

## 3. Architecture

```
┌─────────────────────────── SDK (library, keryx/sdk) ──────────────────────────┐
│                                                                              │
│  keys/        KeyStore iface, ed25519, keyid, backup/restore, signer adapters │
│  tuf/         go-tuf v2 wrapper: root/targets/snapshot/timestamp + channel    │
│               role metadata; sign, verify, rotate; custom-field accessors     │
│  feed/        JSON Feed types; item build/sign/verify (JCS); resources hashes │
│  privatefeed/ capability-feed engine: token, build, sign, verify, expire      │
│  join/        join URL + QR payload encode/decode/validate; QR image          │
│  publisher/   high-level ops = the CLI verbs as library functions             │
│  deploy/      Deployer iface: local dir, S3-compatible                        │
│  workspace/   dir layout (keys/ anchor/ repo/), load/save, atomic writes      │
└───────────────▲───────────────────────────────────────────────────────────────┘
                │  one module: github.com/v1b3coder/keryx/sdk
    ┌───────────┴────────────┐
    │ cmd/pub (cobra CLI)    │   ← the only place prompts/flags/exit/logging live
    └────────────────────────┘
                    ▲
                    │ later: web publishing app (Go backend) imports the same SDK
```

**Design principles (all in service of the future web app):**

1. **No `os.Exit`, no globals, no stdout in the SDK.** All operations are
   methods taking `context.Context`; results are returned as values; logging
   is `slog`-injectable (or absent). The CLI owns prompts and rendering.
2. **Everything through interfaces.** `keys.KeyStore` (file/keychain/KMS),
   `deploy.Deployer` (local/S3), a `Clock` (testability + determinism). The
   web app can back these with its own storage (DB, Vault, object storage)
   without touching the protocol code.
3. **The workspace is the repo.** No hidden database: versions, feed
   content, keyids, and `custom` all derive from the metadata/feed files on
   disk. `Workspace` = `keys/`, `anchor/` (well-known root dir), `repo/`
   (repo base). Single-writer per workspace — documented, enforced by
   advisory lock in the CLI; the web app serializes per repo on its side.
4. **Deterministic bytes.** Same keys + same inputs + same version numbers →
   identical output (fixed serialization, JCS for items, go-tuf canonical
   JSON for metadata). This is what makes hashing, `validate`, and CI
   reproducible.
5. **Atomic writes, verify-before-swap.** Every mutation is staged
   (in-memory or temp dir), the *resulting repo* is verified in full, then
   swapped in. A crash mid-publish never leaves a half-written repo
   (`consistent_snapshot: false` already makes mid-publish reads retry-safe
   for clients; the tool just must not make it *permanent*).
6. **No crypto reimplementation.** TUF metadata via **go-tuf v2**
   (canonicalization = securesystemslib OLPC); signing via the **Sigstore
   `signature.Signer`** interface (KMS/hardware later, additive — the spec's
   own extension point); item signing = JCS + Ed25519 (the spec's ~50 lines,
   kept in `feed/` and shared with the app's logic).

**What we reuse from `demo-tool/`:** keyid-after-label handling, go-tuf
root/targets/snapshot/timestamp + delegated-role construction, JCS
sign/verify (item + whole-document), private-feed build/verify, repo
`verify`. `demo-tool/` stays untouched for now; after the SDK is stable it
can be re-ported onto it (or retired) in a later pass.

---

## 4. Command surface (`pub`, cobra)

Command names follow the normative contract in spec/clients.md §2; additions
are marked **(new)**.

```
pub init --domain company.example --name "ACME s.r.o."
         [--base https://cdn.example.com/keryx] [--logo URL]
         [--mode full] [--workspace .keryx]
pub keys list | generate <name> [--role master|ops|channel|editor|engine] | export | import
pub channel add <name> --display-name … [--description …]
pub channel remove <name>
pub channel list
pub channel key rotate <name> [--announce-next-key]     # overlap, then drop
pub channel key revoke <name> [--keyid …] [--reissue]
pub editor add --channel <name> --keyid <id>
pub editor revoke --channel <name> --keyid <id>
pub editor list
pub pattern add --channel <name> --pattern URL --keyid <id>   # private-feed patterns (master)
pub pattern remove --channel <name>
pub item sign --channel <name> --file draft.json --out signed.json   # (new) editor side
pub publish --channel <name> --file signed.json [--no-channel-sig]
pub item withdraw --channel <name> --id <id>                       # (new)
pub refresh-timestamp [--expires 48h]                              # the cron line
pub company set [--name …] [--logo URL]                            # identity ceremony (master)
pub rotate-root [--announce-next-key]
pub private-feed new|update|expire …                               # (new) engine side
pub validate [--strict]
pub join-url --channels a,b [--private-feed URL …] [--out qr.png]  # (new; split from qr)
pub qr --channels a,b [--private-feed URL …] --out qr.png
pub deploy local --target /var/www/keryx | pub deploy s3 --bucket … --prefix …
```

**Semantics worth calling out (all spec-grounded):**

- `init` generates master + ops keys (channel/editor/engine keys on demand),
  builds the full 4-role repo, writes the anchor dir and repo dir, prints
  the one-time backup; `--logo` fetches once and records `logo_sha256`.
- `publish` = read repo → verify input item (editor-mode threshold against
  `custom.editor_mode.<channel>`; refuses unsigned items there; refuses
  unknown/unauthorized keyids) → optionally add channel-key signature
  (default mode: load-bearing attribution; editor mode: portability extra) →
  insert item (newest first) / replace in place on known `(channel, id)` →
  recompute feed bytes → re-sign `channels.<name>.json` (channel key,
  version+1) → `snapshot.json` (ops) → `timestamp.json` (ops) → verify →
  swap. **No master involvement.**
- `item sign` runs on the editor's machine with the editor's keystore: takes
  a draft (id, content, dates, tags, optional `_sig.resources` via
  `--pin URL=FILE`), fills `_sig.channel`/`_sig.withdrawn:false`, signs JCS
  with the editor key, writes `signed.json`. It **never** touches channel
  keys, and `publish` re-verifies (the two never trust each other).
- `channel add` = targets.json v+1 (master): delegation
  (`channels.<name>`, terminating, `paths: ["channels/<name>/*"]`) + key
  generation + empty feed + role metadata (channel key) + snapshot/timestamp
  (ops). `channel remove` = drop delegation/role metadata/target from
  snapshot; local history is the client's to keep.
- `editor add/revoke` and `pattern add/remove` = targets.json v+1 (master)
  + snapshot/timestamp. Rotations use the overlap protocol
  ([spec/repository.md §5](../spec/repository.md)); the tool prints the
  re-sign reminder (`item sign` with the new key during the overlap).
- `rotate-root` = root.json v+1 signed by previous root keys per threshold;
  writes `root.json` **and** `N.root.json` to the anchor dir only
  ([spec/repository.md §1](../spec/repository.md)); never touches the repo
  base.
- `validate` = the full check: root chain from the anchor; timestamp →
  snapshot → targets → per-channel role metadata; delegation invariants
  (role name `channels.<name>`, terminating, paths inside namespace,
  threshold/keyids); snapshot/timestamp cross-references; feed target
  length + sha256; every item (`_sig.channel` == feed path channel; editor
  mode strict, default attribution; known-keyid failures reject); channel
  name charset; editor/channel key separation; `logo_sha256` freshness
  (warning, not error); pattern glob integrity. `--strict` also verifies
  that no signature was written without verification.
- `qr` validates the payload before encoding: ≤ ~512 encoded bytes, ≤ 2
  private feeds, channel charset, HTTPS origin, base64url no padding, no
  identity in the payload; emits PNG (pure-Go QR encoder). `join-url` prints
  the URL for terminals/CI without a QR.

**Fail-safes from spec/clients.md §2 are implemented in the SDK**, not
duplicated in each command: validate-before-write, deterministic output,
version/expires bumped only on change, refuse revoked key, refuse
unauthorized keyid, refuse unsigned item in editor mode, refuse channel
role key == editor key, reject non-`[a-z0-9-_]+` channel names, always
`channels.<channel>` role names, recompute `logo_sha256` on logo change,
verify every signature written.

---

## 5. SDK package contracts (sketch)

```go
// keys
type KeyStore interface {
    List(ctx context.Context) ([]KeyInfo, error)
    Get(ctx context.Context, id string) (signature.Signer, error) // sigstore iface → KMS/hardware later
    Add(ctx context.Context, info KeyInfo, signer signature.Signer) error
    Remove(ctx context.Context, id string) error
}
type KeyInfo struct { ID, Name, Role string; Key *metadata.Key }

// publisher — one method per CLI verb; all take ctx, all return values
type Publisher struct { Workspace *Workspace; Keys keys.KeyStore; Clock func() time.Time }
func (p *Publisher) Init(ctx, InitParams) (Result, error)
func (p *Publisher) Publish(ctx, PublishParams) (Result, error)
func (p *Publisher) Withdraw(ctx, string, string) (Result, error)
func (p *Publisher) ChannelAdd(ctx, ChannelSpec) error
func (p *Publisher) ChannelRemove(ctx, string) error
func (p *Publisher) EditorAdd(ctx, string, string) error
func (p *Publisher) PatternAdd(ctx, PatternSpec) error
func (p *Publisher) RotateChannelKey(ctx, string, ...) error
func (p *Publisher) RotateRoot(ctx, RotateRootParams) error
func (p *Publisher) RefreshTimestamp(ctx, time.Duration) error
func (p *Publisher) Validate(ctx, ValidateParams) (Report, error)

// feed — item + whole-document signing/verification (JCS), shared with the app
func SignItem(item map[string]any, signer signature.Signer, keyid string) error
func VerifyItem(item map[string]any, mode VerificationMode, trusted keys.KeySet, threshold int) error

// privatefeed — the engine side
func NewToken() (string, error)                    // 128-bit CSPRNG, 22 chars base64url
func Build(ctx, Engine, PatternEntry, Order) (*Document, error)   // version 1, signs whole doc
func Update(ctx, Engine, prev *Document, items ...) (*Document, error) // version+1, expires refresh
func Expire(ctx, Engine, prev *Document) (*Document, error)

// deploy
type Deployer interface { Deploy(ctx context.Context, target Target, anchorDir, repoDir string) (Result, error) }
// impls: LocalDeployer (dir copy/rsync-compatible), S3Deployer (minio-go: AWS S3, R2, B2…)

// join
func BuildPayload(channels []string, private []string) ([]byte, error)
func JoinURL(origin string, payload []byte) (string, error)
func QR(url string, size int) ([]byte, error)
```

**Web-app-readiness checklist** (why these contracts, not `func main` glue):
- `context.Context` everywhere → HTTP handlers cancel cleanly.
- No prompt/exit/log assumptions → the app calls `Publisher.Publish` from a
  request handler; prompts live in the CLI only.
- KeyStore/Deployer interfaces → the app swaps in Vault-backed signers and
  object-storage deployers; private feeds get created by the eshop backend
  via `privatefeed.Build` with its own engine key.
- Deterministic + typed results (`Report`, `Result`) → JSON responses, CI
  gates, and the app's audit log without parsing stdout.
- Single-writer documented → the app serializes per-repo (DB lock) — the
  protocol layer never needs to know.

---

## 6. Phased delivery (implementation order)

1. **Foundation** — `sdk/` module scaffold; `keys` (ed25519, keyid, age-encrypted
   file store, backup export/import); `tuf` (init/build/sign/verify via
   go-tuf v2, custom accessors); `feed` (types, JCS sign/verify, resources);
   `workspace` (layout, atomic swap); CLI skeleton: `init`, `publish`,
   `validate`. Verification: demo-repo built by SDK validates clean;
   golden/determinism tests; JCS + keyid unit tests.
2. **Lifecycle** — `channel add/remove`, `editor add/revoke`, `pattern
   add/remove`, `item sign`, `item withdraw`, `channel key rotate/revoke`,
   `company set`, `rotate-root` (+ chain-walk test), `refresh-timestamp`;
   `privatefeed` package + CLI; fail-safe unit tests (refusals).
3. **Surface** — `join-url`/`qr` (+ payload validation tests), `deploy`
   (local + S3), `--json` output mode, e2e CLI tests (init→publish→validate→
   deploy), README for `pub`.

Non-goals for v1: lite mode, mirrors, KMS/hardware backends (interface is
there, no impl), the web app itself, push, email bridge.

---

## 7. Open questions (to settle during Phase 1)

1. **Module path**: `github.com/v1b3coder/keryx/sdk` — stable import path is
   what matters for the web app; rename-friendly (wire stays codename-neutral
   per [spec/core.md §1.1](../spec/core.md)).
2. **Encrypted keystore**: `age` (passphrase, audited) as the file format —
   alternatives: OS keychain backend behind the same interface (Phase 2).
3. **S3-compatible client**: minio-go (light, covers R2/B2/MinIO) vs
   aws-sdk-go-v2 (heavier, first-party). Leaning minio-go; both behind
   `Deployer`.
4. **Empty-channel feed**: a channel with zero items is allowed (empty
   `items: []`)? Spec doesn't forbid it; needed for `channel add` + publish
   later. Confirm.
5. **`--no-channel-sig` in default mode**: publish signing items with the
   channel key is optional per spec ([spec/feeds.md §1.2](../spec/feeds.md));
   we default to *sign* (portability, zero cost) but keep the flag.
