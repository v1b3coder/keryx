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

1. **The relay carries no content. Ever.** A wake-up request contains only
   an opaque **source hash** (§3) and counters. **Identifiers are not
   content:** a wake-up may name *which* company/channel/order to refresh,
   never what a message says (no title, body, status, amount, URL). The
   relay never sees the underlying identity at all — the caller submits
   only the source hash, from which the relay derives the delivery topic;
   on **topic-based legs** (FCM, ntfy topics) the identifier travels in the
   **topic itself** (the device already subscribed to it; the payload can
   be nearly empty); on **registry legs** (WebPush) no topic exists at
   delivery, so the payload MUST carry the **topic string** (§4) and the
   device maps it to the followed company/channel/order locally. This is
   what bounds the relay's power: a fully malicious or compromised relay
   can only **spam or withhold wake-up signals** — it cannot forge a
   message (the app fetches and verifies content through the TUF/thread
   path), cannot read anything (content never transits; order capability
   tokens never transit — only their hash), cannot learn which
   company/channel/order a wake-up is for (only opaque hashes), and cannot
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

**Out of scope:** content delivery (CDN/TUF), the protocol itself, company
registration/directory, per-company Firebase projects (impossible in one
app binary — one shared project), iOS direct-APNs delivery (native iOS goes
through FCM topics), the order-thread content sync (the app's job), and how
the app discovers or uses wake-ups (the app's contract).

---

## 2. Terminology

| Term | Meaning |
|---|---|
| **topic** | A string naming a fan-out channel. Derived per §3 from the source hash — by the relay (publish) and by the app (subscription); never chosen freely. See §3. |
| **source hash** | The opaque identifier a caller submits on every wake-up: `sha256` over the derivation input (`company_id` + `channel`, or `order_token`). The relay never sees the input itself. See §3. |
| **registration** | A delivery address on a registry leg, stored in SQLite: a WebPush `PushSubscription` (`endpoint` + `keys`). |
| **publisher** | A company (or a company's department/partner engine) with an API key. Recorded in SQLite with the company origin it serves (informational) and its rate limit. |
| **wake-up** | A data-only push message, never content. |
| **company_id** | The canonical join origin: lowercase ASCII host, punycode for IDNs, no scheme, no port, no trailing slash (e.g. `company.example`), i.e. the confirmed origin after the single canonical redirect (http→https, www→apex) per [`../spec/core.md` §1.2](../spec/core.md). This is the same value the user confirms at pairing. |

---

## 3. Topic Derivation (normative for app, relay, and caller tooling)

Topics MUST be derived deterministically so the app (which subscribes
client-side) and the relay (which publishes) agree without any exchange.
Derivation is **two-stage**:

```
h = base64url_nopad(sha256(company_id + "|" + channel))  // channel wake-up
h = base64url_nopad(sha256(order_token))                 // order wake-up

topic = "n-" + base64url_nopad(sha256("keryx/relay/v1|" + h))
```

- `h` is the **source hash** — `base64url_nopad(sha256(...))` over the
  UTF-8 bytes of the derivation input: 43 chars (32 bytes). This is what
  callers submit and the app computes from its own knowledge. The relay
  derives the topic from `h` and never sees the derivation input.
- `channel` = the bare channel name (`[a-z0-9-_]+`, per
  [`../spec/repository.md` §2](../spec/repository.md)).
- `order_token` = the 128-bit base64url capability token (22 chars, no
  padding) from the join QR payload ([`../spec/core.md` §3](../spec/core.md)).
- `"keryx/relay/v1|"` is a **static, public salt** (domain separator): it
  keeps the topic distinct from `h` itself and from other SHA-256 uses in
  the protocol. It is not secret.
- Output is 45 chars (`n-` + 43). Base64url (RFC 4648 §5 alphanumerics,
  `-`, `_`) is valid in both FCM topic names (`[a-zA-Z0-9-_.~%]`) and ntfy
  topic names (`[-_A-Za-z0-9]`, ≤ 64 chars) — no provider-specific escaping.
- **No type marker.** Neither `h` nor the topic distinguishes channel from
  order wake-ups, and none is needed: the two derivation inputs can never
  be the same string (an order token is exactly 22 chars from
  `[A-Za-z0-9_-]`; a channel input always contains `|`, and in practice the
  host's `.`), so a cross-type match would require an actual SHA-256
  collision (infeasible). The type is resolved client-side from the app's
  own subscription state: the app keeps a topic → item map
  (company/channel or order), and the item's type is inherent in that map.
  The relay and providers see only opaque hashes — they cannot even tell
  whether a wake-up concerns a channel or an order.
- The `n-` prefix namespaces the relay's topics on shared providers (e.g. a
  self-hosted ntfy server used by several apps).

**Why two-stage:** callers (publisher tooling, order engine) know the
derivation input; the app knows it too; the relay knows neither. The caller
sends only `h`; the relay (and the delivery providers, which see only the
topic) never learn the company, channel, or order token — `sha256` is
one-way and the order token is 128-bit unguessable, so neither `h` nor the
topic reveals it.

The relay MUST NOT accept raw topic names or raw derivation inputs from
callers — it derives the topic from `h` (§5.1). Callers MUST NOT submit the
derivation input (`company_id`/`channel`/`order_token`) — only `h`.

---

## 4. Wake-up Payload (canonical)

A single JSON object. **What it carries depends on the leg** (§1):

| | Topic-based legs (FCM, ntfy topics) | Registry legs (WebPush) |
|---|---|---|
| identifier | **in the topic** — the payload carries none | **in the payload** as `t` (the derived topic string) |
| counters | `n` (unread, optional) / `seq` (wake-up counter, optional) — independent; either, both, or neither | same |

```json
// FCM data (native) — identity comes from the message's topic
{ "v": 1, "n": 3 }           // channel wake-up
{ "v": 1, "seq": 7 }         // order wake-up
{ "v": 1, "n": 3, "seq": 7 } // either wake-up, both counters

// WebPush payload — no topic at delivery, so carry it
{ "v": 1, "t": "n-<43 chars>", "n": 3 }
{ "v": 1, "t": "n-<43 chars>", "seq": 7 }
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
  caller (optional; technical/internal, not content-derived). Primarily for
  debugging and missed-wake-up detection: a gap in the sequence means a
  wake-up was lost. Not a delivery guarantee, and never for ordering — the
  app reconciles by fetching; feed ordering is editorial and dedup is
  `(channel, id)`
  ([`../spec/feeds.md`](../spec/feeds.md)). Unrelated to the `_sig.seq`
  dropped from the feed format —
  [`../design/why.md` §8](../design/why.md).
- `n` and `seq` are independent: either, both, or neither may appear.
- Nothing else. In particular: no title, no body, no URL, no company name,
  no status text, no raw order token.

The payload is a hint, never authenticated: the app MUST NOT trust it for
anything beyond *which* company/order to refresh.

---

## 5. Publisher API

Base: `https://<relay-host>/v1/`. All requests and responses are JSON over
HTTPS. Authentication: `Authorization: Bearer <api-key>`; the relay stores
only the SHA-256 hash of the key (shown once at provisioning).

### 5.1 `POST /v1/publish` — fan out a wake-up

Request:

```json
{
  "v": 1,
  "h": "<43-char base64url>",
  "n": 3
}
```

or, for an order thread:

```json
{
  "v": 1,
  "h": "<43-char base64url>",
  "seq": 7
}
```

or both counters:

```json
{
  "v": 1,
  "h": "<43-char base64url>",
  "n": 3,
  "seq": 8
}
```

- `h` — the source hash (§3): `base64url_nopad(sha256(company_id + "|" + channel))`
  for channels, `base64url_nopad(sha256(order_token))` for orders. The
  caller computes it; the relay never sees the input. MUST be exactly 43
  chars of base64url (32 bytes). No type marker: `h` is opaque and the
  relay cannot (and need not) tell a channel hash from an order hash.
- `n` / `seq` — optional, independent; forwarded to the payload (§4).
  `n` is the unread hint (UI), `seq` the monotonic wake-up counter
  (debugging). Either, both, or neither may be present.

Response `200 OK` (synchronous fan-out, concurrent across providers):

```json
{
  "topic": "n-<…>",
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
- Errors: `401` bad/unknown key; `400` schema violation (`h` not 43-char
  base64url); `429` rate limit (see §5.3).

Duplicate wake-ups are harmless (the app diffs content anyway); no
idempotency key is required.

Authorization is key-level: the relay cannot check that `h` belongs to the
publisher's company (it never sees the input). A valid key can therefore
publish a wake-up for any topic whose source hash it knows; public-channel
source hashes are derivable by anyone, so a compromised key can spam other
companies' wake-ups — bounded by per-publisher rate limits (§9).

### 5.2 Registration API (WebPush)

The PWA has **one registration per install** (WebPush has no topics) plus
the set of topics it follows. The relay stores registration ↔ topic
mappings in SQLite.

| Method + path | Body | Meaning |
|---|---|---|
| `POST /v1/registrations` | `{ "endpoint": "https://…", "keys": { "p256dh": "…", "auth": "…" }, "topics": ["n-…", …] }` | Register (or replace by `endpoint`). Returns `{ "id": "<uuid>" }`. |
| `PUT /v1/registrations/{id}` | `{ "topics": [ … ] }` | Replace the followed-topic set (called on follow/unfollow). |
| `DELETE /v1/registrations/{id}` | — | Remove (company deletion / uninstall). |

- `endpoint` MUST be an `https://` URL; the relay never fetches it itself.
- Rate-limited and throttled by IP; optionally gated by a shared app secret
  (`X-App-Key`) embedded in the app build — endpoints are unguessable, but
  an open registration endpoint is a spam surface, so the app key SHOULD
  be configured.
- `topics` are derived per §3; the relay accepts only `n-` topics
  (45 chars) and rejects others.
- **Why the payload still needs `t` (§4):** the registration knows *which*
  topics it follows, but a delivered message carries no topic — so the
  wake-up payload must name it. The registry makes delivery *possible*;
  the payload makes it *legible*.

### 5.3 Limits

Per publisher (configurable): default 60 publishes/minute, burst 120.
Per subscription: topic count ≤ 200. Global: the relay MUST rate-limit
subscription registration by IP.

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
the `n-` topics itself; the relay publishes once per topic.

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
- **Bare ntfy app:** users who follow via ntfy directly get the generic
  display path (publish `{ "title": "Keryx", "message": "New
  message" }`); not part of the app's verified flow. Unguessable `n-`
  names are the capability; ntfy read/write keys MAY be added later (§11).

**Rejected at this stage — UnifiedPush.** UnifiedPush is endpoint-based:
the app registers a per-instance endpoint (RFC 8030-style URL) with the
relay and the relay sends 1:1 — a registry at the relay, and the
identifier forced into the payload. That is exactly the per-device state
this design avoids, so it is rejected at this stage. If the
foreground-service cost of topic-based ntfy proves unacceptable in
practice, UnifiedPush is the fallback and §5.2 covers it unchanged.

---

## 7. Database (SQLite)

WAL mode, single file. Schema:

```sql
CREATE TABLE publishers (
  id            INTEGER PRIMARY KEY,
  api_key_hash  TEXT NOT NULL UNIQUE,      -- sha256hex of the key (shown once)
  name          TEXT NOT NULL,
  company_id    TEXT NOT NULL,             -- company origin the key is issued
                                           --  for (informational; not checked
                                           --  per request — §5.1)
  rate_per_min  INTEGER NOT NULL DEFAULT 60,
  created_at    TEXT NOT NULL
);

CREATE TABLE registrations (
  id         TEXT PRIMARY KEY,             -- uuid
  endpoint   TEXT NOT NULL UNIQUE,
  p256dh     TEXT NOT NULL,
  auth       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  last_seen  TEXT NOT NULL,
  user_agent TEXT
);

CREATE TABLE registration_topics (
  registration_id TEXT NOT NULL REFERENCES registrations(id) ON DELETE CASCADE,
  topic           TEXT NOT NULL,
  PRIMARY KEY (registration_id, topic)
);
CREATE INDEX idx_registration_topics_topic ON registration_topics(topic);

CREATE TABLE event_log (
  id          INTEGER PRIMARY KEY,
  publisher_id INTEGER NOT NULL,           -- publishers.id; rows outlive the record
  topic       TEXT NOT NULL,
  fcm         INTEGER, ntfy INTEGER,
  webpush_sent INTEGER, webpush_failed INTEGER, webpush_removed INTEGER,
  at          TEXT NOT NULL
);
```

- `event_log` is bounded (retention config, default 30 days) and is the
  audit/abuse record; it contains hashes only, never content or tokens.
- Migration: schema version table; the relay refuses to start on a
  mismatched version.

---

## 8. Configuration, Deployment, Operations

- **Single binary** (Go, matching the publisher tooling stack), static
  config via flags/env: listen address, SQLite path, service-account JSON
  path, VAPID keys (or key file), VAPID `sub` contact, global ntfy base,
  TTLs, concurrency caps, rate limits, event-log retention.
- **Provisioning:** a subcommand (`relayctl`-style, or `relay
  publishers add --name … --company company.example`) issues the API key
  (shown once) and creates the publisher record.
- **Secrets** (highest to lowest sensitivity): FCM service account (can
  publish to every topic in the app's project), VAPID private key (can send
  to every registered PWA subscription), API keys (per publisher, hashed at
  rest). All in secret storage; never in the DB or logs.
- **Scale:** one instance; SQLite WAL supports the fan-out volume. If a
  queue is needed later, `event_log` is the natural replay source; no
  protocol change.
- **The relay does not need a TUF client.** It never fetches or verifies
  content; it only forwards identifiers it is told.

---

## 9. Security and Privacy Posture

| What the relay knows | What it never learns |
|---|---|
| publisher identity (API key → publisher record) | user identity, email, phone |
| PWA device ↔ topic mapping (registration registry, targeted model) | device ↔ topic mapping on topic legs (FCM, ntfy — anonymous) |
| opaque source hashes and topic hashes, wake-up volume/timing | company, channel, order identity (inputs never transit); even whether a wake-up concerns a channel or an order |
| WebPush payloads (it encrypts them — server-side only) | order capability tokens (only their SHA-256), content, anything the app fetches afterwards |

- **Bounded power (the load-bearing property):** the relay can spam or
  withhold wake-ups. It cannot forge, alter, or read content — the
  protocol's content-side verification is the only trust-bearing step —
  and it cannot learn *which* company/channel/order a wake-up concerns
  (opaque hashes only). This is why centralizing wake-ups in one shared
  server is acceptable, and it is the reason the relay must never be asked
  to carry content.
- **Residual (accepted):** authorization is key-level, not
  company-level (§5.1) — a compromised publisher key can publish wake-ups
  for any topic whose source hash it knows, including other companies'
  public channels (source hashes of public channels are publicly
  derivable). Impact is limited to spurious wake-ups (spam), never content,
  and is bounded by per-publisher rate limits. If per-company binding is
  ever required, it needs a different design (e.g. keyed salts) — out of
  scope for v1.
- **WebPush targeted model:** the relay holds device ↔ company mapping for
  PWA registrations (unavoidable — WebPush is per-instance). This is the
  same linkage class accepted for APNs/FCM in
  [`../design/why.md` §4.10](../design/why.md), but held by the relay
  instead of the provider. The alternative ("pure" variant: send every
  wake-up to every registration, service worker filters locally) keeps the
  mapping out of the relay at the cost of waking all registered devices
  per publish — recorded here as the privacy-preserving fallback; the
  targeted model is the default.
- **Logging:** source hashes and topic names only; `event_log` never logs
  order tokens or payloads. Access to the relay's DB is a privacy incident
  by itself (PWA registry) — treat as sensitive.
- **Abuse:** per-publisher rate limits, per-IP subscription throttling,
  endpoint validation, VAPID `sub` contact for provider abuse contact.

---

## 10. Integration with the Protocol

- Publisher tooling (`pub`) gains a `push` step: after `publish`/order
  event, compute the source hash (§3) and call `POST /v1/publish`
  (idempotent in effect; failures are non-fatal — content sync covers it).
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
   display path (no Keryx integration) in v1 at all, and do order topics
   on it need read/write keys or just unguessable names?
3. **TTLs** — WebPush default 1 h; FCM/ntfy default (FCM stores up to 4
   weeks; ntfy ephemeral unless configured). What cadence matches the
   protocol's freshness model? (With the §3 single namespace the relay
   cannot distinguish channel from order wake-ups, so any TTL policy is
   uniform.)
4. **Order wake-ups through the relay** — the relay is publisher-facing;
   do order events go through the same `/v1/publish` (they have no
   publisher tooling, they come from the order engine)? Confirm the engine
   gets its own publisher record/API key, possibly with tighter limits.
5. **Relay identity** — who operates it (the app publisher), and what
   governance applies if more than one app ships against it?
