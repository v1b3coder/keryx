# Keryx

![Keryx header](assets/keryx_header.jpeg)

Signed, one-way broadcast messaging from a company to its customers —
designed to replace email as the company→customer channel.

A user scans a QR code, confirms the company's **origin**, and subscribes to
the channels they want. Messages arrive authenticated: each item file is
signed and hash-pinned by signed metadata — by the channel key, or by the
channel's authors role where one exists. Verification is binary — it verifies and is
shown, or it is rejected. There is **no email, no phone number, no account, no
PII in the public broadcast, and no per-user state on the company's servers**.

The stack is deliberately boring: HTTPS + QR + TUF + Ed25519.

**Working title:** Keryx (placeholder). **Version:** 0.1 (draft).

---

## Demo

Try it live: the [Keryx PWA](https://v1b3coder.github.io/keryx/) (installable
web app), the [Android app](https://github.com/v1b3coder/keryx/releases)
(debug APK in GitHub Releases) and the demo publisher site
[`keryx-demo.github.io`](https://keryx-demo.github.io/) — its homepage
offers both join links: the plain one (public channels only) and the one
with the private tracking feed.

---

## Documentation

### Normative — the protocol (`spec/`)

| File | Contents |
|---|---|
| [`spec/core.md`](spec/core.md) | Conventions and wire naming, trust model, join URL / QR payload, suspension |
| [`spec/repository.md`](spec/repository.md) | TUF repository layout and root anchor, `targets.json` authorization, channel role metadata, channel and key lifecycle |
| [`spec/feeds.md`](spec/feeds.md) | Item format, signing and verification rules, authors role, private per-order feeds |
| [`spec/clients.md`](spec/clients.md) | Client verification flow, publisher tool contract, lite mode (Phase 2 extension) |

RFC 2119 keywords apply throughout `spec/`. Nothing outside `spec/` is
normative.

### Informative — the reasoning (`design/`)

| File | Contents |
|---|---|
| [`design/why.md`](design/why.md) | Problem, goals and non-goals, every design decision (choice → why → trade-off), solution overview, user journey, standards alignment, rejected alternatives |
| [`design/threats.md`](design/threats.md) | Threat table, QR/MITM analysis, residual risks, security and privacy posture (incl. GDPR) |
| [`design/products.md`](design/products.md) | What the user app and the publisher tool look like |
| [`ROADMAP.md`](ROADMAP.md) | Phases 0–4 and the single list of open questions |

**Where to start:** implementers → `spec/core.md` then `spec/repository.md`;
security reviewers → `design/threats.md`; everyone else → `design/why.md`.

---

## Code in this repository

- `sdk/` — reference **publisher SDK + `pub` CLI** (Go, go-tuf v2 +
  OLPC + Ed25519): the full publisher lifecycle (keys, channels, authors,
  private feeds, ceremonies, join/QR, deploy); see `sdk/README.md`
- `app/` — reference demo **web client** (Vite + React + TS): PWA +
  Capacitor Android/iOS; see `app/README.md`
- `demo-tool/` — demo-site generator (Go) built on the publisher
  SDK: demonstration content + site chrome around an SDK-generated repo
- `demo/` — a generated demonstration publisher artifact (full TUF repo +
  per-channel signed item files + private capability feed), fully local
  (`http://localhost:8000`), based on real public Trezor blog content; not
  committed — regenerate it (below); see `demo/README.md`
- `demo/server.sh` — serve the demo locally with CORS enabled (the browser
  client runs on a different origin)

Run it:

```
./demo/server.sh           # serves demo/ at http://localhost:8000 (CORS-enabled)
cd app && npm install && npm run dev   # web client at http://localhost:5173
```

Then paste the join URL from `demo/join.txt` into the client (or open the
demo join link — `demo/join/?p=…` — and scan the QR code it renders). The
homepage also has a generic join link without a payload.

Regenerate the demo:

```
cd demo-tool && go run . -mode build -site ../demo
```
