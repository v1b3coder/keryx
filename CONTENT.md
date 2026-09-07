# Keryx — Content Model (WIP)

**Working title:** Keryx (placeholder — open to renaming).
**Version:** 0.1 (WIP draft)
**Status:** **Work in progress — proposal, not normative.** This document captures
the open design discussion on *content expressiveness and the content trust
boundary*: what a signed article may contain, how it is rendered, and what the
app may and may not claim about it. Once locked, the normative rules move into
[PROTOCOL.md](PROTOCOL.md) (§8.2 content rules, §4 `custom` fields) and the
UX/design rationale into [DESIGN.md](DESIGN.md) (§8.1, §4 threat model).
Nothing here is normative yet; the current normative text (PROTOCOL §8.2:
*sanitized HTML subset*) stands until this supersedes it.

**Provenance:** discussion outcome. JSON Feed 1.1 already carries the **full
article body** (`content_html` / `content_text`; `summary` is the optional short
form, i.e. the perex) — so the open question was not *where* the body lives, but
**how expressive the authenticated body may be** and **what remains outside the
trust model**.

---

## 0. TL;DR (decisions)

| # | Decision |
|---|---|
| D1 | Articles are **full HTML/CSS**; **no JavaScript** (v1). |
| D2 | Articles render in an **isolated, scriptless container** (sandboxed iframe / WebView: no scripts, no forms, no iframes/embeds, no top-level navigation). CSS cannot escape the container. |
| D3 | **Forms and iframes/embeds stay excluded** (even though they are HTML, not JS) — this keeps "the channel never asks for a password/seed/code" a **structural** property, not a heuristic. |
| D4 | The app **MUST always display the confirmed origin** next to `company_name`/`logo` (contact card + message header). Name/logo are publisher-controlled decoration; the **origin is the identity anchor**. |
| D5 | Links stay intercepted and transparent (real destination domains shown, no auto-open); a "leaving the secure area" notice via a master-signed `custom.web_origins` allowlist is **proposed** (open question, not locked). |
| D6 | `content_text` fallback stays mandatory; media stays **referenced, not inlined** (accepted device-level telemetry at the company's CDN). |

---

## 1. What the authenticated body already is

- The feed document carries the whole article body — `content_html` (full
  content in HTML) and `content_text` (full content in plain text, always
  present as fallback). `summary` is optional and is the perex if a publisher
  chooses to use one (PROTOCOL §8.2).
- The whole document (wrapper + items) is covered by the TUF target hash pinned
  in the channel role metadata — nothing in the body is forgeable by a feed
  host (PROTOCOL §8.1).
- Per-item signatures are **load-bearing only in editor mode**; in default mode
  they are attribution-only (PROTOCOL §8.2).
- **Not covered by the trust model:** the bytes at `image`,
  `attachments[].url`, and any link target. The signed item vouches for the
  *URL*, not for the *bytes* behind it (§3).

## 2. Expressiveness: HTML/CSS, no JS

### Decision

v1 content is **full HTML/CSS within the sandbox** — visual expressiveness on
par with HTML email with embedded images, minus email's rendering chaos,
because the renderer is app-owned and deterministic.

### Why not JavaScript

- **Email's ceiling is HTML/CSS already** — every mail client strips JS; "full
  expressiveness" as in marketing email is CSS + images. JS would go *beyond*
  email, turning the feed into a web app.
- **Code-execution surface:** browser 0-days on content the app presents as
  *verified*; JS can redirect the webview anywhere, fake the URL bar
  (`history.pushState`), open popups, mine crypto, exfiltrate device telemetry
  — "transparent links / no auto-open" becomes unenforceable.
- **Credential capture becomes structural:** with JS, a fake credential form is
  rendered *inside* the verified chrome — no domain transition, no "leaving the
  secure area" notice ever fires. The footer promise "this channel will never
  ask you for a password, seed, or code" becomes false for interactive content.
- **Scripts/media become executable content:** the media-integrity gap (bytes
  behind signed URLs are unpinned) stops being "swapped image" and becomes
  "swapped script = code execution" — script pinning would become mandatory.
- **Feed size / offline-first:** inlined JS bloats feeds (soft cap ~250 KB);
  external JS breaks offline reading and adds fetch-on-render.

### Threat framing (correction)

The original objection ("a phish rendered inside the secure area") was an
**impersonation** argument — and Keryx structurally prevents impersonation: a
message can only appear under ACME's name if ACME's chain (confirmed origin +
pinned keys) said so. The user sees the rightful channel owner. The remaining
abuse vectors with HTML/CSS only:

| Vector | Status |
|---|---|
| Impersonation (fake sender) | **Prevented structurally** (origin + key binding; identity changes never silent, PROTOCOL §1) |
| Visual hijack of the app chrome (CSS overlays, `position: fixed`, z-index) | **Prevented by D2** (content cannot escape the container) |
| Social engineering by the rightful-but-compromised owner | **Residual risk** — exists with plain text and links too; HTML/CSS does not materially change it; documented as such (DESIGN §4) |
| Credential capture by the compromised owner | **Structural mitigation kept** (D3: no forms) + convention (never ask) |
| Media/attachment bytes behind signed URLs | **Open** — see §3 |

## 3. The content trust boundary

| What | Covered by | Guarantee |
|---|---|---|
| Article body (text/HTML/CSS in feed) | TUF target hash (+ item signatures in editor mode) | Authentic bytes |
| `image` / `attachments[].url` / CSS-loaded resources (`url()`, `@font-face`) | **Nothing** (media is NOT a TUF target; JSON Feed has no hash field; `size_in_bytes` is advisory) | Authentic *URL*, not bytes |
| Link targets | **Nothing** (by design: "as trustworthy as a link on the company's own website", DESIGN §3.1) | Web-origin trust only |

### Media integrity — open options

1. **Document as residual risk** — threat-model row + open question; operational
   guidance only (immutable media URLs, strict origin allowlist, sandboxed
   rendering).
2. **Content-addressed media URLs + client verification** *(recommended for
   v1)* — convention: media URL embeds its own hash
   (`/media/<sha256>/fw.jpg`); the app recomputes the hash of downloaded bytes
   and compares it to the URL before rendering. Since the URL is inside the
   signed feed, the hash becomes cryptographically bound. No wire-format or TUF
   change; requires immutable media per URL (good practice anyway).
3. **Pin media as TUF targets** *(full answer)* — media files hash-pinned in the
   channel role metadata (same machinery as `next_url` archives, PROTOCOL §5);
   fetched hash-verified via the TUF client. Strongest guarantee; heavier:
   role metadata grows, publish cadence tied to media changes, media becomes
   repo-versioned.

## 4. Threat model refinement (to add to DESIGN §4)

- Row: **compromised feed host / CDN** — unchanged (feed files are TUF
  targets; harm = availability).
- Row: **media-origin compromise** — images/PDFs at signed URLs can be swapped;
  harm = content display (not feed integrity). Mitigated per §3 option 2/3.
- Row: **compromised *rightful* owner** — can socially engineer via text/links;
  cannot execute code (D1) or capture input structurally (D3); revocation =
  signed metadata update. Framed as *owner compromise*, not *phishing*: the
  user always sees the rightful channel owner (D4).
- Row: **CSS visual hijack** — prevented by container isolation (D2).

## 5. App behavior (to add to DESIGN §8.1)

- Render each article's `content_html` in an **isolated, scriptless container**.
  The app chrome (company name + confirmed origin, footer reminder) is outside
  the container and **cannot be painted over by content**.
- Links: intercepted; real destination domains shown; no auto-open; optional
  "you are leaving \<company\> — external site" notice when the destination
  origin is not in `custom.web_origins` (if D5 is adopted).
- Media: honor remote-media privacy preferences (block / tap-to-load);
  per §3 option 2, verify content-addressed media hashes before rendering.
- Footer reminder "This channel will never ask you for a password, seed, or
  code" remains **structurally true** (D3) — not a heuristic.

## 6. Open items (before locking)

1. **Media integrity decision** — §3 options; recommendation: option 2 for v1,
   option 3 as follow-up; at minimum add the threat-model row.
2. **`custom.web_origins` + leaving-secure-area notice** — include in v1?
   Schema if yes: master-signed array of origins in `targets.json` `custom`
   (ignored by generic readers, TUF-preserved); app warns on links to origins
   outside the list; look-alike detection (typosquat of the confirmed origin)
   is a heuristic bonus, never crypto.
3. **Content profile capability** — explicitly deferred: v1 is `rich`
   (HTML/CSS). An `interactive` (JS) profile is revisited only with a
   hardened-sandbox spec + script pinning; likely never for the default.
4. **Exact CSS capabilities** — animations, web fonts (`@font-face` from
   company origin = extra fetches/telemetry), inline SVG (allowed? scriptless
   container neutralizes SVG scripts; external refs still fetch), tables;
   `<video>`/`<audio>` — keep as attachments, not embeds.
5. **Feed size** — full HTML/CSS bodies inflate feeds vs summary-only;
   re-check the ~250 KB soft cap and archive policy.
6. **Rendering determinism** — CSS is declarative, so no renderer pinning
   needed; note only (no action).

## 7. Targets once locked

- **PROTOCOL §8.2** — replace "sanitized HTML subset" with D1–D3, D5, D6 rules
  (sandbox contract, exclusions, link handling, origin display MUST,
  `content_text` fallback).
- **PROTOCOL §4** — if D5: `custom.web_origins` (master-signed allowlist).
- **DESIGN §8.1** — sandboxed rendering, origin display, link notice, media
  hash verification (per §3).
- **DESIGN §4** — threat-model rows (§4 of this document).
- **DESIGN §3.1 goal 3** — rephrase "as trustworthy as a link on the company's
  own website" into the precise statement: body = authenticated; media =
  URL-authentic only; links = web-origin trust.
