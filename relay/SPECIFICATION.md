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
user devices over two delivery legs:

| Leg | Devices | Mechanism | Registry |
|---|---|---|---|
| **FCM topics** | native Android + iOS (Firebase SDK) | one publish per topic; devices subscribe client-side | **none** (relay never knows devices) |
| **UnifiedPush (WebPush endpoints)** | PWA (browsers); de-Googled Android (Keryx app + a UnifiedPush distributor, e.g. ntfy) | 1:1 send per stored endpoint subscription | SQLite (endpoint ↔ followed topics) |

Both legs are first-class: the PWA is the reference client today, and
native apps are expected — the relay does not distinguish between them.
The endpoint leg has two subscription sources — browser `PushManager` and
the Android UnifiedPush connector — sharing one WebPush publish path
(§6.2); they differ only in where the subscription comes from.

**Hard rules:**

1. **The relay carries no content. Ever.** A wake-up request contains the
   **scope_id** (an authorization handle, §5.1), an opaque **source
   hash** (§3), a sequence number, and a **wake-up signature** (§4.1). **Identifiers
   are not content:** a wake-up may name *which* company/channel/order to
   refresh, never what a message says (no title, body, status, amount,
   URL). The relay derives the delivery topic from the verified company,
   scope_id, and source hash (§3); on the **topic leg** (FCM) the
   identifier travels in the **topic itself** (the device already
   subscribed to it; the payload can be nearly empty); on the **endpoint
   leg** (UnifiedPush) no topic exists at delivery, so the payload MUST carry the
   **topic string** (§4) and the device maps it to the followed
   company/channel/order locally. This is what bounds the relay's power: a
   fully malicious or compromised relay can only **spam or withhold wake-up
   signals** — it cannot forge a message (the app fetches and verifies
   content through the TUF/thread path), cannot forge a wake-up (the
   payload signature verifies against the publisher's TUF keys,
   §4.1/§5.5), never receives content or order capability tokens (only
   their hash), and cannot impersonate a publisher. Unverifiable wake-ups
   can cause bounded client metadata refreshes (§4.2), not trusted messages.
