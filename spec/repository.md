# Keryx — Repository, Authorization, and Key Lifecycle

**Status: normative** (RFC 2119 keywords as in [`core.md`](core.md)).
Rationale is informative and lives in [`design/why.md`](../design/why.md).

Covers: the TUF repository layout and the root anchor (§1), `targets.json`
authorization (§2), channel role metadata (§3), channel lifecycle (§4), and
key rotation/revocation (§5).

---

## 1. Repository Layout

The repo is a standard TUF repository. The publisher generates it with
standard tooling (go-tuf / python-tuf / RSTUF); the app verifies it with a
standard client. Channels are native TUF delegated roles: each channel has
its own role metadata pinning its item file(s).

**Two locations, one trust chain — split by role.** The **root role** lives
at `https://<join-origin>/.well-known/keryx/` — `root.json` plus every
`N.root.json` — the only location-trusted URLs in the protocol and the
**exclusive** source of root metadata: the client's root chain walk runs
against the anchor, never against the repo base. Everything else lives in
the **repo base**, declared by the master-signed `custom.repo_base` in
root.json (optionally `custom.mirrors`, Phase 2). The base MAY be on
entirely different infrastructure (CDN, CMS, bucket) — it is
availability-only: all metadata fetched from it is verified against keys
fixed by a root the base cannot influence.

```
<join-origin>/.well-known/keryx/
├── root.json                     # root anchor; location-trusted
└── 1.root.json, 2.root.json, …   # all released root versions (TUF mandate);
                                  #  the ONLY source of root metadata (chain walk)

<base> = root.json custom.repo_base   # master-signed; may be another host/CDN
                                      #  contains NO root metadata
├── timestamp.json                # online ops key; short expires; cron-refreshed
├── snapshot.json                 # online ops key; pins all metadata files
├── targets.json                  # offline master; channel delegations + app data
├── channels.marketing.json       # K_marketing (delegated role `channels.marketing`):
│                                  # the item index — pins every published item
├── channels.security.json        # K_security (role `channels.security`)
├── channels.marketing.authors.json  # (authored channels only) author keys' role;
│                                  #  pins no items — authorizes item signing
├── channels/
│   ├── marketing/firmware-25-3.json  # one item = one TUF target, plain path
│   └── marketing/new-wallet-ui.json
└── media/…                       # referenced attachments (NOT targets; plain files)
```

**Roles and keys:**

| Role | Signer | Cadence | Contains |
|---|---|---|---|
| `root` | master key (offline) | rare (rotation only) | keys of all roles, thresholds, identity |
| `targets` | master key (offline) | rare (channel/author changes) | channel delegations (incl. authors roles), `custom` (company metadata, channel display metadata, private patterns) |
| `channels.<name>` (delegated) | **channel key** | **every publish of that channel** | that channel's item index: `{path → length, hashes}` |
| `channels.<name>.authors` (delegated, optional) | **author keys** | only when the author set changes | no item targets; authorizes item signing (keyids + threshold) |
| `snapshot` | online ops key | every metadata change | hashes/versions of all metadata files |
| `timestamp` | online ops key | cron (24–72 h) | snapshot version + hash; short `expires` |

- One online ops key MAY fill `snapshot` + `timestamp` (TUF allows one key in
  multiple roles). No master involvement on any publish.
- **`consistent_snapshot: false` (default).** Non-root metadata and target
  files are served at their plain paths; only root keeps versioned filenames
  (`N.root.json` — the TUF spec requires them regardless of the flag; the
  root chain walk fetches root by version number). Anti-rollback is
  unaffected: it comes from `version` fields inside signed metadata,
  snapshot/timestamp pinning, and client version memory — not filenames.
  What `false` gives up is publish atomicity: a client fetching mid-publish
  can get mismatched metadata/item bytes → hash verification fails → retry
  (a transient availability blip; TUF verification is what makes it safe).
  Chosen deliberately for this topology — one publisher, low publish
  frequency, small items — because it avoids unbounded accumulation of
  version-prefixed metadata and hash-named item copies on static hosts.
  A publisher MAY set
  `consistent_snapshot: true` (busy repos, aggressive CDN caching); standard
  clients handle either via the root flag. Versioned names stay unambiguous
  under the opt-in because `N` is numeric and channel role names are
  dot-namespaced: `N.channels.marketing.json` cannot be confused with
  `N.root.json` or with another channel's file.
