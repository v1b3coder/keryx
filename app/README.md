# Keryx — app (reference client)

Reference **client** for the Keryx protocol (see `../spec/`, `../design/` and
the publisher demo in `../demo`). One codebase, four targets:

- **Web** — plain Vite + React + TypeScript app
- **PWA** — installable, offline-capable (service worker via vite-plugin-pwa)
- **Android** — Capacitor native shell (`android/`, WebView + MLKit QR scanner)
- **iOS** — Capacitor native shell (`ios/`, WebView + MLKit QR scanner)

It implements the client flow end to end (spec/clients.md §1): QR/paste join
URL → confirm the origin (the only human step, plain ASCII, nothing else on
screen) → pin the root anchor at `/.well-known/keryx/root.json` → read
master-signed `custom.repo_base` → verify the TUF chain (root → timestamp →
snapshot → targets) → per-channel delegated role metadata
(`channels.<name>.json`) → channel consent → fetch followed channel feeds +
private capability feeds → verify **every** item (JCS/RFC 8785 + Ed25519;
editor-mode items need the editor threshold) → local filtering → offline
cache. Anything that fails verification is **never displayed** (binary rule,
spec/core.md §2), including items that were previously shown. No account, no
PII, no per-user state anywhere.

## Run it

```bash
cd ../demo && ./server.sh            # demo publisher at http://10.110.147.178:8000 (CORS-enabled)
cd ../app
npm install
npm run dev                          # web client (Vite prints the port)
```

Then open the printed URL, tap **Add a company**, and paste the join URL from
`demo/join.txt` (or open the demo join link — `demo/join/?p=…` — and scan
the QR code it renders). The flow: confirm the origin
`10.110.147.178:8000` → choose channels (suggested ones preselected, you tap to
subscribe) → subscribe → verified inbox.

> The demo artifact signs metadata for `http://10.110.147.178:8000` (a
> local-dev exception: the app allows HTTP for private feeds on loopback and
> RFC 1918 private addresses; Android permits cleartext only for this demo
> host via `network_security_config.xml`). A real deployment uses the
> company's HTTPS origin.

## Scripts

| Script | Purpose |
|---|---|
| `npm run dev` | Vite dev server (HMR) |
| `npm run test` | Vitest protocol tests against the real `../demo` artifacts |
| `npm run build` | Production build + PWA service worker (`dist/`) |
| `npm run icons` | Regenerate PWA icons (`public/icons/`) |
| `npm run cap:sync` | Build + sync web assets into `android/` and `ios/` |
| `npm run cap:android` | Build + sync + assemble Android debug APK |

## What's implemented (vs. the spec)

- **Pairing** — join URL parsing (spec/core.md §3: base64url payload `{v,
  channels, private_feeds}` — **no metadata URL**; the root anchor is derived
  from the join origin; unknown members within a known `v` are ignored, an
  unknown `v` refuses to parse); origin confirmation (nothing is fetched from
  an unconfirmed origin, no company name/logo on the screen); TOFU pin + chain
  walk afterwards.
- **Metadata** — full TUF chain in-repo: OLPC canonical JSON verification of
  the root anchor (self-signature + versioned `N.root.json` chain walk, root
  metadata fetched **only** from the well-known anchor), `timestamp.json`,
  `snapshot.json`, `targets.json` and the followed channels' delegated role
  metadata, with per-link signature + hash/length verification (timestamp pins
  snapshot; snapshot pins targets + every channel role), TUF-standard keyid
  verification (keyid = SHA-256 of the canonical key object), and anti-rollback
  via client-side version memory. `consistent_snapshot: true` repos are
  handled (versioned/hash-prefixed fetch with plain-path fallback). Repo base
  comes from the master-signed `custom.repo_base`. Expired-but-verified
  metadata → cached content kept, retry (expiry ≠ suspension).
  > The spec names `tuf-js` as the standard client; it is Node-only and
  > cannot run in this browser app, so the standard TUF 1.0 client workflow
  > is implemented in-repo.
- **Authorization (spec/repository.md §2)** — channels are delegated TUF
  roles named `channels.<channel>` (roles not starting with `channels.` and
  roles whose paths fall outside their namespace are ignored); editor-mode
  entries and private-feed patterns resolve keyids exclusively from their
  per-entry `keys` maps (key publication rule: a keyid without its key object
  is a metadata error → reject); editor keyids must not be channel role
  keyids.
- **Public feeds** — one feed per channel at `channels/<name>/feed.json`, a
  TUF target file pinned by the channel's own role metadata (length + sha256),
  fetched through the TUF target URL and verified byte-exact before parsing.
  Feed-level `expired: true` stops syncing that channel (verified cache kept).
- **Items (spec/feeds.md §1.2)** — JCS canonical bytes with `_sig.signatures`
  removed; Ed25519; base64url; `_sig.channel` cross-checked against the feed
  path (mismatch → reject); dedup by (channel, id); in-place updates
  (content differs → re-verified + replaced, position/read-state kept, marked
  "updated"; signature-only re-signing is not an update); withdrawn items
  (`_sig.withdrawn: true`) hidden **and dropped from the cache**; items that
  fail verification on a re-fetch are dropped even if previously displayed
  (binary rule); items without a valid `date_published` order by feed
  position.
- **Editor mode (spec/feeds.md §2)** — per channel, master-signed: items MUST
  carry `threshold` valid editor signatures; missing/insufficient/bad →
  reject, never shown; additional entries (channel-key signatures) are not
  load-bearing. The demo security channel runs 2-of-2.