2. **Best-effort delivery.** Both legs are best-effort; the relay
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
through FCM topics), and the order-thread content sync (the app's job).
Client wake-up verification and recovery are specified here (§4.2);
content reconciliation and presentation remain the app's contract.

---

## 2. Terminology

| Term | Meaning |
|---|---|
| **topic** | A delivery address derived from the verified company, scope_id, and source hash (§3). On the topic leg, devices subscribe to topics, not scopes or keys; endpoint clients register followed topics with the relay (§5.3). 43 chars, base64url, no prefix. |
| **scope_id** | An opaque, stable identifier for one authorization scope: a public delegation or a private-pattern entry. Verified metadata maps it to keys and a threshold. Keys are not part of its identity; rotation does not change topics (§3.1). |
| **source hash** | `hex(sha256(company_id + "|" + subject))`, where the subject is a channel name or an order token. The publish path treats it as opaque; raw order tokens never transit (§3). |
| **wake-up signature** | Ed25519 signature over the canonical wake-up (§4.1), authorized by the topic's exact scope. Always verified by the relay and by the app. |
| **registration** | A delivery address on the endpoint leg, stored in the registry database: a UnifiedPush/WebPush subscription (`endpoint` + `keys.p256dh`/`keys.auth`), created by the PWA browser or by the Android connector via a distributor (§6.2). |
| **publisher** | A company or partner engine with an API key bound to a company origin and rate limit. Operator provisioning and self-service differ only in admission; both use the same TUF authorization at publish. |
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

topic = base64url_nopad(sha256("keryx/relay/v1|" + JCS({company_id, scope_id, h})))
```

- `h` is the **source hash** — lowercase hex of SHA-256 over the UTF-8
  bytes of the derivation input: 64 hex chars (32 bytes). One uniform rule
  for channels and orders: `hex(sha256(company_id + "|" + subject))`. The
  caller computes it; the relay does not receive `subject` in the publish
  request. Channel names are available in public metadata; order tokens
  never transit. Hex is fine for `h` because it is an intermediate — it never
  reaches a provider; only the topic does.
- `channel` = the bare channel name (`[a-z0-9-_]+`, per
  [`../spec/repository.md` §2](../spec/repository.md)).
- `order_token` = the 128-bit base64url capability token (22 chars, no
  padding) from the join QR payload ([`../spec/core.md` §3](../spec/core.md)).
- `"keryx/relay/v1|"` is a **static, public salt** (domain separator): it
  keeps the topic distinct from `h` itself and from other SHA-256 uses in
  the protocol. It is not secret.
- **Company and scope binding (relay-side, normative).** The derivation includes
  `company_id` as supplied by the **relay's verified publisher record**
  (§5.2/§5.5), never by the caller. `scope_id` MUST resolve in that company's
  verified authorization table (§3.1). Submitting another company's or
  scope's source hash cannot reach that other namespace's topic. The
  publish path MUST NOT inspect the subject or branch on its type.
  JCS input is exactly the three string fields shown above, encoded as UTF-8.
- Output is **43 chars** (base64url of 32 bytes, no padding, no prefix).
  Base64url (RFC 4648 §5 alphanumerics, `-`, `_`) is valid in FCM topic
  names (`[a-zA-Z0-9-_.~%]`) and is an opaque string in endpoint-leg
  payloads — no provider-specific escaping. No namespace prefix: the
  topic is a pure hash. Public-channel topics are predictable; the public
  domain separator does not make them secret. Order-topic unguessability
  comes from the 128-bit capability token. Authenticity comes from signatures.
- **One publish path.** Requests carry no public/private type marker.
  The app keeps a topic → followed item + scope_id map. Even identical
  subjects in different scopes produce different topics.

**Why two-stage:** callers (publisher tooling, order engine) know the
derivation input; the app knows it too; the relay knows only the company
(from its verified record) and the scope_id (authorization handle) —
never the order token and never content. The caller sends `h`; the relay
wraps it with the verified company and resolved scope_id. `sha256` is one-way and the order
token is 128-bit unguessable, so neither `h` nor the topic reveals it.

The relay MUST NOT accept raw topic names or raw derivation inputs from
callers — it derives the topic from `(verified company_id, scope_id, h)` (§5.1).
Callers MUST NOT submit the `order_token` (only its hash) or a topic name;
the scope_id is submitted as the authorization handle.

### 3.1 Authorization scopes

The relay's metadata loader, app, and caller tooling derive scope identifiers
from verified `targets.json` using these exact descriptors:

```text
public_descriptor  = {"kind":"public", "channel": <bare channel name>}
private_descriptor = {"kind":"private", "channel": <entry.channel>,
                      "pattern": <entry.pattern>}
scope_id = hex(sha256("keryx/relay/scope/v1|" + JCS(descriptor)))
```

Use the exact validated metadata strings, without additional normalization;
`scope_id` is 64 lowercase hex characters. Keys, thresholds, and metadata
versions are excluded, so rotation preserves scope identity and topics.
Changing a private pattern changes its scope identity. Duplicate descriptors
within a company's metadata MUST be rejected as ambiguous.

The metadata loader builds a uniform table:
`(company_id, scope_id) → authorized keys + threshold`. Public entries come
from `channels.<channel>` delegations; private entries come from individual
`custom.private_feed_patterns` entries. Matching channel labels MUST NOT
merge their authority. This is the only distinction needed when loading
metadata; **publish authorization, replay handling, and delivery use the
same algorithm for every scope**.

Clients compute topics when following an item and subscribe to those topics
(FCM) or register them with the relay (endpoint clients, §5.3).
They bind each topic to its company and exact scope locally; an incoming
notification MUST NOT select new verification keys or an unrelated scope.
Scope identifiers are authorization handles, not subscriptions to keys.

---

## 4. Wake-up Payload (canonical)

A single JSON object. **What it carries depends on the leg** (§1):

| | Topic leg (FCM) | Endpoint leg (UnifiedPush/WebPush — PWA + de-Googled Android) |
|---|---|---|
| identifier | **in the delivery topic** — omit `t` when recoverable; adapter fallback below | **in the payload** as `t` (the derived topic string) |
| replay protection & authenticity | `seq` (required) / `sig` (required) | same |

```json
// FCM data (native) — identity comes from the message's topic
{ "v": 1, "seq": 7, "sig": [ {"keyid": "…", "sig": "…"} ] }

// Endpoint-leg payload — no topic at delivery, so carry it
{ "v": 1, "t": "<43 chars>", "seq": 7, "sig": [ {"keyid": "…", "sig": "…"} ] }
```

- `v` — schema version (1). Unknown versions: drop the wake-up.
- `t` — the derived topic string (§3). The app maps `t` to its locally
  followed item and scope. The topic leg omits `t` only when the receiving
  API supplies the exact delivery topic (Android FCM's
  `from` = `/topics/<topic>`). A set of subscribed
  topics alone does not identify the source of an individual message.
  An adapter without delivery-topic access MUST carry `t` in its payload;
  it MUST NOT guess. If both are present, they MUST match. The endpoint
  leg always carries `t`. In all cases, `t` is included in the signed bytes.
- `seq` — monotonic counter of wake-ups for this topic, supplied by the
  caller (**required**; integer from 1 through 9007199254740991). Because
  `sig` covers `seq`, the app MUST reject a wake-up whose `seq` is ≤ the
  persisted last accepted value for that topic (§4.2). A sequence gap may
  indicate loss or reordering; it is not proof of loss. Never used for feed ordering —
  the app reconciles by fetching; feed ordering is editorial and dedup is
  `(channel, id)`
  ([`../spec/feeds.md`](../spec/feeds.md)). Unrelated to the `_sig.seq`
  dropped from the feed format —
  [`../design/why.md` §8](../design/why.md).
- `sig` — the wake-up signatures (§4.1), **required for every scope and leg**.
- Nothing else. In particular: no title, no body, no URL, no company name,
  no status text, no raw order token, no unread hint (`n`). Unread state is
  derived by the app from verified content and local read state. No payload
  timestamps or authorization epochs are needed. Reject unknown fields,
  duplicate JSON member names, malformed encodings, and non-integer counters.

The app verifies against its locally trusted authorization for the topic's
scope. Failure permits one bounded metadata refresh (§4.2), not acceptance
of the wake-up. Content fetching and sequence advancement require successful
verification. Content itself is always independently verified.

### 4.1 Wake-up signature

```
signature = Ed25519( "keryx/wakeup/v1|" ‖ JCS({ v, t, seq }) )
```

- Signed bytes: the domain separator `"keryx/wakeup/v1|"` followed by the
  **JCS (RFC 8785)** canonical JSON, encoded as UTF-8, of exactly
  `{v, t, seq}`. `t` is ALWAYS signed, whether carried in the payload or
  recovered from delivery. The `sig` array itself is excluded.
  JCS and domain separation are mandatory: Ed25519
  signatures are reusable across message types, and the protocol's item
  signatures use the same discipline ([`../spec/feeds.md`](../spec/feeds.md)).
- `sig` is always a nonempty array of `{ "keyid": "<hex>",
  "sig": "<base64url>" }`, including threshold 1. Keyids are lowercase
  SHA-256 hex per the protocol's TUF key-object canonicalization; signatures
  are canonical unpadded base64url encodings of 64 Ed25519 signature bytes.
  At least `threshold` distinct authorized keyids MUST verify over the same
  signed bytes. Duplicate keyids do not count twice. Extra signatures from
  other keys do not count and do not invalidate an otherwise satisfied
  threshold (this permits rotation overlap).
- **Key resolution:** use only the exact scope's authorized keys and
  threshold from verified TUF metadata (§3.1). Do not combine scopes.
- **Who verifies:** the relay at publish (§5.5) and the app on delivery.
  The same `sig` is forwarded on both legs, so a malicious relay or push
  service cannot forge a wake-up — it can only spam or withhold. On the
  endpoint leg the relay additionally RFC 8291-encrypts the payload, so
  the push service and any distributor see only ciphertext (§6.2). Client
  recovery allows bounded metadata traffic, but an unverifiable wake-up
  never authorizes content fetching (§4.2).

### 4.2 Client verification, recovery, and replay protection

For every leg and every scope, the app uses the same procedure:

1. Validate the envelope and obtain the topic (§4). Unknown versions,
   malformed messages, and topics not currently followed are dropped without
   a network request. Resolve company and scope from the local topic map.
2. Try the signature threshold against locally trusted, unexpired TUF
   authorization. If valid, proceed to step 5. An extra unknown keyid MUST
   NOT trigger a refresh when known valid signatures already meet threshold.
3. If verification fails or authorization is expired, at most one TUF
   refresh may be started for that company per recovery cooldown. Atomically
   reserve the allowance and persist `next_recovery_at` BEFORE networking.
   Merge simultaneous failures across all of the company's topics into this
   one refresh. A global client rate/concurrency limit also applies.
   Fetch only metadata from already-trusted locations using normal TUF
   verification and root rotation; ignore any untrusted key or URL suggestions.
4. Retry verification after refresh. If it still fails, drop the wake-up;
   the cooldown remains in force even after timeout, network error, or invalid
   metadata. During the cooldown, suppress only further **unverified-triggered
   refreshes**, not the channel: messages valid under cached, unexpired keys
   are still processed. Use bounded pending-message storage during a refresh.
5. Only after successful verification, atomically compare and persist `seq`
   against the last accepted value for the topic. Drop values ≤ that value.
   A newly followed topic starts with zero. Never advance this state from an
   unverified message. Schedule/coalesce content reconciliation; verify content
   independently through the normal protocol. Scheduled/foreground sync is
   the backstop if reconciliation is interrupted or fails.

The recovery cooldown is a client policy of **X hours**, shared per company
and persisted across restarts, not a notification field or a relay setting.
Its concrete value and global request budget must be documented by the client
(§11). Every recovery attempt consumes the allowance, including successful
ones, so repeated key changes or forged messages cannot bypass the limit.
Ordinary scheduled and foreground metadata refresh continues independently
under the client's normal scheduling policy; a push cannot reset that schedule
or clear the recovery cooldown. Valid wake-ups are also coalesced/rate-limited
to bound content traffic from a compromised authorized signer.

This permits prompt recovery after emergency key rotation when an allowance
is available, without trusting the new key from the notification. An attacker
can consume an allowance before a legitimate rotation and delay recovery until
the cooldown ends or independent refresh learns the update. Per-device limits
bound, but do not eliminate, aggregate metadata traffic across many clients.
Wake-ups do not guarantee immediate emergency delivery.

**Sequence ownership:** publishers MUST persist and atomically allocate
monotonic per-topic counters across concurrent writers, restarts, and key
rotations. Retries reuse the same signed envelope. Clients persist their
per-topic high-water marks; relay replay state is memory-only (§5.5).
Counter loss requires recovery, not silently resetting `seq`. A fresh client
or cleared client storage can accept an old signed wake-up; this is a known
limit without retained replay state. A compromised signer can advance a
counter excessively; ordinary content sync remains available, but this draft
does not introduce an epoch/reset mechanism. Exhausted counters MUST NOT wrap.

---

## 5. Publisher API

Base: `https://<relay-host>/v1/`. All requests and responses are JSON over
HTTPS. Authentication: `Authorization: Bearer <api-key>`; the relay stores
only the SHA-256 hash of the key (shown once at provisioning). Every publish
also requires signature verification against verified, unexpired TUF
authorization (§5.5), regardless of provisioning method.

### 5.1 `POST /v1/publish` — fan out a wake-up

Request (one shape for channels and orders — no type marker):

```json
{
  "v": 1,
  "scope_id": "<64-char lowercase hex>",
  "h": "<64-char hex>",
  "seq": 7,
  "sig": [{"keyid": "<64-char lowercase hex>", "sig": "<base64url>"}]
}
```

- `scope_id` — the opaque authorization handle (§3.1). Resolve it in the
  verified company's authorization table (TUF mode; a key-mode publisher
  sends it as submitted — §5.5); no type marker or channel label
  is accepted. It is not a key identifier and does not change on key rotation.
