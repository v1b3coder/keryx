# Relay — Unified Notification Relay (Component Specification)

**Status: draft — component specification.** This describes one component of
the Keryx system: the **relay**, the single active server that delivers
*wake-up* notifications to user devices. It is not part of the protocol
specification in [`../spec/`](../spec/core.md); the protocol treats push as
an optional, provider-agnostic optimization
([`../design/why.md` §4.10](../design/why.md)). Wire identifiers are
codename-neutral per [`../spec/core.md` §1.1](../spec/core.md). Working
title: **relay** (placeholder).

---

## 1. Purpose and Scope

The relay is **the one active server component** of the whole system: a
shared service for *all* publishers, replacing per-publisher push
infrastructure. It exposes a small HTTP API that publisher tooling calls on
every publish event, and fans out a **wake-up signal** (never content) to
user devices over three delivery legs:

| Leg | Devices | Mechanism | Registry |
|---|---|---|---|
| **FCM topics** | native Android + iOS (Firebase SDK) | one publish per topic; devices subscribe client-side | **none** (relay never knows devices) |
| **WebPush** | PWA (browsers) | 1:1 send per stored subscription | SQLite (registration ↔ followed topics) |
| **ntfy topics** | de-Googled Android (Keryx app as its own ntfy client) | one publish per topic; app subscribes client-side | **none** |

All three legs are first-class: the PWA is the reference client today, and
native apps are expected — the relay does not distinguish between them.

(UnifiedPush — the endpoint-based app standard — was considered and
rejected at this stage; see §6.3.)

**Hard rules:**

1. **The relay carries no content. Ever.** A wake-up request contains the
   **channel name** (an authorization handle, §5.1), an opaque **source
   hash** (§3), counters, and a **wake-up signature** (§4.1). **Identifiers
   are not content:** a wake-up may name *which* company/channel/order to
   refresh, never what a message says (no title, body, status, amount,
   URL). The relay derives the delivery topic from the verified company and
   the source hash (§3); on **topic-based legs** (FCM, ntfy topics) the
   identifier travels in the **topic itself** (the device already
   subscribed to it; the payload can be nearly empty); on **registry legs**
   (WebPush) no topic exists at delivery, so the payload MUST carry the
   **topic string** (§4) and the device maps it to the followed
   company/channel/order locally. This is what bounds the relay's power: a
   fully malicious or compromised relay can only **spam or withhold wake-up
   signals** — it cannot forge a message (the app fetches and verifies
   content through the TUF/thread path), cannot forge a wake-up (the
   payload signature verifies against the publisher's TUF keys,
   §4.1/§5.5), cannot read anything (content never transits; order
   capability tokens never transit — only their hash), and cannot
   impersonate a publisher.
2. **Best-effort delivery.** All three providers are best-effort; the relay
   provides no acknowledgement to the app, and the app reconciles by
   fetching and verifying on wake-up. A missed wake-up costs latency, never
   correctness.
3. **Optional.** The system is fully functional without the relay
   (polling/background fetch). The relay is an optimization.
4. **Publishers are registration-free.** One API key per publisher; no
   Google/Apple account, no Firebase project, no VAPID key on the
   publisher's side. All provider credentials live at the relay.
   Registration with the relay is either operator-provisioned (§8) or
   self-service when TUF-gated (§5.2); no relay operator action is needed
   for the TUF path.

