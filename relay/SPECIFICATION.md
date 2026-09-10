# Relay — Unified Notification Relay (Component Specification)

**Status: draft — component specification.** This describes one component of
the Keryx system: the **relay**, the single active server that delivers
*wake-up* notifications to user devices. It is not part of the protocol
specification in [`../spec/`](../spec/core.md); the protocol treats push as
an optional, provider-agnostic optimization
([`../design/why.md` §4.10](../design/why.md)). Wire identifiers are
codename-neutral per [`../spec/core.md` §1.1](../spec/core.md).

Companion reasoning: [`../NOTES.md`](../NOTES.md) (push topic — capability
topics, iOS paths, registration, PWA/WebPush, prior art). Working title:
**relay** (placeholder).

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

(UnifiedPush — the endpoint-based app standard — was considered and
rejected at this stage; see §6.3.)

**Hard rules:**

1. **The relay carries no content. Ever.** A wake-up payload contains only
   opaque identifiers and counters. **Identifiers are not content:** a
   wake-up may name *which* company/channel/order to refresh, never what a
   message says (no title, body, status, amount, URL). Placement is
   leg-dependent — on **topic-based legs** (FCM, ntfy topics) the
   identifier travels in the **topic itself** (the device already
   subscribed to it; the payload can be nearly empty); on **registry
   legs** (WebPush) no topic exists at delivery, so the payload MUST carry
   the **topic string** (§4) and the device maps it to the followed
   company/channel/order locally. This is what bounds the relay's power:
   a fully malicious or compromised relay can only **spam or withhold
   wake-up signals** — it cannot forge a message (the app fetches and
   verifies content through the TUF/thread path), cannot read anything
   (content never transits), and cannot impersonate a publisher.
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
through FCM topics), and the order-thread content sync (the app's job).

---

## 2. Terminology

| Term | Meaning |
|---|---|
| **topic** | A string naming a fan-out channel. Derived, never chosen freely. See §3. |
| **registration** | A delivery address on a registry leg, stored in SQLite: a WebPush `PushSubscription` (`endpoint` + `keys`). (A UnifiedPush endpoint would register the same way — rejected at this stage, §6.3.) |
| **publisher** | A company (or a company's department/partner engine) with an API key. Recorded in SQLite with its allowed company origin(s) and delivery configuration. |
| **wake-up** | A data-only push message, never content. |
| **company_id** | The canonical join origin: lowercase ASCII origin, punycode for IDNs, no scheme, no trailing slash (e.g. `company.example`). This is the same value the user confirms at pairing. |

---

## 3. Topic Derivation (normative for app and relay)

Topics MUST be derived deterministically so the app (which subscribes
client-side) and the relay (which publishes) agree without any exchange:

```
broadcast:  n-b-<sha256hex("b|" + company_id + "|" + channel)>
order:      n-o-<sha256hex("o|" + order_token)>
```

- `sha256hex` = lowercase hex of SHA-256 over the UTF-8 bytes of the string.
- `channel` = the bare channel name (`[a-z0-9-_]+`, per
  [`../spec/repository.md` §2](../spec/repository.md)).
- `order_token` = the 128-bit base64url capability token (22 chars, no
  padding) from the join QR payload ([`../spec/core.md` §3](../spec/core.md)).
- Output is 66 chars (`n-b-` / `n-o-` + 64 hex). Lowercase hex is valid in
  both FCM topic names and ntfy topic names (no provider-specific escaping).
- The domain separator (`b|` / `o|`) prevents cross-type collisions; the
  `n-` prefix namespaces the relay's topics on shared providers (e.g. a
  self-hosted ntfy server used by several apps).

The relay MUST NOT accept raw topic names from callers — it derives them
from `company_id`/`channel`/`order_token` and validates against the
publisher record. The app derives them from the same inputs (from
master-signed metadata and the QR payload).

---

## 4. Wake-up Payload (canonical)

A single JSON object. **What it carries depends on the leg** (§1):

| | Topic-based legs (FCM, ntfy topics) | Registry legs (WebPush) |
|---|---|---|
| identifier | **in the topic** — the payload carries none | **in the payload** as `t` (the derived topic string) |
| counters | `n` (unread, optional) / `seq` (order, optional) | same |

```json
// FCM data (native) — identity comes from the message's topic
{ "v": 1, "n": 3 }          // broadcast
{ "v": 1, "seq": 7 }        // order

// WebPush payload — no topic at delivery, so carry it
{ "v": 1, "t": "n-b-<64 hex>", "n": 3 }
{ "v": 1, "t": "n-o-<64 hex>", "seq": 7 }
```

- `v` — schema version (1). Unknown versions: drop the wake-up.
- `t` — the derived topic string (§3). The app maps `t` to its locally
  followed company/channel or order. Only registry legs carry `t`; on
  topic-based legs the app learns the topic from the delivery itself (FCM
  exposes it as `from` = `/topics/<topic>`; ntfy exposes it in the
  message).
- `n` — unread counter hint (optional; informational only).
- `seq` — monotonic per order (optional gap hint, not a delivery
  guarantee).
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
  "company": "company.example",
  "channel": "marketing",
  "unread": 3
}
```

or, for an order thread:

```json
{
  "v": 1,
  "company": "eshop.example",
  "order_token": "<22-char base64url>",
  "seq": 7
}
```

Exactly one of `channel` / `order_token` MUST be present. `company` MUST
match an origin the API key is authorized for (publisher record).

Response `200 OK` (synchronous fan-out, concurrent across providers):

```json
{
  "topic": "n-b-<…>",
  "delivered": { "fcm": 1, "ntfy": 1,
                 "webpush": { "sent": 0, "failed": 0, "removed": 0 } }
}
```

- `fcm`/`ntfy` are `1` if the provider is enabled for this publisher and
  the publish was accepted (a topic with zero subscribers still counts as
  dispatched — FCM/ntfy do not error on empty topics).
- `webpush`: `sent` = registrations for this topic the provider accepted;
  `failed` = transient errors (retried, then counted); `removed` =
  registrations deleted because the provider said they are dead.
- `202 Accepted` MAY be returned when the relay is under load; the request
  is then queued (implementation detail — ordering between the response and
  delivery is not part of the contract).
- Errors: `401` bad/unknown key; `403` key not authorized for `company`;
  `400` schema violation; `429` rate limit (see §5.3).

Duplicate wake-ups are harmless (the app diffs content anyway); no
idempotency key is required.

### 5.2 Registration API (WebPush)

The PWA has **one registration per install** (WebPush has no topics) plus
the set of topics it follows. The relay stores registration ↔ topic
mappings in SQLite.

| Method + path | Body | Meaning |
|---|---|---|
| `POST /v1/registrations` | `{ "endpoint": "https://…", "keys": { "p256dh": "…", "auth": "…" }, "topics": ["n-b-…", …] }` | Register (or replace by `endpoint`). Returns `{ "id": "<uuid>" }`. |
| `PUT /v1/registrations/{id}` | `{ "topics": [ … ] }` | Replace the followed-topic set (called on follow/unfollow). |
| `DELETE /v1/registrations/{id}` | — | Remove (company deletion / uninstall). |
| `GET /v1/registrations/{id}` | — | Exists check (debug). |

- `endpoint` MUST be an `https://` URL; the relay never fetches it itself.
- Rate-limited and throttled by IP; optionally gated by a shared app secret
  (`X-App-Key`) embedded in the app build — endpoints are unguessable, but
  an open registration endpoint is a spam surface, so the app key SHOULD
  be configured.
