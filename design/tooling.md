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

- **Author signing** — author keys live with authors, never in CI
  ([spec/feeds.md §2](../spec/feeds.md#2-authors-role)); the spec says `publish` *verifies*
  author signatures, so there must be an author-side command that produces
  them.
- **Unpublish** — removing the item's index entry
  ([spec/feeds.md §1.3](../spec/feeds.md#13-ordering-updates-and-withdrawal)), but a dedicated
  command is what a publisher will reach for.
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
| **Publisher / operator** | master (offline) | ceremony machine | `init`, `channel add/remove`, `author add/revoke`, `pattern add/remove`, `company set`, `rotate-root`, `validate`, `keys generate/export` | `Publisher.*` master-path ops, `ceremony.Stage` |
| **Channel publisher** | channel key (+ ops) | CI / pipeline | `publish`, `item withdraw`, `channel key rotate/revoke` (apply side), `ceremony apply` | `Publisher.Publish` etc., `ceremony.Apply` |
| **Ops** | online ops key | cron / CI | `refresh-timestamp` | `Publisher.RefreshTimestamp` |
| **Author** | author key | author's own machine | `item sign` | `feed.SignItem` |
| **Feed engine** | private-feed engine key | eshop backend / logistics partner | `private-feed new/update/expire` (or direct SDK call) | `privatefeed.*` |
| **Deployer** | none | CI / ops | `deploy <backend>` | `deploy.Deployer` |
| **Validator** | none | CI / anyone | `validate` | `Publisher.Validate` |

Key custody split stays as the spec mandates: master offline, author keys
never in CI, channel key in the pipeline, ops key on a cron host. The tool
enforces the *overlaps* it can see (same key as channel role key and author
key → refuse, [spec/feeds.md §2](../spec/feeds.md#2-authors-role)) and never holds keys it
should not (publish never touches master; item signing never touches
channel keys).

---

## 3. Multi-role workflow (the shape that actually works)

### 3.1 Principles

1. **The repo is the only shared state.** The TUF repo (metadata + feeds) is
   what flows between roles; it is *public* and *verifiable*, so every role
   can fetch it (git checkout, rsync, or `pub pull` from the deployed base)
   and every role that reads it verifies the chain first. Nothing else is
   shared.
2. **Keys never leave their role.** No single workspace holds all keys.
   Each role's machine has only its own keys; a role that does not hold a
   key simply cannot run the commands that need it — the CLI fails fast with
   a typed `missing key: channel key "security" — run this on the pipeline
   machine` error instead of producing half-signed metadata.
3. **Every handoff is a signed artifact, verified at the boundary.** Author
   → pipeline: signed item. Operator → pipeline: ceremony bundle
   (master-signed metadata). Operator → author: key export (encrypted).
   Each recipient verifies before trusting; the protocol's "binary
   verification" applies to tooling handoffs too, not just to client
   fetches.
4. **Read-verify-apply-verify.** Every command loads the repo, verifies the
   metadata chain it read, applies the change in memory, verifies the
   resulting repo in full, then swaps atomically.

### 3.2 Role contexts (what each machine has)

```
operator (ceremony machine)        CI / pipeline                  author's machine
┌──────────────────────────┐      ┌──────────────────────────┐   ┌────────────────┐
│ keys/  master (+ ops)    │      │ keys/  channel key(s)    │   │ keys/ author   │
│ repo/  checkout          │      │        + ops             │   │                │
│ anchor/ root.json+chain  │      │ repo/  checkout          │   │ (no repo, no   │
│ config: role=operator    │      │ anchor/ read-only copy   │   │  anchor —      │
└──────────────────────────┘      │ config: role=ci          │   │  item sign     │
          │                       └──────────────────────────┘   │  needs only    │
          │  git: repo + anchor                                    │  the keystore)│
          └───────────────────────────────────────────────────────┘
```

- **Author** is the minimal case: no repo, no anchor, no metadata. `pub
  item sign --channel security --file draft.json --out signed.json` needs
  the author key, the channel name, and the draft. It does not even need to
  know whether the channel is authored — `publish` decides that.
- **CI** needs the repo (latest `targets.json` → author-role requirements;
  feed → append) and its keys. It gets the repo via git (default) or
  `pub pull`; it **pulls before every publish** so it never acts on stale
  authorization.
- **Operator** needs a repo checkout only for version consistency; the
  anchor dir only for root ceremonies and `validate`.

### 3.3 Handoff artifacts

| Artifact | Producer → Consumer | Content | Consumer verifies |
|---|---|---|---|
| **signed item** | author → pipeline (or author → author for thresholds) | full item object with `sig` | authors-role threshold against `targets.json` delegation, path/id consistency, known-keyid validity |
| **key export bundle** | operator → author / operator → CI | encrypted keystore entries, role-tagged | passphrase; role lands in the right store |
| **public key card** | author/CI → operator | public key object + keyid only | keyid = SHA-256 of canonical key object |
| **ceremony bundle** | operator → CI/ops | master-signed `targets.json` (or new root) + pending steps | master signature, version monotonicity, delegation invariants |
| **deploy bundle** | anyone → static host | the two dirs (anchor/, repo/) | `validate` before upload |

### 3.4 Author flow (incl. thresholds)

```
draft.json (id, title, content, dates, tags, [attachments])   author's machine
   │  pub item sign --channel security --keyid <alice>
   ▼
signed.json  (id, …, sig:[alice])
   │  → handed to co-author (same bytes!)                  bob's machine
   ▼  pub item sign --file signed.json (adds bob's sig) — OLPC is
   │  deterministic and the sig field is excluded from the
   │  signed bytes, so threshold signatures accumulate
   ▼
signed.json (alice+bob)  →  PR / upload  →  CI
```

Key points: the signed object is fixed at first signing (`id`,
`title`, content, dates, tags, attachments included — per the OLPC rule in
[spec/feeds.md §1.2](../spec/feeds.md#12-signing-and-verification)), so co-authors re-sign the *same
file*; if the draft has no `id`, `item sign` derives one deterministically
(content hash), so co-authors agree. The pipeline never sees author private
keys; it only verifies.

### 3.5 Ceremony flow (two modes, same command)

**Single-step (default, operator holds master + ops backup):**
`pub channel add --name security` on the ceremony machine does everything:
master signs `targets.json`, channel role metadata is created, ops re-signs
snapshot+timestamp, `validate`, swap, commit. Small publishers run this —
it's the spec's "one tool" promise.

**Strict two-step (master and ops keys never co-locate):**
```
operator (offline)                          CI / ops machine
  pub channel add --name security --stage bundle/
      │  writes bundle/: targets.json (master-signed)
      │  + step manifest (create role metadata for "security",
      │    delegation invariants, expected keyids)          ┌────────────────────┐
      └──────────────────────────────────────────────────▶ │ pub ceremony apply │
                                                             │  verify master sig │
        bundle travels by git, USB, or a                 ─▶ │  + version mono    │
        paste in an air-gapped setup                        │  + invariants      │
                                                             │  create channels.  │
                                                             │  security.json     │
                                                             │  (channel key)     │
                                                             │  re-sign snapshot  │
                                                             │  +timestamp (ops)  │
                                                             │  validate → swap   │
                                                             └────────────────────┘
```

Which commands need which ceremony mode:

| Ceremony | Keys (single-step) | Bundle steps on apply |
|---|---|---|
| `channel add` | master + ops + channel key | verify, create role metadata + empty feed (channel key), snapshot/timestamp |
| `channel remove` | master + ops | verify, drop role metadata/target from snapshot, snapshot/timestamp |
| `author add/revoke` | master + ops | verify, authors role metadata re-sign (authors), snapshot/timestamp |
| `pattern add/remove` | master + ops | verify, snapshot/timestamp |
| `company set` | master + ops | verify, snapshot/timestamp |
| `channel key rotate` | master + ops + channel key | verify, re-sign role metadata with old+new (overlap), snapshot/timestamp |
| `channel key revoke` | master + ops | verify, snapshot/timestamp |
| `rotate-root` | master only | no bundle needed (anchor only) — unless the ops key rotates too, then a bundle whose apply re-signs snapshot/timestamp with the new ops key |

### 3.6 CI publish flow

```
git pull (repo+anchor)          # fresh authorization
pub validate                    # cheap pre-check
pub publish --channel security --file signed.json
    # verify authors-role threshold (or channel-key signing in
    # single-author channels), add the item + index entry,
    # re-sign channels.security.json (channel key), snapshot+timestamp
    # (ops), validate, swap, commit
pub deploy s3 --bucket …        # repo base; anchor separately (or same job)
```

The repo round-trips through git between roles; the SDK never transports —
transport (git, rsync, CDN push) is the operator's existing tooling, which
is also why `deploy` stays a thin interface.

### 3.7 CI as the server (the standard server–client shape, without the server)

"A server that collects new items, re-signs channels and publishes the repo
to the CDN" is the right topology — it is exactly what the web app will be,
and it is RSTUF's model. The question is only what *runs* it in v1:

- **Running a bespoke service in v1 is too heavy.** It would be an
  application server + database, against the protocol's core value
  proposition: passive static files, no application server, weekend deploy
  ([`why.md` §4.14](why.md)). The protocol does not require the publisher
  side to be trusted more than CI anyway — the repo host is already
  untrusted by design, so a "publisher server" adds orchestration, not
  security.
- **CI is the server.** Authors push `signed.json` (PR/upload) → CI runs
  `pub publish` → `pub validate` → `pub deploy` → CDN. Same client–server
  topology, zero new infrastructure, and the channel publisher / ops / deploy
  roles are just CI jobs with scoped secrets.
- **The web app is the server later.** Its backend consumes the same SDK
  (`Publisher.Publish`, `ceremony.Apply`, `Deployer`, `KeyStore`), so nothing
  in the SDK assumes a CLI. If the app ever needs RSTUF-style HTTP
  semantics, the SDK's interfaces map 1:1 onto RSTUF's layers (repository
  service ↔ `Publisher`+`Repo`, offline signer ↔ `ceremony`/`KeyStore`,
  storage backend ↔ `Deployer`).

### 3.8 Standard TUF tooling — what exists, what we adopt, what we don't

Recorded so the decision is not re-litigated. TUF standardizes the *role
model* (root/targets/snapshot/timestamp, delegated roles, thresholds) — no
publishing *workflow*. The ecosystem's workflow tooling:

| Tool | Shape | Why not adopted as our engine | What we borrow |
|---|---|---|---|
| **RSTUF** (official repository service) | HTTP API + worker + PostgreSQL + storage backends; offline metadata-update ceremony; delegated target roles (since 1.0.0); per-target `custom` | Python service + DB to operate (vs. no-app-server posture); key/threshold/expiry ceremony is root-shaped; our load-bearing master-signed `custom` (`repo_base`, `mode`, `company_name`, `logo`, `channels`, `private_feed_patterns`) is not expressible through its ceremony | Its *workflow shape*: repository service computes updates, offline signers sign, publish to storage — mirrored by `Publisher` + `ceremony` + `Deployer`; per-target `custom` (not used for public items in v1) |
| **tuf-on-ci** (TUF repo + signing tool on GitHub Actions) | Guided signing events; delegations with thresholds; hardware/Sigstore/cloud signers; automated online signing | Built for signing *events* on trust-root-style repos (Sigstore), not per-publish delegated-role re-signing by channel keys + our custom fields; Python/GHA-shaped | The "signing event" model = our `ceremony stage/apply`; signer abstraction = our Sigstore `signature.Signer` adapter |
| **go-tuf v1 CLI** | `tuf init/gen-key/add-key/sign/commit`; `keys/` + `staged/` + `repository/` dirs; `--consistent-snapshot=false` | No delegated roles; no custom fields; v1; v2 (which we use) is the metadata library | The file-based offline-root + staged-commit pattern (Sigstore runs it in production) — our workspace/atomic-swap is the same shape; we use go-tuf v2's metadata package as the crypto core |

Adopted: the TUF **role model** via **go-tuf v2** (metadata sign/verify) and
**Sigstore's signer interface** — the same foundations RSTUF/tuf-on-ci
build on, so the door stays open (a future `Deployer`/`Repo` adapter could
push into an RSTUF instance without protocol changes). Not adopted: any
whole-workflow tool — none covers per-channel publish cadence with channel
keys in CI, OLPC item signing, private capability feeds, or the
master-signed `custom` payload, and all are Python/service-shaped against
the project's Go + static-file posture.

---

## 4. Architecture

```
┌─────────────────────────── SDK (library, keryx/sdk) ──────────────────────────┐
│                                                                              │
│  keys/        KeyStore iface, ed25519, keyid, backup/export(role-tagged),     │
│               signer adapters (Sigstore → KMS/hardware later)                 │
│  repo/        Repo iface (Read/Write/List), DirRepo; the shared state         │
│  tuf/         go-tuf v2 wrapper: root/targets/snapshot/timestamp + channel    │
│               role metadata; sign, verify, rotate; custom-field accessors     │
│  feed/        item types; build/sign/verify (OLPC); attachment hashes   │
│  privatefeed/ capability-feed engine: token, build, sign, verify, expire      │
│  join/        join URL + QR payload encode/decode/validate; QR image          │
│  publisher/   high-level ops = the CLI verbs as library functions             │
│  ceremony/    stage/apply bundle types + verification                         │
│  deploy/      Deployer iface: local dir, S3-compatible                        │
│  config/      role-scoped workspace config (paths, origin, base, role)        │
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
2. **Everything through interfaces.** `keys.KeyStore`, `repo.Repo`
   (dir-backed now; object-store/DB-backed by the web app later),
   `deploy.Deployer`, a `Clock`. The web app can back these with its own
   storage without touching protocol code.
3. **The repo is the only state.** No hidden database: versions, item
   content, keyids, and `custom` all derive from the metadata/item files.
   Single-writer per repo — documented, advisory-locked in the CLI; the web
   app serializes per repo on its side.
4. **Deterministic bytes.** Same keys + same inputs + same version numbers →
   identical output (fixed serialization, OLPC for items, go-tuf canonical
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
   own extension point); item signing = OLPC canonical JSON + Ed25519 (the
   spec's ~50 lines, kept in `feed/` and shared with the app's logic).

**What we reuse from `demo-tool/`:** keyid-after-label handling, go-tuf
root/targets/snapshot/timestamp + delegated-role construction, OLPC
sign/verify (item + whole-document), private-feed build/verify, repo
`verify`. `demo-tool/` stays untouched for now; after the SDK is stable it
can be re-ported onto it (or retired) in a later pass.

---

## 5. Command surface (`pub`, cobra)

Command names follow the normative contract in spec/clients.md §2; additions
are marked **(new)**. `--stage`/`apply` implement the two-step ceremonies
(§3.5); without `--stage` the same commands run single-step when the
keystore holds the required keys.

```
pub init --domain company.example --name "ACME s.r.o."
         [--base https://cdn.example.com/keryx] [--logo URL]
         [--mode full] [--workspace .keryx]
pub keys list | generate <name> [--role master|ops|channel|author|engine]
pub keys export [--role …] [--name …] [--public] --out bundle   # (new) role-tagged, encrypted
pub keys import --file bundle                                    # (new)
pub channel add <name> --display-name … [--description …] [--keyid …] [--stage out/]
pub channel set --channel <name> [--display-name …] [--description …] [--stage out/]  # (new)
pub channel remove <name> [--stage out/]
pub channel list
pub channel key rotate <name> [--announce-next-key] [--stage out/]
pub channel key revoke <name> [--keyid …] [--reissue] [--stage out/]
pub author add --channel <name> --keyid <id> [--stage out/]
pub author revoke --channel <name> --keyid <id> [--stage out/]
pub author list
pub pattern add --channel <name> --pattern URL --keyid <id> [--stage out/]   # master
pub pattern remove --channel <name> [--stage out/]
pub ceremony apply --bundle out/                                 # (new) verify + finish
pub item sign --channel <name> --file draft.json --out signed.json          # (new) author side
pub publish --channel <name> --file signed.json
pub item unpublish --channel <name> --id <id>                       # (new)
pub refresh-timestamp [--expires 48h]                              # the cron line
pub company set [--name …] [--logo URL] [--stage out/]             # identity ceremony (master)
pub rotate-root [--announce-next-key]
pub private-feed new|update|expire …                               # (new) engine side
pub validate [--strict]
pub join-url --channels a,b [--private-feed URL …] [--out qr.png]  # (new; split from qr)
pub qr --channels a,b [--private-feed URL …] --out qr.png
pub pull [--base URL]                                              # (new) fetch+verify repo state
pub deploy local --target /var/www/keryx | pub deploy s3 --bucket … --prefix …
```

**Semantics worth calling out (all spec-grounded):**

- `init` generates master + ops keys (channel/author/engine keys on demand),
  builds the full 4-role repo, writes the anchor dir and repo dir, prints
  the one-time backup; `--logo` fetches once and records `logo_sha256`.
- `item sign` runs on the author's machine with the author's keystore only:
  takes a draft (id, title, content, dates, tags, attachments), fills
  nothing extra, signs the OLPC canonical bytes with
  the author key, writes `signed.json`. Re-signing an already-signed
  file *adds* a signature (threshold accumulation, §3.4). It never touches
  channel keys and never needs the repo.
- `publish` = pull-fresh repo → verify input item (authors-role threshold
  against the `channels.<name>.authors` delegation; refuses unsigned items
  there; refuses unknown/unauthorized keyids) → in a single-author channel,
  sign the item with the channel key → write the item file / replace in
  place on known `(channel, id)` → update the index entry → re-sign
  `channels.<name>.json`
  (channel key, version+1) → `snapshot.json` (ops) → `timestamp.json` (ops)
  → verify → swap. **No master involvement.**
- `ceremony apply` is the only command that merges master-signed metadata
  into a live repo: it verifies the bundle's master signature, version
  monotonicity, and delegation invariants before touching anything, then
  performs the pending steps (channel role metadata creation, re-signing,
  snapshot/timestamp) with the keys it holds.
- `channel add` = targets.json v+1 (master): delegation
  (`channels.<name>`, terminating, `paths: ["channels/<name>/*"]`) + role
  metadata + `custom.channels` entry + snapshot/timestamp (ops).
  `channel remove` = drop delegation/role metadata/display entry from
  snapshot;
  local history is the client's to keep.
- `author add/revoke` and `pattern add/remove` = targets.json v+1 (master)
  + snapshot/timestamp (`author add/revoke` also re-signs the authors role
  metadata with the current author keys). Rotations use the overlap protocol
  ([spec/repository.md §5](../spec/repository.md)); the tool prints the
  re-sign reminder (`item sign` with the new key during the overlap).
- `rotate-root` = root.json v+1 signed by previous root keys per threshold;
  writes `root.json` **and** `N.root.json` to the anchor dir only
  ([spec/repository.md §1](../spec/repository.md)); never touches the repo
  base.
- `pull` fetches the repo (and optionally the anchor) from the deployed
  base/join origin and verifies the metadata chain — useful for the
  operator's ceremonies and CI when git is not the transport.
- `validate` = the full check: root chain from the anchor; timestamp →
  snapshot → targets → per-channel role metadata; delegation invariants
  (role name `channels.<name>`, terminating, paths inside namespace,
  threshold/keyids); authors-role invariants (same paths, no targets,
  non-terminating, key separation from the channel role);
  snapshot/timestamp cross-references; item target
  length + sha256; every item (`id` == path segment; authors-role
  threshold strict, single-author channel-key strict; known-keyid failures
  reject); channel
  name charset; author/channel key separation; `logo_sha256` freshness
  (warning, not error); pattern glob integrity. `--strict` also verifies
  that no signature was written without verification.
- `qr` validates the payload before encoding: ≤ ~512 encoded bytes, ≤ 2
  private feeds, channel charset, HTTPS origin, base64url no padding, no
  identity in the payload; emits PNG (pure-Go QR encoder). `join-url` prints
  the URL for terminals/CI without a QR.

**Fail-safes from spec/clients.md §2 are implemented in the SDK**, not
duplicated in each command: validate-before-write, deterministic output,
version/expires bumped only on change, refuse revoked key, refuse
unauthorized keyid, refuse unsigned item or below authors-role threshold,
refuse channel
role key == author key, reject non-`[a-z0-9-_]+` channel names, always
`channels.<channel>` role names, recompute `logo_sha256` on logo change,
verify every signature written.

---

## 6. SDK package contracts (sketch)

```go
// keys
type KeyStore interface {
    List(ctx context.Context) ([]KeyInfo, error)
    Get(ctx context.Context, id string) (signature.Signer, error) // sigstore iface → KMS/hardware later
    Add(ctx context.Context, info KeyInfo, signer signature.Signer) error
    Remove(ctx context.Context, id string) error
}
type KeyInfo struct { ID, Name, Role string; Key *metadata.Key }
func Export(ctx, KeyStore, ExportParams) ([]byte, error)   // role-tagged, encrypted (age)
func Import(ctx, KeyStore, []byte) error
type ErrMissingKey struct { Role, KeyID string }           // typed; CLI renders the hint

// repo — the shared state (dir-backed now, object-store/DB later)
type Repo interface {
    Read(ctx context.Context, path string) ([]byte, error)
    Write(ctx context.Context, path string, data []byte) error
    List(ctx context.Context, prefix string) ([]string, error)
}
type DirRepo struct{ Root string }

// publisher — one method per CLI verb; all take ctx, all return values
type Publisher struct { Repo repo.Repo; Anchor repo.Repo; Keys keys.KeyStore; Clock func() time.Time }
func (p *Publisher) Init(ctx, InitParams) (Result, error)
func (p *Publisher) Publish(ctx, PublishParams) (Result, error)
func (p *Publisher) Unpublish(ctx, string, string) (Result, error)
func (p *Publisher) ChannelAdd(ctx, ChannelSpec) (ceremony.Step, error)      // stage or full
func (p *Publisher) ChannelRemove(ctx, string) (ceremony.Step, error)
func (p *Publisher) AuthorAdd(ctx, string, string) (ceremony.Step, error)
func (p *Publisher) PatternAdd(ctx, PatternSpec) (ceremony.Step, error)
func (p *Publisher) RotateChannelKey(ctx, string, ...) (ceremony.Step, error)
func (p *Publisher) RotateRoot(ctx, RotateRootParams) (Result, error)
func (p *Publisher) RefreshTimestamp(ctx, time.Duration) error
func (p *Publisher) Validate(ctx, ValidateParams) (Report, error)

// ceremony — the operator→CI handoff
type Bundle struct { Targets *metadata.Metadata[metadata.TargetsType]; Steps []Step; … }
func Stage(ctx, Publisher, Step) (*Bundle, error)   // master signs; never touches ops keys
func Apply(ctx, Publisher, *Bundle) (Result, error) // verify master sig + invariants, run steps, ops re-sign

// feed — item + whole-document signing/verification (OLPC), shared with the app
func SignItem(item map[string]any, signer signature.Signer, keyid string) error
func VerifyItem(item map[string]any, authorKeys keys.KeySet, authorThreshold int, channelKeys keys.KeySet, channelThreshold int) error

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
- `KeyStore`/`Repo`/`Deployer` interfaces → the app swaps in Vault-backed
  signers, DB/object-store repos, and its own deploy path; private feeds get
  created by the eshop backend via `privatefeed.Build` with its own engine
  key. The web app's users map onto the same roles (admin=operator,
  author=author, publish=CI), so the multi-role design of §3 is exactly what
  the app will need — one code path, two front ends.
- Deterministic + typed results (`Report`, `Result`) → JSON responses, CI
  gates, and the app's audit log without parsing stdout.
- Single-writer documented → the app serializes per-repo (DB lock) — the
  protocol layer never needs to know.

---

## 7. Phased delivery (implementation order)

1. **Foundation** — `sdk/` module scaffold; `keys` (ed25519, keyid,
   age-encrypted file store, export/import, `ErrMissingKey`); `repo`
   (interface + DirRepo, atomic swap); `tuf` (init/build/sign/verify via
   go-tuf v2, custom accessors); `feed` (types, OLPC sign/verify,
   attachment hashes);
   CLI skeleton: `init`, `publish`, `validate`. Verification: demo-repo
   built by SDK validates clean; golden/determinism tests; OLPC + keyid unit
   tests.
2. **Lifecycle + multi-role** — `channel add/remove`, `author add/revoke`,
   `pattern add/remove`, `item sign` (incl. threshold accumulation, no-repo
   mode), `item unpublish`, `channel key rotate/revoke`, `company set`,
   `rotate-root` (+ chain-walk test), `refresh-timestamp`; `ceremony`
   (stage/apply, strict two-step path); `privatefeed` package + CLI; keys
   export/import; fail-safe unit tests (refusals); role-context e2e tests
   (three machines simulated: author signs → CI publishes → operator
   ceremony staged → CI applies → validate).
3. **Surface** — `join-url`/`qr` (+ payload validation tests), `pull`,
   `deploy` (local + S3), `--json` output mode, e2e CLI tests
   (init→publish→validate→deploy), README for `pub`.

Non-goals for v1: lite mode, mirrors, KMS/hardware backends (interface is
there, no impl), the web app itself, push, email bridge.

---

## 8. Open questions (to settle during Phase 1)

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
6. **Ceremony transport**: git (default), USB/air-gap (bundle), or
   `pub pull` from the deployed base — the SDK is transport-agnostic; the
   doc assumes git for the common case. Confirm.