- **Scope of compromise:** the online ops key is scoped to freshness
  (snapshot/timestamp pinning) — on its own its compromise allows
  rollback/freeze (availability harm) but never content authorization.
  **Combined with a content key, however, it completes the publishing
  chain**: the content key re-signs the channel role metadata, the ops key
  pins it, and the forged content verifies against the pinned root — full
  forgery for the affected channel(s), with no master key and no origin. A
  publisher who cares about that keeps the two apart (a threshold on the
  online key). Channel keys are
  scoped to their own channel: they pin and withdraw that channel's content
  (forging items requires the channel key too — or, in an authored channel,
  the author keys; see [feeds.md §2](feeds.md#2-authors-role)).
  **Publisher note (informative):** "availability harm" is not equally
  harmless on every channel. Freezing metadata suppresses *new* items, so on
  a channel carrying security warnings the harm is silence during exactly
  the incident the channel exists for. Publishers who run such a channel
  should pick their `timestamp` cadence (and `expires`) accordingly — the
  24–72 h default is aimed at ordinary announcement traffic — and may prefer
  a threshold or authors role there. The protocol sets no special cadence:
  which channels are critical is the publisher's call
  ([`design/threats.md`](../design/threats.md)).
- **Root anchor & `custom.repo_base` (normative):** `root.json` **MUST** be
  served at `/.well-known/keryx/root.json` on the join origin and **MUST**
  carry master-signed `custom.repo_base` (exactly one URL — the repo base,
  trailing slash optional) and optional `custom.mirrors` (array of URLs,
  Phase 2). The app fetches the anchor, verifies it (TOFU on first pair,
  chain-walk afterwards), then reads `repo_base` and runs the TUF client
  against it for all non-root metadata and targets. Changing
  `repo_base`/`mirrors` is a root ceremony (root.json version+1,
  master-signed).
- **Root metadata source (normative):** the app **MUST** fetch all root
  metadata — the anchor and every `N.root.json` of the chain walk —
  exclusively from the well-known anchor on the confirmed join origin, and
  **MUST NOT** accept root metadata from the repo base or any mirror. The
  publisher **MUST NOT** publish root metadata in the repo base (a stray copy
  is ignored by compliant clients, but two published sources of the trust
  anchor invite implementation bugs). This is a *fetch-routing* rule, not a
  verification change: the standard TUF client verifies as usual, with its
  fetcher routing `root.json`/`N.root.json` requests to the anchor and
  everything else to the repo base — a supported extension point in all
  three reference clients (python-tuf `FetcherInterface`, tuf-js `fetcher`
  option, go-tuf `RemoteStore`). Rationale: a root rotation — including any
  change to `repo_base`, `mode`, or role keys — reaches clients only via the
  admin-controlled well-known space, so master key + repo-base write is not
  sufficient to forge (the two-lock rule, [core.md §2](core.md)). Failure
  modes: anchor unreachable → root refresh fails → keep the pinned root and
  continue syncing from the repo base until the pinned root's `expires`, then
  degrade to cache + retry (expiry ≠ suspension, §5); malformed or
  unparseable data at the anchor is unavailability (retry), never suspension
  — only a validly-signed-but-unchainable root suspends
  ([core.md §4](core.md)).
- **Mode:** `root.json` `custom.mode` (`"full"` default | `"lite"`,
  [clients.md §3](clients.md)) declares the verification mode; changeable in
  both directions via root ceremony. A mode change is a publisher
  **operational** decision, not an identity change: the app follows the flag
  **silently** — no warning, no acknowledgement, no re-pairing (the identity
  rules in [core.md §2](core.md) cover `company_name` and `logo` only).

---

## 2. `targets.json` (Authorization)

Standard TUF targets metadata; signed by the master key.

```json
{
  "signed": {
    "_type": "targets",
    "spec_version": "1.0.31",
    "version": 7,
    "expires": "2026-12-31T00:00:00Z",
    "targets": {},
    "delegations": {
      "keys": {
        "<K_marketing>": { "keytype": "ed25519", "scheme": "ed25519",
                           "keyval": { "public": "<hex>" } },
        "<K_security>":  { "keytype": "ed25519", "scheme": "ed25519",
                           "keyval": { "public": "<hex>" } },
        "<K_author_alice>": { "keytype": "ed25519", "scheme": "ed25519",
                               "keyval": { "public": "<hex>" } },
        "<K_author_bob>":   { "keytype": "ed25519", "scheme": "ed25519",
                               "keyval": { "public": "<hex>" } }
      },
      "roles": [
        {
          "name": "channels.marketing",
          "keyids": ["<K_marketing>"],
          "threshold": 1,
          "paths": ["channels/marketing/*"],
          "terminating": true
        },
        {
          "name": "channels.security",
          "keyids": ["<K_security>"],
          "threshold": 1,
          "paths": ["channels/security/*"],
          "terminating": true
        },
        {
          "name": "channels.security.authors",
          "keyids": ["<K_author_alice>", "<K_author_bob>"],
          "threshold": 1,
          "paths": ["channels/security/*"],
          "terminating": false
        }
      ]
    },
    "custom": {
      "company_name": "ACME s.r.o.",
      "logo": "https://company.example/logo.png",
      "logo_sha256": "<lowercase hex>",
      "channels": {
        "marketing": {
          "display_name": "Offers",
          "description": "Product news and promotions"
        },
        "security": {
          "display_name": "Security updates",
          "description": "Security advisories and fixes"
        }
      },
      "private_feed_patterns": [
        {
          "channel": "tracking",
          "pattern": "https://eshop.example.com/channels/tracking/*/feed.json",
          "keys": {
            "<K_engine>": { "keytype": "ed25519", "scheme": "ed25519",
                             "keyval": { "public": "<hex>" } }
          },
          "keyids": ["<K_engine>"],
          "threshold": 1,
          "display_name": "Package tracking",
          "purpose": "per-order delivery notifications"
        }
      ]
    }
  },
  "signatures": [ { "keyid": "<K_master>", "sig": "<hex>" } ]
}
```

**Channel roles (native TUF):**

- A channel is a **delegated TUF role** named `channels.<channel>` (see the
  role-name rule below) whose `paths` cover its item namespace.
  `terminating: true` makes each channel authoritative for its own namespace.
- `paths` follow TUF glob semantics: `*` matches exactly one path segment and
  never crosses `/`. `channels/marketing/*` therefore matches every item file
  at `channels/marketing/<id>.json`; deeper nesting requires additional
  explicit patterns (not used in v1 — one item file per item, no archives).
- The channel's item files are pinned in the channel's **own role metadata**
  (`channels.marketing.json`, §3), signed by the channel key — this is what
  makes the channel key's authorization native TUF: it signs the metadata
  that pins the content it distributes.
- A channel exists only if it has a role. There is no "master-signed items"
  fallback.
- **Channel names and role names (normative):** a channel name **MUST** match
  `[a-z0-9-_]+` (lowercase, digits, hyphen, underscore). Its TUF **role name**
  **MUST** be `channels.<channel>` — the channel name prefixed with the
  literal `channels.`; the role metadata file is therefore
  `channels.<channel>.json`. The bare channel name is used verbatim
  everywhere else: `paths` (`channels/<channel>/*`), the item target paths
  (`channels/<channel>/<id>.json`), and the QR payload's
  `channels` array. No escaping or normalization anywhere in the chain.
  **Why the prefix:** delegated role names become `<role>.json` files in the
  same directory as `root.json`, `targets.json`, `snapshot.json`, and
  `timestamp.json`, so an unprefixed channel named `targets` (or any
  top-level role TUF may add later) would collide. Since `.` is outside the
  channel alphabet, the mapping is injective in both directions — a role name
  yields exactly one channel and no channel name can impersonate the prefix —
  which closes the collision structurally rather than by a reserved-name list.
  Clients derive the channel from the item target path
  ([feeds.md §1](feeds.md#1-public-channel-items)), never by string-stripping
  the role name.
- **Authors role (default, normative — [feeds.md §2](feeds.md#2-authors-role)):**
  a channel has an additional delegated role `channels.<channel>.authors`
  whose keyids/threshold authorize item signing. **Authored is the default
  mode**: the publisher tool creates this role on channel creation unless the
  publisher explicitly opts into **simple mode** (no authors role; the channel
  role's keys authorize item signing, [feeds.md §2.1](feeds.md#21-mode-changes-normative)).
  It is a sibling delegation in
  `targets.json` (master-signed — the channel key cannot nominate or withdraw
  authors), has the same `paths` as the channel role, is **not** terminating,
  and **pins no item targets**: it exists to authorize item signatures. Its
  role metadata file (`channels.<channel>.authors.json`) is signed by the
  author keys (threshold), pins no targets, and is pinned by `snapshot.json`.
- The app **MUST** ignore delegated roles whose name does not begin with
  `channels.`, and roles whose `paths` fall outside their own
  `channels/<channel>/*` namespace: they are not channels. `channels.<channel>.authors`
  is not a channel — the channel is derived from the item target path, never
  from the role name.
- Channel role metadata never re-delegates in v1 (`delegations` is always
  `{keys: {}, roles: []}`); a channel is a leaf. The authors role is a
  targets.json-level sibling, so this rule stands unchanged.

**`custom` (master-signed app data):**

- `company_name`, and optional `logo` — company identity.
  These are **self-asserted**: master-signed, so bound to whoever holds the
  root, but attested by nobody outside the company — anyone controlling any
  origin can set them to anything. They are therefore shown only **after the
  chain verifies**, never on the origin confirmation screen, and always
  alongside the join origin, which is the actual trust anchor; the normative
  display rules are in [core.md §2](core.md).
  `logo` is either an **inline data URL** (`data:<mediatype>;base64,…` — its
  bytes are inside the master-signed `custom`, so the logo is authenticated
  by the metadata signature itself and never fetched externally; SHOULD be
  small, RECOMMENDED ≤ 64 KB, since it is embedded in `targets.json`) or an
  **absolute HTTPS URL together with `logo_sha256`** (linked,
  company-controlled origin). The logo is **never a mutable resource**: when
  `logo` is a URL, `logo_sha256` (lowercase hex) is REQUIRED — the app
  **MUST** verify the fetched bytes before rendering and, on mismatch,
  **MUST** fall back to a neutral placeholder: a swapped logo is never
  displayed, and never suspends the
  company or invalidates any content (a media host is not the trust chain).
  A linked `logo` without `logo_sha256` is a metadata error: the app MUST
  NOT display the logo (placeholder), and the reference tool refuses to
  write it. When the field is absent entirely the app shows no logo.
  (Top-level `custom` in targets metadata is an extension field; all three
  reference TUF implementations preserve unrecognized fields, and the app is
  the only consumer.)
- `channels` — **channel display metadata** (master-signed). Keyed by bare
  channel name: `display_name` (shown in the consent summary and channel
  list) and optional `description`. Because it is master-signed, the channel
  key cannot rename or re-describe its own channel — naming is an identity-
  adjacent authority reserved for the master. A channel with no entry falls
  back to its bare name.
- `private_feed_patterns` — per-order/private feed authorization
  ([feeds.md §3](feeds.md)). App-level: TUF has no concept of capability-URL
  feeds, so this is the one place authorization lives outside TUF structures.
  It is master-signed, same trust as everything else. `pattern` is a URL
  glob: origin must match exactly; wildcards only in the path, at segment
  boundaries (`*` = one segment; the capability token is its own segment;
  never in host/query).
- **Key publication (normative):** every keyid referenced from `custom`
  (`private_feed_patterns`) **MUST** have its key object
  (`keytype`/`scheme`/`keyval`) present in the same entry's `keys` map
  (keyid → key object). The app resolves keyids exclusively from these maps;
  a keyid without its key object is a metadata error → reject. Private-feed
  engine keys are NOT delegated TUF roles and **MUST NOT**
  be placed in `delegations.keys` — that map is reserved for delegated role
  keys (channel roles and authors roles). Keyids follow the TUF rule
  ([core.md §1](core.md)).

---

## 3. Channel Role Metadata (`channels.marketing.json`)

Standard TUF delegated-targets metadata; signed by the channel key;
re-signed on every publish of that channel. Delegated-role metadata files are
named `<role>.json` — here the role is `channels.marketing`, so the file is
`channels.marketing.json` (§2) — the same convention every standard client
(tuf-js, python-tuf, go-tuf) uses; the `version` field increments on every
publish; pinned by `snapshot.json`.

```json
{
  "signed": {
    "_type": "targets",
    "spec_version": "1.0.31",
    "version": 123,
    "expires": "2026-04-30T00:00:00Z",
    "targets": {
      "channels/marketing/firmware-25-3.json": {
        "length": 2341,
        "hashes": { "sha256": "…" }
      },
      "channels/marketing/new-wallet-ui.json": {
        "length": 1872,
        "hashes": { "sha256": "…" }
      }
    },
    "delegations": { "keys": {}, "roles": [] }
  },
  "signatures": [ { "keyid": "<K_marketing>", "sig": "<hex>" } ]
}
```

This role metadata **is the channel's feed** — the complete, authoritative
index of what is currently published. It carries no per-target `custom`
(no duplicated display metadata; the client renders lists from item files)
and no archives.

- Each item file is a **TUF target file** — hash-pinned, so the client
  detects tampering (via the metadata chain) without any custom logic.
  Item-level integrity is TUF; the channel key is the publisher of that
  integrity (this is the channel key's load-bearing role).
- **Absence = unpublished (normative).** The index is a snapshot of the
  published set: an item that is not in it is not published, and the app
  MUST drop it (from display and cache). There is no `_sig.withdrawn` flag,
  no tombstones, and no archives: the publisher unpublishes by removing the
  entry and re-signing this metadata.
- **Sync = diff.** The client compares `path → sha256` against its cache and
  fetches only new or changed item files; a fresh subscriber fetches the
  whole published set. Per-item signatures exist for standalone
  verifiability (mirrors, backups, sharing) — [feeds.md §1](feeds.md#1-public-channel-items).
- Item files stay small individually; media is either inlined (data URLs,
  within the item) or referenced via `attachments` (absolute URLs,
  company-controlled origins incl. CDN). Media files are **not** targets;
  an attachment MAY pin its own bytes by hash
  ([feeds.md §1](feeds.md#1-public-channel-items)).
- **History:** only the currently published set is indexed. Older items are
  unpublishable by the publisher at any time; the company's own website
  remains the long-term archive (this is a broadcast newsletter channel, not
  an archive).

---

## 4. Channel Lifecycle

- Channels are **declared categories with their own signing keys** (TUF
  delegation roles in `targets.json`, §2). Channel names use `[a-z0-9-_]+`.
  Items carry free-form `tags`; display metadata lives in master-signed
  `custom.channels` (§2).
- **New channels = a signed metadata update.** The publisher adds a
  delegation role (name `channels.<channel>`, keys, threshold, `paths`),
  creates the channel's role metadata (the index) and the first item(s); the
  app sees it on
  the next refresh and shows it as "new" — the user decides locally whether
  to follow it. Nothing can be faked: authorization is root-signed, the index
  is pinned by the channel's role metadata (channel-key-signed).
- **Removed channels.** The role is dropped; the channel is no longer
  surfaced; cached history MAY be kept.
- **No server-side subscription state exists (normative).** The company
  broadcasts; it cannot know who receives.

---

## 5. Key Lifecycle

Standard TUF, no custom machinery:

- **Root rotation:** new `root.json` (version+1) signed by the previous root
  keys per threshold — standard TUF. Versioned `N.root.json` files let
  outdated clients walk the chain; they are published at the well-known
  anchor, the only location clients accept root metadata from (§1). Rare,
  offline ceremony.
- **Channel key rotation:** `targets.json` version+1 (master-signed) listing
  old+new `keyids` with `threshold: 1` (overlap), then drop the old key; and
  the channel's role metadata (`channels.marketing.json`) is re-signed during
  the overlap by old+new keys. In simple mode (single-author channel, no
  authors role)
  the publisher **MUST re-sign the channel's items with the new key during
  the overlap** (while the old key is still listed), otherwise items signed
  by the old key fail verification on the next client fetch
  ([feeds.md §1](feeds.md#1-public-channel-items)) — the tool does this
  automatically in one command. In an authored channel the items are
  author-signed and need no re-signing; only the index is re-signed.
- **Author key rotation/revocation (authored channels):** `targets.json`
  version+1 updating the `channels.<channel>.authors` delegation (overlap,
  then drop); the authors role metadata
  (`channels.<channel>.authors.json`) is re-signed by the new key set during
  the overlap. **The publisher MUST re-sign the channel's items with the
  new author key(s) during the overlap** (while old keys are still listed) —
  otherwise the items are dropped on the next client fetch (strict
  verification, [feeds.md §2](feeds.md#2-authors-role)). Departed authors'
  keys MAY stay listed until their items are re-signed or leave the index.
- **Channel key revocation:** `targets.json` version+1 **omitting** the key;
  new items signed by a revoked `keyid` are rejected. Compromise response:
  revoke *and* reissue in the same update (re-sign the role metadata with
  the new key).
- **Ops key (snapshot/timestamp) rotation:** `root.json` version+1 (master
  ceremony) listing the new key — snapshot/timestamp keys are top-level TUF
  roles, so they live in `root.json`, not in `targets.json`.
- **Pre-announced rotations (RECOMMENDED):** publish a signed `next_key`
  record ahead of time, so a stolen key cannot redirect the chain.
- **Anti-rollback/freeze:** versioned metadata + client-side version
  memory; `timestamp.json` (short `expires`) is the anti-freeze mechanism;
  all metadata also carries `expires` as backstop. **Expiry ≠ suspension:**
  expired-but-unrefreshed metadata → keep serving cached content and retry;
  only a chain break suspends ([core.md §4](core.md)). RECOMMENDED: root
  expiry ≥ 1 year; the reference default is **10 years** — root is re-signed
  only by an offline master ceremony, so its expiry is a last-resort backstop
  (bounded anchor lifetime, retirement deadline), not an operational cadence;
  freshness is `timestamp.json`'s job.
- **`publish` never touches authorization metadata** — it adds/removes/changes
  item targets in the channel role metadata, re-signs the channel's role
  metadata
  (`channels.marketing.json`), `snapshot.json`, `timestamp.json` (channel key
  + online ops key). Author/channel/root changes are separate ceremonies.
