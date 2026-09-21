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
`RELAY_PUSH_ORIGINS_MODE`, `RELAY_TRUSTED_PROXY_IP_HEADER`,
`RELAY_IDLE_EXIT_SECONDS`,
`RELAY_DEBUG_TRANSPORT`, `RELAY_DEBUG_API_KEY`). Omit a leg's config to
disable it (the relay still runs and reports `0`/`disabled` for it).

Approved push-service origins (`RELAY_PUSH_ORIGINS`) are exact origins or
`*.` host wildcards (`https://*.push.apple.com`); the seed list covers the
major browser push services and `ntfy.sh`, and self-hosted ntfy servers are
added explicitly. `RELAY_PUSH_ORIGINS_MODE=any` accepts any public HTTPS
endpoint instead — an explicit opt-out for relays that serve arbitrary
self-hosted UnifiedPush distributors; the outbound policy (HTTPS, public
destination) still applies.

Test-only flags exist for local end-to-end runs and **must not** be used in
production: `-allow-private-destinations`, `-allow-http-destinations`,
`-test-ca-file`, `-test-well-known company=base` and `-cors-origin origin`.

## Deploy to Fly.io (staging)

`Containerfile` builds the static binary on distroless, and `fly.toml` runs one
machine with a volume for the two SQLite databases, an HTTP check on `/healthz`,
and the Fly proxy in front (`force_https`). Because the proxy overwrites
`Fly-Client-IP`, the relay reads it as the client IP for per-IP rate limits
(`RELAY_TRUSTED_PROXY_IP_HEADER`); only set this behind the platform proxy.

The relay is single-instance by design — the in-memory dispatch queue and replay
cache do not survive a restart, and two machines would double-send. Keep one
machine and one volume:

```sh
fly auth login
fly apps create keryx-relay           # or edit `app` in fly.toml
fly volumes create relay_data --size 1 --region fra -a keryx-relay
fly secrets set -a keryx-relay \
  RELAY_VAPID_PRIVATE=<base64url> RELAY_VAPID_SUB=mailto:ops@example.com
fly deploy -a keryx-relay --remote-only
fly scale count 1 -a keryx-relay
```

Set `RELAY_CORS_ORIGINS` (the PWA origin) and `RELAY_PUSH_ORIGINS` or
`RELAY_PUSH_ORIGINS_MODE` in `fly.toml`'s `[env]` or as secrets. For the FCM
leg, put the service-account JSON on the volume and set
`RELAY_FCM_SERVICE_ACCOUNT=/data/fcm-sa.json`; without it the leg reports
`disabled`.

`min_machines_running = 0` and `RELAY_IDLE_EXIT_SECONDS` let the relay exit
itself when no non-health request has arrived for that long and the dispatch
queue is empty; the platform starts it again on the next request (first request
pays a cold start, ~1–3s, and the in-memory queue/replay cache is lost, which
the spec accepts). The relay decides this itself, with no platform-specific API —
so the same binary and configuration work on any platform that autostarts
stopped processes — at the price of a stop/start rather than Fly's
memory-retaining suspend. `RELAY_IDLE_EXIT_SECONDS=0` keeps it always on. The
demo company's TUF `timestamp.json` expires within ~2 days, so regenerate the
demo repository (`make keryx-demo`) to keep the relay from failing it closed.

## Local browser end-to-end

`cmd/relay-e2e` is a TEST-ONLY harness that serves a resealed copy of the demo
repository over local HTTPS, starts the relay in-process with the test flags, and
exposes `/test/info` (join URL, topic, scope, VAPID public key) and
`/test/publish` (signs a wake-up with the demo channel key and publishes it):

```sh
relay-e2e -demo ../../keryx-demo -relay-listen 127.0.0.1:18099 -https-listen 127.0.0.1:8443
```

With a browser built against the relay URL and VAPID key, the app pairs the demo
company, registers its real WebPush subscription, and receives the wake-up through
the browser's push service — the service worker verifies it and shows the notice.
The relay's own `internal/e2e` test covers the same path without a browser.

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
- `GET /healthz` — liveness for platform health checks (`200`); never rate
  limited and not logged.

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
