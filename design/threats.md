# Keryx — Threat Model, Residual Risks, and Privacy Posture

**Status: informative.** No normative rules here; the mechanisms referenced
are specified in [`spec/`](../spec/core.md). Design rationale is in
[`why.md`](why.md).

---

## 1. Threat Table

| Threat | Addressed by |
|---|---|
| Phishing email impersonating a company | No email channel exists; identity is key-bound, not name-bound |
| Attacker prints a fake QR / look-alike origin | User confirms the exact origin (ASCII) before subscribing; the app pins the canonical `/.well-known/` root anchor on that origin (admin-controlled space — a user-content path on the origin cannot host it) and blocks cross-origin redirects. Pairing is the single-lock step (origin recognition); after pairing everything is two-lock |
| Tampered QR payload (fake private feed URL; there is no metadata URL in the payload to tamper with) | Rejected by pattern authorization against the pinned metadata — no user judgment needed |
| Attacker forges a message | Ed25519 signature by a key authorized in the signed `targets.json`; invalid signature → rejected, never displayed |
| Compromised feed host / CDN | Feed files are **TUF targets** (hash-pinned): withdrawal, tampering, and wrapper forgery all fail hash verification; staleness is bounded by `timestamp.json` (anti-freeze) + client-side version memory (anti-rollback). Root metadata is never fetched from the repo base, so even a planted, validly-signed root rotation is never seen. Caveat: referenced media/attachments are **not** hash-pinned by default (mutable link targets are the norm) — a media host can swap bytes at a URL; high-stakes static downloads (PDFs, firmware) SHOULD be pinned per item via the optional `_sig.resources` hashes. Otherwise harm limited to availability |
| Attacker who stole a *channel* key | Scoped to its own channel: can forge that channel's content and pin it; in editor mode only re-pin/withhold (cannot forge items); revocation = signed metadata update |
| Attacker who stole an *editor* key (editor mode) | Can author items for channels where the key is listed; reaches users only if the publisher publishes them (CI review gate is policy, not protocol); revocation = master-signed update |
| Attacker who stole the online ops key (snapshot/timestamp) | Scoped to freshness: can roll back/freeze metadata (availability), cannot touch channel delegations or feed pinning (channel-key-signed) → no forgery |
| **Suppression of a security warning** (freeze/rollback by whoever holds the ops key or the feed host) | Partly addressed, and worth publisher attention: forgery is impossible, but *silence* is achievable — freezing metadata withholds new items, and on a channel that carries security alerts the harm is not merely "availability", it is no warning during the incident the channel exists for. Bounded by the `timestamp` cadence + `expires`; publishers running such a channel should set those tighter than the 24–72 h default and may prefer a threshold or editor mode there. Not every publisher has a security channel — which channels are critical is the publisher's call |
| Attacker who stole the master key | Root metadata — and therefore any rotation or `repo_base` change — is accepted only from the confirmed origin's admin-controlled `/.well-known/` space; a master-signed rotation planted on the repo base/CDN is never fetched. Key alone is not enough |
| **Full forgery (origin + master key)** | The residual risk — see §3. Mitigated by offline/HSM custody and by the convention that messages never carry credential requests |
| Company silently rebranding / acting as another company | Identity changes are never silent: `company_name` change → prominent warning + re-pair (rescan QR); logo change → one-tap acknowledge. An unverifiable root change (not a rename) → company **suspended** with a possible-compromise warning and no re-pair prompt; no silent trust |
| Push provider linkage | APNs/FCM see the device↔company mapping (token↔topic subscriptions); ntfy's anonymous topics remove identity linkage, but topic relationships remain visible to the relay operator ([`why.md` §4.10](why.md), WIP) |
| Company correlates or profiles users | No user data exists on the server; no registration, no pseudonym, no ID |
| Company breach leaks "customer list" | No customer list for the public broadcast. Per-order data exists only transiently behind capability tokens |
| Replay of old signed items | Not an attack: replayed items are authentic. The app dedups by (channel, `id`) and never gates by recency; staleness is bounded by metadata freshness |

Mechanisms referenced above: [spec/core.md](../spec/core.md) (trust model,
pairing, suspension), [spec/repository.md](../spec/repository.md) (root
anchor, role scoping, key lifecycle), [spec/feeds.md](../spec/feeds.md)
(item verification, `_sig.resources`, editor mode, private feeds).

---

## 2. QR Authenticity and MITM

