package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/v1b3coder/keryx/sdk/feed"
	"github.com/v1b3coder/keryx/sdk/join"
	"github.com/v1b3coder/keryx/sdk/keys"
	"github.com/v1b3coder/keryx/sdk/publisher"
	"github.com/v1b3coder/keryx/sdk/repo"
)

func main() {
	mode := flag.String("mode", "build", "build | verify")
	site := flag.String("site", "../demo", "output directory for the demonstration site")
	keysDir := flag.String("keys", ".keys", "key store directory (outside the published site)")
	base := flag.String("base", "http://localhost:8000", "public origin of the demo site (signed into every artifact URL)")
	flag.Parse()

	setBase(*base)

	switch *mode {
	case "build":
		if err := buildAll(*site, *keysDir); err != nil {
			fatal(err)
		}
	case "verify":
		if err := verifyAll(*site, *keysDir); err != nil {
			fatal(err)
		}
		fmt.Printf("OK: metadata chain and all items verified against %s\n", *site)
	default:
		fatal(fmt.Errorf("unknown mode %q", *mode))
	}
}

// setBase reconfigures every URL the generator signs into the demo
// artifacts (join URL, TUF repo_base, item/media URLs, logo, tracking
// pattern). Must run before build/verify.
func setBase(base string) {
	base = strings.TrimSuffix(base, "/")
	metadataOrigin = base
	companyHome = base + "/"
	logoURL = base + "/media/logo.png"
	repoBase = base + "/keryx/"
	trackingPattern = base + "/channels/tracking/*/feed.json"
}

// newPublisher opens the SDK workspace: the key store lives outside the
// published site, the repo base at keryx/ and the root anchor at
// .well-known/keryx/ inside it.
func newPublisher(site, keysDir string) *publisher.Publisher {
	ks := keys.NewDirStore(keysDir, "")
	pub := publisher.New(
		repo.NewDirRepo(filepath.Join(site, "keryx")),
		repo.NewDirRepo(filepath.Join(site, ".well-known", "keryx")),
		ks,
	)
	pub.GenerateKeys = true // the single-step demo machine mints every key
	return pub
}

func ensureKey(ctx context.Context, ks *keys.DirStore, role keys.Role, name string) (*keys.Key, error) {
	k, err := ks.Find(ctx, role, name)
	if err == nil {
		return k, nil
	}
	if !keys.IsNotFound(err) {
		return nil, err
	}
	k, err = keys.Generate(role, name)
	if err != nil {
		return nil, err
	}
	if err := ks.Add(ctx, k); err != nil {
		return nil, err
	}
	fmt.Printf("generated new demo key: %s (%s)\n", name, role)
	return k, nil
}

// roleFor maps a demo key name to its publisher role.
func roleFor(name string) keys.Role {
	switch name {
	case "master":
		return keys.RoleMaster
	case "ops":
		return keys.RoleOps
	case "tracking":
		return keys.RoleEngine
	case "author-security-a", "author-security-b":
		return keys.RoleAuthor
	default:
		return keys.RoleChannel
	}
}