- `h` — the source hash (§3): `hex(sha256(company_id + "|" + subject))`;
  the caller computes it, the relay never sees `subject` (for orders it
  never sees the token at all). MUST be exactly 64 lowercase hex chars.
  The relay MUST treat it as opaque and MUST NOT validate it against a
  channel name, pattern, or raw subject. The same derivation and authorization
  algorithm applies to every scope; there is no public/private branch.
- `seq` — **required**; monotonic per topic, supplied by the caller,
  forwarded to the payload (§4). Enables missed-wake-up detection and
  replay protection (§4.1).
- `sig` — the signature array (§4.1). The relay always verifies the exact
  scope's threshold before replay-cache mutation or enqueue. Required for
  every **TUF-mode** publisher — including operator-provisioned ones; there
  is no unsigned path for them. A **key-mode** publisher record (§8; debug
  only) MAY omit `sig`; the relay then skips signature verification and
  scope resolution entirely (§5.5).

The uniform publish workflow is: authenticate API key → resolve scope →
derive topic → verify signature threshold (TUF mode; §5.5) → check/update in-memory sequence
cache → enqueue for dispatch (below). The client independently derives the
same topic when subscribing; neither keys nor scope identifiers are delivery
subscriptions.

Response — fast path (`200 OK`). When the fan-out is small — no endpoint
registrations for the topic, or at most `fast-path-max-registrations`
(config, default 500) — the relay dispatches synchronously and returns the
per-leg results:

```json
{
  "topic": "<43 chars>",
  "suppressed": false,
  "providers": { "fcm": "accepted",
                 "webpush": { "sent": 0, "failed": 0, "removed": 0 } }
}
```

Response — async path (`202 Accepted`). When the topic's endpoint registry
exceeds the fast-path threshold, the relay accepts the publish and returns
immediately, before any dispatch:

```json
{ "topic": "<43 chars>", "request_id": "<uuid>", "status": "accepted" }
```

Dispatch continues in the background; the publisher follows it via
§5.1.1. A replay-suppressed publish returns `200` with `suppressed: true`
(§5.5) on either path and carries no `request_id`.

- `fcm` is `disabled`, `accepted`, `failed`, or `suppressed`.
  Acceptance is by the provider, not proof of device delivery; a topic with
  zero subscribers can still be accepted. Errors never count as acceptance.
- `webpush`: `sent` = registrations for this topic the provider accepted;
  `failed` = permanent errors or transient errors after retry exhaustion;
  `removed` =
  registrations deleted because the provider said they are dead.
