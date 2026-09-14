# Keryx — Channel Items, Authors Role, and Private Feeds

**Status: normative** (RFC 2119 keywords as in [`core.md`](core.md)).
Rationale is informative and lives in [`design/why.md`](../design/why.md).

Covers: the public channel item format, signing and verification (§1); the
optional authors role (§2); private per-order capability feeds (§3).

---

## 1. Public Channel Items (`channels/<name>/<id>.json`)

A public channel is a set of **signed item files**; the channel's role
metadata ([repository.md §3](repository.md#3-channel-role-metadata-channelsmarketingjson))
is the index — the complete, authoritative list of what is currently
published. There is **no feed document**: no wrapper, no pagination, no
archives. One item = one TUF target.

### 1.1 Item

```json
{
  "id": "firmware-25-3",
  "title": "Firmware 25.3 released",
  "content_html": "<p>…</p><img src=\"data:image/jpeg;base64,…\">",
  "image": "data:image/jpeg;base64,…",
  "date_published": "2026-03-14T10:00:00Z",
  "date_modified": "2026-03-15T09:00:00Z",
  "tags": ["firmware", "security"],
  "language": "en",
  "attachments": [
    { "name": "fw.pdf", "url": "https://company.example/media/fw.pdf",
      "mime_type": "application/pdf", "size_in_bytes": 123456,
      "sha256": "<lowercase hex>" },
    { "name": "FAQ", "url": "https://company.example/faq" }
  ],
  "sig": [
    { "keyid": "<K_author_alice>", "sig": "<base64url 64 bytes>" }
  ]
}
```

**Fields (normative):**

| Field | Rule |
|---|---|
| `id` | **REQUIRED.** Unique within the channel, stable across updates, one path segment, `[a-z0-9-_]+` (same alphabet as channel names — no dots, so no `.`/`..` ambiguity). Application-derived (e.g. the article slug); the spec defines no format. The item's TUF target path is `channels/<channel>/<id>.json`. |
| `title` | **REQUIRED.** The only list-rendering text; there is no summary or perex. |
| `content_html` | **REQUIRED.** Full HTML/CSS within the app's isolated, scriptless sandbox (CONTENT.md D1–D3): no scripts, no forms, no iframes/embeds, no top-level navigation; CSS cannot escape the container. Links are absolute URLs; images inline as data URLs. |
| `image` | OPTIONAL. Either an inline **data URL** (`data:<mediatype>;base64,…` — the self-contained preview, covered by the item's hash) or an **absolute HTTPS URL together with `image_sha256`** (linked, company-controlled origin). The preview is **never a mutable resource**: when `image` is a URL, `image_sha256` is REQUIRED — the app MUST verify the fetched bytes before rendering; mismatch → the image is unavailable (never shown), the item itself stays valid. A linked `image` without `image_sha256` is a schema violation → the item is rejected. |
| `image_sha256` | REQUIRED when `image` is a linked URL (lowercase hex). Prohibited/ignored for an inline data URL. See `image`. |
| `date_published` | **REQUIRED**, valid RFC 3339. The inbox ordering key. |
| `date_modified` | OPTIONAL, valid RFC 3339. Display-only ("updated at"); never an ordering or update signal. |
| `tags` | OPTIONAL. Free-form strings; local filtering (never sent). |
| `language` | OPTIONAL. RFC 5646; local filtering. |
| `attachments` | OPTIONAL. Array of `{name?, url, mime_type?, size_in_bytes?, sha256?}`. `url` is an absolute HTTPS URL (company-controlled origin). `sha256` is OPTIONAL — some resources cannot be pinned (dynamic landing pages); when present, it is the SHA-256 (lowercase hex) of the resource's bytes and the app MUST verify before rendering, opening, or saving: mismatch → the resource is unavailable (never shown or handed to the user), the item itself stays valid. Unlisted/unhashed resources are ordinary web links, mutable by design. |
| `sig` | **REQUIRED.** Array of `{keyid, sig}` (base64url, no padding, raw 64-byte Ed25519). See §1.2. |

**Deliberately absent:** `content_text` (no plain-text fallback — the content
is self-contained HTML; decided, supersedes CONTENT.md D6), `summary`,
`url` (the company's website remains the publisher's own surface; "read
more" is part of `content_html`), `authors` (authorship is expressed by the
signatures), and any `_sig` object (no extension namespace anywhere in the
protocol — private capability feed documents carry their fields at top
level, §3).

**Size:** per-item size limit is **app policy** — RECOMMENDED 1 MB per item
(inline media included); the app MUST enforce a maximum and abort beyond it.

### 1.2 Signing and verification

**Signing rule (normative):**

1. Take the item object as published, with the `sig` field **removed** (all
   other fields are covered).
2. Serialize with **securesystemslib canonical JSON (OLPC)** — the same
   canonicalization as TUF metadata ([core.md §1](core.md)); one
   canonicalization for the whole protocol.
3. Sign the bytes with Ed25519 using the key whose `keyid` is listed; repeat
   for each threshold requirement.
4. Store `{keyid, sig}` in `sig`.

**Verification (strict — every fetched item is checked before it is
considered):**

- **Authored channel** (`channels.<channel>.authors` delegation exists in
  trusted `targets.json`, §2): the app MUST verify that at least `threshold`
  entries verify against the authors role's keyids. Any failure → item
  rejected, never displayed. Additional entries (e.g. channel-key
  signatures, for portability) MAY be present but are not load-bearing.
- **Single-author channel** (no authors role): the app MUST verify that at
  least `threshold` entries verify against the channel role's keyids.
- In both cases: entries by **unknown** keys are ignored, never a reason to
  reject (attribution only); a **known keyid whose signature does not
  verify** → item rejected. There is no third state
  ([core.md §2](core.md)). Missing signatures → item rejected in both cases
  (all items are signed).
- **Verification gates content, always.** No item is displayed (or kept
  displayed after a re-fetch) without passing the checks above. Unchanged
  known items are skipped (already verified; the item is TUF-hash-pinned, so
  unchanged bytes cannot have been swapped); new or changed content is
  always verified first.

**Why both hash pin and per-item signatures:** the item's bytes are pinned by
the TUF target hash in the channel role metadata (authenticity inside the
protocol); the `sig` makes the item **standalone verifiable** — a file copied
to a mirror, backup, or share can be checked without the index.

**Channel cross-check:** the channel is derived from the item's TUF target
path (`channels/<name>/<id>.json`); there is no `_sig.channel` field to
cross-check. An item served from a path outside its channel's namespace
never resolves through TUF ([repository.md §2](repository.md)).

### 1.3 Ordering, updates, and withdrawal

- **Dedup key:** `(channel, id)`. The same `id` MAY appear in different
  channels; it is a different item there.
- **Ordering:** by `date_published` (required) — descending, newest first is
  the app's default presentation. No feed position exists.
- **Update semantics (in-place):** an item with a known `(channel, id)`
  whose **content differs** from the cached copy (byte comparison of the
  signed item) is an **update**: the app re-verifies it (checks above),
  replaces the cached copy, keeps its position and read-state, and MAY mark
  it "updated" (app policy). `date_modified` is display-only.
- **Absence = unpublished (normative).** The index is the snapshot of what is
  published. An item that is **not in the index** is unpublished: the app
  MUST drop it — from display and from the cache — on the next sync. There
  is **no** `_sig.withdrawn` flag, no tombstones, no retention windows, and
  no archives. A publisher who wants content gone removes the index entry
  (and re-signs the channel role metadata); the file may remain on the host,
  but unindexed targets are never fetched by compliant clients.
  Re-publication of the same `id` after unpublishing is a **new
  publication**: the client treats it as a new item (its position is set by
  `date_published`).
- **Absence ≠ rollback:** metadata versioning + client version memory
  ([repository.md §5](repository.md)) keep a re-signed index from going
  backwards; unpublishing is always a signed, forward metadata change.
- **Backfill:** a new subscriber fetches the complete published set (the
  index plus every item file); there is no pagination and no older-than-now
  history inside the protocol. The company's own website remains the
  long-term archive — Keryx is a broadcast snapshot, not an archive
  ([design/why.md §4.3](../design/why.md)).

### 1.4 Rendering and links

- `content_html` renders in the app's isolated, scriptless container
  (CONTENT.md D2); the app chrome (company name + confirmed origin, footer
  reminder) is outside the container and cannot be painted over.
- Links are intercepted and transparent: the real destination domain is
  shown; no auto-open (CONTENT.md D5). There is no item-level `url` field —
  the message body is self-contained.
- Media is either inline (data URLs, covered by the item hash), a linked
  `image` (always hash-pinned via `image_sha256`), or an
  `attachments` entry (hash-verified when `sha256` present). Remote-media
  privacy preferences apply to linked media.
- Footer reminder "This channel will never ask you for a password, seed, or
  code" stays **structurally true**: no forms, no reply paths, no
  interactive content (CONTENT.md D3).

---

## 2. Authors Role (Optional Protocol Extension)

**Definition (normative).** A channel is **authored** iff `targets.json`
`delegations` contains a role named `channels.<channel>.authors`. The
delegation carries the author key objects, `keyids`, and `threshold`
(default 1); it is **master-signed** — the channel key MUST NOT be able to
modify it, otherwise the distributor could self-authorize. The role:
- has the same `paths` as the channel role (`channels/<channel>/*`) and is
  **not** terminating (target resolution always lands in the channel role,
  which lists every item — the authors role pins **no targets**);
- is a leaf (never re-delegates);
- has a role metadata file (`channels.<channel>.authors.json`) signed by
  the author keys per threshold, pinning no targets, pinned by
  `snapshot.json` (verified with go-tuf v2.4.2; a role with no targets is
  valid TUF — [repository.md §2](repository.md)).

**Signing (normative):** every item in an authored channel MUST carry `sig`
with at least `threshold` valid Ed25519 signatures (OLPC rule, §1.2) by
keys in the authors role's `keyids`. Additional entries (e.g. channel-key
signatures, for portability) MAY be present but are not load-bearing.

**Reader verification (normative):** the app MUST verify before display:
missing signatures, insufficient threshold, bad signature, or unknown
`keyid` → item rejected, never shown; no lower-trust state.

**Scope:** per channel. Verification is against the item's own channel
(derived from the path). A key listed under `security` does not authenticate
items in `marketing`; `marketing` is governed by its own entry (or none).

**Key separation (normative):** the authors role's `keyids` MUST NOT
intersect the channel role's `keyids` — otherwise the channel key holder
could author items and the separation is void (the reference tool refuses
overlap). Custody separation — authors hold author keys, the publisher holds
channel keys — is a deployment property the protocol cannot verify.

**Rotation:** add the new keyid alongside the old (threshold-1 overlap),
then drop; the authors role metadata is re-signed by the new key set during
the overlap. Verification is strict (§1.2): items signed by a key that is
no longer listed are dropped on the next client fetch — even if they were
displayed before. Therefore the publisher **MUST re-sign the channel's
items with the new key(s) during the overlap** (while old keys are still
valid); departing authors' keys MAY stay listed until their items are
re-signed or leave the index. Re-signed items keep their `id` and dates and
do not re-appear as new.

**Why the authors role exists:** "authors approve, channel distributes".
An author compromise can author items, but they reach users only if the
publisher publishes them (CI review gate = policy, not protocol); a
channel-key compromise can re-pin/withhold/unpublish but cannot forge items
in an authored channel. Master authorizes *who* the authors are
([design/why.md §4.11](../design/why.md)).

**Channels without an authors role:** the channel role's keys authorize item
signing (§1.2) — the single-publisher model.

---

## 3. Private (Per-Order) Feeds

Access control is transport-level; this is the one place where per-user data
legitimately exists. Per-order feeds use a **capability-URL signed
document**: a 128-bit unguessable
token in the URL
(`https://eshop.example.com/channels/tracking/<token>/feed.json`).
**Token format (normative):** 22 chars, base64url (RFC 4648 §5, no padding)
= 128 bits from a CSPRNG; URL-safe by construction; the app MAY sanity-check
the segment's charset/length. Note when doing so: 16 bytes encode to 22
characters whose **last character carries only 2 meaningful bits**, so just
four alphabet values (`A`, `Q`, `g`, `w`) are valid in that position — a
charset check MUST NOT reject a token on that basis.

**Trust model — one authority, by design (normative statement).** A private
feed has **no authors role and no channel role**: the pattern entry's keys
(the engine key, held by the eshop backend or the logistics partner) are the
single authority, and the engine is both author and publisher — it can
rewrite the whole document (items, removals, `version`, `expires`,
`expired`) at any time. This is a deliberate simplification compared with
public channels (channel key distributes, optional authors role authors): a
per-order feed has one issuer per order, is small, short-lived, and
PII-bearing, so author/publisher separation would add machinery without a
matching threat. What bounds the engine:

- the **master** authorizes the pattern (namespace + keys + threshold) once
  in `targets.json` — the engine cannot widen its own pattern, change its
  own keys, or reach outside its namespace;
- each order is a **separate capability** (128-bit token), so a compromise
  is scoped to one pattern entry — one engine/partner = one compromise
  scope, never the whole company;
- the `url` binding fails cross-order mix-ups, and clients verify strictly:
  an unverifiable document is never displayed;
- `expires` bounds the window, and `expired: true` ends the feed — after it
  the engine cannot make clients re-poll.

The residual risk, stated plainly: a **compromised engine can rewrite
content within its own orders** (substitute items, remove items, extend
within `expires`) — accepted, order-scoped, transient
([`../design/threats.md`](../design/threats.md)).

- **Document shape:** a single signed document — **not** a JSON Feed
  document and **not** a TUF target. Items use the public item format
  ([§1.1](#11-item)) **without** `sig` — the document signature covers
  everything, and private items are never standalone.

```json
{
  "v": 1,
  "channel": "tracking",
  "url": "https://eshop.example.com/channels/tracking/<token>/feed.json",
  "version": 4,
  "expires": "2026-03-21T00:00:00Z",
  "expired": false,
  "items": [ …public item fields, no `sig`… ],
  "sig": [ { "keyid": "<K_engine>", "sig": "<base64url>" } ]
}
```

- `v` — schema version (1). Unknown `v` → the document is dropped.
- `channel` — MUST equal the pattern entry's `channel` (a label, see below).
- `url` — the feed's canonical capability URL (token included). MUST
  equal the URL the client actually fetched (origin + path, exact).
  Binds the signed document to its capability URL/order, so a document
  served for the wrong token (cross-order mix-up) fails the check.
- `version` — monotonic per feed; anti-rollback / silent-removal
  detection via client version memory.
- `expires` — the order window end; anti-freeze. Refreshed whenever the
  feed is updated (no per-feed cron needed).
- `expired` — boolean, default `false`. `true` = finished, no further
  updates; **MANDATORY** at window end (see below).
- `items` — the published set of this order, in the public item format
  (§1.1) minus `sig`. **Absence = removed**: the engine rewrites the
  document (version+1, `expires` refreshed); the app replaces the item set
  and drops absent ids — the same semantics as public channels (§1.3).
- `sig` — Ed25519 over the **OLPC canonical JSON** of the whole document
  with the `sig` field removed, by the entry's key(s) (threshold as in the
  entry). Covers everything — byte integrity and authenticity in one
  signature.

- **Authorization:** a `custom.private_feed_patterns` entry in `targets.json`
  (master-signed): `{channel, pattern, keys, keyids, threshold}` (`keys` =
  key objects for the entry's keyids, normative,
  [repository.md §2](repository.md)). The master key authorizes the namespace
  once; the feed engine creates feeds under it dynamically, at checkout, with
  no master involvement. **The engine MAY be the company's own
  infrastructure or a third-party partner** (e.g. a logistics provider): the
  pattern's `keys` are whoever signs the feed; one pattern entry per
  engine/partner = one compromise scope. The `channel` field is a **label
  only — NOT a TUF role** (no `channels.tracking.json`, no role metadata, no
  channel role in `delegations`). Because it is a label, it **MAY** coincide
  with a public channel name without any relationship forming between the
  two (normative): a private feed's items are **never** deduped against a
  public channel's — the private dedup key is **(pattern entry, feed URL,
  `id`)**, not (channel, `id`) — and the authors role **never** applies
  to private feeds, whose only authorization is this entry's keys. TUF has no
  concept of such feeds (they are never pinned), so this is the one place
  authorization lives outside TUF structures — master-signed, same trust.
  Pattern matching: origin-exact; wildcard only in the path at segment
  boundaries (`*` = one segment; the token is its own segment; never in
  host/query). Example pattern:
  `https://eshop.example.com/channels/tracking/*/feed.json`.
- **App enforcement (normative sequence):** (0) the response is within the
  app's size limit — private feeds are **not** TUF targets, so no `length` is
  pinned anywhere; the app **MUST** enforce a maximum document size
  (RECOMMENDED 1 MB) and abort the download beyond it, since a compromised
  engine could otherwise stream unbounded data; (1) URL matches an authorized
  pattern (origin-exact per pattern); (2) capability token present (from the
  join QR payload); (3) whole-document signature valid against the entry's
  keys (OLPC rule above), `channel == entry.channel`, and `url`
  equals the fetched URL; (4) `version` not older than last seen; (5)
  `expires` not passed — stale means keep cache + retry; the feed
  closes only on `expired: true`, a later 404/410, or user removal (below).
- **Pattern removal:** if the authorizing `private_feed_patterns` entry
  disappears (master-signed removal/rotation), the app **stops syncing the
  feed and keeps cached items visible** (same as `expired`); it MAY show a
  "delivery feed no longer available" note. It never deletes cached items
  on pattern removal.
- **Not TUF targets** — dynamic per order, never hash-listed in the repo.
  `expired: true` at end of window is **MANDATORY** (the publisher MUST set
  it; the app never closes on `expires` alone — it keeps the cache and
  retries until `expired: true`, 404/410, or the user removes the order).
  `expired` means *finished, no further updates*: the app stops polling the
  feed, marks it closed, and keeps the verified cached items (e.g. the
  final "delivered" status) visible until the user removes them; the
  capability URL MAY be kept locally so a backup/restore can re-fetch the
  final state while the eshop still serves it.
- **Handling (normative):** HTTPS only; `Cache-Control: private` (no CDN);
  no third-party resources (Referer leakage — must not leak the token);
  **deployment must redact the token path from access logs** (default hosts
  log full paths). **The join side leaks too:** the capability token travels
  in the join URL's `p` query parameter, so the join fallback page MUST set
  `Referrer-Policy: no-referrer`, MUST NOT include third-party resources,
  and the join host MUST redact the `p` parameter from access logs
  ([core.md §3](core.md)). Capability URLs are single-issuance — no protocol
  mechanism exists to deliver a rotated token, so a long window is covered
  by 128-bit unguessability + signatures, not rotation. On `expired: true`
  (or a later 404/410) the app stops polling and marks the feed closed — it
  does **not** delete the cached items: the user keeps the final state
  (delivered, invoice) until they explicitly remove the order (the
  capability URL may then be dropped, or kept at user preference). Browser
  history / paste-into-other-tools exposure is an accepted risk (same as
  the printed tracking number).
- **PII:** this is the one place where PII legitimately exists (delivery
  address, tracking, invoice) — never in public feeds, transient only.