- **Private feeds (spec/feeds.md §3)** — capability URL (128-bit token)
  matched against an authorized pattern (origin-exact, segment-boundary
  wildcard) **before** subscribing (tampered QR → rejected) and on every sync;
  whole-document verification: `_sig.channel` == entry.channel, `_sig.url` ==
  fetched URL, Ed25519 over the JCS of the document with top-level
  `_sig.signatures` removed (threshold per entry), `_sig.version` monotonic
  (anti-rollback via version memory), `_sig.expires` window (stale → keep
  cache + retry), `expired: true` (or 404/410/pattern removal) closes the feed
  while cached items stay visible; 1 MB document size limit; HTTPS only
  (loopback exception for the local demo).
- **Identity (spec/core.md §2)** — the app remembers the identity confirmed at
  pairing; `company_name` change → prominent rebranding warning, content
  hidden until re-pair (rescan a fresh QR, same origin); `logo` change →
  one-tap acknowledgement (never silent, never auto-accept); name and logo are
  never shown before the chain verifies and always alongside the join origin;
  `logo_sha256` is verified when present (placeholder on mismatch, nothing
  else affected).
- **Suspension (spec/core.md §4)** — unverifiable root change (validly signed,
  unchainable) → suspended: content rejected, "identity changed; this can mean
  the company's website or signing keys were compromised", offered action is
  **Remove** only — no re-pair prompt. Network errors and malformed anchor
  data are **not** suspension (offline-first: cached content keeps serving).
- **Filtering (spec/feeds.md §1.1)** — purely local; standard JSON Feed
  `language` + free-form `tags` (stored locally, never sent); instant,
  offline.
- **Rendering** — `content_html` sanitized (DOMPurify, script/forms/iframe
  stripped — the "never asks for a password, seed, or code" promise is
  structural), links intercepted with their real destination domain shown
  before opening (no auto-open), media hash-verified against `_sig.resources`
  when present, footer reminder under the feed.
- **Feed** — full articles inline (big square picture, title, date + tags,
  complete content) — there is no separate detail view; articles are marked
  read when they scroll into view.
- **Offline-first** — IndexedDB cache of pinned metadata + verified items +
  media bytes; sync on open + manual refresh.

**Out of scope** (per spec Phase 1/2): push wake-ups (ntfy/APNs/FCM — WIP),
lite mode (spec/clients.md §3), backup/restore export/import (Phase 2).

## Architecture

```
src/lib/            protocol core (framework-free, unit-tested)
  bytes.ts          hex / base64url / sha256
  ed.ts             Ed25519 verification (noble)
  olpc.ts           OLPC canonical JSON (TUF metadata)
  pattern.ts        URL pattern (origin-exact, segment wildcard) + TUF path glob
  payload.ts        join URL / QR payload (no metadata URL; anchor derived)
  tuf.ts            TUF client: root chain, timestamp/snapshot/targets,
                    delegated channel roles, keyids, target pinning,
                    authorization model (channels + editor mode + patterns)
  item.ts           JCS item verification (default + editor mode),
                    withdrawal/update semantics, _sig.resources
  private.ts        private capability feed whole-document verification
  sync.ts           sync engine (metadata chain → channel roles → hash-pinned
                    feeds → private feeds → verify → store)
  pair.ts           pairing flow (TOFU + consent summary + subscribe)
  store.ts          IndexedDB (companies, verified items, media) + prefs
  media.ts          image loading with _sig.resources / logo_sha256 checks
  format.ts         date/domain helpers + local filtering (language + tags)
  scan.ts           QR scan: Capacitor MLKit (native) / BarcodeDetector or
                    jsQR (web)
src/state.tsx       app state + sync orchestration
src/ui/             screens: AddCompany (input → confirm → consent), Contacts,
                    Company (full-article feed + settings sheet), SanitizedHtml
src/lib/protocol.test.ts  tests against the real ../demo artifacts
```

## Protocol tests

`npm run test` validates the client against the **actual** Go-signed demo
artifact: root anchor + repo_base discovery, full metadata chain and
per-channel role verification, all public + private item signatures
(editor-mode threshold), whole-document private-feed verification (signature,
channel, url, version, closed), tamper rejection (modified item, wrong
channel key, missing editor signatures, channel mismatch), chain-break
detection (forged root rotations), rollback rejection (metadata + private-feed
version memory), keyid verification, pattern matching with segment
boundaries, join payload parsing (incl. newer-version refusal), and the
binary drop semantics (withdrawn / no-longer-verifying items leave the
cache). This is the cross-check that the two implementations (Go publisher,
TS client) agree on both canonicalizations.

## Native

- `android/`, `ios/` are generated by Capacitor (`npx cap add android|ios`)
  and wrapped by `npm run cap:sync` after each web build.
- QR scanning: `@capacitor-mlkit/barcode-scanning` (native, on-device model —
  no Play Services dependency) with BarcodeDetector/jsQR fallback (web).
  Camera permission is declared in `AndroidManifest.xml` and
  `ios/App/App/Info.plist`.
- Build the APK: `npm run cap:android` (requires Android SDK; the iOS build
  requires macOS + Xcode).

## Design

Tokens derived from `../GRAPHICAL_DESIGN.md` (Substack-derived: electric blue
#0000ee accent, near-black #313131 text, system-ui stack, 8px spacing base,
pill controls), adapted for mobile per the product brief: body 16px, muted
text neutral gray, fast tap feedback, dark mode from the same palette. One
semantic red for destructive actions only. The app's own branding is
suppressed — the company identity (logo + name + join origin) carries the
screen; the feed shows full articles with big square preview images; the
single-company shortcut (no contacts list when only one source is added).