- **Dispatch (async path).** The relay keeps a bounded, in-memory dispatch
  queue — one entry per topic, holding the **latest accepted** wake-up for
  that topic. Coalescing: a newer `seq` replaces a pending entry; an older
  or duplicate one is a no-op. FCM is a single provider call; the endpoint
  leg expands the entry to the topic's registrations at drain time. Workers
  drain the queue paced by the global per-provider outbound budgets (§5.4),
  with bounded concurrency and per-provider retries/timeouts. The queue is
  **deliberately non-durable**: a restart loses pending wake-ups —
  best-effort per hard rule 2, and clients' persisted replay checks (§4.2)
  remain authoritative — so no outbox or job state exists in this revision.
  Nothing per-registration is written on the publish path (§7). The
  publisher recovers a truncated fan-out by re-publishing the same `seq`:
  the replay cache is in-memory and lost with the queue, so it is accepted
  again, and registrations already served receive a duplicate that clients
  drop by `seq` (§4.2).
  A saturated queue rejects new publishes with `503` (backpressure; §5.4).
- Errors: `401` bad/unknown API key; `400` schema violation (`scope_id` or
  `h` malformed, invalid `seq`, malformed `sig`); `403` unknown scope in the
  verified company or signature threshold not satisfied; `503` required TUF
  authorization unavailable/expired and refresh failed (§5.5), or dispatch
  queue saturated (§5.4); `429` rate limit (see §5.4).

Duplicate wake-ups are harmless (the app diffs content anyway) and coalesce
in the dispatch queue; no idempotency key is required.

### 5.1.1 `GET /v1/publishes/{request_id}` — dispatch status

Same `Authorization: Bearer <api-key>` as §5.1; the request MUST belong to
the authenticated publisher — another publisher's `request_id` returns
`403`. The endpoint reads `event_log` (§7).

- `200` while dispatch is in progress:
  `{ "request_id": "<uuid>", "topic": "<43 chars>", "status": "pending" }`
- `200` when complete (all enabled legs attempted):

```json
{ "request_id": "<uuid>", "topic": "<43 chars>", "status": "complete",
  "providers": { "fcm": "accepted",
                 "webpush": { "attempted": 1000, "sent": 950,
                              "failed": 20, "removed": 30 } } }
```

- `webpush.attempted` = registrations for the topic that were attempted;
  `sent`/`failed`/`removed` partition it (push-service accepted / retries
  exhausted / dead, deleted). No counts are returned while `pending`;
  progress reporting is out of scope for this revision.
- **Results describe attempts, not deliveries.** There is no device
  acknowledgement on any leg: `sent` means the push service accepted the
  message, not that a device received or displayed it; `fcm` reports
  provider acceptance only — per-device information does not exist on the
  topic leg (no registry, §1).
- `404` unknown `request_id`, or the row is older than event-log retention
  (§7). Probe reads are rate-limited per publisher.

Authorization is **company- and scope-bound for every publisher**. A signer
can address hashes only within its authorized scope, not sibling scopes or
other companies. Withdrawn keys are rejected once the withdrawal is present
in verified metadata; cached authorization remains usable only while required
metadata is unexpired. This is not instantaneous global revocation (§5.5).

### 5.2 `POST /v1/publishers` — TUF-gated self-service registration

Optional (enabled by config, §8). Binds a publisher record to a
`company_id` using the company's own TUF metadata as the anchor — **no
relay operator action**. This endpoint never sends notifications; it only
creates/refreshes the binding. Records created here are always TUF-mode
(`auth_mode = "tuf"`).

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
   `targets.json` as the uniform scope table (§3.1), shared per company
   across all publisher credentials (§5.5, §7).
4. Returns the API key (shown once, hashed at rest) and the publisher id.

- Idempotent: re-POSTing the same `company_id` **refreshes** the binding
  through the existing pinned TUF state. It MUST NOT reset trust, rollback
  versions, or replace an API credential. The key is returned only on initial
  creation; a repeat returns the publisher id, not the stored key hash.
  Credential rotation/revocation is an explicit authenticated management
  operation. Metadata refresh and key rotation do not require re-enrollment.
  Public metadata establishes company authorization, not the caller's identity;
  the publish signature is always required. No enrollment challenge is added.
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

### 5.3 Registration API (UnifiedPush endpoints)

One registration per install per client — PWA (browser `PushManager`) and
de-Googled Android (UnifiedPush connector via a distributor, §6.2) both
register here — plus the set of topics it follows. Endpoints have no
topics, so the relay stores registration ↔ topic mappings in the
**registry database** (§7).

| Method + path | Body | Meaning |
|---|---|---|
| `POST /v1/registrations` | `{ "endpoint": "https://…", "keys": { "p256dh": "…", "auth": "…" }, "topics": ["<43 chars>", …] }` | Create registration. Returns `{ "id": "<uuid>", "management_token": "<secret>" }` once. Existing endpoint without its management token: `409`, no overwrite. |
| `PUT /v1/registrations/{id}` | `{ "topics": [ … ] }` | Replace the followed-topic set (called on follow/unfollow). |
| `DELETE /v1/registrations/{id}` | — | Remove the installation's entire registration. Removing one company uses PUT to remove only its topics. |
| `POST /v1/registrations/{id}/heartbeat` | — | Liveness ack (no body): bumps `last_seen`. Sent by the service worker on wake-up receipt, and by the app (PWA or Android) on foreground. `204 No Content`. |

- The relay POSTs to `endpoint` during delivery. It MUST be HTTPS and pass
  the outbound-request policy (§5.6). Validate subscription key encodings
  before storage. Registration is rate-limited by IP.
- Updates, deletion, heartbeat, and replacement by endpoint MUST require
  `Authorization: Bearer <management_token>` for that registration. Generate
  a random 256-bit token and store only its SHA-256 hash. Missing/invalid
  credentials return `401`; never return an existing token from registration.
  A shared secret embedded in a public app build is not ownership proof.
- `topics` are derived per §3; the relay accepts only 43-char base64url
  topics and rejects others.
- **Why the payload still needs `t` (§4):** the registration knows *which*
  topics it follows, but a delivered message carries no topic — so the
  wake-up payload must name it. The registry makes delivery *possible*;
  the payload makes it *legible*.
- **Lifecycle:** track `created_at` and `last_seen` (§7). A client
  re-registers (POST) when a management operation returns `404`/`401` —
  e.g. after GC — since no server-side record then exists for it; the
  Android connector re-registers on the same condition and on endpoint
  rotation from the distributor.

### 5.4 Limits

