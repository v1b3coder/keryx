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

**Provenance:** discussion outcome. The protocol no longer uses a JSON Feed
document for public channels — each item is its own signed TUF target
(`channels/<channel>/<id>.json`), with `content_html` carrying the full
article body; `content_text` and `summary` are gone (D6). The open question
was **how expressive the authenticated body may be** and **what remains outside
the trust model** — settled here; the normative rules moved into
[spec/feeds.md](../spec/feeds.md) (§1) and the design rationale into
[design/why.md](../design/why.md) (§4.3).

---

## 0. TL;DR (decisions)

| # | Decision |
|---|---|
| D1 | Articles are **full HTML/CSS**; **no JavaScript** (v1). |
| D2 | Articles render in an **isolated, scriptless container** (sandboxed iframe / WebView: no scripts, no forms, no iframes/embeds, no top-level navigation). CSS cannot escape the container. |
| D3 | **Forms and iframes/embeds stay excluded** (even though they are HTML, not JS) — this keeps "the channel never asks for a password/seed/code" a **structural** property, not a heuristic. |
| D4 | The app **MUST always display the confirmed origin** next to `company_name`/`logo` (contact card + message header). Name/logo are publisher-controlled decoration; the **origin is the identity anchor**. |
| D5 | Links stay intercepted and transparent (real destination domains shown, no auto-open); a "leaving the secure area" notice via a master-signed `custom.web_origins` allowlist is **proposed** (open question, not locked). |
| D6 | No `content_text` fallback — the item is self-contained HTML; inline media (data URLs) means the feed renders from the repo alone; external attachments are the only fetch-on-demand resources (with optional per-attachment hashes). |

---

## 1. What the authenticated body already is

- Each item file carries the whole article body — `content_html` (full
  content in HTML), `title`, inline `image` (data URL), `date_*`, `tags`,
  `language`, `attachments`, `sig`
  ([spec/feeds.md §1](../spec/feeds.md)). There is no feed document, no
  `summary`, no `content_text` (D6), and no item `url` — the message is
  self-contained; "read more" is part of the content.
- The item file is covered by the TUF target hash pinned in the channel role
  metadata — nothing in the body is forgeable by a host
  ([spec/repository.md §3](../spec/repository.md)).
- Item signatures are **always load-bearing**: by the channel role keys, or by
  the channel's authors role when one exists
  ([spec/feeds.md §1.2](../spec/feeds.md)).
- **Not covered by the trust model:** the bytes at un-hashed `attachments[].url`
  and any link target inside the content. An attachment with a `sha256` is
  covered; a link is not. The signed item vouches for the
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
| Article body (title/content HTML/inline image in the item file) | TUF target hash + item signature | Authentic bytes |
| `attachments[].url` with `sha256` | The item's signed `sha256` | Authentic bytes |
| `attachments[].url` without `sha256` / CSS-loaded external resources (`url()`, `@font-face`) | **Nothing** | Authentic *URL*, not bytes |
| Link targets | **Nothing** (by design: "as trustworthy as a link on the company's own website") | Web-origin trust only |

### Media integrity

Settled for v1:

1. **Inline media (data URLs)** — covered by the item's TUF hash; the feed
   renders from the repo alone. No external fetch, no telemetry, no hash
   bookkeeping.
2. **Attachments with `sha256`** *(RECOMMENDED for static downloads)* — the
   hash lives inside the signed attachment object; the app verifies before
   render/save; mismatch → resource unavailable, item unaffected. No
   wire-format change; requires immutable media per URL.
3. **Attachments without `sha256`** — ordinary web links (dynamic landing
   pages); accepted as residual risk, documented as such.
4. **Media as TUF targets** — rejected for v1 (role metadata growth, publish
   cadence tied to media changes); not needed since inline media is
   self-covered.

## 4. Threat model refinement (to add to DESIGN §4)

- Row: **compromised feed host / CDN** — unchanged (item files are TUF
  targets; harm = availability).
- Row: **media-origin compromise** — un-hashed attachment URLs can be swapped;
  harm = content display (not feed integrity). Mitigated per §3 (attachment
  `sha256`).
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
- Media: inline media renders from the item (data URLs); attachments honor
  remote-media privacy preferences (block / tap-to-load) and are
  hash-verified before render when `sha256` is present.
- Footer reminder "This channel will never ask you for a password, seed, or
  code" remains **structurally true** (D3) — not a heuristic.

## 6. Open items (before locking)

1. **Media integrity** — settled per §3 (inline = covered; attachment
   `sha256` = recommended; no TUF media targets in v1).
2. **`custom.web_origins` + leaving-secure-area notice** — include in v1?
   Schema if yes: master-signed array of origins in `targets.json` `custom`
   (TUF-preserved); app warns on links to origins
   outside the list; look-alike detection (typosquat of the confirmed origin)
   is a heuristic bonus, never crypto.
3. **Content profile capability** — explicitly deferred: v1 is `rich`
   (HTML/CSS). An `interactive` (JS) profile is revisited only with a
   hardened-sandbox spec + script pinning; likely never for the default.
4. **Exact CSS capabilities** — animations, web fonts (`@font-face` from
   company origin = extra fetches/telemetry), inline SVG (allowed? scriptless
   container neutralizes SVG scripts; external refs still fetch), tables;
   `<video>`/`<audio>` — keep as attachments, not embeds.
5. **Item size** — full HTML/CSS + inline media inflate item files vs
   summary-only; app policy cap (1 MB) replaces the old ~250 KB feed cap;
   per-item diffing means this is per-file, not per-channel.
6. **Rendering determinism** — CSS is declarative, so no renderer pinning
   needed; note only (no action).

## 7. Targets once locked

- **[spec/feeds.md §1](../spec/feeds.md)** — the item format now carries D1–D3,
  D5, D6 rules (sandbox contract, exclusions, link handling, origin display
  MUST, no `content_text`).
- **[spec/repository.md §2](../spec/repository.md)** — if D5: `custom.web_origins`
  (master-signed allowlist).
- **[design/products.md §1](../design/products.md)** — sandboxed rendering,
  origin display, link notice, attachment hash verification (per §3).
- **[design/threats.md](../design/threats.md)** — threat-model rows (§4 of
  this document).
- **[design/why.md §3.1 goal 3](../design/why.md)** — rephrase "as trustworthy
  as a link on the company's
  own website" into the precise statement: body = authenticated; media =
  URL-authentic only; links = web-origin trust.
