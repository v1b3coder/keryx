# Keryx — Why It Looks Like This

**Status: informative.** No normative rules here. Every MUST/SHOULD lives in
[`spec/`](../spec/core.md). This file covers the problem, the goals, the
solution shape, the user journey, each design decision (choice → why → what
we gave up), the alternatives we rejected, and how the whole thing lines up
with existing standards.

Companion informative documents: [`threats.md`](threats.md) (threat model,
residual risks, privacy posture), [`products.md`](products.md) (what the app
and the publisher tool feel like), [`../ROADMAP.md`](../ROADMAP.md) (phases
and open questions).

---

## 1. Summary

Email is broken as a company-to-customer channel. It is unauthenticated in
practice, full of PII, and the primary vector for phishing that costs people
real money — most painfully in finance and crypto, where a single convincing
email can drain a wallet. Companies cannot fix phishing on their side: anyone
can spoof their name, and any breach of a customer database becomes a
spam/phishing goldmine.

This protocol replaces the email channel with a **one-way, signed, broadcast
channel** from a company to the user's phone. The user pairs with a company by
scanning a QR code, confirms the company's **origin**, subscribes to the
channels they want (marketing offers, product information, security alerts,
delivery notifications), and receives **authenticated announcements** in a
dedicated app — with **no email, no phone number, no account, no PII in the
public broadcast, and no per-user state on the company's servers at all** (the
one deliberate exception: transient per-order data behind capability tokens,
[spec/feeds.md §3](../spec/feeds.md)).

Messages are public announcements whose authenticity always traces to **keys
the company authorizes** (master key and per-channel delegated keys): each
channel's feed is hash-pinned by signed metadata, and in editor mode every
item additionally carries its author's signature. Authenticity — not
confidentiality — is the property that matters against phishing, and
signatures give it without any key exchange, accounts, or onboarding. The
company distributes a **standard TUF repository** (metadata + signed feed
files); the app verifies the metadata chain with a standard TUF client and
verifies item signatures where the channel requires them (editor mode). The
trust anchor is a canonical `/.well-known/` location on the origin the user
confirms; the repo itself may live anywhere (CDN, CMS). The server does not
even need to know that the user exists.

The trust model is **binary and automatic**: a message is either signed by a
key the pinned metadata authorizes for that channel — then it is shown — or
it is not — then it is rejected, with no warning, no grace period, and no
judgment call for the user. Reaching a user with a forged message requires
compromising **both** the metadata origin *and* the root of trust (master
key); delegated keys are scoped, so a single channel-key compromise cannot
forge other channels.

The protocol is deliberately boring: HTTPS + QR + TUF + Ed25519 + JSON Feed.
A small e-shop can adopt it in an afternoon with one tool; a company with
strict key custody can keep the master key offline and delegate everything
else. It is also cheap to keep running: because every file is signed and
hash-pinned, the serving side is passive and does not have to be trusted — a
directory of static files on any commodity CDN, with no sending
infrastructure, no deliverability engineering, and no email service provider
holding a copy of the customer list (§2.4, §4.14).

---

## 2. Problem Statement

### 2.1 Email is an unauthenticated, PII-laden attack surface

- Anyone can send an email that *looks* like it comes from a trusted company
  (spoofing, look-alike domains, compromised partner mailboxes, replying to a
  leaked DB). DKIM/DMARC/SPF help but are inconsistently deployed and
  invisible to the end user.
- Every company that sends email **must store** an email address (plus often
  name, address, order history). Customer databases are breached at
  industrial scale; each breach becomes a fresh supply of targets for
  phishing campaigns.
- The burden of distinguishing a real email from a fake one is placed
  entirely on the end user — a user who may never have been trained to check
  sender domains or distrust urgency.

### 2.2 The financial cost is concentrated

Finance and bitcoin services are the highest-value targets. Phishing campaigns
routinely trick users into entering **recovery seeds or wallet passwords** into
fake sites. A single successful phish is total, irreversible loss of funds.
Email remains the dominant delivery vector for these attacks, and no amount of
company-side email security fully closes it, because the attacker does not
need to break the company at all — they only need to imitate it.

### 2.3 Why companies can't fix this alone

- Email deliverability, SPF/DKIM/DMARC alignment, and reputation management
  are fragile and increasingly gated by big mailbox providers.
