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

The demo publisher artifact lives **outside this repository**: the site at
[`../keryx-demo`](https://keryx-demo.github.io/) (published as
keryx-demo.github.io, see its [README](../keryx-demo/README.md)) and its
keystore at `../keryx-demo-keys`. Nothing about the demo is committed here.

```
make demo                  # regenerate ../keryx-demo (mints fresh keys if absent)
make demo-verify           # verify the generated artifact
make serve-demo            # serve it at http://localhost:8000 (CORS-enabled)
```

Only the maintainer holds the release keys in `../keryx-demo-keys`. Without
them, `make demo` mints a fresh, independent demo — a new trust anchor that
must not be pushed over the published site (spec/repository.md §5). For a local
copy, override the paths and the signed origin:

```
make demo DEMO_REPO=/tmp/keryx-demo DEMO_KEYS_DIR=/tmp/keryx-demo-keys DEMO_BASE=http://localhost:8000
```

---

## Publisher CLI (`pub`)

Requires Go 1.26+. Build it from the repository:

```
make cli                       # builds bin/pub
# or, from the SDK module:
cd sdk && go install ./cmd/pub
```

Private keys never live in the repository: the key store defaults to
`~/.local/share/keryx/keys` and is overridden with `--keystore DIR` or
`$KERYX_KEYSTORE` (`pub init` refuses a key store inside the workspace).
The workspace (`--workspace`, default `.keryx`) holds only the public
`repo/` and `anchor/` directories.

Quickstart — one machine acting as operator, pipeline and author:

```
pub init --domain company.example --name "ACME s.r.o."
pub channel add security --display-name "Security alerts" --generate-keys
pub item sign --channel security --file draft.json --out signed.json
pub publish --channel security --file signed.json
pub refresh-timestamp          # the one cron line

pub validate
pub deploy local --target dist/
pub join-url --channels security           # prints the join URL
pub qr --channels security --out qr.png     # or render it as a QR
```

`channel add` mints the channel key and, for an authored channel, an author
key (`security-author`); pass `--author <keyid>` instead to use a key the
author generated on their own machine with
`pub keys generate alice --role author`. Each machine holds only the keys its
role needs. The workspace `role`
(`operator`, `ci` or `author`) gates the commands it may run; a command that
needs a key the machine does not hold fails fast with a typed `missing key`
error instead of producing half-signed metadata. `--generate-keys` opts a
ceremony into minting a key, and `--keyid` disambiguates when a role has
more than one key. Every master ceremony also takes `--stage <dir>` for the
strict two-step handoff (the offline machine stages, a CI machine runs
`pub ceremony apply --bundle <dir>`). See [`sdk/README.md`](sdk/README.md)
for the full command reference.

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
- `examples/` — small showcase consumers of the publisher SDK, each a
  separate module (own `go.mod`, `replace` to `../sdk`); `examples/sdk-artifact`
  generates a minimal signed artifact
- `demo-tool/` — demo-site generator (Go) built on the publisher
  SDK: demonstration content + site chrome around an SDK-generated repo. The
  generated site lives outside this repository (Demo section above)
- `tools/serve.py` — serve the demo locally with CORS enabled (the browser
  client runs on a different origin); `make serve-demo`

Run a local demo (a fresh, independent artifact signed for localhost):

```
make demo DEMO_REPO=/tmp/keryx-demo DEMO_KEYS_DIR=/tmp/keryx-demo-keys DEMO_BASE=http://localhost:8000
make serve-demo DEMO_REPO=/tmp/keryx-demo   # serve it at http://localhost:8000 (CORS-enabled)
make app-dev                                # web client at http://localhost:5173
```

Then paste the join URL from `/tmp/keryx-demo/join.txt` into the client (or
open the demo join link — `/tmp/keryx-demo/join/?p=…` — and scan the QR code
it renders). The homepage also has a generic join link without a payload.

Regenerate the published demo with `make demo` (release keys required, see the
Demo section), or verify it with `make demo-verify`.

See `make help` for the full task list (builds, tests, the relay and the Android
APK).
