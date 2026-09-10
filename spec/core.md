# Keryx — Core Protocol

**Status: normative.** The key words **MUST**, **MUST NOT**, **REQUIRED**,
**SHALL**, **SHALL NOT**, **SHOULD**, **SHOULD NOT**, **RECOMMENDED**, **MAY**,
and **OPTIONAL** are to be interpreted as described in RFC 2119. Rationale for
these rules is informative and lives in [`design/why.md`](../design/why.md) and
[`design/threats.md`](../design/threats.md).

**Version:** 0.1 (draft). **Working title:** Keryx (placeholder — open to
renaming).

This file defines conventions, the trust model, pairing, and suspension. The
rest of the normative spec: [`repository.md`](repository.md) (TUF layout,
authorization, key lifecycle), [`feeds.md`](feeds.md) (feed format, editor
mode, private feeds), [`clients.md`](clients.md) (client flow, publisher tool,
lite mode).

---

## 1. Conventions

| Item | Rule |
|---|---|
| Trust/update framework | **Full TUF** — root/targets/snapshot/timestamp + delegated per-channel roles; `consistent_snapshot: false` (default; [repository.md §1](repository.md)); anti-rollback via `version` fields + the versioned `N.root.json` chain (an optional **lite mode** without snapshot/timestamp is defined in [clients.md §3](clients.md)) |
| Distribution unit | The **TUF repo** = metadata + per-channel feed files (targets). Hosted anywhere the publisher chooses (static host, CDN, bucket, CMS). The app discovers it via the root anchor: `/.well-known/keryx/root.json` on the join origin → `custom.repo_base` in root.json |
| Metadata URLs | Root anchor: `https://<join-origin>/.well-known/keryx/root.json` (+ all `N.root.json`) — the **exclusive** source of root metadata, chain walk included. Repo base: `custom.repo_base` in the verified root.json (single URL) with optional `custom.mirrors` (array; Phase 2). Standard TUF layout inside the base — **no root files**: `timestamp.json`, `snapshot.json`, `targets.json`, `channels.<channel>.json` (version-prefixed metadata and hash-prefixed target files exist only if a publisher opts into `consistent_snapshot: true`) |
| Channel names | `[a-z0-9-_]+` (lowercase, digits, hyphen, underscore) — used verbatim as path segments and `_sig.channel`, with no escaping anywhere. The TUF **role name** is the namespaced `channels.<channel>`: `.` is outside the channel alphabet, so the mapping is injective and no channel can collide with a top-level metadata filename |
| Public feeds | **Per-channel TUF target files** at `channels/<name>/feed.json` — fetched by the client, hash-verified automatically (authenticity + anti-withdrawal + anti-tamper) |
| MIME types | `application/json` for metadata; `application/feed+json` for feeds |
| Encoding | UTF-8 |
| Keys | Ed25519 (RFC 8032), 32-byte public / 64-byte signature; algorithm declared by the key object (`keytype`/`scheme`) |
| Keyid | SHA-256 hex of the canonical JSON of the key object `{"keytype":"ed25519","scheme":"ed25519","keyval":{"public":"<hex>"}}` (TUF standard) |
| TUF metadata canonicalization | **securesystemslib canonical JSON (OLPC)** — delegated to the TUF library; never hand-rolled |
| Feed-item canonicalization | **JCS (RFC 8785)** — implemented in the publisher tool and app (small, testable) |
| Metadata signature encoding | hex (`sig` per TUF spec) |
| Item signature encoding | base64url (no padding) of the raw 64-byte Ed25519 signature |
| Freshness | Publisher refreshes `timestamp.json` on a short cadence (cron/CI, default 24–72 h) — the standard TUF anti-freeze mechanism; all metadata also carries `expires` |
| No encryption | authenticity only (public feeds are broadcast; private feeds are access-controlled by capability token, [feeds.md §3](feeds.md)) |

**Two canonicalizations, on purpose:** TUF metadata is canonicalized and
verified by the TUF library (OLPC); feed items use JCS. Both are standard;
they serve different layers and MUST NOT be mixed.

### 1.1 Naming and wire identifiers

The project is under a working title, so wire-level identifiers are
deliberately codename-neutral: the JSON Feed extension is `_sig`, and no
project name appears in any field, path, or key — with **one deliberate
exception**: the well-known location `/.well-known/keryx/` (RFC 8615
registered name), which MUST be a stable protocol name (it is printed into
every QR; renaming it later breaks all printed payloads).

Metadata layout follows standard TUF conventions, with documented path
conventions: public feeds live at `channels/<channel>/feed.json` (one feed
file per channel), each channel's TUF role is named `channels.<channel>`, and
the root anchor lives at `/.well-known/keryx/root.json` on the join origin.