- A company cannot prevent an attacker from sending an email "from" its brand.
- Removing email entirely from their processes is the only way to *guarantee*
  that a company never contacts a customer over email.

### 2.4 Sending email became an industry — and a third-party risk

Running email at scale is not something a company simply switches on. IP
warming, SPF/DKIM/DMARC alignment, list hygiene, bounce and complaint
handling, spam-filter reputation, unsubscribe compliance, per-provider
quirks — the operational surface is large enough that self-hosting newsletter
infrastructure at scale is impractical for most companies. So they outsource
it, and an entire industry of email service providers exists to absorb that
complexity.

The security consequence is that **the customer list leaves the company**. To
send on a company's behalf, an ESP needs the addresses, usually the names,
often the segmentation data and the purchase history behind it. Every company
that outsources sending hands a copy of its most sensitive customer data to a
third party whose breach surface it does not control — and ESP breaches are a
recurring source of leaked customer lists, which then feed exactly the
phishing campaigns of §2.1. A company can be perfectly disciplined about its
own systems and still lose the data.

So the complexity of email does not merely cost money. It forces a structural
choice between running infrastructure most companies cannot run and
duplicating the customer database into a vendor — and most take the second
option, which is why a breach anywhere in the sending industry becomes a
phishing wave everywhere.

### 2.5 Why this is unsustainable long-term

- PII breach → phishing → customer loss → liability → trust erosion: a
  repeating cycle that shifts ever more responsibility onto the
  least-equipped party.
- Regulatory pressure (GDPR, ePrivacy, digital-identity rules) keeps raising
  the cost of holding and processing PII.
- The market needs a channel that is **secure by construction**, not secure by
  user vigilance.

---

## 3. Goals and Non-Goals

### 3.1 Goals

1. **Replace email as the company→customer communication channel** with a
   one-way, authenticated, broadcast channel.
2. **Zero PII and zero per-user state by design**: the company's servers never
   learn the user's email, phone, name, or any user identity; there is no
   customer list to breach. Subscription is a purely local decision on the
   user's device. The one deliberate exception is transient per-order data in
   capability-protected private feeds.
3. **Phishing-resistant by construction, with no user judgment calls**:
   - Every message is covered by a signature chaining to the pinned metadata
     — either through its feed's hash pin (signed by the channel key) or, in
     editor mode, additionally per item. Verification is binary and
     automatic: chain verifies → show; anything fails → reject. There is no
     "maybe trust it" state.
   - The channel is **one-way** — no reply path, no forms, no interactive
     credential capture. It is an announcement feed, not a conversation.
   - **Rich content by default**: the channel is the *primary, trusted source*
     for the company's content — links, blogs, marketing, media. A link inside
     a signed message is as trustworthy as a link on the company's own
     website; the app renders links **transparently** (real destination
     domains shown, no hidden redirects, no auto-open).
4. **Simple trust model, no extra constructs**: the user confirms the
   **origin** (plain ASCII) at pairing — the only human verification. No
   verification codes, no safety-number comparisons, no trust warnings, no
   grace periods, no accounts.
5. **Simple to adopt, and cheap to keep running**: QR pairing, no account
   creation; one publisher tool; broadcast means no per-user crypto or state.
   The serving side is passive static files on infrastructure that does not
   have to be trusted — no sending pipeline, no deliverability engineering,
   no third-party processor holding the customer list (§2.4, §4.14).
6. **Works without Google/Apple push**: the protocol and app must be fully
   functional on de-Googled devices (e.g., GrapheneOS). Push is an *optional
   wake-up optimization*, never a dependency.
7. **Segmentation without profiling**: companies can target content by
   language, region, topic, or product line — without ever storing or
   learning a user profile. Content is tagged; the app filters locally.
8. **Standards-first, no reinvention**: the metadata chain is **TUF** (full
   repo, standard client); the data carrier is **JSON Feed 1.1** with our
   data in a `_sig` extension other readers ignore; signatures are **raw
   Ed25519 over JCS**. Our app *enforces* the scheme on top.
9. **Delegated keys are supported** (TUF-style delegations) — but they change
   *who signs*, never *how it is verified*: the app still makes a binary
   decision against the pinned root, with no trust states, no warnings, no
   user judgment. Thresholds (n-of-m) are optional, off by default.
