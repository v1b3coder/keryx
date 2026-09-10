# Keryx

![Keryx header](assets/keryx_header.jpeg)

Signed, one-way broadcast messaging from a company to its customers —
designed to replace email as the company→customer channel.

A user scans a QR code, confirms the company's **origin**, and subscribes to
the channels they want. Messages arrive authenticated: either a channel key
the signed metadata authorizes pinned the feed, or (in editor mode) each item
carries its author's signature. Verification is binary — it verifies and is
shown, or it is rejected. There is **no email, no phone number, no account, no
PII in the public broadcast, and no per-user state on the company's servers**.

The stack is deliberately boring: HTTPS + QR + TUF + Ed25519 + JSON Feed.

**Working title:** Keryx (placeholder). **Version:** 0.1 (draft).

---

## Documentation

### Normative — the protocol (`spec/`)

| File | Contents |
|---|---|
| [`spec/core.md`](spec/core.md) | Conventions and wire naming, trust model, join URL / QR payload, suspension |
| [`spec/repository.md`](spec/repository.md) | TUF repository layout and root anchor, `targets.json` authorization, channel role metadata, channel and key lifecycle |
| [`spec/feeds.md`](spec/feeds.md) | Public feed and item format, signing and verification rules, editor mode, private per-order feeds |
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

- `demo-tool/` — reference publisher tooling (Go, go-tuf v2 + JCS + Ed25519)
- `app/` — reference demo **web client** (Vite + React + TS): PWA +
  Capacitor Android/iOS; see `app/README.md`
- `demo/` — a generated demonstration publisher artifact (full TUF repo +
  per-channel signed JSON Feeds + private capability feed), fully local
  (`http://localhost:8000`), based on real public Trezor blog content; not
  committed — regenerate it (below); see `demo/README.md`
- `demo/server.sh` — serve the demo locally with CORS enabled (the browser
  client runs on a different origin)

Run it:

```
./demo/server.sh           # serves demo/ at http://localhost:8000 (CORS-enabled)
cd app && npm install && npm run dev   # web client at http://localhost:5173
```

Then paste the join URL from `demo/join.txt` into the client (or open
the pairing page `demo/join/` and scan the QR code it renders from that
URL).

Regenerate the demo:

```
cd demo-tool && go run . -mode build -site ../demo
```
