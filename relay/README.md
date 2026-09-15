# Relay — unified notification relay

The single active server component of the Keryx system (see
[`SPECIFICATION.md`](SPECIFICATION.md)): it fans out **wake-up signals** —
never content — to user devices over two delivery legs:

| Leg | Devices | Mechanism | Registry |
|---|---|---|---|
| FCM topics | native Android + iOS | one publish per topic; devices subscribe client-side | none |
| UnifiedPush (WebPush endpoints) | PWA (browsers); de-Googled Android via a distributor (ntfy) | 1:1 send per stored endpoint subscription (RFC 8291 + VAPID) | SQLite |

Publishers are registration-free: there are no API keys. Every production
publish is authorized by a **wake-up signature** under the company's verified,
unexpired TUF authorization. All provider credentials (FCM service account, VAPID
keypair) live at the relay. Best-effort delivery; the app reconciles by fetching
and verifying content on wake-up.

## Build

```sh
go build ./cmd/relay
```

Requires Go ≥ 1.26 (the TUF client uses `go-tuf/v2`). Pure-Go SQLite
(`modernc.org/sqlite`), no cgo.

## Run

```sh
# generate a VAPID keypair (public goes into the PWA, private into config)
relay vapid generate

# start the server
relay serve \
  -listen :8080 \
  -db relay.db -registry-db registry.db \
  -fcm-service-account /etc/relay/firebase-sa.json \
  -vapid-private <base64url> -vapid-sub mailto:ops@example.com
```

Every flag has a `RELAY_*` environment variable (`RELAY_LISTEN`,
`RELAY_DB`, `RELAY_REGISTRY_DB`, `RELAY_FCM_SERVICE_ACCOUNT`,
`RELAY_VAPID_PRIVATE`, `RELAY_VAPID_KEY_FILE`, `RELAY_VAPID_SUB`,
`RELAY_WEBPUSH_TTL`, `RELAY_WEBPUSH_CONCURRENCY`, `RELAY_FCM_PER_MIN`,
`RELAY_WEBPUSH_PER_MIN`, `RELAY_MAX_CONCURRENT`, `RELAY_QUEUE_SIZE`,
`RELAY_FAST_PATH_MAX`, `RELAY_BATCH_SIZE`, `RELAY_REPLAY_CAPACITY`,
`RELAY_CAPABILITY_TTL_SECONDS`, `RELAY_SEQ_FUTURE_TOLERANCE_SECONDS`,
`RELAY_PUBLISH_PER_MIN`, `RELAY_PUBLISH_BURST`, `RELAY_PUBLISH_IP_PER_MIN`,
`RELAY_PUBLISH_IP_BURST`, `RELAY_REG_PER_MIN`, `RELAY_REG_BURST`,
`RELAY_PROBE_PER_MIN`, `RELAY_PROBE_BURST`, `RELAY_PROBE_GLOBAL_PER_MIN`,
`RELAY_PROBE_GLOBAL_BURST`, `RELAY_EVENT_RETENTION_DAYS`,
`RELAY_REGISTRY_GC_DAYS`, `RELAY_REFRESH_INTERVAL_SECONDS`,
`RELAY_REFRESH_CADENCE_SECONDS`, `RELAY_REFRESH_CONCURRENCY`,
`RELAY_DISCOVERY_PER_MIN`, `RELAY_DISCOVERY_BURST`, `RELAY_PUSH_ORIGINS`,
`RELAY_DEBUG_TRANSPORT`, `RELAY_DEBUG_API_KEY`). Omit a leg's config to
disable it (the relay still runs and reports `0`/`disabled` for it).

Test-only flags exist for local end-to-end runs and **must not** be used in
production: `-allow-private-destinations`, `-allow-http-destinations` and
`-test-ca-file`.

## API

Base: `/v1/`.