10. **Editor mode is optional**: per-channel editor keys sign items, the
    channel key only publishes, and the app enforces both — still with a
    binary rule. It separates authoring from publishing; channels without it
    keep the simpler single-publisher model.

### 3.2 Non-Goals (for v1)

- **End-to-end encryption of messages.** Content is public; authenticity is
  the goal. Per-user encryption would reintroduce key exchange, accounts, and
  onboarding for no security gain (§4.5).
- **Changing the verification model.** Whatever the key topology (one key,
  delegated keys, thresholds), the app never downgrades trust and never asks
  the user to judge a key.
- **Two-way customer support chat.** The channel is one-way; support may be
  added later as a separate, clearly-labeled feature.
- **A universal replacement for transactional email *into* the company**
  (order confirmations to internal systems, invoices, etc.).
- **Push / notification layer** — optional wake-up only, WIP (§4.10). The
  only contract decided so far is that a notification is a wake-up signal,
  never content. No wire format is defined.
- **Email bridge** (per-order virtual addresses, SPF/DKIM/DMARC, mailbox
  discard) — a Phase 3 concept, not part of the protocol.
- **Company directory** — an open question, not a v1 feature.
- Preventing malware on the user's device, or protecting against a fully
  compromised company — residual risks, see [`threats.md`](threats.md).

---

## 4. Design Decisions

Each decision states what we chose, why, and what we gave up. The normative
rules are in [`spec/`](../spec/core.md).

### 4.1 One-way signed broadcast, not a conversation

The channel is an announcement feed with no reply path, no forms, no
interactive credential capture. This removes the entire class of
"conversation-based" phishing — there is nothing to answer. It also keeps the
protocol stateless: a broadcast needs no per-user crypto or state, which is
what makes zero PII possible.

### 4.2 TUF as the metadata chain

The distribution unit is a **standard TUF repository**: root/targets/
snapshot/timestamp plus one delegated role per channel. Why TUF rather than a
custom chain: it is the existing standard for exactly this shape of problem —
authenticated metadata with delegated, scoped keys, thresholds, anti-rollback,
and versioned metadata files — and standard clients (go-tuf, python-tuf,
tuf-js) already verify it. Channel keys are delegated roles (named
`channels.<channel>`, so a channel can never collide with a top-level
metadata filename), so a channel-key compromise is scoped to that channel.
The trust anchor is `/.well-known/keryx/root.json` (RFC 8615) on the join
origin — the only location-trusted URL in the protocol and the exclusive
source of root metadata (chain walk included); the repo base is
master-signed-linked from root.json and may live elsewhere (CDN, CMS,
bucket), making it availability-only — it never serves root metadata.

### 4.3 JSON Feed 1.1 as the carrier

Feeds are plain JSON Feed 1.1 documents — consumable by any generic feed
reader (NetNewsWire, Reeder, podcast apps), which is a free adoption path.
Our data lives in the `_sig` extension, JSON Feed's first-class mechanism for
custom objects: readers that don't understand it must ignore it. We accept
the honest caveat that JSON Feed readers are a niche vs RSS — this is a bonus,
not a headline.

### 4.4 Ed25519 over JCS, no envelope

Item signatures are raw 64-byte Ed25519 over the JCS (RFC 8785) canonical
bytes of the item. No signature envelope (JWS-style wrapper): an envelope
carries algorithm negotiation and header machinery that nothing here
consumes — the algorithm is fixed and the keys come from signed metadata.
Ed25519 over ECDSA for: deterministic nonces (no RNG-failure key leaks), ~2–4×
faster verification, 64-byte signatures, non-malleability, audited
constant-time implementations. TUF metadata uses the TUF library's own
canonicalization (OLPC) — two canonicalizations on purpose; they serve
different layers and must not be mixed.

### 4.5 Authenticity, not confidentiality (no E2EE in v1)

Content is public; the property that defeats phishing is authenticity, and
signatures provide it without key exchange, accounts, or onboarding.
Per-user encryption would reintroduce all three — for no security gain in a
broadcast model.

### 4.6 Origin-confirmed pairing + well-known anchor

