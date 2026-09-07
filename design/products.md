# Keryx — What Gets Built

**Status: informative.** How the two products behave and feel. Where a screen
implements a protocol rule, the rule itself is normative in
[`spec/`](../spec/core.md) and linked from here.

---

## 1. User App

- Web technology core (e.g., React Native / Capacitor), wrapped as native
  Android + iOS apps. Must run on de-Googled Android without Play Services.
- **Zero onboarding:** first screen is "Scan company QR code".
- **Origin confirmation screen:** shows the full join URL origin in plain
  ASCII (punycode for IDNs), with Continue / Cancel — **and nothing else**.
  No company name, no logo, no brand chrome of any kind: those are
  self-asserted values any origin can set, and putting them on this screen
  would move the user's recognition off the one thing that is actually the
  anchor. It is also mechanically impossible to show them honestly here,
  since nothing is fetched from the origin before the user confirms it
  ([spec/core.md §2–§3](../spec/core.md)).
- **Consent screen (after confirmation, before subscribing):** the app has
  refreshed the verified chain by now, so this screen names things in the
  publisher's own signed words — company name and logo, real channel names
  ("Offers", not `marketing`) and descriptions, the private feeds on offer —
  with the confirmed origin still on screen. Suggested channels are
  preselected; the user taps to subscribe.
- **Contacts list:** each company as a card — logo, name, **the join origin
  as a persistent secondary line**, unread count. No "verified" badge:
  nothing outside the company attests to its name or logo, and a badge would
  claim otherwise. The origin line is what keeps the user's mental model on
  the anchor, and it is what makes a rebranding warning legible when one
  fires.
- **Company detail:** channel toggles ("new" badge on newly seen channels), a
  **filter sheet** (language from the standard `language` field; free-form
  tags — stored locally, never sent), delete company (local wipe).
- **One-way inbox:** chronological announcement feed per company; **rich
  content rendered in a sandboxed view** (styled text, media, CTA buttons;
  remote-media privacy preferences honored per app settings); links show
  their real destination domains; security alerts visually distinct; footer
  reminder: "This channel will never ask you for a password, seed, or code".
  In-place updates keep their position and read-state (optionally marked
  "updated"); withdrawn items are hidden.
- **Suspended state:** a company whose key chain broke shows a warning —
  "identity changed; this can mean the company's website or signing keys were
  compromised — messages are not shown" — with **Remove** as the offered
  action. Deliberately **no re-pair affordance**: after a domain takeover the
  attacker controls the QR on that origin too, so "rescan to fix" would be an
  instruction to re-run TOFU against the attacker. No trust dialogs. The
  wording and the absence of a repair prompt are normative
  ([spec/core.md §4](../spec/core.md)).
- **Rebranding (identity change):** `company_name` change → prominent warning
  + re-pair prompt (rescan QR); logo change → one-tap acknowledge. Never
  silent ([spec/core.md §2](../spec/core.md)).
- **Offline-first:** messages cached on-device; expired-but-unrefreshed
  metadata degrades to cache + retry, never a blackout.
- **Standard-reader compatible (adoption path):** public feeds are plain
  JSON Feed documents — any JSON Feed reader can subscribe without the trust
  enforcement. (Honest note: JSON Feed readers are a niche vs RSS — this is a
  bonus, not a headline.)
- **Backup/restore:** export/import the local subscription list — join origin
  + pinned root keys + channel lists + private capability URLs (the repo base
  is re-derived from the root; capability URLs are otherwise unrecoverable) —
  no account, no server-side recovery. Format details are an open question
  ([`../ROADMAP.md`](../ROADMAP.md)).

---

## 2. Publisher Experience — the Promise

**A small e-shop can adopt this in an afternoon, without understanding any
cryptography.** TUF/Ed25519/JCS are the engine; the publisher tool is the
cockpit, and the publisher never sees them.

- **One tool, plain concepts:** a single CLI (working name `pub`) or minimal
  web panel with commands in business language — `init`, `channel add`,
  `editor add/revoke`, `publish`, `rotate`, `revoke`, `qr`,
  `refresh-timestamp`. `init` takes the join origin and the repo base
  (default: same origin) and emits the well-known root anchor plus the repo.
  No "root", "targets", "threshold", "expiry" in the default flow (advanced
  mode only). The exact command contract is normative in
  [spec/clients.md §2](../spec/clients.md).
- **Static output:** a **full TUF repo directory** — metadata + per-channel
  feed files — plus the well-known root anchor
  (`/.well-known/keryx/root.json`), uploaded to any static host/CDN. No
  application server, no database; still a weekend deploy.
- **Key handling (MVP decision):** **software keys first** — Ed25519 master
  + online ops key + per-channel keys generated locally (libsodium), stored
  in OS keychain/encrypted file, one-time backup printout ("lose this = lose
  the channel"). In **editor mode** editor keys live on editor machines
  (they sign items) and the channel key lives in the publishing pipeline —
  the two never co-locate. No external dependencies, works offline.
  **Cloud KMS / hardware = later phases** (additive via the Sigstore signer
  interface; advanced mode also offers thresholds, e.g. 2-of-3 for security
  alerts).
- **One-command lifecycle:** `publish` appends an item and updates the
  channel's feed target, then re-signs the channel's role metadata
  (`channels.marketing.json`) + `snapshot.json` + `timestamp.json` — channel
  key + online ops key, no master involvement. In editor mode `publish`
  verifies editor signatures and refuses unsigned items (it never holds
  editor keys). `channel add/rotate/revoke` and `editor add/revoke` bump
  `targets.json` (master); root rotation is a separate rare ceremony;
  `refresh-timestamp` is one cron line.
- **Fail-safe defaults:** thresholds 1-of-1; deterministic output; validates
  before write; refuses to sign with a revoked key; refuses to publish an
  item whose `keyid` is unauthorized; refuses to publish an unsigned item in
  an editor-mode channel; refuses to configure the same key as both channel
  key and editor key; verifies every signature it writes (a present-but-
  invalid item signature is rejected by clients, so it must never ship).
- **Crypto split:** TUF metadata signing delegated to go-tuf/python-tuf
  (OLPC canonicalization); item signing (JCS + Ed25519) is ~50 lines in the
  tool and app.