- `topics` are derived per §3; the relay accepts only `n-b-`/`n-o-` topics
  and MAY ignore unknown prefixes.
- **Why the payload still needs `t` (§4):** the registration knows *which*
  topics it follows, but a delivered message carries no topic — so the
  wake-up payload must name it. The registry makes delivery *possible*;
  the payload makes it *legible*.
- (If a UnifiedPush leg is ever adopted despite §6.3, this API covers it
  unchanged — a per-instance endpoint registers the same way.)

### 5.3 Limits

Per publisher (configurable): default 60 publishes/minute, burst 120.
Per subscription: topic count ≤ 200. Global: the relay MUST rate-limit
subscription registration by IP.

---

## 6. Delivery Legs

### 6.1 FCM (native Android + iOS)

- **Config:** one shared Firebase project (the app's project; per
  [`../NOTES.md`](../NOTES.md): one app binary = one Firebase project).
  Service account JSON path in relay config. OAuth2 access token fetched
  from `https://oauth2.googleapis.com/token` and cached until ~5 min
  before expiry.
- **Publish:** `POST https://fcm.googleapis.com/v1/projects/<project>/messages:send`
  with `{ "message": { "topic": "<derived>", "data": { <wake-up fields> },
  "android": { "priority": "high" } } }`. Field values in `data` are
  strings; the app parses JSON from a single field or individual fields
  (implementation choice — both are valid; the schema is §4).
- **iOS:** topics work through the Firebase SDK (FCM → APNs proxy); no
  separate APNs handling at the relay.
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
the `n-b-`/`n-o-` topics itself; the relay publishes once per topic.

- **Config:** per publisher, an optional `ntfy_base` (e.g.
  `https://ntfy.acme.example` or the public `https://ntfy.sh`); absent =
  leg disabled for that publisher. The app subscribes to
  `<ntfy_base>/<topic>`; the base comes from `custom.push` metadata, so a
  publisher self-hosting gets its own server and its own topic
  namespace.
- **Publish:** `POST <ntfy_base>/<topic>` with the §4 payload as the JSON
  body. A per-publisher `Authorization: Bearer <ntfy key>` MAY be
  configured for protected servers. Errors: `4xx` → log/count;
  `5xx`/timeout → retry with backoff.
- **Accepted trade:** the app holds the connection itself (foreground
  service; battery; Android 15+ `remoteMessaging` foreground-service
  type) — the price of a registry-free de-Googled leg.
