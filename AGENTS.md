# Keryx: project instructions

## Always test the full circle

Unit tests are not enough for relay/app changes. Run the full circle — local harness,
Firefox E2E, deploy, production E2E — before claiming a feature works.

Standing consent: you may push/deploy to Fly.io and GitHub for this project as
necessary. The relay and the PWA are staging; breaking them is acceptable.

## The stack

- `relay/` → the relay, deployed to Fly.io as `keryx-relay` (region `fra`, volume
  `relay_data`, Fly auth `marekp2310@gmail.com`).
- `app/` → the PWA, deployed to GitHub Pages as
  `https://v1b3coder.github.io/keryx/` by `.github/workflows/deploy-pages.yml`
  on a push to `main`.
- `../keryx-demo` → the demo site, its own repo
  (`keryx-demo/keryx-demo.github.io`), deployed to GitHub Pages on a push to its
  `main`; regenerate it from this repo with `make demo`.
- `../keryx-demo-keys` → the maintainer-only release keystore, outside both
  repos. Only the maintainer holds it; never commit it.
- The production PWA points at the staging relay by default
  (`DEFAULT_RELAY_URL`/`DEFAULT_VAPID_PUBLIC` in `app/src/lib/relay.ts`). The
  deployed relay's `vapid_public` log line must match that constant, or every
  push subscription is dead.

## Full-circle recipe

```sh
# 1. local harness (relay + demo over local HTTPS); prints join_url/vapid_public
cd relay && go run ./cmd/relay-harness          # keys default to ../../keryx-demo-keys
curl http://127.0.0.1:18099/test/info

# 2. PWA against the harness
cd app && VITE_BASE=/ VITE_RELAY_URL=http://127.0.0.1:18099 \
  VITE_VAPID_PUBLIC=<vapid_public> npm run build && npx vite preview --port 4173 --strictPort

# 3. deploy relay (schema v1: no DB break; secrets already set)
cd relay && fly deploy -a keryx-relay --remote-only
curl -X POST https://keryx-relay.fly.dev/v1/registrations/missing/test   # 401 = new code
fly logs -a keryx-relay --no-tail | grep vapid_public   # must match DEFAULT_VAPID_PUBLIC

# 4. deploy demo (only with the release keys) and the PWA
cd ~/projekty/keryx && make demo
(cd ../keryx-demo && git add -A && git commit && git push origin main)
git push origin main   # deploy-pages.yml, VITE_BASE=/keryx/
gh run list --workflow=deploy-pages.yml --limit 1   # then: gh run watch <run-id> --exit-status
```

## Firefox is the only reliable local browser E2E

agent-browser's bundled Chrome runs with `--disable-background-networking`, which
blocks Web Push; do not use it for push E2E. Use Playwright's Firefox from the npx
cache (Firefox cannot be granted notification permission via CDP):

```sh
PW=$(ls -d ~/.npm/_npx/*/node_modules/playwright | head -1)
$(dirname "$PW")/.bin/playwright install firefox          # once
NODE_PATH=$(dirname "$PW") node e2e.cjs
```

Launch prefs: `firefoxUserPrefs: { 'permissions.default.desktop-notification': 1,
'dom.push.enabled': true, 'dom.push.connection.enabled': true,
'dom.push.serverURL': 'wss://push.services.mozilla.com/' }`; add
`newContext({ ignoreHTTPSErrors: true })` for the harness CA. Drive: Add a company →
Paste a link (join URL) → Continue → Continue → Subscribe → Turn on notifications →
Turn on; assert the UI text and the relay log (`POST …/test` → 202). State lives in
IndexedDB `keryx`: `registrations` (keyed by relay base URL) and `relay`
(`test\0<baseUrl>`, `push\0<origin>`, `seq\0…`, `recovery\0…`). Production run:
`APP=https://v1b3coder.github.io/keryx/`, `JOIN=https://keryx-demo.github.io/join.txt`,
`BASE=https://keryx-relay.fly.dev`, no `ignoreHTTPSErrors`.

## Gotchas

- The **first push to a fresh subscription can take minutes** (push-service
  warm-up, then sub-second) — never treat a 10 s wait as failure; keep the pending
  nonce and show a neutral in-flight state that upgrades to green.
- The demo TUF `timestamp.json` expires in ~2 days: run `make demo` and redeploy the
  demo site when the relay starts failing publishes closed.
- The relay is single-instance and idle-exits after 10 min; the next request pays a
  cold start.
