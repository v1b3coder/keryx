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
its own role metadata pinning its feed file(s).

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
│                                  # pins channels/marketing/feed.json
├── channels.security.json        # K_security (role `channels.security`)
├── channels/
│   ├── marketing/feed.json       # public feed (TUF target file, served at this plain path)
│   └── security/feed.json
└── media/…                       # referenced attachments (NOT targets; plain files)
```

**Roles and keys:**

| Role | Signer | Cadence | Contains |
|---|---|---|---|
| `root` | master key (offline) | rare (rotation only) | keys of all roles, thresholds, identity |
| `targets` | master key (offline) | rare (channel/editor changes) | channel delegations, `custom` (company metadata, editor mode, private patterns) |
| `channels.<name>` (delegated) | **channel key** | **every publish of that channel** | that channel's feed files: `{path → length, hashes, custom}` |
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
  can get mismatched metadata/feed bytes → hash verification fails → retry
  (a transient availability blip; TUF verification is what makes it safe).
  Chosen deliberately for this topology — one publisher, low publish
  frequency, small feeds — because it avoids unbounded accumulation of
  version-prefixed metadata and hash-named feed copies on static hosts, and
  the plain-path feed serves TUF clients and generic JSON Feed readers from
  one file ([feeds.md §1](feeds.md)). A publisher MAY set
  `consistent_snapshot: true` (busy repos, aggressive CDN caching); standard
  clients handle either via the root flag. Versioned names stay unambiguous
  under the opt-in because `N` is numeric and channel role names are
  dot-namespaced: `N.channels.marketing.json` cannot be confused with
  `N.root.json` or with another channel's file.
- **Scope of compromise:** the online ops key is scoped to freshness
  (snapshot/timestamp pinning) — its compromise allows rollback/freeze
  (availability harm) but never content authorization. Channel keys are
  scoped to their own channel: they pin and author that channel's content
  (forging items requires the channel key too — or, in editor mode, the
  editor keys; see [feeds.md §2](feeds.md)).
  **Publisher note (informative):** "availability harm" is not equally
  harmless on every channel. Freezing metadata suppresses *new* items, so on
  a channel carrying security warnings the harm is silence during exactly
  the incident the channel exists for. Publishers who run such a channel
  should pick their `timestamp` cadence (and `expires`) accordingly — the
  24–72 h default is aimed at ordinary announcement traffic — and may prefer
  a threshold or editor mode there. The protocol sets no special cadence:
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
        }
      ]
    },
    "custom": {
      "company_name": "ACME s.r.o.",
      "logo": "https://company.example/logo.png",
      "logo_sha256": "<lowercase hex>",
      "editor_mode": {
        "marketing": {
          "keys": {
            "<K_editor_alice>": { "keytype": "ed25519", "scheme": "ed25519",
                                   "keyval": { "public": "<hex>" } }
          },
          "keyids": ["<K_editor_alice>"],
          "threshold": 1
        },
        "security": {
          "keys": {
            "<K_editor_sarah>": { "keytype": "ed25519", "scheme": "ed25519",
                                   "keyval": { "public": "<hex>" } },
            "<K_editor_tom>":   { "keytype": "ed25519", "scheme": "ed25519",
                                   "keyval": { "public": "<hex>" } }
          },
          "keyids": ["<K_editor_sarah>", "<K_editor_tom>"],
          "threshold": 2
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
  role-name rule below) whose `paths` cover its feed namespace.
  `terminating: true` makes each channel authoritative for its own namespace.
- `paths` follow TUF glob semantics: `*` matches exactly one path segment and
  never crosses `/`. `channels/marketing/*` therefore matches
  `channels/marketing/feed.json` (and archive files at the same depth, e.g.
  `channels/marketing/archive.json`); deeper nesting requires additional
  explicit patterns. v1 uses one feed file per channel plus JSON Feed
  `next_url` pagination ([feeds.md §1](feeds.md)).
- The channel's feed files are pinned in the channel's **own role metadata**
  (`channels.marketing.json`, §3), signed by the channel key — this is what
  makes the channel key's authorization native TUF: it signs the metadata
  that pins the content it owns.
- A channel exists only if it has a role. There is no "master-signed items"
  fallback.
- **Channel names and role names (normative):** a channel name **MUST** match
  `[a-z0-9-_]+` (lowercase, digits, hyphen, underscore). Its TUF **role name**
  **MUST** be `channels.<channel>` — the channel name prefixed with the
  literal `channels.`; the role metadata file is therefore
  `channels.<channel>.json`. The bare channel name is used verbatim
  everywhere else: `paths` (`channels/<channel>/*`), the feed target path
  (`channels/<channel>/feed.json`), `_sig.channel`, and the QR payload's
  `channels` array. No escaping or normalization anywhere in the chain.
  **Why the prefix:** delegated role names become `<role>.json` files in the
  same directory as `root.json`, `targets.json`, `snapshot.json`, and
  `timestamp.json`, so an unprefixed channel named `targets` (or any
  top-level role TUF may add later) would collide. Since `.` is outside the
  channel alphabet, the mapping is injective in both directions — a role name
  yields exactly one channel and no channel name can impersonate the prefix —
  which closes the collision structurally rather than by a reserved-name list.
  Clients derive the channel from the feed target path
  ([feeds.md §1.2](feeds.md)), never by string-stripping the role name.
- The app **MUST** ignore delegated roles whose name does not begin with
  `channels.`, and roles whose `paths` fall outside their own
  `channels/<channel>/*` namespace: they are not channels. This keeps the
  namespace open for future non-channel delegations without ambiguity.
- Channel role metadata never re-delegates in v1 (`delegations` is always
  `{keys: {}, roles: []}`); a channel is a leaf.

**`custom` (master-signed app data):**

- `company_name`, `logo`, and optional `logo_sha256` — company identity.
  These are **self-asserted**: master-signed, so bound to whoever holds the
  root, but attested by nobody outside the company — anyone controlling any
  origin can set them to anything. They are therefore shown only **after the
  chain verifies**, never on the origin confirmation screen, and always
  alongside the join origin, which is the actual trust anchor; the normative
  display rules are in [core.md §2](core.md).
  `logo` is an absolute HTTPS URL. Its bytes are **not** a TUF target, so a
  publisher **MAY** pin them with `logo_sha256` — the resource's SHA-256 as
  a lowercase hex string, the same rule and algorithm as `_sig.resources`
  ([feeds.md §1.2](feeds.md)). When present the app **MUST** verify the
  fetched bytes before rendering and, on mismatch, **MUST** fall back to a
  neutral placeholder: a swapped logo is never displayed, and never suspends
  the company or invalidates any content (a media host is not the trust
  chain). When absent the logo is an ordinary mutable web resource.
  (Top-level `custom` in targets metadata is an extension field; all three
  reference TUF implementations preserve unrecognized fields, and the app is
  the only consumer.)
- `editor_mode` — **optional protocol extension (editor mode,
  [feeds.md §2](feeds.md)).** Keyed by channel name. Semantics and
  verification rules are normative in that section; the schema is: per
  channel, `keys` (key objects), `keyids`, `threshold` (default 1).
- `private_feed_patterns` — per-order/private feed authorization
  ([feeds.md §3](feeds.md)). App-level: TUF has no concept of capability-URL
  feeds, so this is the one place authorization lives outside TUF structures.
  It is master-signed, same trust as everything else. `pattern` is a URL
  glob: origin must match exactly; wildcards only in the path, at segment
  boundaries (`*` = one segment; the capability token is its own segment;
  never in host/query).
- **Key publication (normative):** every keyid referenced from `custom`
  (`editor_mode`, `private_feed_patterns`) **MUST** have its key object
  (`keytype`/`scheme`/`keyval`) present in the same entry's `keys` map
  (keyid → key object). The app resolves keyids exclusively from these maps;
  a keyid without its key object is a metadata error → reject. Editor keys
  and private-feed engine keys are NOT delegated TUF roles and **MUST NOT**
  be placed in `delegations.keys` — that map is reserved for channel role
  keys. Keyids follow the TUF rule ([core.md §1](core.md)).

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
      "channels/marketing/feed.json": {
        "length": 48213,
        "hashes": { "sha256": "…" },
        "custom": {
          "display_name": "Offers",
          "description": "Product news and promotions"
        }
      }
    },
    "delegations": { "keys": {}, "roles": [] }
  },
  "signatures": [ { "keyid": "<K_marketing>", "sig": "<hex>" } ]
}
```

- Each public feed is a **TUF target file** — hash-pinned, so the client
  detects withdrawal, tampering, and staleness (via the metadata chain +
  timestamp) without any custom logic. Feed-level integrity is TUF; the
  channel key is the publisher of that integrity (this is the channel key's
  load-bearing role).
- `custom` (per-target, TUF-defined "opaque to the framework") carries
  channel display metadata. Archives (via `next_url`) are additional target
  entries in the same role metadata; the app resolves `next_url` to these
  targets and verifies them exactly like the main feed
  ([feeds.md §1](feeds.md)).
- Feed files stay small: text, metadata, tags, links inline; media
  referenced via `image`/`attachments` (absolute URLs, company-controlled
  origins incl. CDN). Media files are **not** targets; an item MAY pin
  individual resources by hash via `_sig.resources`
  ([feeds.md §1.2](feeds.md)).
- **History:** only the current feed version is served
  (`consistent_snapshot: false`, §1); old items inside the current feed are
  covered by the current hash, and older content moves to archive targets
  reachable via `next_url`. Per-item signatures exist for portability,
  archives, and editor mode.

---

## 4. Channel Lifecycle

- Channels are **declared categories with their own signing keys** (TUF
  delegation roles in `targets.json`, §2). Channel names use `[a-z0-9-_]+`.
  Items carry `_sig.channel` + free-form `tags`.
- **New channels = a signed metadata update.** The publisher adds a
  delegation role (name `channels.<channel>`, keys, threshold, `paths`),
  creates the channel's role metadata and initial feed; the app sees it on
  the next refresh and shows it as "new" — the user decides locally whether
  to follow it. Nothing can be faked: authorization is root-signed, the feed
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
  the overlap by old+new keys. Old items stay verifiable: within the current
  feed they are covered by the current target hash; per-item signatures
  cover archives/portability.
- **Editor key rotation/revocation:** `targets.json` version+1 updating
  `custom.editor_mode` (overlap, then drop). No role metadata change; no CI
  involvement beyond re-pinning `targets.json` (ops key). **The publisher
  MUST re-sign the channel's items with the new key(s) during the overlap**
  (while old keys are still listed) — otherwise the items are dropped on the
  next client fetch (strict verification, [feeds.md §1.2](feeds.md)).
  Departed editors' keys MAY stay listed until their items are re-signed or
  leave the live feed.
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
  expiry ≥ 1 year.
- **`publish` never touches authorization metadata** — it updates the
  channel's feed target, re-signs the channel's role metadata
  (`channels.marketing.json`), `snapshot.json`, `timestamp.json` (channel key
  + online ops key). Editor/channel/root changes are separate ceremonies.
