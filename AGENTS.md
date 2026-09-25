# Keryx: project instructions

Normative: `spec/` · rationale: `design/` · open questions: `ROADMAP.md` ·
wake-ups: `relay/SPECIFICATION.md`. Nothing outside `spec/` is normative. The
spec and design are the root of trust, but not frozen: the design is still
converging, so challenge decisions and propose improvements — surgically and
narrowly. Keep code and spec in sync; never diverge silently.

## Hard rules — never break

- **Binary verification.** Verified and shown, or rejected and never shown — no
  third state, no user judgment; a shown item that stops verifying is dropped.
- **Serving side is untrusted.** Root metadata only from the well-known anchor;
  the relay carries wake-up signals only, never content or PII.
- **No PII, no accounts, no per-user state.** Filtering is local; identity
  changes are never silent; expiry is not suspension (`spec/core.md`).
- **Keys never enter the repo.** Keystore and repo are a pair: validate before
  publishing, never mint keys for a published repo (`sdk/README.md`).
- **One canonicalization** (OLPC + Ed25519) on both sides; cross-stack tests
  against real signed artifacts are the proof.
- **Relay is single-instance by design.** The PWA's baked-in push key must
  match the deployed relay's, or every subscription is dead.

## Always test the full circle

Unit tests are not enough for relay/app changes. Local: `make verify`; then the
full circle — harness E2E → deploy → production E2E over the real network path.
End-to-end verification always runs on the adb-connected phones — Samsung (stock
Android, Google services → FCM) and Graphene (de-Googled, ntfy → UnifiedPush) —
for both the Android app and the PWA. Standing consent: push/deploy to Fly.io and
GitHub as needed; relay and PWA are staging (breaking them is fine), the demo
site is public.

## The stack

- `relay/` → relay, Fly.io `keryx-relay`; single machine, idle-exit + cold start.
- `app/` → PWA, GitHub Pages on a push to `main`; the deployed relay's
  `vapid_public` must match the PWA's default (`app/src/lib/relay.ts`).
- `../keryx-demo` → demo site, own repo; regenerate with `make demo`.
- `../keryx-demo-keys` → maintainer-only release keystore, never commit it.

## Publishing

Use `pub` (SDK CLI) for every publisher step, never the demo tool. Reference
flow: `../keryx-demo/publish.sh` (validate → sign → publish → validate → push
→ notify).

```sh
bin/pub validate --repo <repo>/keryx --anchor <repo>/.well-known/keryx
bin/pub item sign --file draft.json --channel security --keyid <author> --keystore <keys>
bin/pub publish --channel security --file draft.json --repo <repo>/keryx \
  --anchor <repo>/.well-known/keryx --keystore <keys>
(cd <repo> && git add -A && git commit && git push origin main)
bin/pub notify --repo <repo>/keryx --channel security --company <origin> \
  --relay https://keryx-relay.fly.dev --keystore <keys>
```

`notify` waits for the deployed repo to serve the new versions — never sleep a
guessed delay; a device woken early silently shows one publish behind. It refuses
(403) until the relay synchronized the company: hint `POST
/v1/companies/{id}/refresh`, allow ~60s.

## Full-circle recipe

```sh
cd relay && go run ./cmd/relay-harness   # prints join_url/vapid_public
cd app && VITE_BASE=/ VITE_RELAY_URL=http://127.0.0.1:18099 \
  VITE_VAPID_PUBLIC=<vapid_public> npm run build && npx vite preview --port 4173 --strictPort
cd relay && fly deploy -a keryx-relay --remote-only
curl -X POST https://keryx-relay.fly.dev/v1/registrations/missing/test   # 401 = new code
fly logs -a keryx-relay --no-tail | grep vapid_public   # must match the PWA
make demo && (cd ../keryx-demo && git add -A && git commit && git push origin main)
git push origin main   # PWA deploy; then: gh run watch <run-id> --exit-status
```

## Firefox is the only reliable local browser E2E

Chrome automation blocks Web Push. Use Playwright's Firefox; the checked-in
drivers (`e2e.cjs`, `e2e-prod.cjs`) encode the flow and assertions:

```sh
PW=$(ls -d ~/.npm/_npx/*/node_modules/playwright | head -1)
$(dirname "$PW")/.bin/playwright install firefox          # once
NODE_PATH=$(dirname "$PW") node e2e.cjs
```

Prefs: `permissions.default.desktop-notification=1`, `dom.push.enabled=true`,
`dom.push.connection.enabled=true`, `dom.push.serverURL=wss://push.services.mozilla.com/`; `ignoreHTTPSErrors` for the harness CA. `node e2e-prod.cjs` runs production (URLs are the defaults). Headless Firefox cannot show the notice (`showNotification` fails); it proves delivery only — verify the notice on a real device.

## Gotchas

- First push to a fresh subscription can take minutes: never treat a 10 s wait as
  failure; keep the pending state neutral.
- Demo TUF timestamp is refreshed by the demo repo's CI; an abandoned demo expires
  by design — expiry keeps cached content, it is not suspension.
- Relay idle-exits after 10 min; the next request pays a cold start.