The user confirms the join URL's origin in plain ASCII — the single human
verification step. The app then pins the root anchor at that origin's
`/.well-known/keryx/root.json`. The well-known space is admin-controlled
(RFC 8615 §4.1), so a user-content path on the same origin cannot host the
anchor; the QR payload carries **no metadata URL**, so there is nothing
attacker-controllable in the payload to point the app at metadata. Pairing is
the single-lock step (origin recognition); after pairing, messaging is
two-lock (key + confirmed origin) — enforced mechanically: the app fetches
root metadata (the chain walk included) only from the well-known anchor,
never from the repo base, so every root rotation — including any `repo_base`
change — must pass through the origin's admin-controlled space. We accept the
TOFU residual: at pairing time, origin control alone suffices — mitigated by
QR placement on company-controlled surfaces ([`threats.md`](threats.md)). We
also accept one canonical redirect (http→https, www→apex); cross-origin
redirects stay blocked.

### 4.7 Binary trust, no user judgment

There is no "lower trust", no grace period, no yellow warning, no
"do you trust this?" dialog. Either content verifies against the chain, or it
is rejected. This is a deliberate product decision: security decisions are
not delegated to users who are the target of the attack. The one human step
(origin confirmation) is recognition of a brand string, not a judgment about
cryptography.

### 4.8 Zero per-user state, local filtering

Companies target content with standard JSON Feed fields (`tags`,
`language`) — no curated vocabulary, no user profiles; the app filters
locally. A shared catalog gives the server no signal about which topics are
popular: every user fetches the same bytes per channel. Honest caveat: rich
content is *referenced, not inlined*, so fetching media and per-channel feeds
leaves device-level telemetry at the host — a transport property, not a
protocol requirement, analysed in [`threats.md`](threats.md). Referenced bytes
are also mutable by the media host unless the item pins them with the optional
`_sig.resources` hashes.

### 4.9 Capability-URL private feeds for per-order data

Per-order delivery/invoice feeds need access control, but JSON Feed has no
privacy semantics. We use a **capability-URL JSON Feed**: a 128-bit
unguessable token in the URL, authorized by a master-signed pattern entry in
`targets.json`. The master authorizes the namespace once; a feed engine
creates feeds under it dynamically at checkout — no per-order TUF metadata, no
mini repo lingering. The whole document is signed (wrapper + items) by the
pattern entry's keys. Trade-offs accepted: the token travels in URLs (log
redaction and Referrer-Policy are mandated); capability URLs are
single-issuance with no rotation mechanism — long windows rely on 128-bit
unguessability + signatures; the feed host sees device-level fetch telemetry.
**Why 128 and not more:** the only brute-force path is online guessing
against the feed host, where 128 bits leaves ~57 bits of margin against a
century of 10¹² guesses/second, and ~98 bits even against an attacker
targeting *any* of a billion live orders. The real exposure is leakage
(referrer, logs, browser history, sharing), which more entropy does not
address — hence the mandated Referrer-Policy, log redaction, and
no-third-party-resources rules instead. 128 bits is also the norm for
comparable constructs (OWASP puts session identifiers at ≥128 bits). Size was
not the reason: measured against the QR encoder, dropping 43 characters to 22
leaves the common single-private-feed payload at the same QR version (13,
69×69) and saves one version (17→16) only with two feeds — the payload is
dominated by the origin and path, not the token.
This is the one place PII legitimately exists (delivery address, invoice) —
transient, expiring, discardable.

### 4.10 Push as optional wake-up, not a dependency (WIP)

The protocol and app must work fully on de-Googled devices, so push is
optional and never carries content: a notification is at most a channel
identifier + an unencrypted "new messages" counter — a wake-up signal only.
The transport is plug-in (FCM/APNs, ntfy, background fetch, or nothing) and
WIP — the only contract decided so far is that a notification is never
content. The **neutral notification relay** for small companies is WebSub-like
in spirit, but WebSub's per-subscriber callback model is **rejected on privacy
grounds** (a callback URL is per-user state at the hub); the relay uses
anonymous-topic semantics — worst case it knows *that* a device woke up.
Push linkage honesty: APNs/FCM topic subscriptions reveal the device↔company
mapping to the provider; ntfy's anonymous topics avoid *identity* linkage, but
topic subscriptions remain visible to the relay operator, and a self-hosted
relay is per-user state on company infrastructure.