func buildAll(site, keysDir string) error {
	ctx := context.Background()
	fmt.Println("== building TUF repository with the publisher SDK ==")
	// stale artifacts from older layouts are removed so the repo is
	// self-contained and matches the current protocol exactly.
	for _, stale := range []string{".well-known", "beacon", "keryx", "join.png", "_sig", "channels", "blog", "keys"} {
		if err := os.RemoveAll(filepath.Join(site, stale)); err != nil {
			return err
		}
	}
	pub := newPublisher(site, keysDir)
	pub.AllowLocalHTTP = true

	// The logo is linked (with the required logo_sha256) on an HTTPS origin —
	// the spec's linked-logo path — and embedded as an inline data URL on the
	// local HTTP demo, where a linked logo is not allowed (spec/repository.md §2).
	logo, logoSHA := "", ""
	if strings.HasPrefix(metadataOrigin, "https://") {
		logo, logoSHA = logoURL, logoSHA256()
	} else {
		data, err := os.ReadFile(filepath.Join("assets", "logo.png"))
		if err != nil {
			return fmt.Errorf("read assets/logo.png: %w", err)
		}
		logo = "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	}
	if _, err := pub.Init(ctx, publisher.InitParams{
		RepoBase: repoBase, CompanyName: companyName, Logo: logo, LogoSHA256: logoSHA,
	}); err != nil {
		return err
	}

	fmt.Println("== channels ==")
	authorIDs := make([]string, 0, len(authorChannels["security"].KeyNames))
	for _, name := range authorChannels["security"].KeyNames {
		k, err := ensureKey(ctx, pub.Keys.(*keys.DirStore), keys.RoleAuthor, name)
		if err != nil {
			return err
		}
		authorIDs = append(authorIDs, k.KeyID())
	}
	if _, err := pub.ChannelAdd(ctx, publisher.ChannelSpec{
		Name: "security", DisplayName: channels[0].DisplayName,
		Description: channels[0].Description, Threshold: 2, Authors: authorIDs,
	}); err != nil {
		return err
	}
	for _, ch := range channels {
		if ch.Name == "security" {
			continue
		}
		if _, err := pub.ChannelAdd(ctx, publisher.ChannelSpec{
			Name: ch.Name, DisplayName: ch.DisplayName, Description: ch.Description, Simple: true,
		}); err != nil {
			return err
		}
	}

	fmt.Println("== private-feed pattern ==")
	engine, err := ensureKey(ctx, pub.Keys.(*keys.DirStore), keys.RoleEngine, "tracking")
	if err != nil {
		return err
	}
	if _, err := pub.PatternAdd(ctx, publisher.PatternSpec{
		Channel: trackingPatternEntry.Channel, Pattern: trackingPattern,
		KeyID: engine.KeyID(), DisplayName: trackingPatternEntry.DisplayName,
		Purpose: trackingPatternEntry.Purpose,
	}); err != nil {
		return err
	}

	fmt.Println("== signing public items (one TUF target per item) ==")
	ks := pub.Keys.(*keys.DirStore)
	for _, it := range publicItems {
		item := itemToMap(it)
		signers := []string{it.Channel}
		if ac, ok := authorChannels[it.Channel]; ok {
			signers = ac.KeyNames
		}
		for _, name := range signers {
			k, err := ensureKey(ctx, ks, roleFor(name), name)
			if err != nil {
				return err
			}
			if err := feed.SignItem(item, k); err != nil {
				return err
			}
		}
		if _, err := pub.Publish(ctx, publisher.PublishParams{Channel: it.Channel, Item: item}); err != nil {
			return err
		}
		fmt.Printf("  %s: %s\n", it.Channel, it.ID)
	}

	fmt.Println("== media & join payload ==")
	if err := os.MkdirAll(filepath.Join(site, "media"), 0o755); err != nil {
		return err
	}
	if err := copyDir(filepath.Join("assets", "img"), filepath.Join(site, "media", "img")); err != nil {
		return err
	}
	if data, err := os.ReadFile(filepath.Join("assets", "logo.png")); err == nil {
		if err := os.WriteFile(filepath.Join(site, "media", "logo.png"), data, 0o644); err != nil {
			return err
		}
	} else {
		fmt.Printf("  note: assets/logo.png not found, skipping logo copy (%v)\n", err)
	}

	fmt.Println("== signing private (capability) feed ==")
	token, err := feed.NewToken()
	if err != nil {
		return err
	}
	privateURL := metadataOrigin + "/channels/tracking/" + token + "/feed.json"
	attach := "Order summary for Trezor order #2026-0841.\n\nCarrier: DHL Express\nTracking number: DHL-8491-2203-77\nEstimated delivery: 3-5 business days\n\nThis is demo data only - not an invoice.\n"
	attachPath := filepath.Join(site, "media", "order-2026-0841.txt")
	if err := os.MkdirAll(filepath.Dir(attachPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(attachPath, []byte(attach), 0o644); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(attach))
	attachment := privateItem.Attachments[0]
	attachment["url"] = metadataOrigin + "/media/order-2026-0841.txt"
	attachment["size_in_bytes"] = int64(len(attach))
	attachment["sha256"] = hex.EncodeToString(sum[:])
	doc, err := pub.BuildPrivateDocument(ctx, engine.KeyID(), privateURL, "tracking",
		[]map[string]any{itemToMap(privateItem)}, time.Now().UTC().Add(45*24*time.Hour).Truncate(time.Second))
	if err != nil {
		return err
	}
	if err := writeJSON(doc, filepath.Join(site, "channels", "tracking", token, "feed.json")); err != nil {
		return err
	}

	// Join payload (spec/core.md §3): NO metadata URL — the app derives the
	// root anchor from the join origin (/.well-known/keryx/root.json).
	payload, err := join.BuildPayloadOptions([]string{"security", "news", "insights"}, []string{privateURL}, true)
	if err != nil {
		return err
	}
	joinURL, err := join.JoinURLOptions(metadataOrigin, payload, true)
	if err != nil {
		return err
	}
	joinQuery := strings.TrimPrefix(joinURL, metadataOrigin+"/")
	if err := os.WriteFile(filepath.Join(site, "join.txt"), []byte(joinURL+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(site, "join"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(site, "join", "index.html"), []byte(joinPage(joinURL)), 0o644); err != nil {
		return err
	}
	fmt.Printf("  join URL: %s\n", joinURL)

	fmt.Println("== local permalink pages ==")
	if err := writeLocalPages(site, token, joinQuery); err != nil {
		return err
	}

	fmt.Println("== verifying everything ==")
	if _, err := pub.Validate(ctx); err != nil {
		return err
	}
	fmt.Printf("OK: demonstration site written to %s\n", site)
	return nil
}

// verifyAll re-loads the site and runs the SDK's full repository check.
func verifyAll(site, keysDir string) error {
	_, err := newPublisher(site, keysDir).Validate(context.Background())
	return err
}

// writeJSON writes obj (an item or a private-feed document) deterministically.
func writeJSON(obj map[string]any, path string) error {
	data, err := feed.Encode(obj)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// copyDir copies every file from src to dst (creating dst).
func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// qrLib is the vendored QR code generator (MIT, kazuhikoarase/qrcode-generator,
// qrcode-generator@1.4.4 minified). It is inlined into the join page so the
// page stays fully self-contained: no third-party request, works offline.
//
//go:embed assets/qrcode.min.js
var qrLib string

// installSection lists the three client options on the demo pages. The PWA
// and the Android APK (GitHub Releases) are live; the iOS Capacitor shell
// exists in keryx/app but has no store build yet, so it stays a placeholder.
// The PWA link carries ?domain=<origin> (the join page appends &p=
// client-side) — an out-of-spec PWA deep link that starts pairing without
// the input screen.
func installSection(origin string) string {
	pwa := "https://v1b3coder.github.io/keryx/?domain=" + url.QueryEscape(origin)
	return `<h2>Installation</h2>
<ul>
<li><a id="pwa-link" href="` + pwa + `">PWA</a> — installable web app (works now)</li>
<li><a href="https://github.com/v1b3coder/keryx/releases">Android app</a> — debug APK in GitHub Releases</li>
<li>iOS app — coming soon</li>
</ul>
`
}

// jsonString renders s as a JavaScript/JSON string literal.
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// joinPage is the spec/core.md §3 fallback page served at /join (shown when
// the Keryx app is not installed). Per §3 it sets Referrer-Policy:
// no-referrer (the capability token travels in ?p=) and includes no
// third-party resources. Everything payload-specific is derived at runtime
// from the page's own URL: with ?p= the payload is decoded and rendered
// (channels, private feeds); without one the page shows generic join
// content only — no channels, no private capability feed.
func joinPage(demoJoinURL string) string {
	b := &strings.Builder{}
	b.WriteString(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Keryx — subscribe (demo)</title>
<meta name="referrer" content="no-referrer"></head>
<body style="font-family:sans-serif;max-width:720px;margin:2rem auto;padding:0 1rem">
<h1>Keryx — subscribe to Trezor announcements</h1>
<p>This is the fallback page for the Keryx join URL (spec/core.md §3). In a
real deployment it is shown only when the Keryx app is not installed; with
the app installed, the URL above would be handed to it and pairing would
continue there. The root anchor is derived from this page's origin
(<code>/.well-known/keryx/root.json</code>) — there is no metadata URL in
the payload.</p>
<div id="join-qr-section">
<h2>Join QR code</h2>
<p>Scan this code with the Keryx app to pair and subscribe. It is generated
from this page's own URL, so it always matches the join link you opened.</p>
<div id="join-qr"></div>
</div>
<div id="join-content"></div>
<script>
`)
	b.WriteString(qrLib)
	b.WriteString(`
var DEMO_JOIN = ` + jsonString(demoJoinURL) + `;
(function () {
  var url = window.location.href;
  var qr = qrcode(0, 'M');
  qr.addData(url);
  qr.make();
  var n = qr.getModuleCount(), s = 8;
  var c = document.createElement('canvas');
  c.width = c.height = n * s;
  c.style.maxWidth = '100%';
  c.style.height = 'auto';
  var ctx = c.getContext('2d');
  ctx.fillStyle = '#fff';
  ctx.fillRect(0, 0, c.width, c.height);
  ctx.fillStyle = '#000';
  for (var r = 0; r < n; r++)
    for (var col = 0; col < n; col++)
      if (qr.isDark(r, col)) ctx.fillRect(col * s, r * s, s, s);
  document.getElementById('join-qr').appendChild(c);
})();
(function () {
  function el(tag, text) {
    var e = document.createElement(tag);
    if (text !== undefined) e.textContent = text;
    return e;
  }
  var root = document.getElementById('join-content');
  function renderPayload(p) {
    root.appendChild(el('h2', 'Join payload'));
    var pre = el('pre');
    pre.style.background = '#f4f4f4';
    pre.style.padding = '1rem';
    pre.style.overflowX = 'auto';
    pre.textContent = JSON.stringify(p, null, 2);
    root.appendChild(pre);
    root.appendChild(el('h2', 'What this would subscribe you to'));
    var ul = el('ul');
    if (Array.isArray(p.channels) && p.channels.length) {
      ul.appendChild(el('li', 'Suggested public channels: ' + p.channels.join(', ')));
    }
    (Array.isArray(p.private_feeds) ? p.private_feeds : []).forEach(function (f) {
      var li = el('li');
      li.appendChild(document.createTextNode('Private capability feed: '));
      var a = el('a', f);
      a.href = f;
      li.appendChild(a);
      ul.appendChild(li);
    });
    root.appendChild(ul);
  }
  var m = window.location.search.match(/[?&]p=([A-Za-z0-9_-]+)/);
  if (m) {
    // pass the join payload through to the PWA deep link (same p bytes).
    // The inline script runs before the <a id=pwa-link> is parsed, so wait
    // for DOMContentLoaded before touching it.
    function passPayload() {
      var pl = document.getElementById('pwa-link');
      if (pl) pl.href += '&p=' + m[1];
    }
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', passPayload);
    else passPayload();
  }
  if (!m) {
    root.appendChild(el('h2', 'Generic join link'));
    root.appendChild(el('p', 'This link carries no join payload: no suggested channels and no private capability feed. It still pairs — the app will show the publisher\u2019s public channels to choose from. The demo join link below also suggests channels:'));
    var a = el('a', DEMO_JOIN);
    a.href = DEMO_JOIN;
    root.appendChild(a);
    return;
  }
  try {
    var b64 = m[1].replace(/-/g, '+').replace(/_/g, '/');
    while (b64.length % 4) b64 += '=';
    renderPayload(JSON.parse(atob(b64)));
  } catch (e) {
    root.appendChild(el('p', 'This join link is damaged — ask the company for a new one.'));
  }
})();
</script>
` + installSection(metadataOrigin) + `
<p><small>Demo only — software demo keys, origin `)
	b.WriteString(html.EscapeString(metadataOrigin))
	b.WriteString(`. Not published by Trezor.</small></p>
</body></html>
`)
	return b.String()
}

// writeLocalPages generates the local HTML pages so every URL in the signed
// artifacts resolves on the local server: one page per announcement, the
// private order page and an index.
func writeLocalPages(site, token, joinQuery string) error {
	blogDir := filepath.Join(site, "blog")
	if err := os.MkdirAll(blogDir, 0o755); err != nil {
		return err
	}
	var blogItems []string
	for _, it := range publicItems {
		if err := os.WriteFile(filepath.Join(blogDir, it.Slug+".html"), []byte(articlePage(it)), 0o644); err != nil {
			return err
		}
		blogItems = append(blogItems, "<li><a href=\""+it.Slug+".html\">"+html.EscapeString(it.Title)+"</a> <small>("+
			html.EscapeString(it.Channel)+", "+html.EscapeString(it.Published)+")</small></li>")
	}
	blogIndex := `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Keryx demo — announcements</title></head>
<body style="font-family:sans-serif;max-width:720px;margin:2rem auto;padding:0 1rem">
<h1>Announcements (human-readable copies)</h1>
<p>These pages are demo chrome only — the signed artifacts are the JSON item
files in <code>keryx/channels/&lt;channel&gt;/&lt;id&gt;.json</code>.</p>
<ul>` + strings.Join(blogItems, "\n") + `
</ul>
</body></html>
`
	if err := os.WriteFile(filepath.Join(blogDir, "index.html"), []byte(blogIndex), 0o644); err != nil {
		return err
	}
	privDir := filepath.Join(site, "channels", "tracking", token)
	if err := os.WriteFile(filepath.Join(privDir, privateItem.Slug+".html"), []byte(articlePage(privateItem)), 0o644); err != nil {
		return err
	}
	index := strings.Replace(strings.Replace(strings.Replace(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Keryx demo — Trezor</title></head>
<body style="font-family:sans-serif;max-width:720px;margin:2rem auto;padding:0 1rem">
<h1>Keryx protocol — local demo</h1>
<p>Demonstration of a signed company-to-user broadcast channel, served from this
directory. All URLs point at <code>{{ORIGIN}}</code>; this demo is not
published by Trezor.</p>
<ul>
<li><a href="join/">Generic join link</a> — no payload, no suggested channels
or private feeds (pairs with the public channels; QR rendered on the page)</li>
<li><a href="{{JOIN}}">Demo join link</a> — with suggested public channels and
a private capability feed (QR code rendered on the page; the URL is also in
<a href="join.txt">join.txt</a>)</li>
<li><a href=".well-known/keryx/root.json">.well-known/keryx/root.json</a> (root anchor on the join origin; the ONLY root metadata source)</li>
<li><a href="keryx/targets.json">keryx/targets.json</a> (channel + authors role authorization, company identity, private-feed patterns)</li>
<li><a href="keryx/channels.security.json">keryx/channels.security.json</a> (channel role metadata pins the security items)</li>
<li><a href="keryx/channels.security.authors.json">keryx/channels.security.authors.json</a> (authors role metadata — authorizes item signing)</li>
<li><a href="keryx/channels/security/msg-2026-03-18-phishing-attacks.json">keryx/channels/security/msg-2026-03-18-phishing-attacks.json</a> (one item = one TUF target)</li>
<li><a href="blog/">Announcements (human-readable copies)</a></li>
</ul>
{{INSTALL}}
</body></html>
`, "{{ORIGIN}}", metadataOrigin, 1), "{{JOIN}}", joinQuery, 1), "{{INSTALL}}", installSection(metadataOrigin), 1)
	return os.WriteFile(filepath.Join(site, "index.html"), []byte(index), 0o644)
}

// articlePage renders a minimal local permalink page for one announcement.
func articlePage(it demoItem) string {
	lang := it.Language
	if lang == "" {
		lang = "en"
	}
	source := ""
	if it.Source != "" {
		source = "<hr><p><small>Keryx demo. Original content (not a link): " + html.EscapeString(it.Source) + "</small></p>\n"
	}
	return fmt.Sprintf(`<!doctype html>
<html lang="%s"><head><meta charset="utf-8"><title>%s</title></head>
<body style="font-family:sans-serif;max-width:720px;margin:2rem auto;padding:0 1rem">
<h1>%s</h1>
<p><em>%s · %s</em></p>
<img src="%s/media/img/%s" alt="" style="max-width:100%%;border-radius:8px">
%s%s
</body></html>
`, lang, html.EscapeString(it.Title), html.EscapeString(it.Title),
		it.Channel, it.Published, metadataOrigin, it.Image, it.ContentHTML, source)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