**Key naming:** snake_case throughout TUF metadata, including `custom` (TUF's
own style). `_sig` members inside a JSON Feed document are the single
lowercase words defined in [feeds.md](feeds.md) (`about`, `channel`,
`withdrawn`, `resources`, `signatures`, `url`, `version`, `expires`); a future
multi-word member would be camelCase, matching JSON Feed's own style for
extension content.

### 1.2 Metadata origin (normative)

The trust boundary is the **join origin** — the origin of the join URL the
user scans, whose `/.well-known/keryx/root.json` is the root anchor (the only
URL in the whole protocol that is trusted by location, not by signature). It
MUST be a company-controlled origin: a brand-containing hostname, never a
shared/multi-tenant origin (the confirmed string must identify the company).
The well-known space is admin-controlled (RFC 8615 §4.1), so user-content
paths on the same origin cannot host the anchor. The repo base MAY live on
*different* infrastructure (CDN, CMS, bucket) — it is master-signed-linked
from root.json and is therefore availability-only (root metadata is never
fetched from it). At most one canonical redirect (http→https, www→apex) is
allowed; cross-origin redirects stay blocked.

### 1.3 Key roles (summary)

One **master key** (offline; `root` + `targets`), one **online ops key**
(`snapshot` + `timestamp`), and **one key per channel** (`channels.<channel>`
role; signs that channel's feed-pinning metadata). Optional: **editor keys**
(`custom.editor_mode`, [feeds.md §2](feeds.md)), a **private-feed engine key**
(`custom.private_feed_patterns`, [feeds.md §3](feeds.md)), and additional keys
per threshold.

**Accepted simplification (normative note):** one key filling both `root` and
`targets` means a compromise of the targets key **is** a compromise of the
root key — the two roles are not independently recoverable. This is a
deliberate trade for a three-key minimum; both roles are offline, so the
exposure is the same custody event. A publisher with stricter custody **MAY**
use separate keys for `root` and `targets` (standard TUF; no protocol or app
change).

---

## 2. Trust Model

- **One human step:** the user confirms the exact origin (plain ASCII,
  punycode for IDNs, no UTF-8). Everything else is automatic.
- **Automatic pinning:** the app records the first-seen root as the trust
  anchor. No verification code, no safety number, nothing to compare. TOFU on
  first pair; chain-walk afterwards.
- **Binary decision:** a message is either signed by a key the pinned
  metadata authorizes for that channel — then it is shown — or it is not —
  then it is rejected. There is **no third state**: no "lower trust", no
  grace period, no yellow warning, no "do you trust this?" dialog. A failing
  item is dropped, **including one that was previously displayed**.
- **Two locks:** *"valid root of trust"* **and** *"root metadata served from
  the user-confirmed origin's `/.well-known/` space"*. The second lock is
  mechanical, not a pairing-time event: the app fetches **all** root
  metadata — the initial anchor and every `N.root.json` of the chain walk —
  exclusively from the well-known anchor ([repository.md §1](repository.md),
  [clients.md §1](clients.md)). A root rotation (including any change to
  `repo_base`, `mode`, or role keys) therefore reaches clients only through
  the admin-controlled well-known space. An attacker with only the master key
  cannot get a rotation served from the anchor (a copy planted on the repo
  base/CDN is never fetched); an attacker with only the origin cannot sign.
  To send a forged message, both must be compromised.
- **Company identity — recorded at pairing, then change-visible (never
  silent).** The user confirms the **origin**; they never confirm the
  company's name or logo. Those are self-asserted values that anyone
  controlling any origin can set to anything, so they are deliberately not
  part of the human verification step (§3). Once the chain verifies, the app
  records the identity in force at pairing (`custom.company_name`,
  `custom.logo`) and watches it. On a **per-change** basis:
  - a change to `company_name` **MUST** trigger a prominent rebranding
    warning and require **re-pairing** (rescan a fresh QR, re-confirm the
    origin) before the company's content is shown;
  - a `logo` or `logo_sha256` (cosmetic) change requires a one-tap
    acknowledgement;
  - confirming stores the new identity snapshot; the same value never
    re-warns;
  - feed titles/icons and channel display metadata are **not** identity and
    never warn.

  **Identity display (normative).** `company_name` and `logo` **MUST NOT** be
  shown before the chain verifies — in particular never on the origin
  confirmation screen (§3), where displaying them would require either a
  fetch from an unconfirmed origin or a value taken from the unsigned
  payload. After the chain verifies they MAY be shown freely, but wherever
  the app identifies the company — contacts row, company detail header,
  rebranding and suspension warnings — the **join origin MUST remain visible
  alongside them**. The origin is the trust anchor; the name and logo are
  decoration under a pinned anchor. An app **MUST NOT** present them as
  externally verified (no "verified" badge): nothing outside the company
  attests to them.
- **Suspension:** an unverifiable root change (no valid chain from the pinned
  root) → the company is **suspended** (all channels): content is rejected
  outright, never displayed with lower trust, and the app warns that this may
  indicate a compromise. The offered action is Remove; the app does not
  prompt for re-pairing (§4).

**TUF mapping (informative):**

| TUF concept | This protocol |
|---|---|
| Root key (offline, threshold) | master key |
| Root metadata | `root.json` (pinned; versioned for chain walk; served only from the well-known anchor) |
| Targets role (offline) | `targets.json` — channel delegations + master-signed `custom` (company, editor mode, private patterns) |
| Delegated roles | channel keys (one role per channel, named `channels.<name>`; signs that channel's feed pinning) |
| Delegation paths | `channels/<name>/*` (channel namespace) |
| Role keyids + threshold | `delegations[].keyids` + `threshold` |
| Snapshot/timestamp | freshness + anti-freeze (publisher-side cadence; consumed by the standard client) |
| Repo base discovery | `root.json` `custom.repo_base` (master-signed; availability-only) |
| Rotation / revocation | metadata version+1 signed by old/remaining keys |
| Private capability feeds | not TUF targets; authorization in master-signed `custom.private_feed_patterns` |

---

## 3. Join URL and QR Payload

The QR encodes a **standard HTTPS URL** (never a custom scheme), so the whole
flow — install app + pair + subscribe — works with iOS Universal Links and
Android App Links:

```
https://company.example/join?p=<base64url(payload)>
```

The payload is optional: `https://company.example/join` without `p` is a
valid join URL (payload-less join, below).

**Payload** (JSON, base64url without padding):

```json
{
  "v": 1,
  "channels": ["marketing", "product"],
  "private_feeds": ["https://eshop.example.com/channels/tracking/8f3a…/feed.json"]
}
```

- **Payload-less join (normative).** `p` MAY be omitted. `https://<origin>/join`
  without `p` is a valid join URL whose payload is `{"v":1,"channels":[],
  "private_feeds":[]}`: the app pairs with the confirmed origin and shows the
  publisher's **public** channels for the user to choose (the consent rule
  below still applies — nothing is auto-subscribed). There are no preselects
  and, because capability URLs travel only in `?p=`, **never any private
  feeds** — publishers with order-scoped content MUST keep using `?p=`. A
  client MUST accept a payload-less join only from the join path (`/join` or
  `/join/`) or the bare origin (`https://<origin>` or `https://<origin>/`);
  other paths without `p` are not join URLs. The payload-less form is a
  pairing convenience, not a trust mechanism — trust is unchanged (§1.2).
- **Versioning (normative):** `v` is the payload's major version (`1` for
  this spec). An app that does not implement the payload's `v` **MUST NOT**
  attempt a partial parse: it stops and tells the user the code needs a newer
  version of the app. Within a known `v`, unrecognized members **MUST** be
  ignored (forward compatibility).
