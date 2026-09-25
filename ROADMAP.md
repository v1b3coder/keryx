# Keryx — Roadmap and Open Questions

**Status: informative.** Where the project is going, and everything still
undecided. This is the *single* list of open items — wire-format questions and
design questions live here together so they cannot drift apart.

---

## 1. Phases

| Phase | Status | Scope |
|---|---|---|
| **0 — Design docs** | done | Protocol, wire format, threat model. The feedback loop with finance/crypto companies stays open. |
| **1 — MVP** | **done** (pilots pending) | Reference publisher tool (`pub`: full TUF repo, channels, per-channel role metadata, optional authors roles, QR); reference app (TUF client + one-way inbox, no push dependency); private capability feeds; suspension. Remaining: pilots with 1–2 friendly companies. |
| **2 — Ecosystem** | **current** | Done: the neutral relay (FCM + UnifiedPush/WebPush legs, deployed on Fly.io) and the wake-up transports — PWA WebPush, native Android FCM topics, and de-Googled Android via the ntfy distributor — with the app-wide registration, self-test and liveness recovery. Pending: lite mode ([spec/clients.md §3](spec/clients.md)); the refresh-TTL revisit; per-item auto-hide (v2, app policy); cloud-KMS/hardware signer backends (Sigstore signer interface); the optional TOFU-free anchor (zone-published root key, `_keryx.<domain>` TXT, DNSSEC-required); backup/restore UX; optional company directory. |
| **3 — Bridge & federation** | planned | Email bridge with virtual mailboxes (SPF/DKIM/DMARC verification); partner-channel semantics; possible standardization path. |
| **4 — Standards** | planned | Open governance, formal spec, independent implementations, security audit. |

---

## 2. Open Questions — Wire Format (v1 locked; extensions and interop checks open)

1. **Item field set:** locked — `id`, `title`, `content_html`, `image`,
   `date_published`, `date_modified`, `tags`, `language`, `attachments`,
   `sig` ([spec/feeds.md §1](spec/feeds.md#1-public-channel-items)); no
   `_sig` extension, no `withdrawn`, no `resources` (hashes live in
   attachments).
2. **`sig` canonicalization:** OLPC (TUF canonical JSON) — verified against
   go-tuf v2.4.2 ([spec/feeds.md §1.2](spec/feeds.md#12-signing-and-verification)).
3. **Mirrors:** v1 uses the single `custom.repo_base`; `custom.mirrors` is
   named in the spec as a Phase 2 field ([spec/core.md §1](spec/core.md))
   but is not implemented. Mirror selection (multiple bases, failover) is a
   Phase 2 question.
4. **TUF client interop:** go-tuf v2.4.2's client verifies the generated
   repos (SDK, relay, `pub pull`), and the app's in-repo TypeScript client
   implements the standard TUF 1.0 workflow (tuf-js is Node-only and cannot
   run in a browser); cross-stack tests run against real Go-signed artifacts.
   Verified across the two implementations: keyid computation (hash of the
   canonical key object); delegation glob semantics (`*` matches one path
   segment and never crosses `/` — a client that matched across `/` would
   silently widen a channel's authorization); dotted delegated-role names
   (`channels.<channel>`, `channels.<channel>.authors`) round-tripping
   through metadata and filenames; the authors role (a delegated role pinning
   no targets, sibling of a terminating role — resolution short-circuits at
   the terminating channel role; the authors metadata verifies against the
   delegation); and the fetcher routing that keeps root at the well-known
   anchor. Remaining: an independent python-tuf check before locking v1.
5. **Root pruning:** the publisher keeps all `N.root.json` (TUF mandate);
   app-side retention policy for old roots is an open detail.
6. **Lite mode:** specified ([spec/clients.md §3](spec/clients.md)) but not
   implemented — the app refuses a lite repo and `pub` rejects `--mode lite`.
   Open: implement it, then confirm the convention discovery path and the
   per-metadata `expires` cadence against real publishers.

---

## 3. Open Questions — Design and Operations (Phase 2+ and field questions)

1. **Key custody, cadence, and the hardware signing flow.** Software keys
   shipped: a passphrase-encrypted (scrypt + AES-256-GCM) key store outside
   the repo, role-scoped
   workspaces (`operator|ci|author`), encrypted export/import between
   machines, strict stage/apply master ceremonies, and
   `rotate-root --announce-next-key`. Cloud KMS via the Sigstore signer
   interface is Phase 2; **hardware signing devices** (e.g. Trezor) from
   Phase 2+ need a defined signing *flow*, not a different wire format —
   the signature stays raw Ed25519 over the TUF canonicalization
   ([`design/why.md` §4.4](design/why.md)). Open: how the device presents
   what is being signed (a human-readable summary of the item, not opaque
   bytes), how the tool hands it over, and how the resulting signature is
   collected. This applies to **author keys as much as to the root key** —
   device-held author keys are a main motivation, since per-item signing is
   what makes device signing usable day to day
   ([`design/why.md` §4.11](design/why.md)). Also open: root rotation cadence
   and pre-announced `next_key` practice.
2. **Index size/retention:** the channel role metadata is the published set —
   it grows ~100 B per item and is pruned by unpublishing; there is no
   archive policy and no `next_url`. Open: practical index size limits and
   whether a channel should cap its published set.
3. **Rich content format:** the item model settles inline data-URL media and
   the referencing conventions
   ([spec/feeds.md §1.1](spec/feeds.md#11-item), [§1.4](spec/feeds.md#14-rendering-and-links));
   the reference renderer is stricter than the spec's sandbox and strips
   content CSS entirely. Open: whether to allow the spec's HTML/CSS subset
   (styles, buttons, tables) in an isolated container.
4. **Thresholds in practice:** n-of-m signing (e.g. 2-of-3 for security
   alerts) is supported but off by default — how commonly will real companies
   use it?
5. **Authors role operations:** implemented (`author add/revoke/list`,
   stage/apply ceremonies, re-sign during the overlap —
   [spec/feeds.md §2](spec/feeds.md#2-authors-role), [spec/repository.md §5](spec/repository.md)).
   Open: is the offline-master cadence (like channel changes) acceptable
   in practice, and do real publishers need batch changes?
6. **Local subscription portability:** backup/restore **format details**
   (join origin + pinned root keys + channel lists + private capability URLs
   across devices).
7. **Push transport:** the endpoint leg is shipped and verified end to end —
   the relay (Fly.io, idle-exit + cold start), PWA WebPush, and de-Googled
   Android via the ntfy distributor, with the app-wide registration,
   self-test and liveness recovery
   ([relay/SPECIFICATION.md](relay/SPECIFICATION.md)). The topic leg is also
   shipped: native Android FCM topics with the topic-leg self-test and the
   UnifiedPush fallback. Open (relay spec §11): the client recovery
   budget X hours; shared-topic ntfy (bare ntfy app) in v1; the endpoint/FCM
   registry TTLs against the protocol's freshness model; relay identity and
   governance; the deferred on-premise relay; the refresh-TTL revisit.
8. **Directory:** is a public, opt-in directory of companies worth the trust
   implications?
9. **Delivery partner reality check:** which logistics/delivery providers
   accept arbitrary sender addresses, and what does the email bridge need to
   handle (reply-to, attachments, unsubscribe headers)?
