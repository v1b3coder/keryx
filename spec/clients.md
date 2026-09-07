# Keryx — Client Flow, Publisher Tool, and Lite Mode

**Status: normative** (RFC 2119 keywords as in [`core.md`](core.md)).
Rationale is informative and lives in [`design/why.md`](../design/why.md);
the product-level view of both sides is in
[`design/products.md`](../design/products.md).

Covers: the client verification flow (§1), the publisher tool contract (§2),
and the optional lite-mode extension (§3, Phase 2).

---

## 1. Client Verification Flow

The app uses a standard TUF client for the metadata chain; item verification
is ours (and editor-mode verification is protocol-mandated).

```
payload = decode_qr(QR)                 // join URL -> base64url JSON (core §3)
origin = join_url.origin                // the one confirmed origin
user confirms origin (ASCII)            // only human step

anchor_dir = origin + "/.well-known/keryx/"
root = fetch_verify_root(anchor_dir + "root.json")
                                        // TOFU on first pair; chain-walk after
base = root.custom.repo_base            // master-signed (mirrors: Phase 2)

client = tufjs.Updater(base = base,     // full mode (or lite path, §3)
    fetcher = route("*.root.json" -> anchor_dir,  // root ONLY from anchor
                    else          -> base))       // the rest from the repo base
client.refresh()                        // root chain (from anchor) -> timestamp
                                        // -> snapshot -> targets
                                        // (all hash/signature/expiry verified)
channels = delegated roles from verified targets.json
           // roles named "channels.<name>" whose paths stay inside
           // channels/<name>/ ; other roles ignored (repository §2). Read from
           // the client's verified metadata store.

// consent summary (core §3) — the chain is verified by this point, so the
// summary can name things in the publisher's signed words. Nothing below
// was displayable on the confirmation screen.
show_consent(origin  = origin,                          // stays on screen
             company = targets.custom.company_name,     // never shown earlier
             logo    = targets.custom.logo,             // logo_sha256 checked
             offered = payload.channels,                // display_name from
                                                        //  channels.<name>.json
             private = payload.private_feeds)
user taps Subscribe                                     // explicit opt-in

for name in followed_channels:          // from channels + local prefs
    ti = client.getTargetInfo("channels/" + name + "/feed.json")
                                        // TUF resolves the path to the role
                                        // channels.<name> and verifies
                                        // channels.<name>.json vs role keys
    feed = client.downloadTarget(ti)    // hash-verified
    for item in feed.items:
        if item._sig.channel != name: reject (not shown)
        if editor_mode[name] defined:
            verify JCS(item, editor_mode[name].keys, item._sig.signatures,
                       editor_mode[name].threshold)   // failure -> drop
        else:
            optional attribution verification (see feeds §1.2)
        if item._sig.withdrawn: hide (never shown, not unread)
        if (name, item.id) not in cache:              // new
            if matches local prefs: display
        elif content differs from cache:              // update
            re-verified above; replace cache,
            keep position (date_published) + read-state
        // else: unchanged -> skip (already verified)

for f in payload.private_feeds:         // private capability feeds from QR
    entry = pattern_match(targets.custom.private_feed_patterns, f)  // else reject
    subscribe_local(f, entry)           // token stored locally; whole-doc
                                        // signature + channel + url + version
                                        // + expires verified on fetch (feeds §3)

// company identity watch (core §2): if custom.company_name changed ->
// prominent warning + re-pair prompt; logo (cosmetic) -> one-tap
// acknowledge. Never silent, never auto-accept.
```

**Rules (summary of the normative decisions above):**

- Root metadata only from the well-known anchor
  ([repository.md §1](repository.md)); malformed anchor data → retry, never
  suspension.
- No signature → no display (editor mode); unknown `keyid` → reject (editor
  mode); known `keyid` whose signature fails → item rejected (both modes,
  [feeds.md §1.2](feeds.md)).
- Unverifiable root change (validly signed, unchainable) → **company
  suspended** with a possible-compromise warning and no re-pair prompt
  ([core.md §4](core.md)).
- Expired-but-unrefreshed metadata → keep cache + retry.
- `expired` private feed → stop polling, keep cache, mark closed.
- Items signed by a removed key → dropped (publisher must re-sign,
  [repository.md §5](repository.md)).
- Withdrawn items → hidden; items that merely left the live feed → kept in
  cache (absence ≠ withdrawal, [feeds.md §1.2](feeds.md)).
- A fetched resource that mismatches its `_sig.resources` hash → resource
  unavailable, item unaffected.
- Company identity changed since pairing: `company_name` → prominent
  rebranding warning + **re-pair** (rescan QR) before content is shown;
  `logo` (cosmetic) → one-tap acknowledge; never silent, no auto-accept.
- Company name and logo are never shown before the chain verifies, never
  without the join origin beside them, and never as externally verified
  ([core.md §2](core.md)); a `logo_sha256` mismatch falls back to a
  placeholder and affects nothing else.
- Nothing is ever shown with "lower trust".

---

## 2. Publisher Tool (Working Name `pub`)

