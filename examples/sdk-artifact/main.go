// Command demo generates a complete demonstration artifact with the SDK:
// anchor + repo + keys + join URL + QR + private capability feed. It is the
// SDK counterpart of demo-tool and the artifact the app tests run against.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/v1b3coder/keryx/sdk/config"
	"github.com/v1b3coder/keryx/sdk/feed"
	"github.com/v1b3coder/keryx/sdk/join"
	"github.com/v1b3coder/keryx/sdk/keys"
	"github.com/v1b3coder/keryx/sdk/publisher"
	"github.com/v1b3coder/keryx/sdk/repo"
)

// 1x1 transparent PNG (the demo items carry inline, self-contained media).
const pngDataURL = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="

func main() {
	out := flag.String("out", "../demo-sdk", "output directory")
	base := flag.String("base", "http://localhost:8000", "public origin")
	keysDir := flag.String("keys", "", "key store directory (default <out>.keys, outside the artifact)")
	flag.Parse()

	if err := run(*out, *base, *keysDir); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(out, base, keysDir string) error {
	ctx := context.Background()
	now := func() time.Time { return time.Now().UTC().Truncate(time.Second) }
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	cfg := config.Default(out)
	cfg.RepoBase = base + "/keryx"
	if keysDir == "" {
		keysDir = out + ".keys"
	}
	cfg.Keystore = keysDir
	if err := cfg.Save(); err != nil {
		return err
	}
	repoDir := filepath.Join(out, "keryx")
	anchorDir := filepath.Join(out, ".well-known", "keryx")
	ks := keys.NewDirStore(cfg.KeysDir(), "")
	pub := publisher.New(repo.NewDirRepo(repoDir), repo.NewDirRepo(anchorDir), ks)
	pub.Now = now
	pub.GenerateKeys = true // single-step showcase: this machine mints every key

	if _, err := pub.Init(ctx, publisher.InitParams{
		RepoBase: cfg.RepoBase, CompanyName: "Trezor (demo)",
		Logo: pngDataURL,
	}); err != nil {
		return err
	}
	// author keys for the security channel (2-of-2), engine key for tracking
	authors := make([]*keys.Key, 0, 2)
	for _, name := range []string{"author-security-a", "author-security-b"} {
		k, err := ks.Find(ctx, keys.RoleAuthor, name)
		if err != nil {
			k, err = keys.Generate(keys.RoleAuthor, name)
			if err != nil {
				return err
			}
			if err := ks.Add(ctx, k); err != nil {
				return err
			}
		}
		authors = append(authors, k)
	}
	engine, err := ks.Find(ctx, keys.RoleEngine, "tracking")
	if err != nil {
		engine, err = keys.Generate(keys.RoleEngine, "tracking")
		if err != nil {
			return err
		}
		if err := ks.Add(ctx, engine); err != nil {
			return err
		}
	}
	if _, err := pub.PatternAdd(ctx, publisher.PatternSpec{
		Channel: "tracking", Pattern: base + "/channels/tracking/*/feed.json",
		KeyID: engine.KeyID(), DisplayName: "Package tracking",
		Purpose: "per-order delivery notifications",
	}); err != nil {
		return err
	}
	for _, name := range []string{"news", "insights"} {
		if _, err := ks.Find(ctx, keys.RoleChannel, name); err != nil {
			k, err := keys.Generate(keys.RoleChannel, name)
			if err != nil {
				return err
			}
			if err := ks.Add(ctx, k); err != nil {
				return err
			}
		}
	}
	if _, err := pub.ChannelAdd(ctx, publisher.ChannelSpec{
		Name: "security", DisplayName: "Security updates",
		Description: "Security advisories and fixes",
		Threshold:   2,
		Authors:     []string{authors[0].KeyID(), authors[1].KeyID()},
	}); err != nil {
		return err
	}
	if _, err := pub.ChannelAdd(ctx, publisher.ChannelSpec{
		Name: "news", DisplayName: "News", Description: "Product news", Simple: true,
	}); err != nil {
		return err
	}
	if _, err := pub.ChannelAdd(ctx, publisher.ChannelSpec{
		Name: "insights", DisplayName: "Insights", Description: "Long reads", Simple: true,
	}); err != nil {
		return err
	}

	type content struct {
		channel string
		id      string
		title   string
		body    string
		date    string
		tags    []string
	}
	items := []content{
		{"security", "msg-2026-03-18-phishing-attacks", "Phishing attacks are on the rise",
			"<p>We have seen a rise in phishing attempts. Never share your recovery seed.</p><img src=\"" + pngDataURL + "\">",
			"2026-03-18T10:00:00Z", []string{"security", "phishing"}},
		{"security", "msg-2026-08-05-coldcard", "Coldcard firmware advisory",
			"<p>Update your Coldcard firmware to the latest release.</p><img src=\"" + pngDataURL + "\">",
			"2026-08-05T09:00:00Z", []string{"firmware"}},
		{"news", "msg-2026-06-30-suite-sync", "Trezor Suite sync is here",
			"<p>Suite sync keeps your labels across devices.</p><img src=\"" + pngDataURL + "\">",
			"2026-06-30T08:00:00Z", []string{"suite"}},
		{"news", "msg-2026-05-28-stablecoin-yield", "Stablecoin yield, explained",
			"<p>What backs a stablecoin yield, and what does not.</p><img src=\"" + pngDataURL + "\">",
			"2026-05-28T08:00:00Z", []string{"stablecoins"}},
		{"insights", "msg-2026-03-17-own-your-coins", "Own your coins",
			"<p>Self-custody is a practice, not a product.</p><img src=\"" + pngDataURL + "\">",
			"2026-03-17T08:00:00Z", []string{"self-custody"}},
	}
	for _, it := range items {
		item := map[string]any{
			"id": it.id, "title": it.title, "content_html": it.body,
			"date_published": it.date, "tags": it.tags, "language": "en",
		}
		signers := []*keys.Key{}
		if it.channel == "security" {
			signers = authors
		} else {
			k, err := ks.Find(ctx, keys.RoleChannel, it.channel)
			if err != nil {
				return err
			}
			signers = []*keys.Key{k}
		}
		for _, k := range signers {
			if err := feed.SignItem(item, k); err != nil {
				return err
			}
		}
		if _, err := pub.Publish(ctx, publisher.PublishParams{Channel: it.channel, Item: item}); err != nil {
			return err
		}
	}

	token, err := feed.NewToken()
	if err != nil {
		return err
	}
	privateURL := base + "/channels/tracking/" + token + "/feed.json"
	privateItem := map[string]any{
		"id": "order-2026-0841", "title": "Order #2026-0841",
		"content_html": "<p>Carrier: DHL Express<br>Tracking number: DHL-8491-2203-77</p>",
		"date_published": "2026-03-18T10:00:00Z",
	}
	doc, err := pub.BuildPrivateDocument(ctx, engine.KeyID(), privateURL, "tracking",
		[]map[string]any{privateItem}, time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(out, "channels", "tracking", token, "feed.json"), doc); err != nil {
		return err
	}

	payload, err := join.BuildPayloadOptions([]string{"security", "news", "insights"}, []string{privateURL}, true)
	if err != nil {
		return err
	}
	joinURL, err := join.JoinURLOptions(base, payload, true)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "join.txt"), []byte(joinURL+"\n"), 0o644); err != nil {
		return err
	}
	png, err := join.QR(joinURL, 512)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "qr.png"), png, 0o644); err != nil {
		return err
	}
	if _, err := pub.Validate(ctx); err != nil {
		return err
	}
	fmt.Println(joinURL)
	return nil
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := feed.Encode(v.(map[string]any))
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