**Out of scope:** content delivery (CDN/TUF), the protocol itself, company
registration/directory, per-company Firebase projects (impossible in one
app binary — one shared project), iOS direct-APNs delivery (native iOS goes
through FCM topics), the order-thread content sync (the app's job), and how
the app discovers or uses wake-ups (the app's contract).

---

## 2. Terminology

| Term | Meaning |
|---|---|
| **topic** | A string naming a fan-out channel. Derived per §3 from the verified company + source hash — by the relay (publish) and by the app (subscription); never chosen freely. 43 chars, base64url, no prefix. See §3. |
| **source hash** | The opaque identifier a caller submits on every wake-up: `hex(sha256(company_id + "|" + subject))` where `subject` is a channel name or an order token. The relay never sees the subject (it sees the channel *name* as an authorization handle, §5.1; order tokens never transit). See §3. |
| **wake-up signature** | The Ed25519 signature over the canonical wake-up (§4.1) by the TUF-authorized key for the channel: verified by the relay at publish (§5.5) and by the app on delivery (§4.1). |
| **registration** | A delivery address on a registry leg, stored in the registry database: a WebPush `PushSubscription` (`endpoint` + `keys`). |
| **publisher** | A company (or a company's department/partner engine) with an API key. Recorded in the main database with the company origin it serves (binding for TUF-gated publishers, §5.2) and its rate limit. Two registration paths: operator-provisioned (§8) and TUF-gated self-service (§5.2). |
| **wake-up** | A data-only push message, never content. |
| **company_id** | The canonical join origin: lowercase ASCII host, punycode for IDNs, no scheme, no port, no trailing slash (e.g. `company.example`), i.e. the confirmed origin after the single canonical redirect (http→https, www→apex) per [`../spec/core.md` §1.2](../spec/core.md). This is the same value the user confirms at pairing. |

---

## 3. Topic Derivation (normative for app, relay, and caller tooling)

Topics MUST be derived deterministically so the app (which subscribes
client-side) and the relay (which publishes) agree without any exchange.
Derivation is **two-stage**:

```
h = hex(sha256(company_id + "|" + channel))       // channel wake-up
h = hex(sha256(company_id + "|" + order_token))   // order wake-up (same rule)

topic = base64url_nopad(sha256("keryx/relay/v1|" + company_id + "|" + h))
```

- `h` is the **source hash** — lowercase hex of SHA-256 over the UTF-8
  bytes of the derivation input: 64 hex chars (32 bytes). One uniform rule
  for channels and orders: `hex(sha256(company_id + "|" + subject))`. The
  caller computes it; the relay never sees `subject` (it never sees the
  order token; it sees the channel name only as the §5.1 authorization
  handle). Hex is fine for `h` because it is an intermediate — it never
  reaches a provider; only the topic does.
- `channel` = the bare channel name (`[a-z0-9-_]+`, per
  [`../spec/repository.md` §2](../spec/repository.md)).
- `order_token` = the 128-bit base64url capability token (22 chars, no
  padding) from the join QR payload ([`../spec/core.md` §3](../spec/core.md)).
- `"keryx/relay/v1|"` is a **static, public salt** (domain separator): it
  keeps the topic distinct from `h` itself and from other SHA-256 uses in
  the protocol. It is not secret.
- **Company wrap (relay-side, normative).** The topic derivation includes
  `company_id` as supplied by the **relay's verified publisher record**
  (§5.2/§5.5), never by the caller. This makes cross-company wake-ups
  structurally impossible: a key bound to company A publishing a hash of
  company B yields a topic nobody subscribed to (a silent no-op), never a
  spam of B's subscribers.
- Output is **43 chars** (base64url of 32 bytes, no padding, no prefix).
  Base64url (RFC 4648 §5 alphanumerics, `-`, `_`) is valid in both FCM
  topic names (`[a-zA-Z0-9-_.~%]`) and ntfy topic names (`[-_A-Za-z0-9]`,
  ≤ 64 chars) — no provider-specific escaping. No namespace prefix: the
  topic is a pure hash; isolation on shared providers rests on the hash's
  unguessability plus the static salt, and domain separation is the
  company wrap.
- **No type marker.** Neither `h` nor the topic distinguishes channel from
  order wake-ups, and none is needed for delivery: both derive from the
  same rule. The type is resolved client-side from the app's own
  subscription state: the app keeps a topic → item map (company/channel or
  order), and the item's type is inherent in that map. (One honest edge:
  because both inputs are `company_id|subject`, a channel named exactly
  like one of its own order tokens would share that topic — negligible
  probability, and the impact is only a spurious wake; content remains
  TUF-verified independently.)

**Why two-stage:** callers (publisher tooling, order engine) know the
derivation input; the app knows it too; the relay knows only the company
(from its verified record) and the channel name (authorization handle) —
never the order token and never content. The caller sends `h`; the relay
wraps it with the verified company. `sha256` is one-way and the order
token is 128-bit unguessable, so neither `h` nor the topic reveals it.

The relay MUST NOT accept raw topic names or raw derivation inputs from
callers — it derives the topic from `(verified company_id, h)` (§5.1).
Callers MUST NOT submit the `order_token` (only its hash) or a topic name;
the channel name is submitted as the authorization handle.

---

## 4. Wake-up Payload (canonical)

A single JSON object. **What it carries depends on the leg** (§1):

| | Topic-based legs (FCM, ntfy topics) | Registry legs (WebPush) |
|---|---|---|
| identifier | **in the topic** — the payload carries none | **in the payload** as `t` (the derived topic string) |
| counters & authenticity | `seq` (required) / `n` (optional) / `sig` (required) | same |

```json
// FCM data (native) — identity comes from the message's topic
{ "v": 1, "n": 3, "seq": 7, "sig": "…" }   // channel wake-up (threshold 1)
{ "v": 1, "seq": 7, "sig": [ {"keyid": "…", "sig": "…"} ] }  // threshold > 1

// WebPush payload — no topic at delivery, so carry it
{ "v": 1, "t": "<43 chars>", "n": 3, "seq": 7, "sig": "…" }
```

- `v` — schema version (1). Unknown versions: drop the wake-up.
- `t` — the derived topic string (§3). The app maps `t` to its locally
  followed company/channel or order. Only registry legs carry `t`; on
  topic-based legs the app knows the topic from its own subscription state
  (and on Android, FCM also exposes it as `from` = `/topics/<topic>` —
  an implementation aid, not a requirement).
- `n` — unread counter hint (optional; informational only). A UI aid: lets
  the app show a badge without fetching. Never authoritative.
- `seq` — monotonic counter of wake-ups for this topic, supplied by the
  caller (**required**; technical/internal, not content-derived). Primarily
  for debugging and missed-wake-up detection: a gap in the sequence means a
  wake-up was lost. Because `sig` covers `seq`, it also provides replay
  protection: the app MUST reject a wake-up whose `seq` is ≤ the last seen
  for that topic (§4.1). Not a delivery guarantee, and never for ordering —
  the app reconciles by fetching; feed ordering is editorial and dedup is
  `(channel, id)`
  ([`../spec/feeds.md`](../spec/feeds.md)). Unrelated to the `_sig.seq`
  dropped from the feed format —
  [`../design/why.md` §8](../design/why.md).
- `sig` — the wake-up signature (§4.1), **required by default for all
  channels and all legs**.
- Nothing else. In particular: no title, no body, no URL, no company name,
  no status text, no raw order token.

The payload is no longer a bare hint: the app MUST verify `sig` against the
TUF-authorized key(s) for the topic's channel (§4.1) and MUST drop a wake-up
whose signature does not verify or whose `seq` is stale. Verification uses
keys the app already holds from its own chain verification — the same
authorization that gates content.

### 4.1 Wake-up signature

```
sig = Ed25519( "keryx/wakeup/v1|" ‖ JCS({ v, t, n?, seq }) )
```

- Signed bytes: the domain separator `"keryx/wakeup/v1|"` followed by the
  **JCS (RFC 8785)** canonical JSON of the wake-up object (`v`, `t` when
  present on registry legs — on topic-based legs the app reconstructs `t`
  from its own subscription, since `t` is not transmitted — optional `n`,
  required `seq`). JCS and domain separation are mandatory: Ed25519
  signatures are reusable across message types, and the protocol's item
  signatures use the same discipline ([`../spec/feeds.md`](../spec/feeds.md)).
- `sig` shape follows the role's threshold (§5.5):
  - threshold 1: a single base64url signature.
  - threshold > 1: an array of `{ "keyid": "<hex>", "sig": "<base64url>" }`,
    at least `threshold` distinct keyids, each verifying over the same
    signed bytes.
- **Key resolution:** the authorized keys for the channel come from the
  publisher's verified TUF metadata — the `channels.<channel>` delegation
  for public channels, the matching `custom.private_feed_patterns` entry
  for private/order channels (both are looked up by the channel name; if a
  name exists in both, a signature from either set is accepted). Keyids are
  computed per the protocol (SHA-256 hex of the canonical key object).
- **Who verifies:** the relay at publish (§5.5) and the app on delivery.
  The same `sig` is forwarded on all three legs, so a malicious relay, push
  service, or open ntfy platform cannot forge a wake-up — it can only spam
  or withhold. A forged or unverifiable wake-up is dropped by the app
  before any fetch.

---

## 5. Publisher API

Base: `https://<relay-host>/v1/`. All requests and responses are JSON over
HTTPS. Authentication: `Authorization: Bearer <api-key>`; the relay stores
only the SHA-256 hash of the key (shown once at provisioning). For
TUF-registered publishers the wake-up signature (§4.1) is additionally
verified against the live chain (§5.5).

### 5.1 `POST /v1/publish` — fan out a wake-up

Request (one shape for channels and orders — no type marker):

```json
{
  "v": 1,
  "channel": "marketing",
  "h": "<64-char hex>",
  "seq": 7,
  "n": 3,
  "sig": "<base64url>"
}
```

- `channel` — the **authorization handle**: a public channel name
  (`channels.<name>` delegation) or, for private/order channels, the
  pattern entry's `channel` label (`custom.private_feed_patterns`). The
  relay looks up the channel's authorized keys by this name (§5.5).
  Callers MUST NOT submit the raw `order_token` here — only the pattern
  label plus `h`.
- `h` — the source hash (§3): `hex(sha256(company_id + "|" + subject))`;
  the caller computes it, the relay never sees `subject` (for orders it
  never sees the token at all). MUST be exactly 64 hex chars (32 bytes).
  Opaque: the relay treats it as a hash and does not validate it against
  the channel name (that would be a public/private branch — there is
  none).
- `seq` — **required**; monotonic per topic, supplied by the caller,
  forwarded to the payload (§4). Enables missed-wake-up detection and
  replay protection (§4.1).
- `n` — optional unread hint (UI), forwarded to the payload (§4).
- `sig` — the wake-up signature (§4.1): a single base64url signature
  (threshold 1) or an array of `{keyid, sig}` (threshold > 1). The relay
  verifies it per §5.5 when the publisher is TUF-registered; for
  operator-registered publishers it is forwarded unverified (clients still
  verify).

Response `200 OK` (synchronous fan-out, concurrent across providers):

```json
{
  "topic": "<43 chars>",
  "delivered": { "fcm": 1, "ntfy": 1,
                 "webpush": { "sent": 0, "failed": 0, "removed": 0 } }
}
```

- `fcm`/`ntfy` are `1` if the leg is enabled in the relay config and the
  publish was accepted (a topic with zero subscribers still counts as
  dispatched — FCM/ntfy do not error on empty topics).
- `webpush`: `sent` = registrations for this topic the provider accepted;
  `failed` = transient errors (retried, then counted); `removed` =
  registrations deleted because the provider said they are dead.
- `202 Accepted` MAY be returned when the relay is under load; the request
  is then queued (implementation detail — ordering between the response and
  delivery is not part of the contract).
- Errors: `401` bad/unknown API key; `400` schema violation (`channel`
  unknown or malformed, `h` not 64 hex, `seq` missing, malformed `sig`);
  `403` signature invalid or channel not authorized for this publisher
  (§5.5); `429` rate limit (see §5.4).

Duplicate wake-ups are harmless (the app diffs content anyway); no
idempotency key is required.

Authorization is **company- and role-scoped for TUF-registered
publishers**: the signature must verify against the current authorized
keys for the channel (§5.5), so a compromised key cannot publish wake-ups
for sibling channels or other companies, and a withdrawn key is rejected
immediately. **Operator-registered publishers remain key-level** (§8): the
relay cannot verify their signatures, so a valid key can publish any
channel/hash it knows — bounded by rate limits (§5.4); clients still
verify signatures before acting on the wake-up.

### 5.2 `POST /v1/publishers` — TUF-gated self-service registration

Optional (enabled by config, §8). Binds a publisher record to a
`company_id` using the company's own TUF metadata as the anchor — **no
relay operator action**. This endpoint never sends notifications; it only
creates/refreshes the binding.

Request: `{ "company_id": "company.example" }`

Flow:

1. The relay validates the hostname and fetches
   `https://<company_id>/.well-known/keryx/root.json` (HTTPS only, no
   redirects, bounded size/timeout — SSRF hardening), plus the
   `N.root.json` chain-walk, exclusively from the well-known anchor
   (same rule as the app, [`../spec/repository.md` §2](../spec/repository.md)).
   TOFU: the anchor is that this origin served valid root metadata.
2. The relay reads `custom.repo_base`, fetches `timestamp.json`,
   `snapshot.json`, `targets.json`, and verifies the chain (standard TUF,
   `consistent_snapshot: false`). **Metadata only** — never feed files or
   content.
3. The relay binds: stores the pinned root + verified metadata state
   (anti-rollback versions) and the authorization derived from
   `targets.json` (channel delegations + `private_feed_patterns`) per
   publisher (§5.5, §7).
4. Returns the API key (shown once, hashed at rest) and the publisher id.

- Idempotent: re-POSTing the same `company_id` **refreshes** the binding
  (new metadata, new keys, new chain state) — this is the key-rotation and
  new-channel path.
- Errors: `400` malformed `company_id`; `401` invalid/absent credentials
  (endpoint may be gated by an operator token); `503` TUF metadata
  unreachable or unverifiable.
- Role rights (who may sign wake-ups, enforced at publish — §5.5):

| TUF role | Wake-up authority |
|---|---|
| `channels.<channel>` (channel key, CI) | signs wake-ups for **that channel** |
| `private_feed_patterns` entry keys (engine key) | signs wake-ups for orders under **that pattern** |
| `root` / `targets` (master, offline) | none — the metadata *is* the registration proof; master never signs wake-ups |
| `snapshot` / `timestamp` (online ops key) | **none** — freshness key only; giving it wake-up authority would expand its blast radius to all channels |
| `editor` keys | **none** — item-level signing only |

### 5.3 Registration API (WebPush)

The PWA has **one registration per install** (WebPush has no topics) plus
the set of topics it follows. The relay stores registration ↔ topic
mappings in the **registry database** (§7).

| Method + path | Body | Meaning |
|---|---|---|
| `POST /v1/registrations` | `{ "endpoint": "https://…", "keys": { "p256dh": "…", "auth": "…" }, "topics": ["<43 chars>", …] }` | Register (or replace by `endpoint`). Returns `{ "id": "<uuid>" }`. |
| `PUT /v1/registrations/{id}` | `{ "topics": [ … ] }` | Replace the followed-topic set (called on follow/unfollow). |
| `DELETE /v1/registrations/{id}` | — | Remove (company deletion / uninstall). |
| `POST /v1/registrations/{id}/heartbeat` | — | Liveness ack (no body): bumps `last_seen`. Sent by the service worker on wake-up receipt, and by the app on foreground. `204 No Content`. |

- `endpoint` MUST be an `https://` URL; the relay never fetches it itself.
- Rate-limited and throttled by IP; optionally gated by a shared app secret
  (`X-App-Key`) embedded in the app build — endpoints are unguessable, but
  an open registration endpoint is a spam surface, so the app key SHOULD
  be configured.
- `topics` are derived per §3; the relay accepts only 43-char base64url
  topics and rejects others.
- **Why the payload still needs `t` (§4):** the registration knows *which*
  topics it follows, but a delivered message carries no topic — so the
  wake-up payload must name it. The registry makes delivery *possible*;
  the payload makes it *legible*.
- **Lifecycle:** `created_at`, `last_seen` (heartbeat / registration
  update) and `last_sent_at` (per registration↔topic row, bumped when the
  relay attempts a send) are tracked for garbage collection (§7).

### 5.4 Limits

Per publisher (configurable): default 60 publishes/minute, burst 120.
Per subscription: topic count ≤ 200. Global: the relay MUST rate-limit
subscription registration by IP. TUF metadata refreshes are rate-limited
per company (see §5.5).

### 5.5 TUF authorization at publish

For TUF-registered publishers, every publish is authorized against the
**live chain** — the relay verifies that the signing key is still valid
and not withdrawn:

- **Per-company TUF client:** root pinned at registration (§5.2);
  `timestamp → snapshot → targets` fetched from `custom.repo_base` and
  verified with the standard chain walk (anti-rollback via versions,
  anti-freeze via `expires`).
- **Resolution:** from the current verified `targets.json`, the relay
  extracts the authorization for the wake-up's `channel` name: the
  `channels.<name>` delegation (public) or the matching
  `private_feed_patterns` entry (private/order); if the name exists in
  both, either set is accepted. The signature must satisfy the role's
  **threshold** (keyids computed per the protocol): threshold 1 → one
  valid sig from any key of the set; threshold > 1 → at least `threshold`
  distinct authorized keyids (§4.1).
- **Cache/refresh:** verified metadata is cached per company with its
  version state (persisted in the main DB, §7) and refreshed in the
  background on a cadence (config; the protocol's own timestamp cadence is
  24–72 h) and on demand when stale. The common publish is a pure
  signature check against the cached authorization — no network.
- **Fail policy (fail closed):** cached metadata that is **unexpired** is
  used; if it is expired or absent and the refresh fails, the publish is
  **rejected** (503, alarm) rather than accepted on trust. Cost: a
  publisher's metadata outage temporarily blocks its wake-ups —
  acceptable, since wake-ups are best-effort and polling is the backstop.
- **Replay:** the relay keeps an in-memory LRU of `(topic, seq)` and drops
  duplicates before fan-out (bandwidth protection); the app's monotonic
  `seq` check per topic is the enforcement point (§4.1).

---

## 6. Delivery Legs

### 6.1 FCM (native Android + iOS)

- **Config:** one shared Firebase project (the app's project — one app
  binary = one Firebase project). Service account JSON path in relay
  config. OAuth2 access token fetched from
  `https://oauth2.googleapis.com/token` and cached until ~5 min before
  expiry.
- **Publish:** `POST https://fcm.googleapis.com/v1/projects/<project>/messages:send`
  with `{ "message": { "topic": "<derived>", "data": { <wake-up fields> },
  "android": { "priority": "high" } } }`. Field values in `data` are
  strings; the app parses JSON from a single field or individual fields
  (implementation choice — both are valid; the schema is §4).
- **iOS:** topics work through the Firebase SDK (FCM → APNs proxy); no
  separate APNs handling at the relay. Honest caveat: data-only messages
  arrive on iOS as **silent pushes** — Apple may throttle or defer them,
  they never display a notification by themselves (the app must surface one
  after fetching), and delivery when the app is terminated is not
  guaranteed. Wake-ups on iOS are best-effort; background fetch is the
  backstop (§1, hard rule 2).
- **Errors:** `401/403` (credentials) → alarm; `429` → back off; `404`/
  `INVALID_ARGUMENT` on a topic → log and count as dispatched (empty topic
  is not an error). Transient errors retried with exponential backoff
  (max 3 attempts).
- **Observability:** the relay sees topic names (opaque) and volume — never
  devices.

### 6.2 WebPush (PWA)

- **Config:** one VAPID keypair (RFC 8292). Public key embedded in the PWA
  (`applicationServerKey`); private key in relay config. No registration
  with any vendor.
- **Publish:** for each subscription with the topic: `POST <endpoint>` with
  an RFC 8291 encrypted payload (ECDH P-256 + HKDF + AES-128-GCM), headers
  `Authorization: vapid t=<JWT ES256>, k=<public>`, `TTL: 3600` (config,
  default 1 h), `Urgency: normal`. JWT claims: `aud` = endpoint origin,
  `exp` = now + 12 h (≤ 24 h), `sub` = `mailto:` contact from config
  (Chrome requires it).
- **Errors:** `404`/`410` → delete the subscription (count `removed`);
  `429`/`5xx` → retry with backoff, then `failed`. Concurrent fan-out with
  a configurable concurrency cap.
- **Payload:** the §4 JSON **including `t`** (the topic string), encrypted
  (the push service cannot read it).

### 6.3 ntfy topics — de-Googled (chosen path)

**Chosen: topic-based ntfy, registry-free** — the de-Googled leg has the
same shape as FCM. The Keryx app embeds an ntfy client and subscribes to
the derived topics itself; the relay publishes once per topic.

- **Config:** a **single global** ntfy base in relay config (e.g. the
  operator's own ntfy server, or `https://ntfy.sh` as default); absent =
  leg disabled. No per-publisher custom URLs in v1 — a per-publisher ntfy
  base is a future extension. The app ships with the same base (the app
  publisher operates the relay); how the app is told of it is the app's
  contract, not the relay's.
- **Publish:** `POST <ntfy_base>/<topic>` with the §4 payload as the JSON
  body. No per-publisher credentials. Errors: `4xx` → log/count;
  `5xx`/timeout → retry with backoff.
- **Accepted trade:** the app holds the connection itself (foreground
  service; battery; Android 15+ `remoteMessaging` foreground-service
  type) — the price of a registry-free de-Googled leg.
- **Open platform, signed payloads:** ntfy topics are open — anyone can
  publish to (and subscribe to) a topic name without credentials. The
  wake-up signature (§4.1) is what makes this safe: a forged wake-up is
  dropped by the app before any fetch, because it does not verify against
  the publisher's TUF keys. What remains observable is volume/timing on
  public channels; order topics stay unguessable (128-bit capability
  names). ntfy read/write keys remain a possible later hardening (§11).
- **Bare ntfy app:** users who follow via ntfy directly get the generic
  display path (publish `{ "title": "Keryx", "message": "New
  message" }`); not part of the app's verified flow. Unguessable topic
  names are the capability; ntfy read/write keys MAY be added later (§11).

**Rejected at this stage — UnifiedPush.** UnifiedPush is endpoint-based:
the app registers a per-instance endpoint (RFC 8030-style URL) with the
relay and the relay sends 1:1 — a registry at the relay, and the
identifier forced into the payload. That is exactly the per-device state
this design avoids, so it is rejected at this stage. If the
foreground-service cost of topic-based ntfy proves unacceptable in
practice, UnifiedPush is the fallback and §5.3 covers it unchanged.

---

## 7. Database (SQLite)

WAL mode. **Two databases**, versioned independently (schema version
  tables; the relay refuses to start on a mismatched version):

- **Main database** (`-db`): `publishers`, `publisher_tuf`, `event_log` —
  occasional writes (provisioning, publishing, metadata refresh).
- **Registry database** (`-registry-db`): `registrations`,
  `registration_topics` — write-heavy (registration/follow/unfollow/
  heartbeat churn); kept separate so the PWA registry's volume never
  touches the main production DB.

Main database schema:

```sql
CREATE TABLE publishers (
  id            INTEGER PRIMARY KEY,
  api_key_hash  TEXT NOT NULL UNIQUE,      -- sha256hex of the key (shown once)
  name          TEXT NOT NULL,
  company_id    TEXT NOT NULL,             -- company origin the key is issued for
  rate_per_min  INTEGER NOT NULL DEFAULT 60,
  tuf           INTEGER NOT NULL DEFAULT 0, -- 1 = TUF-registered (§5.2)
  created_at    TEXT NOT NULL
);

CREATE TABLE publisher_tuf (               -- TUF-registered publishers only
  publisher_id  INTEGER NOT NULL REFERENCES publishers(id) ON DELETE CASCADE,
  root          TEXT NOT NULL,             -- pinned root.json (TOFU anchor)
  root_version  INTEGER NOT NULL,
  chain_state   TEXT NOT NULL,             -- verified timestamp/snapshot/targets versions + authorization (keys/threshold per channel)
  refreshed_at  TEXT NOT NULL
);

CREATE TABLE event_log (
  id          INTEGER PRIMARY KEY,
  publisher_id INTEGER NOT NULL,           -- publishers.id; rows outlive the record
  topic       TEXT NOT NULL,
  fcm         INTEGER, ntfy INTEGER,
  webpush_sent INTEGER, webpush_failed INTEGER, webpush_removed INTEGER,
  at          TEXT NOT NULL
);
```

Registry database schema:

```sql
CREATE TABLE registrations (
  id         TEXT PRIMARY KEY,             -- uuid
  endpoint   TEXT NOT NULL UNIQUE,
  p256dh     TEXT NOT NULL,
  auth       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  last_seen  TEXT NOT NULL,                -- heartbeat / registration update
  user_agent TEXT
);

CREATE TABLE registration_topics (
  registration_id TEXT NOT NULL REFERENCES registrations(id) ON DELETE CASCADE,
  topic           TEXT NOT NULL,
  last_sent_at    TEXT,                    -- bumped when the relay attempts a send to this (registration, topic)
  PRIMARY KEY (registration_id, topic)
);
CREATE INDEX idx_registration_topics_topic ON registration_topics(topic);
```

- `event_log` is bounded (retention config, default 30 days) and is the
  audit/abuse record; it contains hashes and topic names only, never
  content or tokens.
- **Garbage collection (registry):** `404/410` from the push service
  deletes the registration immediately (authoritative death). Otherwise a
  registration is a GC candidate only if it was **sent to and never
  acked**: `max(last_sent_at over its topics) > last_seen` and
  `now − that last_sent_at > TTL` (config). A low-traffic channel never
  bumps `last_sent_at`, so its registrations are never swept — the missing
  ack is explained by the absence of sends, not by a dead device.
- Migration: both databases have their own schema version table; the
  relay refuses to start on a mismatched version.

---

## 8. Configuration, Deployment, Operations

- **Single binary** (Go, matching the publisher tooling stack), static
  config via flags/env: listen address, main SQLite path and registry
  SQLite path, service-account JSON path, VAPID keys (or key file), VAPID
  `sub` contact, global ntfy base, TTLs, concurrency caps, rate limits,
  event-log retention, TUF-gated registration on/off, TUF refresh cadence,
  registry GC TTL.
- **Provisioning:** a subcommand (`relayctl`-style, or `relay
  publishers add --name … --company company.example`) issues the API key
  (shown once) and creates the publisher record (operator path, key-level
  authorization). The TUF-gated self-service path (§5.2) is separate and
  config-gated.
- **Secrets** (highest to lowest sensitivity): FCM service account (can
  publish to every topic in the app's project), VAPID private key (can send
  to every registered PWA subscription), API keys (per publisher, hashed at
  rest). All in secret storage; never in the DB or logs.
- **Scale:** one instance; SQLite WAL supports the fan-out volume. If a
  queue is needed later, `event_log` is the natural replay source; no
  protocol change.
- **TUF client — authorization metadata only.** The relay maintains a
  per-company TUF client for the publish-time authorization check (§5.5):
  root pinned at registration, `timestamp/snapshot/targets` fetched from
  `custom.repo_base`, verified and cached. It never fetches or verifies
  content — feed files and messages remain the app's TUF job.

---

## 9. Security and Privacy Posture

| What the relay knows | What it never learns |
|---|---|
| publisher identity (API key → publisher record, verified TUF binding for TUF-registered publishers) | user identity, email, phone |
| PWA device ↔ topic mapping (registration registry, targeted model) | device ↔ topic mapping on topic legs (FCM, ntfy — anonymous) |
| channel names (authorization handles) and source/topic hashes, wake-up volume/timing | order capability tokens (only their hash), message content, anything the app fetches afterwards |
| WebPush payloads (it encrypts them — server-side only) | the wake-up *content* semantics — only that something changed |

- **Bounded power (the load-bearing property):** the relay can spam or
  withhold wake-ups. It **cannot forge** them (wake-up signatures verify
  against the publisher's TUF keys — §4.1/§5.5), cannot forge or alter
  content (the protocol's content-side verification is the only
  trust-bearing step for messages), and never sees order tokens or
  message content. This is why centralizing wake-ups in one shared server
  is acceptable, and it is the reason the relay must never be asked to
  carry content.
- **Residual (accepted):** for **operator-registered** publishers,
  authorization is key-level (§5.1) — a valid key can publish any
  channel/hash it knows, bounded by rate limits. **TUF-registered**
  publishers are company- and role-scoped: the signature must verify
  against the live chain (§5.5), so a compromised or withdrawn key is
  rejected immediately. In both cases the impact of abuse is spurious
  wake-ups, never content.
- **Wake-up authenticity:** clients verify the wake-up signature before
  fetching (§4.1), so a malicious relay, push service, or open ntfy
  platform cannot inject fake wake-ups or fake unread counters; the relay
  itself verifies at publish for TUF-registered publishers (§5.5).
- **WebPush targeted model:** the relay holds device ↔ company mapping for
  PWA registrations (unavoidable — WebPush is per-instance). This is the
  same linkage class accepted for APNs/FCM in
  [`../design/why.md` §4.10](../design/why.md), but held by the relay
  instead of the provider. The alternative ("pure" variant: send every
  wake-up to every registration, service worker filters locally) keeps the
  mapping out of the relay at the cost of waking all registered devices
  per publish — recorded here as the privacy-preserving fallback; the
  targeted model is the default.
- **Logging:** channel names, source hashes and topic names only;
  `event_log` never logs order tokens or payloads. Access to the relay's
  DBs is a privacy incident by itself (PWA registry) — treat as
  sensitive.
- **Abuse:** per-publisher rate limits, per-IP subscription throttling,
  endpoint validation, VAPID `sub` contact for provider abuse contact,
  TUF metadata refresh rate limits, fail-closed authorization (§5.5).

---

## 10. Integration with the Protocol

- Publisher tooling (`pub`) gains a `push` step: after `publish`/order
  event, compute the source hash (§3), maintain the monotonic `seq` per
  topic, **sign the wake-up with the channel key** (public channels) or
  the pattern-entry engine key (private/order channels) per §4.1, and
  call `POST /v1/publish` (idempotent in effect; failures are non-fatal —
  content sync covers it).
- The wake-up is an optimization: publishers that don't want the relay
  simply don't call it. How the app learns whether to expect wake-ups is
  the app's contract, not the relay's.

---

## 11. Open Questions

1. **Targeted vs pure (WebPush)** — default is targeted (efficient); is
   the device↔company mapping at the relay acceptable long-term, or does
   the protocol's zero-state stance require the pure variant (send every
   wake-up to every registration, filter locally) or a config switch?
2. **Shared-topic ntfy (bare ntfy app)** — is the generic "New message"
   display path (no Keryx integration) in v1 at all, and do topics on it
   need read/write keys or just unguessable names (the wake-up signature
   already defeats forgery client-side, §6.3)?
3. **TTLs** — WebPush default 1 h; FCM/ntfy default (FCM stores up to 4
   weeks; ntfy ephemeral unless configured). What cadence matches the
   protocol's freshness model? Any TTL policy is uniform (the relay does
   not rely on wake-up type).
4. **Order wake-ups through the relay** — order events go through the same
   `/v1/publish` (`channel` = pattern label). How does the partner engine
   get its publisher record: TUF-gated registration binds the *company*
   domain (§5.2), so does the engine register the same `company_id`
   (subject to operator policy), or does the company issue sub-keys? The
   engine's wake-up signature verifies against the company's pattern-entry
   keys either way.
5. **Relay identity** — who operates it (the app publisher), and what
   governance applies if more than one app ships against it?
6. **Wake-up threshold simplification** — full TUF threshold semantics are
   specified (§4.1/§5.5); if multi-signature wake-ups prove operationally
   expensive, a simplified "any authorized key" rule can be adopted later
   without a protocol change (clients and relay both decide by the same
   metadata).