**This is the one genuinely unsettled area of the design.** Everything else in
this file is decided; the push transport is not.

### 4.11 Editor mode: authoring separated from publishing

In editor mode, per-channel editor keys sign items and the channel key only
publishes; the app enforces both with the same binary rule. Why: an editor
compromise can author items, but they reach users only if the publisher
publishes them (CI review gate = policy, not protocol); a channel-key
compromise can re-pin/withhold but cannot forge items. It is master-signed so
the publisher cannot self-authorize, and the reference tool refuses to
configure the same key as both channel key and editor key.

This is aimed at **larger publishers**, where authoring, review, and
operations are already different people — which is why it stays in the Phase 1
MVP despite adding machinery a one-person e-shop will never enable. Two
properties follow from per-item signing and are worth naming: editor keys can
live on **hardware signing devices** (an editor *is* a device the CI trusts,
rather than a credential on a build machine), and the signing UX stays
proportionate — an editor signs the piece they wrote, not the whole feed. No
wire-format change is implied: a device produces the same raw Ed25519
signature over JCS as a software key, so what remains open is the signing
*flow* around it ([`../ROADMAP.md`](../ROADMAP.md)) — chiefly how the device
shows the editor what they are approving.

Editor rotation requires re-signing the items that stay published: that is a
feature, not overhead — when someone leaves, a person still with the company
puts their name on what remains live, instead of the feed carrying signatures
nobody stands behind. Editor keys stay with editors; they do not belong in CI.

### 4.12 Boring key lifecycle, standard machinery

All rotation/revocation is standard TUF (metadata version+1, threshold
overlaps, versioned files) with the addition of two conventions: pre-announced
`next_key` records (recommended), and the rule that expired-but-unrefreshed
metadata is **not** suspension — only a chain break suspends (deterministic,
no "maybe"). Three keys minimum: offline master (root + targets), online ops
key (snapshot + timestamp), one key per channel. The master key filling both
`root` and `targets` is an accepted simplification — a targets-key compromise
is therefore a root-key compromise, which is tolerable because both roles are
offline and share one custody event; publishers wanting stricter separation
may split them (standard TUF, no protocol change).

### 4.13 Full mode is the base; lite mode is a downgrade offered to small publishers

Full TUF requires the publisher to keep `timestamp.json` fresh (a cron/CI
line) — the single biggest operational objection for small publishers. **Lite
mode** ([spec/clients.md §3](../spec/clients.md)) is a formal extension that
drops snapshot/timestamp and the online ops key while preserving
authentication, authorization, feed hash-pinning, anti-rollback, binary
verification, editor mode, and private feeds. It is not part of the Phase 1
MVP; graduation is one root re-sign (flip `custom.mode`, publish
snapshot/timestamp, set up the cron).

Since lite mode preserves most properties, it is fair to ask which should be
the default. Full mode wins on two grounds that matter for who we are building
for: the tooling exists today (go-tuf, python-tuf, RSTUF, tuf-js — a lite repo
needs a custom verification path), and the target adopters are **larger
companies**, for whom a cron line is not an obstacle and timestamp-bounded
freshness is worth having — especially on channels where withholding a message
is itself the attack ([`threats.md`](threats.md)).

Lite mode is therefore a **downgrade for small publishers who want zero
recurring maintenance**, not a recommended configuration and not the shape the
protocol is designed around: dropping timestamp means the staleness bound —
and therefore how long a revoked key remains acceptable to a client that can
be served old metadata — becomes `targets.json`'s own `expires` rather than
the timestamp's hours. A shoe shop that wants zero recurring maintenance may
reasonably take that trade; a publisher with security-relevant channels should
not.

### 4.14 Passive infrastructure: a high cryptographic ceiling on a near-zero operational floor

The honest first impression of this protocol is that it looks heavier than a
mailing list: TUF roles, key custody, thresholds, two canonicalizations. That
impression is about the **setup**, not the **running**. What a publisher
operates after `init` is a directory of static files.

