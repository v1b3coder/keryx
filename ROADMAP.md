# Keryx — Roadmap and Open Questions

**Status: informative.** Where the project is going, and everything still
undecided. This is the *single* list of open items — wire-format questions and
design questions live here together so they cannot drift apart.

---

## 1. Phases

| Phase | Scope |
|---|---|
| **0 — Design docs (current)** | Protocol, wire format, threat model. Feedback loop with finance/crypto companies. |
| **1 — MVP** | Reference publisher tool (`pub`: full TUF repo, channels, per-channel role metadata, editor mode, QR); reference app (TUF client + one-way inbox, no push dependency); private capability feeds; suspension; pilots with 1–2 friendly companies. |
| **2 — Ecosystem** | Lite mode implementation ([spec/clients.md §3](spec/clients.md)); ntfy/unified push + neutral relay (incl. refresh-TTL revisit); `_sig.expiresAt` (v2, per-item auto-hide); cloud-KMS/hardware signer backends (Sigstore signer interface); optional TOFU-free anchor (zone-published root key, `_keryx.<domain>` TXT, DNSSEC-required); backup/restore UX; optional company directory. |
| **3 — Bridge & federation** | Email bridge with virtual mailboxes (SPF/DKIM/DMARC verification); partner-channel semantics; possible standardization path. |
| **4 — Standards** | Open governance, formal spec, independent implementations, security audit. |

---

## 2. Open Questions — Wire Format (resolve before locking v1)

1. **`_sig` member set:** finalize (`about`, `channel`, `withdrawn`,
   `signatures`, optional `resources`; `url`/`version`/`expires` for private
   feeds — `seq` dropped; `expiresAt` deferred to v2).
2. **`_sig.about` URL:** must be a real published URL before lock — it is the
   extension's identity ([spec/feeds.md §1.1](spec/feeds.md)).
3. **Mirrors:** v1 uses the single `custom.repo_base`; mirror selection
   (multiple bases, failover) is a Phase 2 question.
4. **TUF client interop** (verify against go-tuf / python-tuf / tuf-js before
   lock): keyid computation (hash of the canonical key object); delegation
   glob semantics (`*` matches one path segment and never crosses `/` — a
   client that matched across `/` would silently widen a channel's
   authorization); dotted delegated-role names (`channels.<channel>`)
   round-tripping through metadata and filenames; and the fetcher routing
   that keeps root at the well-known anchor.
5. **Root pruning:** the publisher keeps all `N.root.json` (TUF mandate);
   app-side retention policy for old roots is an open detail.
6. **Lite mode:** implemented in Phase 2 — confirm the convention discovery
   path and the per-metadata `expires` cadence against real publishers.

---

## 3. Open Questions — Design and Operations (resolve in Phase 1)

1. **Key custody, cadence, and the hardware signing flow.** Software keys for
   the MVP; cloud KMS via the Sigstore signer interface later; **hardware
   signing devices** (e.g. Trezor) from Phase 2+ need a defined signing
   *flow*, not a different wire format — the signature stays raw Ed25519 over
   JCS ([`design/why.md` §4.4](design/why.md)). Open: how the device presents
   what is being signed (a human-readable summary of the item, not opaque
   bytes), how the tool hands it over, and how the resulting signature is
   collected. This applies to **editor keys as much as to the root key** —
   device-held editor keys are a main motivation, since per-item signing is
   what makes device signing usable day to day
   ([`design/why.md` §4.11](design/why.md)). Also open: root rotation cadence
   and pre-announced `next_key` practice.
2. **Feed size/retention:** soft cap for the live feed (~250 KB per channel);
   archive policy for very old items (archive target files via `next_url`);
   how far back a new subscriber gets.
3. **Rich content format:** the exact HTML subset the sandboxed renderer
   allows (styles, buttons, tables, embeds); media referencing conventions.
4. **Thresholds in practice:** n-of-m signing (e.g. 2-of-3 for security
   alerts) is supported but off by default — how commonly will real companies
   use it?
5. **Editor mode operations:** editor onboarding/removal requires the offline
   master (like channel changes) — acceptable cadence in practice? Batch
   changes; the reference tool supports it. (The re-signing question is
   settled: strict verification, re-sign during the overlap —
   [spec/feeds.md §2](spec/feeds.md), [spec/repository.md §5](spec/repository.md).)
6. **Local subscription portability:** backup/restore **format details**
   (join origin + pinned root keys + channel lists + private capability URLs
   across devices).
7. **Push transport:** the whole wake-up layer is WIP — relay shape, ntfy vs
   unified push, refresh TTL ([`design/why.md` §4.10](design/why.md)).
8. **Directory:** is a public, opt-in directory of companies worth the trust
   implications?
9. **Delivery partner reality check:** which logistics/delivery providers
   accept arbitrary sender addresses, and what does the email bridge need to
   handle (reply-to, attachments, unsubscribe headers)?