Per publisher (configurable): default 60 publishes/minute, burst 120.
Per subscription: topic count ≤ 200. Global: the relay MUST rate-limit
subscription registration by IP; TUF metadata refreshes are rate-limited
per company (config; §5.5).

**Global outbound budgets (config, per provider).** Dispatch is paced by
per-provider token buckets — FCM publish rate, endpoint-leg concurrency
and rate — so a burst of publishes cannot exceed what the providers
accept. These budgets are why the publish API can accept faster than it
delivers (§5.1). The endpoint leg fans out to many push services
(browser push services, ntfy instances), each with its own limits; the
WebPush budget paces the aggregate. Budget exhaustion **paces**
the queue (workers wait); it does not fail the publish. A queue that stays
saturated rejects new publishes with `503` (§5.1).

### 5.5 TUF authorization at publish

For every TUF-mode publisher, the relay always verifies each publish against
its verified, unexpired TUF authorization. Provisioning method does not
change this check — operator-provisioned and self-service records are both
TUF-mode. Cached authorization is not proof that no newer metadata exists.

**Key-mode exception (debug).** An operator MAY provision a publisher record
with `auth_mode = "key"` (§8): the API key alone authorizes the publish — no
signature check, no per-company TUF client, no scope table. This is a
debugging/development path, never a production configuration: clients verify
wake-up signatures unconditionally (§4.1), so a key-mode wake-up is **never
accepted by the app** — it can trigger only the bounded client recovery of
§4.2 and generic-notification presentation (§6.2), the same residual as any
forged wake-up. Key-mode records are an explicit operator choice, visible in
`publishers.auth_mode`, and SHOULD be rate-limited low; they do not change
the client-visible security model.

- **Per-company TUF client:** root pinned at registration (§5.2/§8);
  `timestamp → snapshot → targets` fetched from `custom.repo_base` and
  verified with the standard chain walk (anti-rollback via versions,
  anti-freeze via `expires`). Root updates use the versioned chain from the
  existing well-known anchor, never fresh TOFU on refresh or re-registration.
- **Resolution:** load the uniform scope table (§3.1) from verified metadata,
  then look up `(company_id, scope_id)`. The publish path knows only keys and
  threshold; it MUST NOT branch on public/private channel type. The signature
  array must satisfy the exact scope's threshold (§4.1).
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
- **Replay:** after signature verification, atomically compare `seq` with a
  bounded in-memory LRU mapping `topic → highest_seen_seq`. Values ≤ the
  cached value are suppressed before enqueue; a higher value updates the
  cache and the publish is enqueued for dispatch (§5.1). The cache is
  updated at acceptance, before dispatch. A suppressed publish returns
  `200` with `suppressed: true`, zero WebPush counts, and `suppressed`
  for the enabled topic leg (`disabled` otherwise). Suppression is not a
  delivery acknowledgement: an already attempted fan-out may have failed
  partially or entirely. Retries may therefore be suppressed; polling
  remains the backstop. This cache is bandwidth/DoS protection only:
  **MUST NOT persist it**. Eviction or restart may permit another dispatch
  (the in-memory queue is lost with it — accepted, §5.1); client persistent
  replay checks (§4.2) remain authoritative.

### 5.6 Outbound-request policy

Every outbound HTTP request, including root/repository metadata and WebPush
delivery, MUST use HTTPS with certificate validation and bounded size,
decompression, timeout, and concurrency. Reject loopback, private, link-local,
and other non-public destination addresses; validate all DNS results and the
actual connection destination to prevent DNS rebinding. Redirects are disabled
for roots and push endpoints; any allowed repository redirect must pass the
same checks at every hop. Endpoint URLs must additionally match the
operator's configured approved push-service origins — browser push
services plus the default public ntfy instance and any operator-listed
self-hosted ntfy servers, since UnifiedPush endpoints live on the user's
chosen ntfy server. Signed `repo_base`
metadata authenticates its source, not the safety of its network destination.

---

## 6. Delivery Legs

Two implementations sharing the §4 wake-up semantics: FCM topics (§6.1)
and the UnifiedPush/WebPush endpoint leg (§6.2–6.3).

### 6.1 FCM (native Android + iOS)

- **Config:** one shared Firebase project (the app's project — one app
  binary = one Firebase project). Service account JSON path in relay
  config. OAuth2 access token fetched from
  `https://oauth2.googleapis.com/token` and cached until ~5 min before
  expiry.
- **Publish:** `POST https://fcm.googleapis.com/v1/projects/<project>/messages:send`
  with `message.topic` = derived topic and `message.data.wakeup` = one JSON
  string containing the §4 envelope. This is the only FCM payload encoding.
  Android background reconciliation uses normal priority. For iOS, include
  `apns.headers` with `apns-push-type: background` and `apns-priority: 5`,
  and `apns.payload.aps` with `content-available: 1`. Topic recovery or the
  `t` fallback follows §4; every supported adapter must establish this before
  omitting `t`.
- **iOS:** topics work through the Firebase SDK (FCM → APNs proxy); no
  separate APNs handling at the relay. Honest caveat: data-only messages
  arrive on iOS as **silent pushes** — Apple may throttle or defer them,
  they never display a notification by themselves (the app must surface one
  after fetching), and delivery when the app is terminated is not
  guaranteed. Wake-ups on iOS are best-effort; background fetch is the
  backstop (§1, hard rule 2).
- **Errors:** `401/403` (credentials) → alarm and fail; `429` → back off;
  `404`/`INVALID_ARGUMENT` → log and fail, never count as dispatched.
  An empty topic's successful response does not turn errors into success.
  Transient errors retried with exponential backoff
  (max 3 attempts).
- **Observability:** the relay sees topic names (opaque) and volume — never
  devices.

### 6.2 UnifiedPush / WebPush — one publish path, two endpoint sources

One implementation for every endpoint-based client; the relay cannot and
need not distinguish PWA from de-Googled Android subscriptions.

