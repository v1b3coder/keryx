package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	mode := flag.String("mode", "build", "build | verify")
	site := flag.String("site", "../demo", "output directory for the demonstration site")
	base := flag.String("base", "http://10.110.147.178:8000", "public origin of the demo site (signed into every artifact URL)")
	flag.Parse()

	setBase(*base)

	keysDir := filepath.Join(*site, "keys")
	keys, err := loadOrCreateKeys(keysDir, keyNames)
	if err != nil {
		fatal(err)
	}

	switch *mode {
	case "build":
		if err := buildAll(keys, *site); err != nil {
			fatal(err)
		}
	case "verify":
		if err := verifyRepo(keys, *site); err != nil {
			fatal(err)
		}
		fmt.Printf("OK: metadata chain and all feed items verified against %s\n", *site)
		return
	default:
		fatal(fmt.Errorf("unknown mode %q", *mode))
	}
}

// setBase reconfigures every URL the generator signs into the demo
// artifacts (join URL, TUF repo_base, feed/item URLs, logo, tracking
// pattern, _sig identity). Must run before build/verify.
func setBase(base string) {
	base = strings.TrimSuffix(base, "/")
	metadataOrigin = base
	companyHome = base + "/"
	logoURL = base + "/media/logo.png"
	repoBase = base + "/keryx/"
	trackingPattern = base + "/channels/tracking/*/feed.json"
	sigAbout = base + "/_sig"
}