- **Encoding (normative):** base64url (RFC 4648 §5) **without padding** —
  URL-safe by construction (`-`, `_`, alphanumerics only; all unreserved per
  RFC 3986, no percent-encoding needed, no `+`/`/`/`=`). Standard base64
  MUST NOT be used: `+` decodes to a space in query strings and `/` is
  ambiguous.
- **There is no `root` URL in the payload (normative).** The app derives the
  root anchor itself: `https://<join-url-origin>/.well-known/keryx/root.json`
  (RFC 8615). Nothing attacker-controllable in the payload can point the app
  at metadata; the origin the user confirms is the join origin, and the
  well-known space is admin-controlled (a path-write on the origin cannot
  host the anchor).
- `channels` (optional) — suggested public channels. **Normative consent
  rule:** they are **preselects in the consent summary — the user MUST tap
  to subscribe** (explicit opt-in); the payload never auto-subscribes.
  `private_feeds` auto-subscribe (they are order-scoped).
- `private_feeds` (optional) — **URLs only**, not definitions: private
  capability feeds (per-order delivery, invoices…). All metadata (channel,
  display name, purpose, expiry) is resolved from the signed metadata
  (`custom.private_feed_patterns`) and the feed itself (`_sig.channel`,
  `_sig.version`, `_sig.expires`, standard `expired`). Each URL is
  unguessable and validated against an authorized pattern *after* pairing —
  a tampered QR cannot smuggle in an unauthorized feed. Multiple orders =
  multiple scans (the app appends) or multiple entries in one payload.
- **The payload MUST NOT carry company identity (normative).** No name, no
  logo, no display string. The payload is unsigned, so any identity field in
  it would be attacker-controlled brand chrome shown at the exact moment the
  user is deciding whether to trust the origin. Identity comes from
  master-signed `custom` once the chain verifies (§2). Unrecognized members
  are ignored, so an app **MUST NOT** render one it does not understand.