Pairing is the single-lock step (origin recognition); messaging is two-lock
(key + confirmed origin). The QR payload *cannot* be signed — no key is
trusted yet — so its authenticity is the user's recognition of the origin,
same as typing a URL.

Attack scenarios:

1. **QR swapped for an attacker's origin** → caught by origin confirmation
   (look-alike domains are the residual risk, still strictly less than
   email's spoofable sender + hidden display names).
2. **Tampered payload (real origin)** → the private feed URL fails the
   authorized-pattern check and is rejected without user judgment; there is
   no metadata URL in the payload to tamper with at all.
3. **Network MITM after scanning** → cannot redirect (TLS/CA binds the
   origin).

Optional hardening (no codes): DNSSEC/DANE for repo serving; an in-app
"compare with the site you're on" affordance.

---

## 3. Residual Risks

Accepted; mitigated but not eliminated.

- **Simultaneous compromise of the origin and the master key** → full
  impersonation with valid cryptography; undetectable by the app. Mitigated
  by offline custody, by the no-credential-requests convention, and by
  incident response on the company side. The irreducible core of any
  key-based system.
- **At pairing time, origin control alone suffices** (TOFU: the attacker
  controls the origin and serves their own `/.well-known/keryx/root.json`;
  the user confirms the attacker's origin) — mitigated by the QR being
  printed on company-controlled surfaces and by the user confirming the
  origin. The single-lock step by design; the well-known location removes the
  *path* variant of this attack, not the origin variant. **This is why
  suspension never prompts for re-pairing**: after a domain takeover the
  attacker also controls whatever QR the user would find on that origin, so a
  "rescan to fix" affordance would walk the user into TOFU at the worst
  possible moment. The suspended screen warns and offers removal; a Phase-2
  TOFU-free anchor (zone-published root key) is the structural answer.
- **A lookalike app** → the whole model presumes the genuine app; a fake
  "Keryx" client could simply skip verification. Naturally narrowed by the
  intended discovery path: the QR lives on the company's confirmed origin
  and resolves through the platform app-store flow (Universal/App Links), so
  the user is steered to the real app by the same origin they are trusting
  for the root anchor — not by search results. Residual, like every
  install-time trust question on a phone.
- **Device compromise** → attacker reads cached messages locally (standard
  mobile risk).
- **Relay metadata** → device-level wake-up timing; acceptable given content
  opacity.
- **Remote-media telemetry** → fetching company-hosted images/attachments
  reveals device-level telemetry (IP, UA, timing) to the company's CDN; not
  identity, and whether to fetch is the app's privacy preference. The same
  applies to metadata and feed fetches — fetching a per-channel feed reveals
  which channels a device follows. How far an app goes to reduce this (fetch
  policies, an anonymising transport) is an **implementation** choice, not a
  protocol rule — the protocol only guarantees that there is no account or
  per-user state to correlate those fetches with.

---

## 4. Security and Privacy Analysis

**What we solve (short version):** email spoofing is impossible (key-anchored,
not name-anchored); the public broadcast carries no PII and no customer list
to breach; messages are authenticated against keys the signed metadata
authorizes (and, in optional editor mode, signed by per-channel editor keys
and verified by the app — authoring and publishing are separate); feed
content is hash-pinned (no withdrawal/tampering); forgery needs the root of
trust *and* the confirmed origin's well-known space; rotations/revocations
are automatic and chain-verified; identity changes are user-visible
(warning/re-pair), never silent; chain breaks suspend deterministically,
never "maybe".

**Residual risks (documented, mitigated):** simultaneous origin + master-key
compromise (full impersonation — mitigated by offline custody and the
no-credential-requests convention); pairing-time origin control (TOFU —
mitigated by QR placement + origin confirmation); device compromise (cached
messages); relay metadata (wake-up timing); remote-media telemetry (CDN
observability; app-level preference). Detail in §3.

**GDPR posture (selling point):** no identifiers, no accounts, and no
customer list for the core broadcast — the company stores nothing about the
user, so there is nothing to disclose, port, or erase server-side (erasure =
local deletion), and the relay operator sees only opaque wake-ups. Two honest
qualifications: the transient per-order private feeds do process personal data
(capability-protected, expiring, discardable), and any device that fetches
files leaves connection metadata — IP and timing — in the host's or CDN's logs,
which is personal data under GDPR even though it carries no identity from this
protocol. That residual is a property of fetching over the internet, common to
every system that serves files; how far the app reduces it (fetch policy,
anonymising transport) is an implementation choice. Still a materially better
profile than any email-based system, which must hold an address per recipient
by construction.