func buildAll(keys map[string]*keyPair, site string) error {
	fmt.Println("== building TUF repository ==")
	// stale artifacts from older layouts are removed so the repo is
	// self-contained and matches the current protocol exactly.
	for _, stale := range []string{".well-known", "beacon", "keryx", "join.png"} {
		if err := os.RemoveAll(filepath.Join(site, stale)); err != nil {
			return err
		}
	}

	fmt.Println("== signing public feeds (one per channel) ==")
	feeds := map[string][]byte{}
	for _, ch := range channels {
		feed, err := buildChannelFeed(keys, ch)
		if err != nil {
			return err
		}
		feedBytes, err := feedToBytes(feed)
		if err != nil {
			return err
		}
		feeds[ch.Name] = feedBytes
		path := filepath.Join(site, "channels", ch.Name, "feed.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, feedBytes, 0o644); err != nil {
			return err
		}
	}
	if err := buildRepo(keys, site, feeds); err != nil {
		return err
	}
	fmt.Printf("  master keyid: %s\n", keys["master"].KeyID)
	fmt.Printf("  ops keyid:    %s\n", keys["ops"].KeyID)

	fmt.Println("== signing private (capability) feed ==")
	trackingRoot := filepath.Join(site, "channels", "tracking")
	// regenerate: drop capability dirs from previous runs
	if err := os.RemoveAll(trackingRoot); err != nil {
		return err
	}
	token := randomToken()
	privateURL := metadataOrigin + "/channels/tracking/" + token + "/feed.json"
	privateDir := filepath.Join(trackingRoot, token)
	if err := os.MkdirAll(privateDir, 0o755); err != nil {
		return err
	}
	pfeed, err := buildPrivateFeed(keys, token, privateURL)
	if err != nil {
		return err
	}
	if err := writeFeed(pfeed, filepath.Join(privateDir, "feed.json")); err != nil {
		return err
	}

	fmt.Println("== media & join payload ==")
	if err := os.MkdirAll(filepath.Join(site, "media"), 0o755); err != nil {
		return err
	}
	if err := copyDir(filepath.Join("assets", "img"), filepath.Join(site, "media", "img")); err != nil {
		return err
	}
	logoSrc := filepath.Join("assets", "logo.png")
	logoDst := filepath.Join(site, "media", "logo.png")
	if data, err := os.ReadFile(logoSrc); err == nil {
		if err := os.WriteFile(logoDst, data, 0o644); err != nil {
			return err
		}
	} else {
		fmt.Printf("  note: %s not found, skipping logo copy (%v)\n", logoSrc, err)
	}

	// Join payload (PROTOCOL §2): NO metadata URL — the app derives the root
	// anchor from the join origin (/.well-known/keryx/root.json).
	payload := map[string]any{
		"v":             1,
		"channels":      []string{"security", "news", "insights"},
		"private_feeds": []string{privateURL},
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	joinQuery := "join?p=" + base64.RawURLEncoding.EncodeToString(payloadJSON)
	joinURL := metadataOrigin + "/" + joinQuery
	if err := os.WriteFile(filepath.Join(site, "join.txt"), []byte(joinURL+"\n"), 0o644); err != nil {
		return err
	}
	// no static QR: the join page renders one client-side from the URL payload
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
	if err := verifyRepo(keys, site); err != nil {
		return err
	}
	fmt.Printf("OK: demonstration site written to %s\n", site)
	return nil
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

// installSection lists the three client options on the demo pages. Only the
// PWA is live; Android/iOS Capacitor shells exist in keryx/app but have no
// store builds yet, so they are placeholders.
func installSection() string {
	return `<h2>Installation</h2>
<ul>
<li><a href="https://v1b3coder.github.io/keryx/">PWA</a> — installable web app (works now)</li>
<li>Android app — coming soon</li>
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

// joinPage is the PROTOCOL §2 fallback page served at /join (shown when
// the Keryx app is not installed). Per §2/§10 it sets Referrer-Policy:
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
<p>This is the fallback page for the Keryx join URL (PROTOCOL §2). In a real
deployment it is shown only when the Keryx app is not installed; with the
app installed, the URL above would be handed to it and pairing would
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
` + installSection() + `
<p><small>Demo only — software demo keys, origin `)
	b.WriteString(html.EscapeString(metadataOrigin))
	b.WriteString(`. Not published by Trezor.</small></p>
</body></html>
`)
	return b.String()
}

// writeLocalPages generates the local HTML pages so every URL in the signed
// artifacts resolves on the local server: one page per announcement, the
// private order page, the _sig extension page and an index.
func writeLocalPages(site, token, joinQuery string) error {
	blogDir := filepath.Join(site, "blog")
	if err := os.MkdirAll(blogDir, 0o755); err != nil {
		return err
	}
	for _, it := range publicItems {
		if err := os.WriteFile(filepath.Join(blogDir, it.Slug+".html"), []byte(articlePage(it)), 0o644); err != nil {
			return err
		}
	}
	privDir := filepath.Join(site, "channels", "tracking", token)
	if err := os.WriteFile(filepath.Join(privDir, privateItem.Slug+".html"), []byte(articlePage(privateItem)), 0o644); err != nil {
		return err
	}
	sigPage := `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Keryx _sig extension</title></head>
<body style="font-family:sans-serif;max-width:720px;margin:2rem auto;padding:0 1rem">
<h1>Keryx <code>_sig</code> extension</h1>
<p>This is the identity URL of the <code>_sig</code> JSON Feed extension used by the
Keryx protocol (see <code>README.md</code> in this repository). Generic JSON Feed
readers ignore the extension; the Keryx app enforces it.</p>
<h2>Items (PROTOCOL §8.2)</h2>
<ul>
<li><code>channel</code> — the delegated channel role the item belongs to (cross-checked against the feed path)</li>
<li><code>withdrawn</code> — signed retraction: the app hides the item entirely</li>
<li><code>signatures</code> — raw Ed25519 (base64url) over the JCS (RFC 8785) canonical bytes of the item with this field removed</li>
</ul>
<h2>Private capability feeds (PROTOCOL §10)</h2>
<p>The top-level <code>_sig</code> of a private feed additionally carries:</p>
<ul>
<li><code>channel</code> — MUST equal the authorized pattern entry's channel</li>
<li><code>url</code> — the canonical capability URL (MUST equal the fetched URL)</li>
<li><code>signatures</code> — Ed25519 over the whole document (top-level <code>_sig.signatures</code> removed)</li>
<li><code>version</code> — monotonic per feed (anti-rollback via client version memory)</li>
<li><code>expires</code> — the order window end (anti-freeze)</li>
</ul>
</body></html>
`
	if err := os.MkdirAll(filepath.Join(site, "_sig"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(site, "_sig", "index.html"), []byte(sigPage), 0o644); err != nil {
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
<li><a href="keryx/targets.json">keryx/targets.json</a> (channel authorization + editor mode + private patterns)</li>
<li><a href="keryx/channels.security.json">keryx/channels.security.json</a> (channel role metadata pins channels/security/feed.json)</li>
<li><a href="keryx/channels/security/feed.json">channels/security/feed.json</a> (signed public feed)</li>
<li><a href="_sig/">_sig extension</a></li>
<li><a href="blog/">Announcements</a></li>
</ul>
{{INSTALL}}
</body></html>
`, "{{ORIGIN}}", metadataOrigin, 1), "{{JOIN}}", joinQuery, 1), "{{INSTALL}}", installSection(), 1)
	if err := os.WriteFile(filepath.Join(site, "index.html"), []byte(index), 0o644); err != nil {
		return err
	}
	return nil
}

// articlePage renders a minimal local permalink page for one announcement.
func articlePage(it demoItem) string {
	lang := it.Language
	if lang == "" {
		lang = "en"
	}
	body := ""
	for _, para := range splitParagraphs(it.ContentText) {
		body += "<p>" + html.EscapeString(para) + "</p>\n"
	}
	source := ""
	if it.Source != "" {
		source = "<hr><p><small>Keryx demo. Original content (not a link): " + html.EscapeString(it.Source) + "</small></p>\n"
	}
	return fmt.Sprintf(`<!doctype html>
<html lang="%s"><head><meta charset="utf-8"><title>%s</title></head>
<body style="font-family:sans-serif;max-width:720px;margin:2rem auto;padding:0 1rem">
<h1>%s</h1>
<p><em>%s · %s · %s</em></p>
<img src="%s/media/img/%s" alt="" style="max-width:100%%;border-radius:8px">
%s%s
</body></html>
`, lang, html.EscapeString(it.Title), html.EscapeString(it.Title),
		it.Channel, it.Published, html.EscapeString(it.Author), metadataOrigin, it.Image, body, source)
}

func splitParagraphs(s string) []string {
	var out []string
	cur := ""
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		if cur != "" {
			cur += " "
		}
		cur += line
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// randomToken returns a 128-bit unguessable capability token (base64url,
// 22 chars — spec/feeds.md §3 token format).
func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