There is no sending, so there is no deliverability, no IP reputation, no
bounce or complaint handling, no list hygiene, no unsubscribe plumbing, and no
recipients to address — every subscriber fetches the same bytes. There is no
per-user state, so there is no customer list to hold, to hand to a vendor, or
to lose (§4.8). And because every file is signed and every feed is
hash-pinned, the serving infrastructure is **explicitly not trusted**: the
repo base can be any commodity CDN, bucket, or CMS — a host that can drop or
delay files but cannot forge one, tamper with one, or silently withdraw one
(the repo base is availability-only, §4.2). Self-hosting the whole thing is a
static web server.

The recurring operational cost is therefore CDN bytes plus one cron line to
refresh `timestamp.json` — and lite mode drops even the cron (§4.13). No ESP,
no third-party data processor, no per-recipient pricing, no vendor to
re-negotiate with when the list grows.

This is the trade the protocol makes against §2.4: it moves the difficulty
from a **permanent operational burden** — the one companies outsource, and by
outsourcing leak their customer data — to a **one-time setup plus key-custody
discipline**, which stays in-house and involves no personal data at all. The
cryptography buys the strong properties; the passivity is what makes those
properties affordable to keep. Making the setup boring too is the publisher
tool's job ([`products.md`](products.md)).

---

## 5. Solution Overview

```
┌──────────────┐  scan QR   ┌──────────────────────────────────┐
│  User's app  │──────────▶ │  Company repo (TUF, static)      │
│  (phone)     │            │  root/targets/<channel>/snapshot/ │
│              │◀───────────│  timestamp + feed files          │
│              │  HTTPS     │  (verified by standard TUF client)│
└──────┬───────┘            └──────────────────────────────────┘
       │
       │  optional wake-up only (no content)
       └──────────────┬───────────────┐
                      │               │
        ┌─────────────▼─────┐   ┌─────▼──────────────────┐
        │ APNs / FCM / ntfy │   │ Neutral notification   │
        │ (unified push)    │   │ relay (small companies)│
        └───────────────────┘   └────────────────────────┘
```

**Three parties, three responsibilities:**

1. **User app** — the customer's phone. Runs a standard TUF client against
   the master-signed `repo_base` (discovered via the well-known root anchor
   on the confirmed origin), verifies item signatures, filters locally,
   renders the one-way inbox. No account, no keys to register, nothing to
   send.
2. **Company endpoint** — the root anchor at `/.well-known/keryx/root.json`
   on the confirmed origin, plus a static TUF repo at the master-signed
   `repo_base` (self-hosted, bucket, CDN, or CMS — possibly elsewhere).
   No application server, no database, no user identity data (transient
   per-order data only, behind capability tokens).
3. **Notification relay (optional)** — neutral infrastructure delivering only
   wake-up signals ("company X has new messages"). Content never passes
   through.

---

## 6. User Journey

1. **Pair.** The user scans a QR code printed on the company's website,
   packaging, receipt, or app. The QR encodes a standard **HTTPS join URL**
   with a payload: suggested public channels and optional **private
   capability feeds** (e.g. this order's delivery tracking) — there is no
   metadata URL in the payload; the app derives the root anchor from the join
   URL's origin. Being a plain HTTPS URL, it works with the standard
   app-install flow (Universal/App Links): no app yet → store page → install
   → re-open the same link → everything imported in one pass.
2. **Confirm the origin.** The app shows the join URL's origin in plain
   ASCII (punycode for IDNs, no UTF-8): `company.example` — "Subscribe to
   messages from this origin?" This single confirmation is the entire human
   verification step.
3. **Fetch & pin.** The app fetches the root anchor from
   `/.well-known/keryx/root.json` on the confirmed origin, reads the
   master-signed `custom.repo_base`, then runs the TUF client: root chain
   (versioned `N.root.json`, always from the anchor — never the repo base),
   timestamp, snapshot, targets, delegated channel metadata — all verified
   against the pinned root.
4. **Subscribe.** Only now — with the chain verified — does the app show the
   company's name and logo, and its **channels** under their real display
   names (marketing, product, security, delivery, …); payload-suggested
   channels are **preselected — the user must tap to subscribe** (explicit
   opt-in). Private feeds auto-subscribe (order-scoped). All local decisions;
   the company is never told. The confirmed origin stays visible next to the
   brand from here on: the origin is the anchor, the brand is decoration
   under it.
5. **Receive.** On wake-up or sync, the app refreshes metadata, fetches the
   public feed files (hash-verified TUF targets), verifies each item
   against its channel's authorization (strict: failing items are dropped,
   even if previously shown), applies updates/withdrawals, filters by local
   preferences, and shows messages. Anything that fails verification is
   simply not shown.