| Endpoint source | Who creates the subscription | Who holds the device-side connection |
|---|---|---|
| **PWA / browsers** | browser `PushManager` (endpoint on the browser vendor's push service) | the browser (Chrome on Android rides FCM/GMS) |
| **De-Googled Android** | UnifiedPush connector via a distributor — ntfy today (§6.3) | the distributor app (ntfy, WebSocket) |

- **Config:** one VAPID keypair (RFC 8292). Public key embedded in the PWA
  (`applicationServerKey`) and provided to the Android connector for
  registration; private key in relay config. No registration with any
  vendor.
- **Publish:** for each registration with the topic — streamed from the
  registry in batches, never loaded wholesale into memory — `POST <endpoint>`
  with an RFC 8291 encrypted payload (ECDH P-256 + HKDF + AES-128-GCM),
  headers `Authorization: vapid t=<JWT ES256>, k=<public>`, `TTL: 3600`
  (config, default 1 h), `Urgency: normal`. JWT claims: `aud` = endpoint
  origin, `exp` = now + 12 h (≤ 24 h), `sub` = `mailto:` contact from config
  (Chrome requires it). The endpoint leg has no delivery topic — the
  payload always carries `t` (§4).
- **Errors:** `404`/`410` → delete the registration (count `removed`);
  `429`/`5xx` → retry with backoff, then `failed`. Sends are paced by the
  global endpoint-leg outbound budget (§5.4) with a configurable
  concurrency cap.
- **Encryption is the leg's privacy boundary:** the relay encrypts with
  RFC 8291; browser push services, ntfy servers, and the distributor only
  ever see ciphertext and the random endpoint URL.
- **Client presentation:** browsers such as Safari require a visible
  notification for each push; WebPush is not a portable silent-refresh API.
  The app must define a locally authored generic notice for rejected,
  redundant, or failed updates where visibility is required, without claiming
  a publisher message exists or fetching unverified content. This means a
  malicious relay can cause generic notification spam. Signature verification
  and the recovery cooldown do not override platform presentation rules.
  See [WebKit's Web Push requirements](https://webkit.org/blog/12945/meet-web-push/).

### 6.3 De-Googled Android: ntfy as UnifiedPush distributor

The de-Googled path reuses §6.2 entirely; only the client side differs.

- **Client:** the Keryx app embeds a UnifiedPush **connector** (a small
  Capacitor plugin wrapping `org.unifiedpush.android:connector`). The user
  picks a distributor — ntfy today, any other UnifiedPush distributor later.
- **Distributor:** ntfy (F-Droid flavor, no Firebase) holds the device's one
  push connection (WebSocket, "instant delivery" foreground service) and
  forwards messages to every UnifiedPush app on the device. Keryx keeps no
  socket and is not running between wake-ups: the distributor wakes it via a
  package-scoped broadcast (manifest receiver, raised to foreground ~5 s),
  it verifies and reconciles, then exits (§4.2).
- **Endpoint:** the distributor creates a per-instance capability URL on the
  user's ntfy server — `https://ntfy.sh/<random>` by default, or the
  company's self-hosted server. The connector sends `{endpoint, keys}`
  through the same registration API as the PWA (§5.3); the relay never
  talks to ntfy directly and holds no ntfy-specific config. ntfy is just
  the push server behind some endpoint URLs.
- **Trade (vs. the earlier topic-based plan):** the price of the shared
  socket is the endpoint registry at the relay (§9). What was avoided: a
  per-app foreground service on every de-Googled phone (battery; Android
  15+ `remoteMessaging` foreground-service type). Wake-ups remain signed
  (§4.1) and contentless, so the registry exposes at most random capability
  URLs, never content or identity.
- **Bare ntfy app:** users who follow a company's shared topic in ntfy
  directly get the generic display path only (§11); the relay never
  publishes to shared topics — it does not know them.
- **Future distributors:** the connector is distributor-agnostic. NextPush,
  microG, or others later require no relay change — their endpoints are
  ordinary WebPush endpoints — only client-side discovery and docs.

---

## 7. Database (SQLite)

WAL mode. **Two databases**, versioned independently (schema version
  tables; the relay refuses to start on a mismatched version):

- **Main database** (`-db`): `publishers`, `company_tuf`, `event_log` —
  occasional writes (provisioning, publishing, metadata refresh).
- **Registry database** (`-registry-db`): `registrations`,
  `registration_topics` — write-heavy (registration/follow/unfollow/
  heartbeat churn); kept separate so the endpoint registry's volume never
  touches the main production DB.

Main database schema:

```sql
CREATE TABLE publishers (
  id            INTEGER PRIMARY KEY,
  api_key_hash  TEXT NOT NULL UNIQUE,      -- sha256hex of the key (shown once)
  name          TEXT NOT NULL,
  company_id    TEXT NOT NULL,             -- company origin the key is issued for
  rate_per_min  INTEGER NOT NULL DEFAULT 60,
  provisioned_by TEXT NOT NULL,            -- operator or self_service; admission only
  auth_mode     TEXT NOT NULL DEFAULT 'tuf', -- tuf (production) | key (debug; no sig check, §5.5)
  created_at    TEXT NOT NULL
);

CREATE TABLE company_tuf (                 -- shared by every credential for a company
  company_id    TEXT PRIMARY KEY,
  root          TEXT NOT NULL,             -- pinned root.json (TOFU anchor)
  root_version  INTEGER NOT NULL,
  chain_state   TEXT NOT NULL,             -- verified metadata, expiry/version state + scope_id → keys/threshold
  refreshed_at  TEXT NOT NULL
);

CREATE TABLE event_log (
  id          INTEGER PRIMARY KEY,
  request_id  TEXT NOT NULL UNIQUE,        -- uuid; backs the dispatch-status probe (§5.1.1)
  publisher_id INTEGER NOT NULL,           -- publishers.id; rows outlive the record
  status      TEXT NOT NULL,               -- pending | complete
  topic       TEXT NOT NULL,
  fcm         TEXT,                        -- disabled/accepted/failed/suppressed
  webpush_attempted INTEGER, webpush_sent INTEGER,
  webpush_failed INTEGER, webpush_removed INTEGER,
  at          TEXT NOT NULL,               -- acceptance time
  completed_at TEXT                        -- set when dispatch finishes
);
CREATE INDEX idx_event_log_request_id ON event_log(request_id);
```

Registry database schema:

```sql
CREATE TABLE registrations (
  id         TEXT PRIMARY KEY,             -- uuid
  endpoint   TEXT NOT NULL UNIQUE,
  p256dh     TEXT NOT NULL,
  auth       TEXT NOT NULL,
  management_token_hash TEXT NOT NULL,
  source     TEXT NOT NULL DEFAULT 'pwa',  -- pwa | android_up (observability)
  created_at TEXT NOT NULL,
  last_seen  TEXT NOT NULL,                -- heartbeat / registration update
  user_agent TEXT
);

CREATE TABLE registration_topics (
  registration_id TEXT NOT NULL REFERENCES registrations(id) ON DELETE CASCADE,
  topic           TEXT NOT NULL,
  PRIMARY KEY (registration_id, topic)
);
CREATE INDEX idx_registration_topics_topic ON registration_topics(topic);
```

- `event_log` is bounded (retention config, default 30 days) and is the
  audit/abuse record; it contains hashes and topic names only, never
  content or tokens. Each publish writes a row at acceptance (`pending`)
  and updates it at completion; the dispatch-status probe (§5.1.1) reads it
  by `request_id`, and rows older than retention return `404`. Rows left
  `pending` by a restart are closed on startup with legs recorded as
  `failed` (the in-memory queue was lost; audit accuracy, not a delivery
  guarantee).
- **Garbage collection (registry):** `404/410` deletes the registration
  immediately. Nothing is written per send attempt — the publish path
  performs **no per-registration database writes** (§5.1), so a fan-out to
  a million-registration topic costs one `event_log` row, not a million
  updates. A registration whose `last_seen` is older than the configured
  GC TTL is a GC candidate, swept on a low-frequency cadence; an
  authenticated heartbeat or registration update refreshes `last_seen`.
  A registration of a user who neither opens the app nor receives a
  wake-up for longer than the TTL is swept (the PWA re-registers on next
  open, §5.3); this is the accepted hygiene trade.
- Relay per-topic sequence state is deliberately absent from both databases.
  Company trust state MUST survive credential replacement or removal; admission
  changes cannot reset pinned roots or rollback protection.
- Migration: both databases have their own schema version table; the
  relay refuses to start on a mismatched version.

---

## 8. Configuration, Deployment, Operations

- **Single binary** (Go, matching the publisher tooling stack), static
  config via flags/env: listen address, main SQLite path and registry
  SQLite path, service-account JSON path, VAPID keys (or key file), VAPID
  `sub` contact, TTLs, concurrency caps, per-provider
  outbound budgets (§5.4), dispatch queue capacity, fast-path max
  registrations (§5.1), rate limits, event-log retention, TUF-gated
  registration on/off, TUF refresh cadence,
  registry GC TTL (default 30 days — `last_seen` advances only on client
  activity, so a short TTL sweeps live users on quiet channels), replay-cache
  capacity, and approved push-service origins.
- **Provisioning:** a subcommand (`relayctl`-style, or `relay
  publishers add --name … --company company.example`) issues the API key
  (shown once), verifies/pins the company's TUF metadata, and creates the
  publisher record (`auth_mode = "tuf"` default). `--auth key` creates a
  debug-only key-mode record: no TUF verification, no metadata — the API key
  alone authorizes (§5.5). Self-service (§5.2) is config-gated and always
  TUF-mode.
- **Secrets** (highest to lowest sensitivity): FCM service account (can
  publish to every topic in the app's project), VAPID private key (can send
  to every registered PWA subscription), API keys (per publisher, hashed at
  rest), and registration management tokens (hashed at rest). Provider private
  credentials stay in secret storage; raw API/management tokens never enter
  the DB or logs.
- **Scale:** one instance; the publish API accepts quickly and dispatch is
  asynchronous (§5.1): a bounded, in-memory, per-topic coalescing queue
  drained by workers paced to the per-provider budgets (§5.4). The queue is
  deliberately non-durable — a restart loses pending wake-ups (best-effort,
  hard rule 2; clients' replay checks remain authoritative) — so this
  revision needs no outbox. `event_log` is an audit record, not a replay
  source: it does not contain signed envelopes. A durable queue, if ever
  needed, requires a separate outbox contract and storage.
- **TUF client — authorization metadata only.** The relay maintains a
  per-company TUF client for the publish-time authorization check (§5.5):
  root pinned at registration, `timestamp/snapshot/targets` fetched from
  `custom.repo_base`, verified and cached. It never fetches or verifies
  content — feed files and messages remain the app's TUF job.

---

## 9. Security and Privacy Posture

| What the relay knows | What it never learns |
|---|---|
| publisher identity (API key → publisher record, verified TUF company binding for every TUF-mode publisher) | user identity, email, phone |
| device ↔ topic mapping for endpoint registrations (PWA + de-Googled Android; targeted model) | device ↔ topic mapping on the topic leg (FCM — anonymous) |
| scope identifiers, public authorization metadata, source/topic hashes, wake-up volume/timing | order capability tokens (only their hash), message content, anything the app fetches afterwards |
| endpoint-leg payloads (it encrypts them — server-side only) | the wake-up *content* semantics — only that something changed |

- **Bounded power (the load-bearing property):** the relay can spam or
  withhold wake-ups. It **cannot forge** them (wake-up signatures verify
  against the publisher's TUF keys — §4.1/§5.5), cannot forge or alter
  content (the protocol's content-side verification is the only
  trust-bearing step for messages), and never sees order tokens or
  message content. This is why centralizing wake-ups in one shared server
  is acceptable, and it is the reason the relay must never be asked to
  carry content.
- **Residual (accepted):** all publishers are company- and scope-bound.
  A compromised authorized signer can send valid wake-ups within its scope;
  relay and client rate limits bound that abuse. Withdrawn keys are rejected
  when verified metadata contains the withdrawal, not instantaneously.
  The relay's sequence cache can be lost on restart without weakening the
  client's persisted replay checks. Client state loss has the replay limit
  described in §4.2; content verification remains independent.
- **Key-mode records (debug):** an operator-issued `auth_mode = "key"`
  publisher skips signature verification (§5.5). Its wake-ups are never
  accepted by clients (unconditional client verification, §4.1), so the
  residual is bounded recovery traffic plus generic notices — but the key
  is still a blast-capability handle for the company's topics and MUST NOT
  be used in production; the operator audits `publishers.auth_mode`.
- **Wake-up authenticity and recovery:** the relay always verifies before
  dispatch. Clients verify before accepting a wake-up or fetching content.
  Unknown/invalid signatures may trigger one company-wide metadata refresh
  per persisted cooldown (§4.2). Forged messages cannot advance sequence state
  or stop valid cached-key messages during that cooldown. An attacker can
  consume a recovery allowance, delay rotation recovery, and cause bounded
  metadata traffic across devices. These are accepted limits, not a claim
  of zero amplification or guaranteed emergency delivery.
- **UnifiedPush targeted model:** the relay holds device ↔ company mapping
  for every endpoint registration — PWA and de-Googled Android alike
  (unavoidable — endpoints are per-instance). This is the same linkage
  class accepted for APNs/FCM in
  [`../design/why.md` §4.10](../design/why.md), but held by the relay
  instead of the provider. On the de-Googled path the ntfy server sees
  only random capability URLs and ciphertext — never the company mapping.
  Broadcasting every wake-up to every registration is not a fallback in
  this revision: it amplifies client wake-ups and conflicts with browser
  notification requirements (§6.2).
- **Logging:** scope identifiers, source hashes and topic names only;
  `event_log` never logs order tokens or payloads. Access to the relay's
  DBs is a privacy incident by itself (endpoint registry) — treat as
  sensitive.
- **Abuse:** per-publisher rate limits, per-IP subscription throttling,
  outbound destination validation, registration management tokens, VAPID `sub`
  contact for provider abuse contact, TUF metadata refresh rate limits,
  fail-closed relay authorization (§5.5), and client recovery cooldowns (§4.2).

---

## 10. Integration with the Protocol

- Publisher tooling (`pub`) gains a `push` step: after any publish/order
  event, resolve the scope_id from verified metadata, compute the source
  hash and topic (§3), durably allocate `seq`, satisfy that scope's signature
  threshold (§4.1), and call `POST /v1/publish`. The relay performs the same
  algorithm for every scope. The call returns fast (acceptance, §5.1);
  tooling may follow dispatch status (§5.1.1) or ignore it. Failures are
  non-fatal; independent sync covers it.
- When following an item, the app derives the same topic and stores its
  company/scope binding. It subscribes through FCM or adds the topic to
  its endpoint registration (PWA or Android). Delivery identifies that
  existing topic; it does
  not establish new trust or subscribe the app to a scope's other items.
- Routine key rotation MAY use overlapping old/new signatures. Clients
  count only their currently authorized keys; the required threshold is never
  reduced. Emergency rotation need not retain a compromised key: bounded
  recovery (§4.2) and independent metadata refresh learn the replacement.
  Rotation does not reset `seq` or change scope_id/topic.
- This draft changes topic derivation and signature shape from its earlier
  draft. Publisher tooling, relay, and clients must adopt it together and
  recreate subscriptions derived using the old formula; the two formulas
  are not interoperable. These are relay-contract changes, not new fields
  required in the parent protocol's TUF metadata.
- The wake-up is an optimization: publishers that don't want the relay
  simply don't call it. How the app learns whether to expect wake-ups is
  the app's contract, not the relay's.

---

## 11. Open Questions

1. **Client recovery budget** — clients must choose and document X hours
   per company plus a global rate/concurrency budget (§4.2). Larger X reduces
   attacker-triggered traffic but increases possible emergency-rotation delay.
   X is not yet fixed by this component specification; no payload field or
   relay public/private policy is introduced to express it.
2. **Shared-topic ntfy (bare ntfy app)** — users who follow a company's
   shared ntfy topic in the ntfy app directly get the generic "New
   message" display path (no Keryx integration, no relay involvement). In
   v1 at all, and do those topics need read/write keys or just
   unguessable names (the wake-up signature already defeats forgery
   client-side, §6.3)?
3. **TTLs** — endpoint leg default 1 h; FCM default (stores up to 4
   weeks). What cadence matches the protocol's freshness model? Any TTL
   policy is uniform (the relay does not rely on wake-up type).
4. **Order wake-ups through the relay** — order events go through the same
   `/v1/publish` (`scope_id` = that pattern entry's identifier). How does the partner engine
   get its publisher record: TUF-gated registration binds the *company*
   domain (§5.2), so does the engine register the same `company_id`
   (subject to operator policy), or does the company issue sub-keys? The
   engine's wake-up signature verifies against the company's pattern-entry
   keys either way.
5. **Relay identity** — who operates it (the app publisher), and what
   governance applies if more than one app ships against it?

---

## 12. Conformance checks

Implementations must cover these cases; public and private fixtures exercise
the same publish path:

- Identical scope descriptors yield identical scope identifiers; key rotation
  leaves them unchanged. A public channel and private pattern with the same
  label, or two private patterns with different patterns, remain isolated.
- A source hash submitted under another scope cannot reach the original
  scope's topic. Raw topics, subjects, and unknown scope identifiers are rejected.
- Topic-leg reconstruction and endpoint-leg `t` produce identical signed bytes.
  Adapters that cannot recover a delivery topic carry `t`; mismatches fail.
- Threshold one and higher use the same signature array. Duplicate keyids
  cannot satisfy a threshold; extra rotation signatures do not force refresh.
- Invalid signatures never mutate relay/client sequence state. Relay restart
  loses only its cache; clients still reject previously accepted sequences.
- Concurrent invalid wake-ups across one company's topics cause at most one
  recovery attempt per cooldown. Timeouts consume it; client restarts preserve
  it; forged key IDs and different topics cannot bypass it.
- A new authorized key verifies after successful recovery. Failed recovery
  suppresses only further recovery attempts; cached-key valid notifications
  continue. Unknown topics never trigger recovery or select metadata URLs.
- All TUF-mode publishers, including operator-provisioned ones, fail closed
  when signature thresholds or required TUF freshness checks fail; key-mode
  (debug) records are the explicit exception and their wake-ups are never
  accepted by clients (§4.1).
- Registration mutation requires its management token; destination validation
  covers delivery and metadata; registry GC is driven by `404/410` and
  `last_seen`, never by send attempts. PWA and Android endpoint
  registrations are indistinguishable on the publish path; a `404/410`
  from either deletes the registration.
- The publish path writes one `event_log` row per publish and no
  per-registration rows, even for a fan-out covering a million-registration
  topic.
- Publish acceptance is fast and independent of registry size: a large
  fan-out returns `202` with `request_id` before dispatch; results are
  available through §5.1.1 after completion; a small fan-out returns `200`
  with per-leg results.
- Dispatch coalesces per topic: a newer `seq` replaces a pending entry; only
  the latest wake-up per topic is sent. Provider budgets pace dispatch;
  saturation rejects new publishes with `503` instead of silently dropping
  queued work.
- Probe semantics: `pending` → `complete` with per-leg results; another
  publisher's `request_id` is `403`; results describe provider attempts,
  never device delivery.
