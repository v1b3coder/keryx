# Keryx — Feeds, Editor Mode, and Private Feeds

**Status: normative** (RFC 2119 keywords as in [`core.md`](core.md)).
Rationale is informative and lives in [`design/why.md`](../design/why.md).

Covers: the public feed document and item format, including signing and
verification (§1); editor mode (§2); private per-order capability feeds (§3).

---

## 1. Public Feed Document (`channels/<name>/feed.json`)

A JSON Feed 1.1 document; served as a TUF target file. One feed per channel.

### 1.1 Top level

Standard fields + `_sig` extension:

```json
{
  "version": "https://jsonfeed.org/version/1.1",
  "title": "ACME — offers",
  "feed_url": "https://cdn.example.com/keryx/channels/marketing/feed.json",
  "description": "Official announcements from ACME.",
  "icon": "https://company.example/icon.png",
  "favicon": "https://company.example/favicon.png",
  "authors": [ { "name": "ACME s.r.o.", "url": "https://company.example/" } ],
  "language": "en",
  "user_comment": "Signed broadcast; items carry signatures in the _sig extension.",
  "_sig": {
    "about": "https://github.com/…/channel-protocol/_sig"
  },
  "items": [ … ]
}
```

- `_sig.about` describes the extension and appears at least once (**must be
  a real published URL before lock** — it is the extension's identity).
- **Filtering vocabulary: none in v1.** Item `tags` (free-form strings) and
  `language` (RFC 5646) are standard JSON Feed; companies define their own
  tag conventions. The app filters locally on tags/language. If a curated,
  labeled vocabulary is ever needed, it can be added as another optional
  `_sig` field later (readers ignore unknown fields). Per-channel
  `title`/`description` are feed-level.
- **Integrity:** the whole document (wrapper + items) is covered by the TUF
  target hash in the channel role metadata
  ([repository.md §3](repository.md)) — nothing about the feed is forgeable
  by a feed host, and nothing can be silently withdrawn. No feed-level
  self-signature needed.
- **Pagination/archives:** JSON Feed `next_url` (standard) points to older
  items; archive documents are additional targets in the same role
  metadata. `feed_url` is self-description and unique identifier — the app
  **MUST NOT** use it as a fetch URL (it fetches through the TUF client,
  hash-verified; with the default `consistent_snapshot: false` the fetched
  path coincides with the plain path — the rule is about the *verified
  fetch*, never about trusting a URL string inside the document).
- **`next_url` (normative):** MUST be a URL within the repo base (same
  origin; path inside the channel's `paths` namespace) and MUST correspond
  to a TUF target in the same channel role metadata. The app MUST resolve
  it to that target and hash-verify it exactly like the main feed; it MUST
  NOT fetch `next_url` directly. Archive documents are signed/pinned the
  same way as the main page — a forged or withdrawn archive fails hash
  verification.
- **Generic (non-TUF) consumers:** with the default
  `consistent_snapshot: false` there is exactly one copy of each feed — at
  its plain path (`channels/<name>/feed.json`, i.e. the `feed_url`) — and it
  serves both consumers: TUF clients fetch it hash-verified, generic JSON
  Feed readers subscribe to the same URL without the trust enforcement. A
  publisher who opts into `consistent_snapshot: true` MUST additionally
  serve every public feed at its plain path, byte-identical to the
  hash-prefixed TUF target; TUF clients then fetch the hash-prefixed name
  and never trust the plain copy.

### 1.2 Item

```json
{
  "id": "msg-000123",
  "url": "https://company.example/news/msg-000123",
  "title": "Firmware 25.3 released",
  "content_html": "<p>…rich content…</p>",
  "content_text": "Firmware 25.3 released…",
  "summary": "…",
  "image": "https://company.example/media/fw.jpg",
  "date_published": "2026-02-07T14:04:00+01:00",
  "date_modified": "2026-02-08T09:00:00Z",
  "tags": ["wallet"],
  "language": "en",
  "authors": [ { "name": "ACME s.r.o." } ],
  "attachments": [ { "url": "https://company.example/media/fw.pdf",
                     "mime_type": "application/pdf",
                     "size_in_bytes": 123456 } ],
  "_sig": {
    "channel": "marketing",
    "withdrawn": false,
    "resources": {
      "https://company.example/media/fw.pdf": "<sha256, lowercase hex>"
    },
    "signatures": [
      { "keyid": "<K_marketing>", "sig": "<base64url 64 bytes>" }
    ]
  }
}
```

**Signing rule (normative):**

1. Take the item object as published, with the `_sig.signatures` field
   **removed** (all other fields — including `_sig.channel`,
   `_sig.withdrawn`, and `_sig.resources` — are covered).
2. Serialize with **JCS (RFC 8785)**.
3. Sign the bytes with Ed25519 using the key whose `keyid` is listed; repeat
   for each threshold requirement.
4. Store `{keyid, sig}` (base64url, no padding) in `_sig.signatures`.

Verification (strict — every fetched item is checked before it is considered):

- **Editor mode** (`custom.editor_mode.<channel>` defined,
  [repository.md §2](repository.md), §2 below): the app MUST verify that at
  least `threshold` entries verify against `editor_mode.<channel>.keyids`.
  Any failure → item rejected, never displayed. Additional entries (e.g.
  channel-key signatures, for portability) MAY be present but are not
  load-bearing.
- **Default mode** (no `editor_mode` entry): `signatures` is optional.
  Authenticity is the TUF target hash pinned by the channel role metadata
  ([repository.md §3](repository.md)). If `signatures` is present, the app
  MUST verify entries whose `keyid` it knows; entries by **unknown** keys are
  ignored, never a reason to reject (attribution only), but a **known keyid
  whose signature does not verify → item rejected**. Rationale: an absent
  signature claims nothing, while a present-and-failing one is a claim that
  demonstrably does not hold — there is no third state
  ([core.md §2](core.md)). This never makes signatures mandatory; it only
  makes them honest. (The publisher tool validates signatures before writing,
  [clients.md §2](clients.md), so a publisher bug surfaces at publish time,
  not on devices.) Per-item signatures remain the portability mechanism for
  archives, mirrors, and re-publication.
- **Verification gates content, always.** No item is displayed (or kept
  displayed after a re-fetch) without passing the checks above. In
  particular: an item whose editor-mode signatures no longer verify (e.g.
  its key was rotated away) is **dropped on the next fetch — even if it was
  previously displayed** — unless the publisher re-signed it
  ([repository.md §5](repository.md)). Unchanged known items are skipped
  (already verified; the feed is TUF-hash-pinned, so unchanged bytes cannot
  have been swapped); new or changed content is always verified first.

**Channel cross-check (normative):** `_sig.channel` MUST equal the bare
channel name the item arrived from (not the `channels.`-prefixed role name).
The app determines the channel from the feed target path
(`channels/<name>/feed.json`) and verifies the field matches; mismatch →
item rejected.

**Resource integrity (`_sig.resources`, optional extension — normative when
present):** an item MAY pin external resources it references by hash.
`resources` maps an **exact URL** — as it appears in the item
(`attachments[].url`, `image`, or a link/media URL inside `content_html`) —
to the resource's **SHA-256 digest** (lowercase hex string). The map lives
inside the item, so it is covered by the item's signatures and the feed's
TUF target hash like every other field. The algorithm is fixed — SHA-256,
matching keyids and TUF target hashes; there is no agility field (a future
algorithm would be a new `_sig` member, which old readers ignore — the
resource is then merely unpinned for them, never mis-verified). No declared
size: a size limit on downloads is generic app policy (it must cover
unpinned media anyway, and a hash match implies the exact length);
`attachments[].size_in_bytes` remains the standard field for display.

- **Verification:** for a listed URL the app MUST verify the SHA-256 of the
  fetched bytes **before rendering, opening, or saving the resource**.
  Mismatch → the resource is treated as unavailable (never shown or handed
  to the user); the **item itself stays valid** — a swapped file is a
  media-host problem, not a reason to drop an authentic message.
- **Unlisted URLs are ordinary web links** — mutable by design (homepages,
  blog posts, live pages). The self-contained message is the default model;
  pinning is a per-resource choice, RECOMMENDED for high-stakes static
  downloads (PDFs, invoices, firmware images) where a media-host compromise
  could substitute content. It is not meant for live pages.
- **Updating a pinned resource** = publishing the item with the new hash —
  the ordinary in-place update flow (content-diff, below).
- The app MAY surface the distinction (e.g., a "verified download"
  affordance on pinned attachments).

**Ordering / dedup / update semantics:**

- `date_published` (RFC 3339) is **optional**, as in JSON Feed. It is the
  preferred inbox ordering key; when present it MUST be a valid RFC 3339
  timestamp. Items without a valid `date_published` are **not** rejected —
  the app orders them by feed position instead, so items that do carry a
  valid `date_published` are never silently mis-ordered.
- `id` is **stable and unique within its channel** — the dedup key is
  **(channel, id)** (across feeds, mirrors, and pagination of that channel).
  The same `id` MAY appear in different channels; it is a different item
  there. No per-feed sequence number — ordering is editorial.
- **Update semantics (in-place):** an item with a known `(channel, id)` whose
  **content differs** from the cached copy (byte/JCS comparison of the
  signed item) is an **update**: the app re-verifies it (checks above),
  replaces the cached copy, keeps its position (`date_published`) and
  read-state, and MAY mark it "updated" (app policy). A forgotten
  `date_modified` bump cannot cause a missed update; `date_modified` is
  display-only ("updated at").
- **Withdrawal:** `_sig.withdrawn: true` (signed with the item; default
  `false`) — the publisher retracts the item. Withdrawal is just an update
  with the flag set. The app **hides withdrawn items entirely** (no
  tombstone; they are not shown and not counted as unread).
- **Absence is not withdrawal (normative).** An item that simply stops
  appearing in the live feed — rolled off into an archive, or trimmed for
  size — is **retained** in the cache and stays visible. Retraction is only
  ever explicit (`_sig.withdrawn: true`). This is distinct from *failing
  verification*, which drops an item even if it was previously displayed
  (above): a publisher who wants content gone must withdraw it, not delete
  it from the feed.
- **`expired` (feed-level)** = feed finished: no further updates will be
  published — the app stops syncing the feed and keeps the verified cached
  items visible until the user removes them. Expiry never deletes content.
  (For private feeds, `expired: true` is mandatory at window end — §3.)
- Backfill always works: following a channel later, `next_url` pagination,
  and archive feeds all include old items (authentic; duplicates are the
  only thing suppressed; re-signed items with unchanged content do **not**
  re-appear as new).
- **Rich content:** sanitized HTML subset (block/inline elements, links,
  images, tables; no scripts/forms/iframes/embeds; absolute URLs);
  `content_text` always present as fallback. Remote media is referenced,
  not inlined — fetching it is observable to the company's CDN (device-level
  telemetry, not identity); whether to fetch/display it is **the app's
  privacy preference**, not a protocol rule.
- **No credential requests (convention + heuristic):** the protocol never
  carries reply paths or forms; a message asking for passwords/seeds/codes
  is a red flag even from a verified company. UX safeguard, not a content
  restriction.

---

## 2. Editor Mode (Optional Protocol Extension)

**Definition (normative).** A channel is in editor mode iff
`custom.editor_mode.<channel>` is present in the trusted `targets.json`
(`keys` = editor key objects, `keyids` = editor keys, `threshold` = required
valid signatures, default 1). It is master-signed: the channel key MUST NOT
be able to modify it, otherwise the publisher could self-authorize.

- **Signing (normative):** every item in an editor-mode channel MUST carry
  `_sig.signatures` with at least `threshold` valid Ed25519 signatures (JCS
  rule, §1.2) by keys in `editor_mode.<channel>.keyids`. Additional entries
  (e.g. channel-key signatures, for portability) MAY be present but are not
  load-bearing.
- **Reader verification (normative):** the app MUST verify before display:
  missing signatures, insufficient threshold, bad signature, or unknown
  `keyid` → item rejected, never shown; no lower-trust state.
- **Scope:** per channel. Verification is against the item's own channel
  (from the feed path, cross-checked with `_sig.channel`). A key listed
  under `marketing` does not authenticate items in `releases`; `releases` is
  governed by its own entry (or none).
- **Key separation (normative):** `editor_mode.<channel>.keyids` MUST NOT
  contain the channel role's `keyids` — otherwise the channel key holder
  could author items and editor mode is void (the reference tool refuses
  overlap). Custody separation — editors hold editor keys, the publisher
  holds channel keys — is a deployment property the protocol cannot verify.
- **Rotation:** add the new keyid alongside the old (threshold-1 overlap),
  then drop. Verification is strict (§1.2): items signed by a key that is
  no longer listed are dropped on the next client fetch — even if they were
  displayed before. Therefore the publisher **MUST re-sign the channel's
  items with the new key(s) during the overlap** (while old keys are still
  valid); departing editors' keys MAY stay listed until their items are
  re-signed or leave the live feed. Re-signed items keep their `id` and
  dates and do not re-appear as new.
- **Channels without an `editor_mode` entry:** per-item signatures are
  optional, attribution only (§1.2).

Why editor mode exists, and why re-signing is a feature rather than
overhead: [`design/why.md`](../design/why.md).

---

## 3. Private (Per-Order) Feeds

JSON Feed has no privacy semantics; access control is transport-level.
Per-order feeds use a **capability-URL JSON Feed**: a 128-bit unguessable
token in the URL
(`https://eshop.example.com/channels/tracking/<token>/feed.json`).
**Token format (normative):** 22 chars, base64url (RFC 4648 §5, no padding)
= 128 bits from a CSPRNG; URL-safe by construction; the app MAY sanity-check
the segment's charset/length. Note when doing so: 16 bytes encode to 22
characters whose **last character carries only 2 meaningful bits**, so just
four alphabet values (`A`, `Q`, `g`, `w`) are valid in that position — a
charset check MUST NOT reject a token on that basis.

- **Document shape:** a private feed is a **full JSON Feed 1.1 document** per
  §1.1 — `version`, `title`, `feed_url` (the capability URL), `items` — with
  the `_sig` extension per this section. Generic readers can consume it via
  the capability URL (accepted risk, same as the printed tracking number).
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
  `id`)**, not (channel, `id`) — and `custom.editor_mode` **never** applies
  to private feeds, whose only authorization is this entry's keys. TUF has no
  concept of such feeds (they are never pinned), so this is the one place
  authorization lives outside TUF structures — master-signed, same trust.
  Pattern matching: origin-exact; wildcard only in the path at segment
  boundaries (`*` = one segment; the token is its own segment; never in
  host/query). Example pattern:
  `https://eshop.example.com/channels/tracking/*/feed.json`.