```
pub init --domain company.example --name "ACME s.r.o." [--base https://cdn.example.com/keryx] [--logo https://company.example/logo.png]
     → master key + online ops key (+ per-channel keys on demand),
       full 4-role repo (root/targets/snapshot/timestamp + channel roles);
       emits /.well-known/keryx/root.json (with custom.repo_base) for the
       join origin + the repo at the base; prints one-time backup.
       --logo also fetches the image once and records custom.logo_sha256
pub channel add marketing              # public channel: delegation role
                                       #  channels.marketing + role metadata
                                       #  channels.marketing.json
pub editor add --channel marketing --keyid <id>
pub editor revoke --channel marketing --keyid <id>
pub publish --channel marketing --file msg.json
     # appends item to channels/marketing/feed.json, updates the target,
     # re-signs channels.marketing.json (channel key) + snapshot + timestamp;
     # in editor mode: refuses to publish items that fail editor-mode
     # verification (the tool verifies editor signatures — it never holds
     # editor keys)
pub rotate --channel marketing         # overlap (old+new, threshold 1)
pub revoke --channel marketing --keyid <id> [--reissue]
pub rotate-root                        # root.json v+1 signed by old master;
                                       #  written to the well-known anchor dir
pub refresh-timestamp                  # cron line: re-sign timestamp
pub validate                           # full check of the repo
pub qr --channels marketing,product --private-feed "https://…/tracking/<token>/feed.json" --out qr.png
     # join URL only — no root in the payload; the app derives the anchor
     # from the join origin (/.well-known/keryx/root.json)
```

- Output = two static directories: the well-known root dir
  (`/.well-known/keryx/` — `root.json` + all `N.root.json`; root metadata
  lives **only** here) for the join origin, and the repo (no root files)
  for the base, uploadable to any static host/CDN; `timestamp` refresh = one
  cron/CI line.
- Fail-safes: validates before write; deterministic output; bumps
  `version`/`expires` on metadata changes only; refuses to sign with a
  revoked key; refuses to publish an item whose `keyid` is unauthorized;
  refuses to publish an unsigned item in an editor-mode channel; refuses
  to configure the same key as both channel role key and editor key; rejects
  channel names outside `[a-z0-9-_]+` and always writes the role as
  `channels.<channel>`; recomputes `custom.logo_sha256` whenever `logo`
  changes, and refuses to write a stale one; verifies every `_sig.signatures` entry it writes (a
  present-but-invalid signature is rejected by clients, so it must never
  leave the tool). Thresholds default to 1-of-1.
- Key custody (MVP): software keys in OS keychain/encrypted file + one-time
  backup printout. **Editor mode:** editor keys live with the editors — on
  their own machines or signing devices — and never in CI; the channel key
  lives in the publishing pipeline (CI); the master key offline. Advanced:
  cloud KMS / hardware ceremony (Phase 2+, additive via the Sigstore
  `signature.Signer` interface).
- Crypto split: TUF metadata signing delegated to go-tuf/python-tuf (OLPC);
  item signing (JCS + Ed25519) is ~50 lines in the tool and app.

---

## 3. Lite Mode (Optional Protocol Extension — Phase 2)

Lite mode is a formal extension of the protocol for small publishers: it
drops `snapshot.json` and `timestamp.json` (and the online ops key and its
cron) while keeping authentication, authorization, feed hash-pinning,
anti-rollback, and binary verification. It is **not** part of the Phase 1
MVP; the app and tool MUST implement full mode first, and MUST understand the
`mode` flag ([repository.md §1](repository.md)) so a lite repo never breaks a
full-mode client.

- **Flag:** `root.json` `custom.mode` (`"full"` default | `"lite"`).
  Changeable in both directions via root ceremony (version+1, master-signed).
- **Layout:** identical to full mode minus snapshot/timestamp — root anchor
  at `/.well-known/keryx/root.json` (with `custom.repo_base`; root metadata
  only at the anchor), repo base with `targets.json`,
  `channels.<channel>.json`, feed targets. Targets are served at their plain
  paths in both modes (`consistent_snapshot: false` default).
- **Discovery (convention, no pointers):** the app reads channel role names
  (`channels.*`) from the verified `targets.json` `delegations.roles` and
  fetches `<role>.json` at the repo base — standard TUF layout, no snapshot
  to walk, no extra constructs. The role file is load-bearing: it carries the
  channel key's per-publish signature (`targets.json` delegations define
  *who*, the role file pins *what*).
- **Verification path:** root (well-known) → targets (master) → `<role>.json`
  (channel key, vs delegation keys) → feed target (hash in role metadata).
  This is a custom app path — TUF clients as-is do not verify lite repos.
- **Freshness:** per-metadata `expires` only — targets (master-set, long),
  channel role metadata (publisher-set per publish), root; plus client-side
  version memory (anti-rollback). No timestamp anti-freeze: the staleness
  bound is the nearest metadata `expires` instead of the timestamp's hours.
- **Preserved:** authentication, authorization (channel delegations, editor
  mode, private-feed patterns), feed hash-pinning (anti-withdrawal/tamper),
  anti-rollback (version memory), binary verification, editor mode, private
  feeds.
- **Given up:** timestamp-hour anti-freeze precision; mix-and-match
  detection (theoretical in this topology — only one metadata author per
  role); standard-client (tuf-js as-is) verification. **Revocation latency
  (be aware):** without timestamp, the staleness bound on `targets.json` is
  its own `expires`, so a revoked channel or editor key stays acceptable to
  a client that can be served the old (still-unexpired) `targets.json` —
  a new subscriber, a device restored from backup, or one an attacker
  freezes — until that expiry. In full mode the same window is bounded by
  the timestamp cadence (hours). Publishers trade revocation speed for the
  absence of a cron; the balance is a publisher-side operational choice
  (a long expiry is a legitimate pick for low-stakes channels).
- **Publisher ops:** per-publish re-sign of the channel's role metadata
  only. **No cron, no snapshot, no timestamp.**
- **Graduation:** flip `mode` to `"full"`, publish snapshot/timestamp, set
  up the cron — one root re-sign; no app or wire-format change beyond the
  flag.
