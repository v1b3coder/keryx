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
   **company_id** (the company domain), **scope_id** (an authorization handle,
   §5.1), an opaque **source
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
   content through the TUF path), cannot forge a wake-up (the
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
4. **Publishers are registration-free.** No publisher accounts or production
   API keys; no Google/Apple account, Firebase project, or VAPID key on the
   publisher's side. All provider credentials live at the relay. One unsigned
   company metadata synchronization endpoint (§5.2) bootstraps trust on first
   use and refreshes it thereafter. Production publishes are authorized only
   by signatures under the company's verified TUF authorization. Transport
   debugging uses a separate runtime mode and URL endpoints (§5.7).

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
| **wake-up signature** | Ed25519 signature over the canonical wake-up (§4.1), authorized by the topic's exact scope. Always verified by the production relay and by the app. |
| **registration** | A delivery address on the endpoint leg, stored in the registry database: a UnifiedPush/WebPush subscription (`endpoint` + `keys.p256dh`/`keys.auth`), created by the PWA browser or by the Android connector via a distributor (§6.2). |
| **publisher** | A company or partner engine holding the signing keys for a scope authorized by the company's TUF metadata. No relay account or production API key is required. |
| **company synchronization** | An unsigned request to bootstrap or refresh the relay's TUF state for a company domain. TOFU applies only when no trusted state exists; subsequent updates use standard TUF verification, including root rotation (§5.2). |
| **request_id** | A short-lived, unguessable bearer capability for reading one publish's dispatch status and following its supersession chain; it grants no publishing or registration-management rights (§5.1.1). |
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

topic = base64url_nopad(sha256("keryx/relay/v1|" + OLPC({company_id, scope_id, h})))
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
- **Company and scope binding (relay-side, normative).** The caller supplies
  canonical `company_id` to select the relay's persisted company TUF state
  (§5.2/§5.5); that input alone grants no authority. `scope_id` MUST resolve
  in that company's verified authorization table (§3.1). The relay derives
  the topic using this company and verifies the signature over that topic.
  Changing `company_id`, `scope_id`, or `h` therefore changes the signed
  topic and requires an authorized signature for it. Submitting another company's or
  scope's source hash cannot reach that other namespace's topic. The
  publish path MUST NOT inspect the subject or branch on its type.
  OLPC input is exactly the three string fields shown above, encoded as UTF-8.
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
(from its verified TUF state) and the scope_id (authorization handle) —
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
scope_id = hex(sha256("keryx/relay/scope/v1|" + OLPC(descriptor)))
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
- `seq` — monotonic sequence value for this topic, supplied by the caller
  (**required**; integer from 1 through 9007199254740991). By convention it
  is the event's Unix timestamp in seconds, but never less than one more
  than the last value the caller emitted for that topic
  (`seq = max(now_seconds, last_seq + 1)`, §4.2). Callers MUST NOT use it as
  a timestamp (not a feed-order key, not an expiry); clients MUST NOT
  compare it to their own clock. Because `sig` covers `seq`, the app MUST
  reject a wake-up whose `seq` is ≤ the persisted last accepted value for
  that topic (§4.2). A sequence gap is not proof of loss — with
  timestamp-derived values it is merely elapsed time. Never used for inbox
  ordering — the app reconciles by fetching; ordering is by the item's
  `date_published` and dedup is `(channel, id)`
  ([`../spec/feeds.md`](../spec/feeds.md)). Unrelated to any per-item
  sequence number — the item format has no sequence field
  ([`../design/why.md` §8](../design/why.md)).
- `sig` — the wake-up signatures (§4.1), **required for every scope and leg**.
- Nothing else. In particular: no title, no body, no URL, no company name,
  no status text, no raw order token, no unread hint (`n`). Unread state is
  derived by the app from verified content and local read state. No payload
  timestamps or authorization epochs are needed (`seq` is a monotonic
  sequence value, not a timestamp field). Reject unknown fields,
  duplicate JSON member names, malformed encodings, and non-integer counters.

The app verifies against its locally trusted authorization for the topic's
scope. Failure permits one bounded metadata refresh (§4.2), not acceptance
of the wake-up. Content fetching and sequence advancement require successful
verification. Content itself is always independently verified.

### 4.1 Wake-up signature

```
signature = Ed25519( "keryx/wakeup/v1|" ‖ OLPC({ v, t, seq }) )
```

- Signed bytes: the domain separator `"keryx/wakeup/v1|"` followed by the
  **OLPC canonical JSON** (securesystemslib — the same canonicalization the
  protocol uses for TUF metadata and item signing), encoded as UTF-8, of exactly
  `{v, t, seq}`. `t` is ALWAYS signed, whether carried in the payload or
  recovered from delivery. The `sig` array itself is excluded.
  OLPC and domain separation are mandatory: Ed25519
  signatures are reusable across message types, and the protocol applies the
  same canonicalization and discipline to item signatures
  ([`../spec/feeds.md`](../spec/feeds.md#12-signing-and-verification)).
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
- **Who verifies:** the production relay at publish (§5.5) and the app on delivery.
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

**Sequence ownership:** `seq` is the writer's event timestamp (Unix
seconds) with a monotonic guard: `seq = max(now_seconds, last_seq + 1)`,
where `last_seq` is the value the writer last emitted. This requires no
per-topic state and no coordination between writers; each writer keeps at
most **one** scalar, persisted across restarts to survive backward clock
jumps (NTP correction, snapshot restore) — losing it degrades to `now`,
which is safe unless the clock moved backwards. The guard absorbs
same-second double publishes, forward clock jumps, and backward jumps
while running. Cross-writer caveat: two writers publishing to the same
topic with skewed clocks may drop a wake-up when both fall in the same
second — a latency cost (the client reconciles by fetching; polling is
the backstop), never a correctness cost. The relay rejects publishes
whose `seq` is ahead of its clock by more than the configured tolerance
(§5.1), so a compromised or buggy signer cannot poison a topic's
high-water mark with an implausible future value. Retries reuse the same
signed envelope. Clients persist their per-topic high-water marks; relay
replay state is memory-only (§5.5). A fresh client or cleared client
storage can accept an old signed wake-up; this is a known limit without
retained replay state. A compromised signer can advance `seq` excessively;
ordinary content sync remains available, but this draft does not introduce
an epoch/reset mechanism.

### 4.3 Self-test payload (not a wake-up)

The registration self-test (§5.3.1) delivers a distinct payload, never a
wake-up:

```json
{ "v": 1, "test": true, "nonce": "<43-char base64url>" }
```

- It carries no topic, no `seq`, and no `sig`, and the relay never signs
  it. A client MUST NOT treat it as a wake-up: no content fetch, no sequence
  advancement, no recovery allowance, no authorization change. A client that
  receives it outside a pending test ignores it.
- `nonce` is generated by the relay and returned with the test. A client
  accepts a test only while it is waiting for that nonce (one pending nonce
  per install, cleared on use or expiry). A payload without the pending
  nonce MUST NOT be reported as a successful test.
- A forged test payload can therefore cause at most a locally authored generic
  notice — within the spam bound already accepted for a malicious relay (§9).

---

## 5. Publisher API

Base: `https://<relay-host>/v1/`. Requests and responses use JSON over HTTPS
unless an endpoint specifies no body. Production publishing requires a
wake-up signature under verified, unexpired TUF authorization (§5.5); there
are no publisher API keys. Company synchronization is unsigned (§5.2), and
dispatch status uses the returned `request_id` as a bearer capability
(§5.1.1). Device registration management has its own ownership tokens (§5.3).
Debug API keys are accepted only in the separate transport-debug mode (§5.7).

### 5.1 `POST /v1/publish` — fan out a wake-up

Request (one shape for channels and orders — no type marker):

```json
{
  "v": 1,
  "company_id": "company.example",
  "scope_id": "<64-char lowercase hex>",
  "h": "<64-char hex>",
  "seq": 7,
  "sig": [{"keyid": "<64-char lowercase hex>", "sig": "<base64url>"}]
}
```

- `company_id` — the canonical company domain (§2). Selects existing TUF
  state; an unknown company must first be synchronized through §5.2. Publish
  requests do not perform first-use TOFU or issue credentials.
- `scope_id` — the opaque authorization handle (§3.1). Resolve it in the
  verified company's authorization table; no type marker or channel label
  is accepted. It is not a key identifier and does not change on key rotation.
- `h` — the source hash (§3): `hex(sha256(company_id + "|" + subject))`;
  the caller computes it, the relay never sees `subject` (for orders it
  never sees the token at all). MUST be exactly 64 lowercase hex chars.
  The relay MUST treat it as opaque and MUST NOT validate it against a
  channel name, pattern, or raw subject. The same derivation and authorization
  algorithm applies to every scope; there is no public/private branch.
- `seq` — **required**; monotonic per topic, supplied by the caller per
  §4.2 (timestamp with monotonic guard), forwarded to the payload (§4).
  Enables replay suppression (§4.1). The relay rejects a `seq` ahead of its
  clock by more than the configured tolerance (`400`).
- `sig` — the signature array (§4.1), required for every production publish.
  The relay verifies the exact scope's threshold before replay-cache mutation
  or enqueue. No API token can bypass or substitute for this verification.

The uniform publish workflow is: select company TUF state → resolve scope →
derive topic → verify signature threshold (§5.5) → atomically admit dispatch
and advance the in-memory sequence cache (below). The client independently
derives the same topic when subscribing; neither keys nor scope identifiers are delivery
subscriptions.

**Acceptance boundary:** serialize admission per topic. Check replay state,
reserve dispatch capacity, and commit the acceptance audit row (including any
supersession) before making work visible to workers and advancing replay state.
These form one logical acceptance operation on both paths. On admission or
storage failure, release reservations and leave the replay cache, existing
queued work, and its audit state unchanged. In particular, `503` for a full
queue MUST NOT suppress a later retry of the same signed envelope. A pending
entry can be replaced without reserving an additional queue slot. Process
failure after durable audit commit remains subject to the documented
non-durable queue/restart behavior (§7).

Response — fast path (`200 OK`). When the fan-out is small — no endpoint
registrations for the topic, or at most `fast-path-max-registrations`
(config, default 500) — the relay dispatches synchronously and returns the
per-leg results:

```json
{
  "topic": "<43 chars>",
  "suppressed": false,
  "providers": { "fcm": "accepted",
                 "webpush": { "sent": 0, "failed": 0, "dead": 0 } }
}
```

Response — async path (`202 Accepted`). When the topic's endpoint registry
exceeds the fast-path threshold, the relay accepts the publish and returns
immediately, before any dispatch:

```json
{ "topic": "<43 chars>", "request_id": "<43-char secret>", "status": "accepted",
  "expires_at": "<RFC 3339 UTC timestamp>" }
```

Dispatch continues in the background; the publisher follows it via
§5.1.1. A replay-suppressed publish returns `200` with `suppressed: true`
(§5.5) on either path and carries no `request_id`.

- `fcm` is `disabled`, `accepted`, `failed`, or `suppressed`.
  Acceptance is by the provider, not proof of device delivery; a topic with
  zero subscribers can still be accepted. Errors never count as acceptance.
- `webpush`: `sent` = registrations for this topic the provider accepted;
  `failed` = permanent errors or transient errors after retry exhaustion;
  `dead` =
  registrations reported dead by the provider (`404/410`); no registry write
  occurs during dispatch (§7).
- **Dispatch (async path).** The relay keeps a bounded, in-memory dispatch
  queue — one entry per topic, holding the **latest accepted** wake-up for
  that topic. Coalescing: a newer `seq` replaces a pending entry; an older
  or duplicate one is a no-op. The replaced request becomes `superseded`
  and links to the newer request's status capability (§5.1.1). A replacement
  always uses the async response, even if the registry has since shrunk below
  the fast-path threshold. Once any provider attempt starts, that dispatch
  cannot be superseded: finish its results normally and coalesce later
  publishes into a separate pending entry for the same topic, returning `202`
  regardless of registry size. Serialize dispatches for each topic. FCM is a single provider call; the endpoint
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
- Errors: `400` schema violation (noncanonical `company_id`, malformed `scope_id`
  or `h`, invalid `seq` — including a value implausibly far in the future,
  §4.2 — missing/malformed `sig`); `404` company not yet known
  (synchronize through §5.2); `403` unknown scope in the
  verified company or signature threshold not satisfied; `503` required TUF
  authorization unavailable/expired while refresh is pending or failed (§5.5), or dispatch
  queue saturated (§5.4); `429` rate limit (see §5.4).

Duplicate wake-ups are harmless (the app diffs content anyway) and coalesce
in the dispatch queue; no idempotency key is required.

### 5.1.1 `GET /v1/publishes/{request_id}` — dispatch status

Possession of `request_id` is the only authorization required: no API key or
request signature. Generate it from 32 cryptographically random bytes,
encoded as canonical unpadded base64url (43 characters). It authorizes only
reading this dispatch's status and following its supersession chain. Store
the capability itself in `event_log` (§7) as the lookup key — raw status
capabilities are acceptable at rest because they are read-only and expire
within their TTL, and they grant nothing but this dispatch's status. The
caller retains the secret returned at acceptance. When superseding a
pending request, record the successor's row id in the predecessor row; the
successor capability is read from its own row at probe time, so no capability
is ever stored twice. Management tokens and any capability that can mutate
state remain hashed or in secret configuration.

Capabilities expire independently of audit retention: configurable TTL,
default **1 hour from acceptance**, returned as `expires_at` in the `202`
response. Expiry does not cancel dispatch. Invalid, unknown, and expired
capabilities return `404`, even if the audit row still exists. Responses
MUST use `Cache-Control: no-store`. Raw capabilities, including `superseded_by`, MUST NOT enter access
logs, tracing, or analytics; redact the request-id path segment at the relay
and any reverse proxy. Status reads are rate-limited by IP and globally.

- `200` while dispatch is in progress:
  `{ "request_id": "<43-char secret>", "topic": "<43 chars>", "status": "pending" }`
- `200` when superseded before dispatch began:
  `{ "request_id": "<original secret>", "topic": "<43 chars>", "status": "superseded",
     "superseded_by": "<newer request_id>", "expires_at": "<successor expiry>" }`
  This is terminal for the original request and carries no provider results.
  `superseded_by` is the successor capability, read from the successor's own
  row — each capability is stored once, in its own row; `expires_at` is that
  capability's own expiry. Tooling follows `GET /v1/publishes/{superseded_by}`
  to monitor the replacement; repeat if that request is also superseded.
  Links only point to later accepted requests for the same topic, so chains
  cannot cycle. Each capability retains its own expiry and retention rules;
  following a link grants access to the successor's status, not proof that the
  original envelope was sent. An expired or removed predecessor returns `404`
  and cannot reveal its successor; if the successor row itself has been
  removed by retention, return `superseded` without the link (following it
  would `404` anyway).
- `200` when complete (all enabled legs attempted):

```json
{ "request_id": "<43-char secret>", "topic": "<43 chars>", "status": "complete",
  "providers": { "fcm": "accepted",
                 "webpush": { "attempted": 1000, "sent": 950,
                              "failed": 20, "dead": 30 } } }
```

- `webpush.attempted` = registrations for the topic that were attempted;
  `sent`/`failed`/`dead` partition it (push-service accepted / retries
  exhausted / endpoint reported dead). No counts are returned while `pending`;
  progress reporting is out of scope for this revision.
- **Results describe attempts, not deliveries.** There is no device
  acknowledgement on any leg: `sent` means the push service accepted the
  message, not that a device received or displayed it; `fcm` reports
  provider acceptance only — per-device information does not exist on the
  topic leg (no registry, §1).
- `404` also applies when the audit row has been removed by retention (§7).
  `429` indicates the probe's read budget is exhausted.

Authorization is **company- and scope-bound for every publisher**. A signer
can address hashes only within its authorized scope, not sibling scopes or
other companies. Withdrawn keys are rejected once the withdrawal is present
in verified metadata; cached authorization remains usable only while required
metadata is unexpired. This is not instantaneous global revocation (§5.5).

### 5.2 `POST /v1/companies/{company_id}/refresh` — synchronize company metadata

One **unsigned** endpoint for first-use bootstrap and later refresh. No body,
API token, request signature, or publisher account. The canonical domain in
the path (§2) identifies the company; the request cannot supply keys,
metadata, repository URLs, or a trust-reset instruction. It requests a check
of authoritative metadata and never sends notifications. Publisher tooling
calls it before its first publish and after changing metadata, including key
rotation or withdrawal. No old signing key is needed to request recovery.

The relay uses the [standard TUF client workflow](https://theupdateframework.github.io/specification/latest/#detailed-client-workflow)
and the protocol's repository layout
([`../spec/repository.md` §2](../spec/repository.md)). The only extra
bootstrap step is **TOFU for a domain with no previously trusted state**:

1. For an unknown company, fetch
   `https://<company_id>/.well-known/keryx/root.json` with HTTPS certificate
   validation, no redirects, and the outbound bounds in §5.6. Validate the
   root's structure and self-signature threshold before persisting it as the
   initial trust anchor. HTTPS delivery by this domain establishes the initial
   domain-to-root binding; self-signatures alone do not prove domain ownership.
2. For a known company, load its persisted trusted root. In both cases, continue
   with standard TUF updates, including sequential `N.root.json` rotation from the existing
   well-known anchor. Pinning preserves continuity of trust; it does **not**
   freeze root keys. Root replacement must satisfy standard TUF rotation
   verification, never fresh TOFU. Concurrent bootstrap/refresh operations
   for one domain are serialized.
3. From the verified root's `custom.repo_base`, refresh `timestamp.json`,
   `snapshot.json`, and `targets.json` using standard TUF version, signature,
   hash, expiry, and rollback checks (`consistent_snapshot: false`). Fetch
   metadata only. Persist trusted-state transitions as required by the TUF
   client, including valid root updates when a later step fails.
4. After a successful update, atomically expose the authorization table (§3.1)
   for the verified metadata. New keys, changed thresholds, and removed keys
   or scopes take effect for subsequent publish authorization. Update
   `refreshed_at` only after successful synchronization, including a successful
   check that metadata is unchanged. Failure never extends authorization expiry.

Known-company trust state MUST survive expiry, partial bootstrap, refresh
failure, process restart, and eviction of in-memory caches. These conditions
must not make a domain eligible for TOFU again. Retain the TUF client's
persisted root and verification state; apply any state resets required by
standard TUF root rotation through that client, never by clearing the company
record in response to an HTTP request. A pinned company without usable
authorization remains known and fails publishing closed until synchronization
succeeds.

**Scheduling and abuse bounds:**

- At most **one synchronization attempt starts per 60 seconds per canonical
  company_id**, shared by this endpoint, scheduled updates, and stale-cache
  recovery (§5.5). Failures and timeouts consume the interval too. Reserve
  the next eligible time before networking; persist it for known companies.
- Keep at most one running attempt and one pending follow-up per company.
  Requests during cooldown schedule one attempt at the next eligible time;
  repeated hints coalesce without moving that time later. A hint received
  during an active attempt may reserve that single follow-up so changes
  published during the current fetch are not missed. Global scheduling may
  delay execution, but additional hints must not postpone it.
- Use a bounded, fair queue plus global fetch-rate/concurrency limits. Unknown
  company discovery has a separate, stricter global and per-IP admission budget
  and bounded scheduling/storage capacity. Preserve unknown-domain cooldown
  entries until their interval elapses; if capacity is full, reject new
  discovery work instead of evicting an entry to bypass its cooldown.
  Already-pinned companies retain their trust state when admission is full.
- Bound each attempt's total requests, downloaded/decompressed bytes, and
  elapsed time, including root-rotation chains. If a long chain needs more
  than one attempt, continue from persisted TUF progress on a later attempt.
- Scheduled refresh continues independently of hints. An unsigned hint cannot
  clear trusted state, prolong metadata validity, or postpone ordinary refresh.
  The one-minute interval bounds per-company amplification, not total traffic
  across companies, and is not a guarantee of completion within one minute.

Response: `202 Accepted` with
`{ "company_id": "company.example", "status": "scheduled" }` for newly
scheduled or coalesced work. This acknowledges scheduling, **not** successful
TUF verification; there is no credential to return. Tooling retries publishing
after synchronization: before a root is pinned it receives `404`, and a known
company without usable authorization returns `503` (§5.5). A restart may lose
pending work; scheduled refresh or another hint
recovers it without resetting trust or the persisted cooldown.

Errors: `400` malformed/noncanonical domain or unexpected request body; `429`
admission/rate limit with `Retry-After`; `503` scheduling capacity unavailable.
Fetch or verification failures after `202` are recorded in operational metrics
and leave publishing governed by §5.5. Requests already represented in the
queue coalesce without consuming another queue slot.
- Role rights (who may sign wake-ups, enforced at publish — §5.5):

| TUF role | Wake-up authority |
|---|---|
| `channels.<channel>` (channel key, CI) | signs wake-ups for **that channel** |
| `private_feed_patterns` entry keys (engine key) | signs wake-ups for orders under **that pattern** |
| `root` / `targets` (master, offline) | none — authorizes metadata and its updates; master never signs wake-ups |
| `snapshot` / `timestamp` (online ops key) | **none** — freshness key only; giving it wake-up authority would expand its blast radius to all channels |
| `channels.<channel>.authors` keys | **none** — item-level signing only |

### 5.3 Registration API (UnifiedPush endpoints)

One registration per install per client — PWA (browser `PushManager`) and
de-Googled Android (UnifiedPush connector via a distributor, §6.2) both
register here — plus the set of topics it follows. Endpoints have no
topics, so the relay stores registration ↔ topic mappings in the
**registry database** (§7).

| Method + path | Body | Meaning |
|---|---|---|
| `POST /v1/registrations` | `{ "endpoint": "https://…", "keys": { "p256dh": "…", "auth": "…" }, "topics": ["<43 chars>", …] }` | Create registration. Returns `{ "id": "<uuid>", "management_token": "<secret>" }` once. Existing endpoint: `409`, no overwrite; use authenticated PUT for replacement. |
| `PUT /v1/registrations/{id}` | `{ "topics": [ … ], "endpoint": "https://…", "keys": { "p256dh": "…", "auth": "…" } }` | Replace topics; optionally replace endpoint and keys together. Omitted endpoint/keys remain unchanged. Preserve id and management token. |
| `DELETE /v1/registrations/{id}` | — | Remove the installation's entire registration. Removing one company uses PUT to remove only its topics. |
| `POST /v1/registrations/{id}/heartbeat` | — | Liveness ack (no body): bumps `last_seen`. Sent by the service worker on wake-up receipt, and by the app (PWA or Android) on foreground. `204 No Content`. |
| `POST /v1/registrations/{id}/test` | — | Deliver one self-test payload (§4.3) to this registration's endpoint. Management token required; `202 { "nonce", "expires_at" }` acknowledges the provider attempt, not device receipt. |
| `POST /v1/fcm/test` | — | Start one FCM self-test: `202 { "test_id", "topic", "nonce", "expires_at" }`. The relay never sees the device's FCM token. |
| `POST /v1/fcm/test/{test_id}/ready` | — | The client subscribed to `topic`; publish the test payload. `test_id` is the capability. |

- The relay POSTs to `endpoint` during delivery. It MUST be HTTPS and pass
  the outbound-request policy (§5.6). Validate subscription key encodings
  before storage. Registration is rate-limited by IP.
- Updates, deletion, heartbeat, and endpoint/key replacement MUST require
  `Authorization: Bearer <management_token>` for that registration. Generate
  a random 256-bit token and store only its SHA-256 hash. Missing/invalid
  credentials return `401`; never return an existing token from registration.
  An unknown registration id returns `404`; `401` does not imply absence.
  PUT validates the entire replacement before mutation; endpoint and keys must
  be supplied together, pass the same validation as creation, and an endpoint
  owned by another registration returns `409` without modifying either record.
  A shared secret embedded in a public app build is not ownership proof.
- `topics` are derived per §3; the relay accepts only 43-char base64url
  topics and rejects others.
- **Why the payload still needs `t` (§4):** the registration knows *which*
  topics it follows, but a delivered message carries no topic — so the
  wake-up payload must name it. The registry makes delivery *possible*;
  the payload makes it *legible*.
- **Lifecycle:** track `created_at` and `last_seen` (§7). Persist the id and
  management token together after creation. On `404` (e.g. after GC), POST
  the current subscription and followed topics. On endpoint/key rotation,
  use authenticated PUT when the existing id/token is available.
  On token loss, `401`, or a lost creation response followed by `409`, do not
  repeatedly POST the same endpoint: the relay record may still exist.
  Unsubscribe/unregister the old push subscription through the browser or
  Android connector, obtain a fresh subscription with a different endpoint,
  and POST it with the current topic set. The old relay record expires through
  GC; unauthenticated recovery never overwrites it or reveals its token.
  If a different endpoint cannot be obtained, defer registration and retry
  through the provider's lifecycle; independent content sync remains available.

#### 5.3.1 Self-test (`POST …/test`)

An explicit, user-triggered delivery test — the honest answer to "are my
wake-ups actually working?", without a periodic-notification heuristic (a
company that publishes rarely would make such a heuristic false-positive).
Both legs are tested; neither reveals a device identifier to the relay.

**Endpoint leg.** The registration's management token authorizes the delivery
of the §4.3 payload through the ordinary endpoint path (RFC 8291, VAPID,
TTL, outbound policy). `202` acknowledges the provider attempt, not device
receipt.

**FCM leg.** The relay never learns the device's FCM token: the topic leg
stays registry-free and anonymous (§9). The test therefore uses a
short-lived, relay-generated topic:

1. `POST /v1/fcm/test` returns `202 { "test_id", "topic", "nonce",
   "expires_at" }`. `topic` is 43-char base64url derived from 32
   cryptographically random bytes; the relay keeps `{topic, nonce, state,
   expires_at}` in memory only and never durably.
2. The client subscribes to `topic` through the native FCM SDK, then calls
   `POST /v1/fcm/test/{test_id}/ready`. `test_id` is the unguessable
   capability that authorizes this call; the relay then publishes
   `message.topic` with the §4.3 payload under `message.data.test`, once
   or a few times within `expires_at` to absorb FCM subscription
   propagation.
3. The client accepts the first payload whose `nonce` matches, unsubscribes
   from `topic`, and reports the result. A payload without the pending nonce
   is ignored and MUST NOT be reported as a successful test.

The relay generates the topic; the client cannot choose it, so it cannot be made
to subscribe to a widely known one, and a test reaches only the installation
that started it. Both test endpoints are rate-limited (§5.4); an unknown or expired
`test_id` returns `404`.

- A test is never a wake-up: it MUST NOT touch replay state, authorization,
  the registry, or sequence state, and it MUST NOT schedule content
  reconciliation. It consumes no publish budget.
- The client reports "delivered" only when its own handler observes the
  matching nonce (§4.3). The relay cannot observe device receipt and MUST NOT
  claim it.
- Errors: `404` unknown or expired registration or `test_id`, `401` invalid
  management token, `400` rejected destination, `410` endpoint reported dead
  (404/410 from the push service), `503` provider unavailable, `429` rate
  limit.

### 5.4 Limits

Per verified company (configurable): default 60 publishes/minute, burst 120,
shared by its scopes and signing keys; rotation does not create a fresh
company budget. Bound unauthenticated publish traffic by IP and globally
before signature work. The client IP is the transport peer unless the operator
configures a trusted platform proxy's client-IP header (e.g. Fly.io's
`Fly-Client-IP`), which MUST only be trusted when the relay is reachable
exclusively through that proxy. Invalid requests must not consume another
company's authenticated publish budget merely by naming its domain.
Per subscription: topic count ≤ 200. Global: the relay MUST rate-limit
subscription registration by IP. Company synchronization shares the
one-attempt-per-minute scheduler and discovery limits in §5.2. Probe reads
have separate IP/global budgets (§5.1.1). Self-test deliveries are separately
rate-limited per registration and per IP, the FCM test has its own per-IP and
global admission budget and shares the FCM outbound budget, and a test never
consumes the publish budget.

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

Every production publish verifies its own signature against the company's
cached, verified, unexpired TUF authorization. This does **not** download or
re-verify the metadata chain for every request. There is no API-key fallback.
Cached authorization is not proof that no newer metadata exists.

- **Per-company TUF client:** standard tooling maintains the trust state;
  TOFU occurs only for an unknown domain and subsequent root rotations and
  metadata updates follow §5.2. No separate publisher credential state exists.
- **Resolution:** load the uniform scope table (§3.1) from verified metadata,
  then look up `(company_id, scope_id)`. The publish path knows only keys and
  threshold; it MUST NOT branch on public/private channel type. The signature
  array must satisfy the exact scope's threshold (§4.1).
- **Cache/refresh:** verified metadata is cached per company with its
  version state (persisted in the main DB, §7) and refreshed in the
  background on a cadence (config; the protocol's own timestamp cadence is
  24–72 h), through unsigned publisher hints (§5.2), and on demand when stale.
  All triggers share §5.2's scheduler. The common publish is a signature and
  expiry check against cached authorization — no network. An unknown company
  returns `404` and must bootstrap via §5.2. An unknown scope or invalid
  signature under otherwise usable cached authorization returns `403`;
  tooling requests synchronization explicitly after metadata changes.
- **Fail policy (fail closed):** cached metadata that is **unexpired** is
  used only while the TUF client's current trusted state permits that
  authorization; a partially completed update must not resurrect authority
  invalidated by a verified root rotation. If usable authorization is expired
  or absent for a known company, schedule/coalesce refresh and **reject** the
  publish with `503` while recovery is pending or failed. Refresh failure does
  not extend expiry or erase trust history. Cost: a
  publisher's metadata outage temporarily blocks its wake-ups —
  acceptable, since wake-ups are best-effort and polling is the backstop.
- **Replay:** after signature verification, atomically compare `seq` with a
  bounded in-memory LRU mapping `topic → highest_seen_seq`. Values ≤ the
  cached value are suppressed before enqueue; a higher value updates the
  cache only as part of successful dispatch admission (§5.1). A rejected
  publish never advances it. The cache is updated at acceptance, before dispatch.
  A suppressed publish returns
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
chosen ntfy server. Entries are exact origins or `*.` host wildcards
(e.g. `https://*.push.apple.com`), where a wildcard matches the suffix
host and its subdomains. The operator may instead explicitly configure
`any`, which accepts any public HTTPS endpoint while keeping every other
check in this section; it is an opt-out for relays that serve arbitrary
self-hosted UnifiedPush distributors, not a default. Signed `repo_base`
metadata authenticates its source, not the safety of its network destination.

### 5.7 Transport-debug mode (development/testing only)

An explicit runtime flag `--debug-transport` selects a separate debugging
mode. Production is the default. In debug mode, expose
`POST /debug/v1/publish` and `GET /debug/v1/publishes/{request_id}`; do not
mount the production publish or company-synchronization routes. In production,
debug routes return `404`. The two publishing modes are mutually exclusive
within an instance; no request or database record can switch authorization
mode or fall back from production verification to debug authorization.

Debug publish requires `Authorization: Bearer <debug-api-key>`, configured
once for the test instance at startup. Missing configuration prevents debug
startup; missing/invalid request credentials return `401`. There are no
per-publisher debug accounts, provisioning flows, or credential tables.
Use the §5.1 request shape, including `company_id`, but allow `sig` to be
omitted. Validate routing fields and derive the topic normally, then bypass
TUF lookup and signature verification. This exercises the shared queue,
provider adapters, encryption, and result accounting. Debug status uses the
same short-lived request-id capability contract as §5.1.1.

Keep debug runs on separate test databases, provider projects/credentials,
and subscriptions so they cannot mutate production replay state, trust,
registrations, or dispatch work. Reuse the transport implementation; do not
build a second production authorization architecture. Device registration
routes (§5.3) remain available against the test registry with their ordinary
management-token checks. Provider pacing and outbound validation still apply.

Production client verification is unchanged. Unsigned or invalid debug
envelopes are not accepted as wake-ups; a correctly signed envelope can be
accepted by a test client through its normal verification path. Debug mode
does not make unsigned messages trusted by clients.

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
- **Self-test:** a short-lived, relay-generated `message.topic` (never a
  device token, never a topic the client follows) with the §4.3 payload under
  `message.data.test` — a one-shot, user-triggered send that reveals no
  device identifier (§5.3.1).
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
- **Errors:** `404`/`410` → count `dead`, do not retry or mutate the registry;
  stale registrations are removed by the TTL sweep (§7).
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

- **Main database** (`-db`): `company_tuf`, `event_log` —
  trust-state, publishing, and metadata-refresh writes. No publisher accounts.
- **Registry database** (`-registry-db`): `registrations`,
  `registration_topics` — write-heavy (registration/follow/unfollow/
  heartbeat churn); kept separate so the endpoint registry's volume never
  touches the main production DB.

Main database schema:

```sql
CREATE TABLE company_tuf (                 -- standard TUF trust state per domain
  company_id    TEXT PRIMARY KEY,
  root          TEXT NOT NULL,             -- current trusted root, updated by TUF rotation
  root_version  INTEGER NOT NULL,
  chain_state   TEXT NOT NULL,             -- persisted TUF state, possibly partial; usable scopes derived from it
  refreshed_at  TEXT,                      -- last successful full sync; NULL until bootstrap completes
  next_refresh_at TEXT NOT NULL            -- earliest next attempt; reserved before network access
);

CREATE TABLE event_log (
  id          INTEGER PRIMARY KEY,
  request_id  TEXT NOT NULL UNIQUE,        -- raw status capability; lookup key (§5.1.1)
  request_expires_at TEXT NOT NULL,        -- capability lifetime, independent of audit retention
  company_id  TEXT NOT NULL,
  scope_id    TEXT NOT NULL,
  status      TEXT NOT NULL,               -- pending | complete | superseded
  superseded_by_id INTEGER,                -- successor row id; superseded only (§5.1.1)
  topic       TEXT NOT NULL,
  fcm         TEXT,                        -- disabled/accepted/failed/suppressed
  webpush_attempted INTEGER, webpush_sent INTEGER,
  webpush_failed INTEGER, webpush_dead INTEGER,
  at          TEXT NOT NULL,               -- acceptance time
  completed_at TEXT                        -- set on completion or supersession
);
```

The TUF schema is a logical storage contract: an implementation MAY use its
standard TUF library's persistent store in place of `root`/`chain_state`,
provided trust continuity, partial-update recovery, and the synchronization
cooldown survive restarts. Do not implement a second TUF verification engine
to match these illustrative columns. A partial bootstrap may have a trusted
root and no usable scope table; it is still a known company (§5.2).

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
  audit/abuse record; it contains dispatch metadata and status capabilities,
  never content or management tokens. Status capabilities are stored raw as
  the lookup key (§5.1.1): they are read-only and expire within their TTL, so
  a database leak exposes at most transient dispatch status, never publishing
  or registration rights. Each accepted publish writes a
  row (`pending`) and updates it at completion or supersession; the
  dispatch-status probe (§5.1.1) reads it
  by the capability itself. Expired capabilities and rows removed by
  retention return `404`. Raw status capabilities are otherwise never
  persisted. Rows left
  `pending` by a restart are closed on startup with legs recorded as
  `failed` (the in-memory queue was lost; audit accuracy, not a delivery
  guarantee).
- **Garbage collection (registry):** dispatch performs **no per-registration
  database writes**, including on `404/410`. Dead responses contribute only
  to aggregate event results; they do not delete rows or persist cleanup tasks.
  A fan-out to a million-registration topic costs one `event_log` row, not a
  million updates. Registry mutations occur only through client management
  operations and the independent low-frequency TTL sweep. Dead endpoints may
  be attempted again on later publishes until that sweep removes them.
  A registration whose `last_seen` is older than the configured
  GC TTL is a GC candidate, swept on a low-frequency cadence; an
  authenticated heartbeat or registration update refreshes `last_seen`.
  A registration of a user who neither opens the app nor receives a
  wake-up for longer than the TTL is swept (the PWA re-registers on next
  open, §5.3); this is the accepted hygiene trade.
- Relay per-topic sequence state is deliberately absent from both databases.
  Company trust state is durable and is not registry/event-log GC data. Cache
  eviction, expiry, synchronization failure, and admission-policy changes
  cannot reset a known domain to TOFU. Root rotation proceeds through the
  standard TUF client (§5.2).
- Migration: both databases have their own schema version table; the
  relay refuses to start on a mismatched version.

---

## 8. Configuration, Deployment, Operations

- **Single binary** (Go, matching the publisher tooling stack), static
  config via flags/env: listen address, main SQLite path and registry
  SQLite path, service-account JSON path, VAPID keys (or key file), VAPID
  `sub` contact, TTLs, concurrency caps, per-provider
  outbound budgets (§5.4), dispatch queue capacity, fast-path max
  registrations (§5.1), company/IP/global rate limits, event-log retention,
  dispatch-status capability TTL (default 1 hour), TUF refresh cadence,
  synchronization queue/fetch limits, unknown-company discovery limits,
  registry GC TTL (default 30 days — `last_seen` advances only on client
  activity, so a short TTL sweeps live users on quiet channels), replay-cache
  capacity, replay-seq future tolerance (default 5 min), approved push-service
  origins (strict list or explicit `any` mode), and the explicit debug-mode
  flag/API-key configuration (§5.7). The per-company minimum synchronization
  interval is 60 seconds (§5.2).
- **Liveness:** `GET /healthz` returns `200` for platform health checks. It
exposes no state, is never rate-limited, and is not logged.
- **Company synchronization:** no publisher provisioning or key issuance.
  Tooling calls §5.2 for first-use TOFU and subsequent standard TUF updates.
  Existing company trust state is retained across configuration changes.
- **Transport debugging:** `--debug-transport` with a configured debug API key
  enables only the alternate publishing routes (§5.7). Use test databases,
  subscriptions, and provider credentials; never deploy this mode as the
  production relay.
- **Secrets** (highest to lowest sensitivity): FCM service account (can
  publish to every topic in the app's project), VAPID private key (can send
  to registered endpoint subscriptions), the debug API key (test mode only),
  registration management tokens, and dispatch-status capabilities. Provider
  private credentials and the debug key stay in secret configuration;
  management tokens are hashed at rest. Dispatch-status capabilities are
  stored raw in `event_log` (§5.1.1): they are read-only and expire within
  their TTL, so a database leak exposes at most transient dispatch status,
  never publishing or registration rights. No bearer secret ever enters logs,
  traces, or analytics.
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
  root bootstrapped by domain TOFU once, then rotated by standard TUF tooling;
  `timestamp/snapshot/targets` fetched from the verified `custom.repo_base`,
  verified and cached through §5.2. It never fetches or verifies
  content — feed files and messages remain the app's TUF job.

---

## 9. Security and Privacy Posture

| What the relay knows | What it never learns |
|---|---|
| verified company and scope authorization for a signed publish (not a separate caller account) | user identity, email, phone |
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
- **Transport debug mode:** the test-only routes (§5.7) bypass relay signature
  verification and are never mounted in production. Client verification is
  unconditional; debug mode does not establish client trust. Keep test
  storage, subscriptions, and provider projects separate from production.
- **Company trust:** anyone may request synchronization, including after old
  keys have been withdrawn. The caller supplies no replacement trust. TOFU
  applies only to unknown domains; known domains follow standard TUF updates,
  including root rotation. A failed or expired cache never reopens TOFU.
  One attempt per minute per company plus global/discovery limits bounds
  refresh work; ordinary refresh and expiry checks remain the backstop.
- **Wake-up authenticity and recovery:** the production relay verifies before
  dispatch. Clients verify before accepting a wake-up or fetching content.
  Unknown/invalid signatures may trigger one company-wide metadata refresh
  per persisted cooldown (§4.2). Forged messages cannot advance sequence state
  or stop valid cached-key messages during that cooldown. An attacker can
  consume a recovery allowance, delay rotation recovery, and cause bounded
  metadata traffic across devices. These are accepted limits, not a claim
  of zero amplification or guaranteed emergency delivery.
- **Self-test:** the §4.3 payload is unauthenticated but nonce-bound and is
  never a wake-up; a forged one can cause at most a locally authored generic
  notice, within the relay-spam bound above. The FCM test uses a short-lived
  relay-generated topic and never learns the device's FCM token.
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
- **Abuse:** per-company publish limits, IP/global unauthenticated-request limits,
  per-IP subscription throttling,
  outbound destination validation, registration management tokens, VAPID `sub`
  contact for provider abuse contact, TUF metadata refresh rate limits,
  fail-closed relay authorization (§5.5), and client recovery cooldowns (§4.2).

---

## 10. Integration with the Protocol

- Publisher tooling calls `POST /v1/companies/{company_id}/refresh` before
  first use and after publishing metadata changes. The same unsigned call
  bootstraps an unknown domain or updates a known one, including root-key
  rotation. A `202` means scheduled/coalesced, not verified; tooling retries
  publishing while authorization is unavailable or still lacks the new scope
  or keys. No enrollment credential or retained old key is needed.
- Publisher tooling (`pub`) gains a `push` step: after any publish/order
  event, resolve the scope_id from verified metadata, compute the source
  hash and topic (§3), compute `seq` per §4.2 (timestamp with monotonic
  guard), satisfy that scope's signature threshold (§4.1), and call
  `POST /v1/publish` with `company_id` and no API
  token. The relay performs the same
  algorithm for every scope. The call returns fast (acceptance, §5.1);
  tooling may follow dispatch status (§5.1.1) or ignore it. Failures are
  non-fatal; independent sync covers it.
- Partner engines use the company's `company_id` and the authorized private
  pattern's `scope_id` and signing keys. They need no publisher record or
  company-issued relay sub-key. Per-company limits apply across engines/scopes.
- Dispatch status requires only the short-lived `request_id` capability;
  tooling keeps it secret until expiry. It is not a publishing credential.
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

These relay-specific questions are summarized in the project's single
list of open items, [`../ROADMAP.md`](../ROADMAP.md) §3 ("Push
transport"); resolve a question in both places together.

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
4. **Relay identity** — who operates it (the app publisher), and what
   governance applies if more than one app ships against it?
5. **On-premise relay (deferred)** — a company running its own relay would
   need its relay URL and VAPID public key in signed metadata
   (`targets.custom.relay`) plus a per-relay registration in the app. Deferred:
   the topic leg (FCM) is the primary channel and is structurally central —
   one app binary embeds one Firebase project, so an on-prem relay could not
   reach native devices without holding the app publisher's service account. The
   relay stays centralized; revisit if the topic leg's role changes.

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
- Callers emit `seq` with the monotonic guard (§4.2): two wake-ups for one
  topic within the same second get distinct values; a backward clock jump
  does not decrease it; a restart continues above the previous value while
  `last_seq` is persisted. Clients never compare `seq` to their own clock,
  and the relay rejects values ahead of its clock beyond the configured
  tolerance.
- Concurrent invalid wake-ups across one company's topics cause at most one
  recovery attempt per cooldown. Timeouts consume it; client restarts preserve
  it; forged key IDs and different topics cannot bypass it.
- A new authorized key verifies after successful recovery. Failed recovery
  suppresses only further recovery attempts; cached-key valid notifications
  continue. Unknown topics never trigger recovery or select metadata URLs.
- Production publishes require no API token and always fail closed when
  signature thresholds or required TUF freshness checks fail. An API key
  cannot bypass verification. Changing the request's company, scope, or source
  hash invalidates its signature for the newly derived topic.
- Unsigned synchronization bootstraps an unknown domain using HTTPS TOFU;
  subsequent synchronization uses persisted standard TUF trust. Root rotation
  succeeds through the versioned chain; an unrelated replacement root fails.
  Expiry, process restart, cache eviction, and a partial/failed bootstrap or
  refresh never reset an already-pinned domain to TOFU. Standard TUF updates
  persist verified root progress even if a later metadata step fails.
- Concurrent synchronization hints cause at most one attempt per minute per
  company; timeouts consume the interval and known-company cooldowns survive
  restart. Requests during cooldown coalesce into one pending attempt and
  cannot postpone it. Scheduled/stale-cache updates share the same budget.
  Unknown-domain admission is bounded without evicting active cooldowns to
  bypass them, and cannot monopolize known-company refresh workers.
- After metadata rotation or withdrawal, an unsigned hint can schedule refresh
  without any old key. Successful refresh updates authorization, including
  removed keys/scopes; failure does not extend expiry. Routine cached-key
  publishes perform request-signature and expiry checks without fetching TUF.
- Debug routes are absent in production. Explicit debug mode exposes only its
  alternate publishing routes, requires its configured API key for publish,
  and uses isolated test state and providers. It shares the transport code;
  valid signed test envelopes verify normally, while unsigned/invalid ones
  are never accepted as trusted client wake-ups.
- Registration mutation requires its management token; destination validation
  covers delivery and metadata; registry GC is driven by
  `last_seen`, never by send attempts or their results. PWA and Android endpoint
  registrations are indistinguishable on the publish path; a `404/410`
  from either counts as `dead` without a registry mutation.
- A self-test is never a wake-up: it advances no sequence, grants no content
  fetch, and consumes no recovery allowance or publish budget; only the
  client's matching pending nonce counts as delivered, and the FCM test never
  reveals the device's FCM token to the relay.
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
- Probe semantics: `pending` → `complete` with per-leg results; possession of
  the unexpired request-id capability suffices without an API key or signature.
  Invalid/unknown/expired capabilities return `404`. Expiry does not cancel
  dispatch, and audit retention does not extend capability lifetime. Raw
  capabilities are never logged; they are stored raw in `event_log` as the
  lookup key (§5.1.1), read-only and TTL-bounded; responses are not cached.
  Results describe provider attempts, never device delivery.
- Queue or audit-storage rejection leaves replay state and previous pending
  work unchanged; retrying the rejected envelope can be admitted. Concurrent
  requests for a topic cannot interleave admission and replay advancement.
- Replacing a pending publish terminates its status as `superseded` with the
  newer request_id and expiry; tooling can follow repeated replacements to
  the final result. Supersession links survive restart via a row reference
  (`superseded_by_id`); the successor capability is returned from its own row
  and is never duplicated or logged. Started dispatches finish normally;
  later publishes occupy a separate pending entry.
- A lost registration creation response or lost token cannot cause an endless
  `401`/`409` recovery loop: clients obtain a different endpoint. Authenticated
  PUT replaces endpoint and keys atomically; conflicts leave records unchanged.
- A fan-out consisting entirely of `404/410` responses increments aggregate
  `dead` counts without any per-registration database writes or durable cleanup
  tasks. Only client operations and the independent TTL sweep mutate registry
  rows; subsequent sends may encounter the same dead endpoint before GC.