- `POST /v1/publish` — `{v, company_id, scope_id, h, seq, sig}`; no API token.
  The relay resolves the scope in the company's verified TUF authorization,
  derives the topic, verifies the signature threshold, then fans out. Returns
  `{"topic":…,"suppressed":false,"providers":{"fcm":…,"webpush":{"sent":…,"failed":…,"dead":…}}}`
  on the fast path (`200`), `{"topic":…,"request_id":…,"status":"accepted","expires_at":…}`
  when the endpoint registry exceeds the fast-path threshold (`202`), `200` with
  `suppressed:true` for a replayed `seq`, `404` for an unknown company, `403`
  for an unknown scope or a failed signature, `503` for unavailable
  authorization or a saturated queue, and `429` for a rate limit.
- `GET /v1/publishes/{request_id}` — dispatch status; the capability alone
  authorizes the read (`pending` | `complete` | `superseded`), `Cache-Control:
  no-store`, `404` for unknown/expired.
- `POST /v1/companies/{company_id}/refresh` — unsigned TUF synchronization
  hint; `202` when scheduled/coalesced, `404` is not used (the hint bootstraps
  an unknown domain by TOFU).
- `POST /v1/registrations` — `{endpoint, keys:{p256dh,auth}, topics, source?}`
  → `{id, management_token}`; an existing endpoint returns `409`.
- `PUT /v1/registrations/{id}` — replace topics, optionally endpoint+keys
  together; `Authorization: Bearer <management_token>`.
- `DELETE /v1/registrations/{id}` — remove the registration.
- `POST /v1/registrations/{id}/heartbeat` — `204` liveness ack.

With `-debug-transport`, only `POST /debug/v1/publish` and
`GET /debug/v1/publishes/{request_id}` are mounted (production publish and
company synchronization return `404`); debug publish requires
`Authorization: Bearer <debug-api-key>` and skips TUF/signature verification.

## Layout

- `internal/topic` — two-stage derivation:
  `h = hex(sha256(company_id + "|" + subject))`,
  `topic = base64url(sha256("keryx/relay/v1|" + OLPC({company_id, scope_id, h})))`
  (43 chars, no prefix).
- `internal/scope` — the uniform `(company_id, scope_id) → keys + threshold`
  table from verified `targets.json` (§3.1).
- `internal/wakeup` — the §4 envelope, strict parsing, and the Ed25519
  threshold verification.
- `internal/tufclient` — the per-company TUF client: well-known root TOFU,
  standard root rotation and `timestamp`/`snapshot`/`targets` verification.
- `internal/companytuf` — cached authorization tables, the synchronization
  scheduler and the recovery cooldown.
- `internal/store` — two SQLite databases (WAL): the main DB
  (`company_tuf`, `event_log`) and the registry DB (`registrations`,
  `registration_topics`); versioned schema (refuses to start on a mismatch).
- `internal/push` — the two legs: FCM HTTP v1 and UnifiedPush/WebPush
  (RFC 8291 encryption, VAPID ES256 JWT).
- `internal/relay` — the bounded per-topic coalescing dispatch queue, the
  in-memory replay cache and the audit rows.
- `internal/api` — HTTP handlers, validation, rate limits and the debug mode.
- `internal/netpolicy` — the outbound-request policy (HTTPS, certificate
  validation, no private/loopback destinations, DNS-rebinding protection).
- `internal/ratelimit` — token buckets and provider outbound budgets.

## Tests

```sh
go test ./...
```

The WebPush encryption is validated against the RFC 8291 Appendix A test vector;
FCM/WebPush HTTP behavior is tested against `httptest` servers; the API and store
are tested with fake legs (auth, validation, rate limits, registration lifecycle,
queueing, supersession, probes, debug isolation).

`internal/e2e` runs the full path against the **registered demo repository**
(`KERYX_DEMO_DIR`, default `../../keryx-demo`): local HTTPS serving of a resealed
copy, unsigned hint → TOFU + TUF verification → scope table → a real signed
wake-up → RFC 8291 delivery to a fake push service → decryption and signature
verification. Set `KERYX_WRITE_FIXTURES=1` to emit the cross-stack fixture the
PWA's tests consume.

## Security notes

The relay never sees content or derivation inputs — only source hashes, from which it
derives topics. Logs and `event_log` contain hashes, topic names and read-only
status capabilities only. Authorization is scope-bound: a signer can address hashes
only within its authorized scope, bounded by per-company rate limits. Treat both
databases as sensitive: the registry holds the device ↔ topic mapping.
