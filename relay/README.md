# Relay — unified notification relay

The single active server component of the Keryx system (see
[`SPECIFICATION.md`](SPECIFICATION.md)): it fans out **wake-up signals** —
never content — to user devices over three delivery legs:

| Leg | Mechanism | Registry |
|---|---|---|
| FCM topics | one publish per topic; devices subscribe client-side | none |
| WebPush | 1:1 send per stored subscription (RFC 8291 + VAPID) | SQLite |
| ntfy topics | one publish per topic; the app is its own ntfy client | none |

Publishers are registration-free: one API key, hashed at rest, shown once at
provisioning. All provider credentials (FCM service account, VAPID keypair)
live at the relay. Best-effort delivery; the app reconciles by fetching and
verifying content on wake-up.

## Build

```sh
go build ./cmd/relay
```

Requires Go ≥ 1.25 (uses `crypto/hkdf`). Pure-Go SQLite (`modernc.org/sqlite`),
no cgo.

## Run

```sh
# provision a publisher (prints the API key once)
relay publishers add -name Acme -company company.example

# generate a VAPID keypair (public goes into the PWA, private into config)
relay vapid generate

# start the server
relay serve \
  -listen :8080 \
  -db relay.db \
  -fcm-service-account /etc/relay/firebase-sa.json \
  -vapid-private <base64url> -vapid-sub mailto:ops@example.com \
  -ntfy-base https://ntfy.sh
```

Every flag has a `RELAY_*` environment variable (`RELAY_LISTEN`,
`RELAY_DB`, `RELAY_FCM_SERVICE_ACCOUNT`, `RELAY_VAPID_PRIVATE`,
`RELAY_VAPID_SUB`, `RELAY_VAPID_KEY_FILE`, `RELAY_NTFY_BASE`, `RELAY_WEBPUSH_TTL`,
`RELAY_WEBPUSH_CONCURRENCY`, `RELAY_MAX_CONCURRENT`, `RELAY_QUEUE_SIZE`,
`RELAY_REG_PER_MIN`, `RELAY_REG_BURST`, `RELAY_PUBLISH_BURST_MULT`,
`RELAY_APP_KEY`, `RELAY_EVENT_RETENTION_DAYS`). Omit a leg's config to
disable it (the relay still runs and reports `0` for it).

## API

- `POST /v1/publish` — `Authorization: Bearer <api-key>`,
  `{"v":1,"kind":"channel|order","h":"<43-char base64url source hash>","n":3,"seq":7}`.
  `n`/`seq` are optional and independent. Returns
  `{"topic":"n-b-…","delivered":{"fcm":0|1,"ntfy":0|1,"webpush":{"sent":…,"failed":…,"removed":…}}}`;
  `202` when queued under load; `401` bad key, `400` schema violation,
  `429` rate limited.
- `POST /v1/registrations` — `{"endpoint":"https://…","keys":{"p256dh":"…","auth":"…"},"topics":["n-b-…"]}`
  → `{"id":"<uuid>"}`. Optional `X-App-Key` gate. HTTPS endpoints only;
  derived `n-b-`/`n-o-` topics only; ≤ 200 topics; throttled by IP.
- `PUT /v1/registrations/{id}` — replace the followed-topic set.
- `DELETE /v1/registrations/{id}` — remove (uninstall / company deletion).

## Layout

- `internal/topic` — two-stage derivation: source hash
  `base64url(sha256("b|" + company + "|" + channel))` /
  `base64url(sha256("o|" + order_token))`, then topic
  `"n-b-"/"n-o-" + base64url(sha256("keryx/relay/v1|" + h))` (47 chars).
- `internal/store` — SQLite (WAL): publishers, registrations,
  registration_topics, event_log; versioned schema (refuses to start on a
  mismatch).
- `internal/push` — the three legs: FCM HTTP v1 (OAuth2 service-account
  token, cached until 5 min before expiry), WebPush (RFC 8291 encryption,
  VAPID ES256 JWT), ntfy (plain HTTP).
- `internal/relay` — concurrent fan-out across legs, webpush
  sent/failed/removed accounting, dead-subscription cleanup, event_log.
- `internal/api` — HTTP handlers, auth, per-publisher and per-IP rate
  limits, 202-under-load queue.
- `internal/ratelimit` — per-key token buckets.

## Tests

```sh
go test ./...
```

The WebPush encryption is validated against the RFC 8291 Appendix A test
vector; FCM/ntfy/WebPush HTTP behavior is tested against `httptest` servers;
the API is tested end-to-end with fake legs (auth, validation, rate limits,
registration lifecycle, 202 queueing).

## Security notes

The relay never sees content or derivation inputs — only source hashes, from
which it derives topics. Logs and `event_log` contain hashes and topic names
only. Authorization is key-level, not company-level (by design; a compromised
key can spam wake-ups for any topic whose hash it knows, bounded by rate
limits). Treat the database as sensitive: it holds the PWA device ↔ topic
registry.