- **Self-authenticating document (one file, no mini repo):** the feed is a
  single JSON Feed document signed **as a whole**. The top-level `_sig`
  carries:
  - `about` (extension identity),
  - `channel` — MUST equal the pattern entry's `channel`,
  - `url` — the feed's canonical capability URL (token included). MUST
    equal the URL the client actually fetched (origin + path, exact).
    Binds the signed document to its capability URL/order, so a document
    served for the wrong token (cross-order mix-up) fails the check.
  - `signatures` — Ed25519 over the **JCS (RFC 8785)** bytes of the whole
    document with the **top-level** `_sig.signatures` field removed
    (item-level `_sig.signatures` are covered by the signature), by the
    entry's key(s) (threshold as in the entry). Covers wrapper *and*
    items — byte integrity and authenticity in one signature.
  - `version` — monotonic per feed; anti-rollback / silent-withdrawal
    detection via client version memory.
  - `expires` — the order window end; anti-freeze. Refreshed whenever the
    feed is updated (no per-feed cron needed).

  Per-item `_sig.signatures` MAY remain (uniform format, portability) but
  are not load-bearing here. **Full §1.2 item semantics apply unchanged** —
  append new items, in-place updates (content-diff), withdrawal — the
  engine chooses per event; the whole-document signature covers the updated
  document. There is **no manifest, no per-order TUF metadata** — the trust
  anchor is the master-authorized pattern entry's key.