6. **Manage.** Mute/leave channels or delete the company (keys and cached
   messages wiped) — all local; no server-side unsubscription needed.

Zero onboarding: no email, no phone number, no account, no password, no
codes, no trust dialogs.

---

## 7. Standards Alignment

| Area | Existing standard | Verdict |
|---|---|---|
| Metadata chain | **TUF (The Update Framework)** — root/targets/snapshot/timestamp, delegations, thresholds, versioned files | **Adopted in full** (full mode; lite mode is a documented extension with its own verification path) — the publisher produces a standard TUF repo with standard tooling (go-tuf/python-tuf/RSTUF); the app consumes it with a standard client. The trust anchor is `/.well-known/keryx/root.json` (RFC 8615) and the exclusive source of root metadata (the client's fetcher routes root requests there — a supported extension point, not a fork); the repo base is the master-signed `custom.repo_base`. Channel keys are delegated roles; private feeds are authorized by a master-signed pattern list in `custom` (never TUF targets). |
| Item/message schema | **JSON Feed 1.1** (`application/feed+json`) | **Adopted** — feeds are JSON Feed documents; our data lives in the `_sig` extension (ignored by other readers). |
| Signatures | **EdDSA (RFC 8032)** — raw over **JCS (RFC 8785)** | **Adopted** — no signature envelope (JWS-style wrappers add algorithm negotiation and headers that nothing here consumes). Ed25519 over ECDSA: deterministic nonces, no RNG-failure key leaks, ~2–4× faster verification, 64-byte signatures, non-malleable, audited constant-time implementations. |
| Domain binding | DNSSEC/DANE (RFC 6698), TLS certs | Optional hardening; a Phase-2 optional anchor (zone-published root key) is DNSSEC-required by design. |
| Wake-up / push | **WebSub** (W3C) | **Rejected** — per-subscriber callback URLs are per-user state at the hub (the linkage this protocol eliminates). Wake-up is transport-agnostic, ntfy-first (anonymous topics). |
| Signed public broadcast | Nostr; ActivityPub | Inspiring but rejected: Nostr treats a key as a permanent identity — no rotation, revocation, delegation, or threshold chain — and relays provide no freshness proof; ActivityPub is social and two-way. Reasoning in §8. |
| Auditability | Key Transparency / CT-style logs (RFC 9162 style) | Later phase (optional). |

**Honest scope of "adopted in full":** the metadata chain is genuine TUF,
verified by a standard client with standard tooling — but a real
implementation also carries a documented profile on top: root fetched from
the well-known anchor via the client's fetcher hook, the app-level semantics
living in master-signed `custom` blobs (company identity, editor mode,
private-feed patterns, `repo_base`, `mode`), item signing in JCS alongside
TUF's own canonicalization, and — from Phase 2 — lite mode's non-standard
verification path. TUF carries the part it was built for (delegated, scoped,
rotatable authorization with anti-rollback); the rest is ours, by necessity
rather than preference, since TUF has no notion of company identity or
capability-URL feeds.

**What is genuinely novel:** the trust model for company→customer messaging —
origin-confirmed pairing, TUF-anchored keys, binary verification, suspension
on chain break, zero per-user state, local filtering. After adopting the
above, this is roughly 70% glue over existing standards and 30% new trust
model.

---

## 8. Rejected Alternatives

Recorded so they are not re-proposed. Each was considered and turned down for
the stated reason.

| Alternative | Verdict |
|---|---|
| **JWS / signature envelope** for items | Rejected — algorithm negotiation and header machinery that nothing here consumes; the algorithm is fixed and keys come from signed metadata (§4.4). |
| **ECDSA** instead of Ed25519 | Rejected — RNG-dependent nonces, slower verification, malleability (§4.4). |
| **WebSub** for wake-up | Rejected — per-subscriber callback URLs are per-user state at the hub (§4.10). |
| **Nostr** as the carrier | Rejected on key lifecycle, not on signing — no rotation/revocation chain, one key per identity (so no offline master, per-channel scoping, editor keys, or thresholds), and no anti-freeze bound. See the note below. |
| **ActivityPub** as the carrier | Rejected — social and two-way; this channel is a one-way announcement feed with no reply path (§4.1). |
| **Company name/logo on the origin confirmation screen** | Rejected — they are self-asserted, so any origin can claim any brand; showing them at the decision moment would anchor recognition on forgeable chrome instead of the domain, re-importing email's hidden-display-name failure. Shown only after the chain verifies, always beside the origin ([spec/core.md §2](../spec/core.md)). |
| **A metadata URL in the QR payload** | Rejected — anything attacker-controllable in the payload that points at metadata weakens pairing; the anchor is derived from the confirmed origin instead (§4.6). |
| **Lite mode as the default** | Rejected — tooling exists for full TUF today, and the target adopters can run a cron line; lite is an opt-in downgrade (§4.13). |
| **`consistent_snapshot: true` as the default** | Rejected for this topology — unbounded accumulation of versioned metadata and hash-named feed copies on static hosts, and it splits the feed into two copies (TUF clients vs generic readers). Publishers may opt in ([spec/repository.md §1](../spec/repository.md)). |
| **Re-pair prompt on suspension** | Rejected — after a domain takeover the attacker controls the QR on that origin too; a "rescan to fix" affordance walks the user into TOFU at the worst moment ([spec/core.md §4](../spec/core.md)). |
| **A curated tag/topic vocabulary** in v1 | Rejected for now — free-form `tags` + `language` suffice; a labeled vocabulary can be added as an optional `_sig` field later ([spec/feeds.md §1.1](../spec/feeds.md)). |
| **Per-feed sequence numbers (`seq`)** | Dropped — ordering is editorial; dedup is `(channel, id)`. |
| **Per-item `_sig.expiresAt`** (auto-hide) | Deferred to v2, not rejected ([`../ROADMAP.md`](../ROADMAP.md)). |
| **Per-user encryption (E2EE)** in v1 | Rejected — reintroduces key exchange, accounts, and onboarding for no gain on public broadcast content (§4.5). |

**On Nostr specifically.** Nostr is the closest existing system to this one —
signed broadcast, pubkey identity, no accounts, untrusted relays — so it earns
more than a table row. It even has a domain anchor: NIP-05 maps a name to a
pubkey through a `/.well-known/nostr.json` file, structurally the same trick as
our root anchor. The difference is that NIP-05 is a *lookup*, not a pinned
anchor with a chain behind it — it answers which key claims a name today, with
no version, no signature by a prior key, no threshold, and no revocation
semantics — and everything we need falls into that gap. A stolen key on Nostr
ends an identity: recovery means starting over and asking users to trust a new
key by hand, which is the judgment call this protocol exists to remove, whereas
rotation, revocation, and suspension are most of what Keryx is built out of.
One key per identity also rules out the topology (§4.12): no offline master, no
per-channel scoping, no editor keys, no thresholds — NIP-26 delegated signing
saw little adoption and is effectively abandoned — so a company's identity key
would sit on the publishing machine. Freshness is the other half: a relay can
simply not return an event, absence is indistinguishable from withholding, and
no signed statement of staleness exists anywhere, which bites hardest on
exactly the security-alert channel where silence *is* the attack
([`threats.md`](threats.md)). Two operational consequences compound it — a
relay is a stateful websocket server with a database, reversing the
passive-CDN property of §4.14, and a client's subscription filter discloses the
set of pubkeys it follows, so a relay (most likely the company's own) sees a
stable interest set per connection rather than the device-level telemetry a CDN
sees (§4.8). What Nostr does better is worth recording so the trade stays
honest: relay federation survives the seizure of a domain, where we suspend the
company instead — a real advantage under a different threat model, since our
adversary is a phisher imitating a company rather than a state seizing one;
setup is far cheaper; addressable events give in-place update semantics
comparable to ours (its deletion requests are weaker — relays may or may not
honour them, where `_sig.withdrawn` is signed into the item and enforced by the
client); and the curve choice is a wash, since BIP-340 Schnorr is sound and
Ed25519 was picked for TUF-ecosystem fit, not because Nostr's is weak. The
short version: Nostr is a good broadcast substrate with no key lifecycle and no
freshness proof, and this protocol is mostly key lifecycle and freshness.