- **Size policy (honest numbers):** without a `root` URL the payload is
  smaller (~130–230 chars encoded with one private feed; a worked example —
  two suggested channels, one private feed, short origin — is 186 encoded
  chars, giving a 217-byte join URL and a version-13 QR at ECC Q). With the
  join URL, the total QR is realistically **~400–700 bytes at ECC Q** for
  longer origins and feed paths — dense, and
  dot-matrix receipt printers often fail to scan it; test with the target
  printer before printing. Cap: ≤ ~512 encoded payload bytes and ≤ 2 private
  feeds per QR; more feeds via additional scans. If payloads ever need to
  grow, a server-backed join endpoint is the fallback (the QR URL stays
  short; the endpoint returns the payload) — not needed for v1.
- **The payload is not signed (normative).** Trust comes from (a) the user
  confirming the join origin (which pins the well-known root anchor), and
  (b) pattern authorization of private feeds against the signed metadata. A
  fake QR is caught by (a); a tampered private feed URL is caught by (b).
  Same defense as before, no new construct. The scenario analysis is in
  [`design/threats.md`](../design/threats.md).
- **Join-side privacy (normative):** the capability token travels in the join
  URL's `p` parameter, so the join fallback page **MUST** set
  `Referrer-Policy: no-referrer`, **MUST NOT** include third-party
  resources, and the join host **MUST** redact `p` from access logs.

**One-scan flow (informative):**

1. Scan → OS opens the join URL.
2. App not installed → the fallback page at `/join` offers the store link;
   after install the user re-opens the **same link** (standard iOS/Android
   pattern — the URL carries everything, so no data is lost).
3. App ready → parses the payload → **shows the join URL's origin and asks
   the user to confirm it — before any network request to that origin**
   (normative ordering: nothing is fetched from an unconfirmed origin, so a
   scan of a hostile QR that the user declines leaks no request at all; the
   screen shows the origin and nothing else — no name, no logo, §2) →
   derives and fetches the root anchor
   (`<origin>/.well-known/keryx/root.json`) → reads `custom.repo_base` from
   the verified root → runs the TUF client (root chain → timestamp →
   snapshot → targets → the offered channels' role metadata) → **shows the
   consent summary**, which can now name what is on offer in the publisher's
   own signed words: company name and logo from master-signed `custom`,
   channel `display_name`/`description` from each channel's role metadata,
   suggested channels preselected, private feeds listed (e.g. "Order 12345
   delivery"), the confirmed origin still on screen → user taps Subscribe →
   fetches and hash-verifies the channel feed files → validates private feed
   URLs against patterns → **subscribed to the channels the user confirmed +
   this order's delivery feed in one pass**.

   **Where the refresh sits (normative).** The metadata chain is refreshed
   **after** origin confirmation and **before** the consent summary, so the
   summary describes what the user is agreeing to in verified terms rather
   than as raw slugs from the QR. The ordering rule binds *confirmation*, not
   consent: a user who declines at the summary has caused fetches only
   against an origin they already confirmed, which is the same exposure that
   confirming it implied.

**Publisher setup:** the join path hosts a small fallback HTML page
(app-store links / "open in app"); `assetlinks.json` and
`apple-app-site-association` map the URL to the app (standard for both
platforms). The publisher tool emits the payload and the QR
([clients.md §2](clients.md)).

---

## 4. Suspension — What Happens When the Chain Breaks

If a root change **cannot be chained** to the pinned anchor (domain takeover,
lost key, incompatible reissue):

- The **company** enters a **suspended** state (all its channels): new
  content is **rejected** (rejected outright, not displayed with lower
  trust).
- **Suspension is a warning, not a repair prompt (normative).** A broken
  chain is indistinguishable from a compromise of the company's origin or
  keys, so the UI **MUST** state that plainly — "This company's identity
  changed. This can mean the company's website or signing keys were
  compromised. Messages are not shown." — and **MUST NOT** offer re-pairing
  as a remedy: no "fix this" affordance, no one-tap rescan, no prompt to
  scan a fresh QR. The offered action is **Remove**.
  Rationale: in the takeover case the attacker controls the origin, and
  therefore also whatever QR the user would find there; a re-pair prompt
  would walk the user through TOFU at the exact moment the origin is least
  trustworthy. A user may of course pair with the company again later as a
  new, deliberate pairing, but the app never steers them there while
  suspended.
- The user is never asked to judge trust or to compare keys.
- **Trigger precision (normative):** suspension requires a **validly-signed
  but unchainable** root fetched from the well-known anchor. Malformed or
  unparseable anchor data (host misconfiguration, error pages, truncated
  responses) is unavailability — keep cache + retry
  ([repository.md §5](repository.md)) — never suspension: junk at the anchor
  must not be able to brick a pairing.
- **Expiry ≠ suspension.** Expired-but-unrefreshed metadata degrades to cache
  + retry; only a chain break suspends.