- **Bare ntfy app:** users who follow via ntfy directly get the generic
  display path (publish `{ "title": "Keryx", "message": "New
  message" }`); not part of the app's verified flow. Unguessable `n-o-`
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
  api_key_hash  TEXT NOT NULL UNIQUE,      -- sha256hex of the key
  name          TEXT NOT NULL,
  company_id    TEXT NOT NULL,             -- canonical join origin
  fcm_enabled   INTEGER NOT NULL DEFAULT 1,
  ntfy_base     TEXT,                      -- NULL = disabled
  ntfy_key      TEXT,                      -- optional write key
  webpush_enabled INTEGER NOT NULL DEFAULT 1,
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
  publisher   TEXT NOT NULL,
  kind        TEXT NOT NULL,               -- channel | order
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
  path, VAPID keys (or key file), VAPID `sub` contact, default ntfy base,
  TTLs, concurrency caps, rate limits, event-log retention.
- **Provisioning:** a subcommand (`relayctl`-style, or `relay
  publishers add --name … --company company.example`) issues the API key
  (shown once), creates the publisher record, and (optionally) configures
  `ntfy_base`/`ntfy_key`.
- **Secrets** (highest to lowest sensitivity): FCM service account (can
  publish to every topic in the app's project), VAPID private key (can send
  to every registered PWA subscription), API keys (per publisher, hashed at
  rest), ntfy keys. All in secret storage; never in the DB or logs.
- **Scale:** one instance; SQLite WAL supports the fan-out volume. If a
  queue is needed later, `event_log` is the natural replay source; no
  protocol change.
- **The relay does not need a TUF client.** It never fetches or verifies
  content; it only forwards identifiers it is told.

---

## 9. Security and Privacy Posture

| What the relay knows | What it never learns |
|---|---|
| publisher identity (API key → company origin) | user identity, email, phone |
| PWA device ↔ topic mapping (registration registry, targeted model) | device ↔ topic mapping on topic legs (FCM, ntfy — anonymous) |
| opaque topic hashes, wake-up volume/timing | content, message text, order data |
| WebPush payloads (it encrypts them — server-side only) | anything the app fetches afterwards |

- **Bounded power (the load-bearing property):** the relay can spam or
  withhold wake-ups. It cannot forge, alter, or read content — the
  protocol's content-side verification is the only trust-bearing step. This
  is why centralizing wake-ups in one shared server is acceptable, and it
  is the reason the relay must never be asked to carry content.
- **WebPush targeted model:** the relay holds device ↔ company mapping for
  PWA registrations (unavoidable — WebPush is per-instance). This is the
  same linkage class accepted for APNs/FCM in
  [`../design/why.md` §4.10](../design/why.md), but held by the relay
  instead of the provider. The alternative ("pure" variant: send every
  wake-up to every registration, service worker filters locally) keeps the
  mapping out of the relay at the cost of waking all registered devices
  per publish — recorded here as the privacy-preserving fallback; the
  targeted model is the default.
- **Logging:** topic names and hashes only; `event_log` never logs order
  tokens or payloads. Access to the relay's DB is a privacy incident by
  itself (PWA registry) — treat as sensitive.
- **Abuse:** per-publisher rate limits, per-IP subscription throttling,
  endpoint validation, VAPID `sub` contact for provider abuse contact.

---

## 10. Integration with the Protocol

- Publisher tooling (`pub`) gains a `push` step: after `publish`/order
  event, call `POST /v1/publish` (idempotent in effect; failures are
  non-fatal — content sync covers it).
- `custom.push` in master-signed metadata
  ([`../NOTES.md`](../NOTES.md)) selects the provider for a company:
  `{ "provider": "relay" }` (default for the app), `{ "provider": "ntfy",
  "base": … }`, or `{ "provider": "none" }`. The app reads it after chain
  verification and subscribes accordingly; the publisher tooling uses the
  same config to know whether to call the relay or its own ntfy.
- The relay corresponds to `provider: "relay"` only. A publisher that
  prefers zero third parties runs its own ntfy and never calls the relay.

---

## 11. Open Questions

1. **Topic derivation finalization** — hash inputs, prefix (`n-b-`/`n-o-`),
   hex vs base64url; must be frozen before printed QRs ship, and must match
   app + relay + (for order topics) the publisher's order engine.
2. **Targeted vs pure (WebPush)** — default is targeted (efficient); is
   the device↔company mapping at the relay acceptable long-term, or does
   the protocol's zero-state stance require the pure variant (send every
   wake-up to every registration, filter locally) or a config switch?
3. **Shared-topic ntfy (bare ntfy app)** — is the generic "New message"
   display path (no Keryx integration) in v1 at all, and do order topics
   on it need read/write keys or just unguessable names?
4. **TTLs** — WebPush default 1 h; FCM/ntfy default (FCM stores up to 4
   weeks; ntfy ephemeral unless configured). What cadence matches the
   protocol's freshness model?
5. **Order wake-ups through the relay** — the relay is publisher-facing;
   do order events go through the same `/v1/publish` (they have no
   publisher tooling, they come from the order engine)? Confirm the engine
   gets an API key too, or a separate `/v1/publish-order` endpoint with
   tighter limits.
6. **Relay identity** — who operates it (the app publisher), and what
   governance applies if more than one app ships against it?