- **App enforcement (normative sequence):** (0) the response is within the
  app's size limit — private feeds are **not** TUF targets, so no `length` is
  pinned anywhere; the app **MUST** enforce a maximum document size
  (RECOMMENDED 1 MB) and abort the download beyond it, since a compromised
  engine could otherwise stream unbounded data; (1) URL matches an authorized
  pattern (origin-exact per pattern); (2) capability token present (from the
  join QR payload); (3) whole-document signature valid against the entry's
  keys (JCS rule above), `_sig.channel == entry.channel`, and `_sig.url`
  equals the fetched URL; (4) `_sig.version` not older than last seen; (5)
  `_sig.expires` not passed — stale means keep cache + retry; the feed
  closes only on `expired: true`, a later 404/410, or user removal (below).
- **Pattern removal:** if the authorizing `private_feed_patterns` entry
  disappears (master-signed removal/rotation), the app **stops syncing the
  feed and keeps cached items visible** (same as `expired`); it MAY show a
  "delivery feed no longer available" note. It never deletes cached items
  on pattern removal.
- **Not TUF targets** — dynamic per order, never hash-listed in the repo.
  `expired: true` at end of window is **MANDATORY** (the publisher MUST set
  it; the app never closes on `_sig.expires` alone — it keeps the cache and
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
  history / paste-into-generic-reader exposure is an accepted risk (same as
  the printed tracking number).
- **PII:** this is the one place where PII legitimately exists (delivery
  address, tracking, invoice) — never in public feeds, transient only.
