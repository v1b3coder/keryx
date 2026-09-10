package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"image/color"
	"os"
	"path/filepath"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

func main() {
	mode := flag.String("mode", "build", "build | verify")
	site := flag.String("site", "../demo", "output directory for the demonstration site")
	flag.Parse()

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

func buildAll(keys map[string]*keyPair, site string) error {
	fmt.Println("== building TUF repository ==")
	// stale artifacts from older layouts are removed so the repo is
	// self-contained and matches the current protocol exactly.
	for _, stale := range []string{".well-known", "beacon", "keryx"} {
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
	joinURL := metadataOrigin + "/join?p=" + base64.RawURLEncoding.EncodeToString(payloadJSON)
	if err := os.WriteFile(filepath.Join(site, "join.txt"), []byte(joinURL+"\n"), 0o644); err != nil {
		return err
	}
	if err := qrcode.WriteColorFile(joinURL, qrcode.Medium, 512, color.Black, color.White, filepath.Join(site, "join.png")); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(site, "join"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(site, "join", "index.html"), []byte(joinPage(payload, joinURL)), 0o644); err != nil {
		return err
	}
	fmt.Printf("  join URL: %s\n", joinURL)

	fmt.Println("== local permalink pages ==")
	if err := writeLocalPages(site, token); err != nil {
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

// joinPage is the PROTOCOL §2 fallback page served at /join (shown when
// the Keryx app is not installed). It renders the payload contents; per
// §2/§10 it sets Referrer-Policy: no-referrer (the capability token travels
// in ?p=) and includes no third-party resources.
func joinPage(payload map[string]any, joinURL string) string {
	pretty, _ := json.MarshalIndent(payload, "", "  ")
	channels := []string{}
	if cs, ok := payload["channels"].([]string); ok {
		channels = cs
	}
	feeds := []string{}
	if fs, ok := payload["private_feeds"].([]string); ok {
		feeds = fs
	}
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
<h2>Join payload</h2>
<pre style="background:#f4f4f4;padding:1rem;overflow-x:auto">`)
	b.WriteString(html.EscapeString(string(pretty)))
	b.WriteString(`</pre>
<h2>What this would subscribe you to</h2>
<ul>
<li>Suggested public channels: `)
	b.WriteString(html.EscapeString(strings.Join(channels, ", ")))
	b.WriteString(`</li>`)
	for _, f := range feeds {
		b.WriteString(`
<li>Private capability feed: <a href="`)
		b.WriteString(html.EscapeString(f))
		b.WriteString(`">`)
		b.WriteString(html.EscapeString(f))
		b.WriteString(`</a></li>`)
	}
	b.WriteString(`
</ul>
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
func writeLocalPages(site, token string) error {
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
	index := `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Keryx demo — Trezor</title></head>
<body style="font-family:sans-serif;max-width:720px;margin:2rem auto;padding:0 1rem">
<h1>Keryx protocol — local demo</h1>
<p>Demonstration of a signed company-to-user broadcast channel, served from this
directory. All URLs point at <code>http://localhost:8000</code>; this demo is not
published by Trezor.</p>
<ul>
<li><a href="join/">Pairing page (join/)</a> — <a href="join.png">QR code</a>, <a href="join.txt">join.txt</a></li>
<li><a href=".well-known/keryx/root.json">.well-known/keryx/root.json</a> (root anchor on the join origin; the ONLY root metadata source)</li>
<li><a href="keryx/targets.json">keryx/targets.json</a> (channel authorization + editor mode + private patterns)</li>
<li><a href="keryx/channels.security.json">keryx/channels.security.json</a> (channel role metadata pins channels/security/feed.json)</li>
<li><a href="keryx/channels/security/feed.json">channels/security/feed.json</a> (signed public feed)</li>
<li><a href="_sig/">_sig extension</a></li>
<li><a href="blog/">Announcements</a></li>
</ul>
</body></html>
`
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
