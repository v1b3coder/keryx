package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gowebpki/jcs"
)

// feedDoc is a JSON Feed 1.1 document with the Keryx `_sig` extension.
// Public feeds carry `_sig.about` only (PROTOCOL §8.1); private capability
// feeds carry `about/channel/url/signatures/version/expires` (PROTOCOL §10).
type feedDoc struct {
	Version     string           `json:"version"`
	Title       string           `json:"title"`
	HomePageURL string           `json:"home_page_url,omitempty"`
	FeedURL     string           `json:"feed_url,omitempty"`
	Description string           `json:"description,omitempty"`
	Icon        string           `json:"icon,omitempty"`
	Favicon     string           `json:"favicon,omitempty"`
	Authors     []feedAuthor     `json:"authors,omitempty"`
	Language    string           `json:"language,omitempty"`
	UserComment string           `json:"user_comment,omitempty"`
	Expired     *bool            `json:"expired,omitempty"`
	Sig         *feedSig         `json:"_sig,omitempty"`
	Items       []map[string]any `json:"items"`
}

type feedAuthor struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

type feedSig struct {
	About      string           `json:"about"`
	Channel    string           `json:"channel,omitempty"`   // private feeds only
	URL        string           `json:"url,omitempty"`       // private feeds only
	Version    int64            `json:"version,omitempty"`   // private feeds only
	Expires    string           `json:"expires,omitempty"`   // private feeds only
	Signatures []map[string]any `json:"signatures,omitempty"` // private feeds: whole document
}

const sigAbout = metadataOrigin + "/_sig" // extension identity (served locally by the demo)

// itemToMap renders a demoItem as the published JSON Feed item object
// (without _sig.signatures; those are added by addSignature).
func itemToMap(it demoItem) map[string]any {
	return map[string]any{
		"id":             it.ID,
		"url":            metadataOrigin + "/blog/" + it.Slug + ".html",
		"title":          it.Title,
		"content_html":   it.ContentHTML,
		"content_text":   it.ContentText,
		"summary":        it.Summary,
		"image":          metadataOrigin + "/media/img/" + it.Image,
		"date_published": it.Published,
		"tags":           it.Tags,
		"language":       it.Language,
		"authors":        []map[string]any{{"name": it.Author}},
		"_sig": map[string]any{
			"channel":   it.Channel,
			"withdrawn": false,
		},
	}
}

// addSignature computes the JCS (RFC 8785) canonical bytes of the object
// with its `_sig.signatures` field removed, signs them with Ed25519 and
// appends {name, keyid, sig} (base64url, no padding) to _sig.signatures.
// The `name` is the demo key's human-readable label (feed-derived), for
// readable artifacts; verification uses `keyid` only. Calling it multiple
// times accumulates threshold signatures.
func addSignature(obj map[string]any, kp *keyPair) error {
	sigObj, ok := obj["_sig"].(map[string]any)
	if !ok {
		return fmt.Errorf("object %v: missing _sig", obj["id"])
	}
	canonical, err := canonicalBytesWithoutSignatures(obj)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(kp.Priv, canonical)
	sigs, _ := sigObj["signatures"].([]any)
	sigObj["signatures"] = append(sigs, map[string]any{
		"name":  kp.Name,
		"keyid": kp.KeyID,
		"sig":   base64.RawURLEncoding.EncodeToString(sig),
	})
	return nil
}

// canonicalBytesWithoutSignatures returns the JCS (RFC 8785) canonical
// bytes of the object with `_sig.signatures` removed (PROTOCOL §8.2).
func canonicalBytesWithoutSignatures(obj map[string]any) ([]byte, error) {
	clone := deepCopyMap(obj)
	if sigObj, ok := clone["_sig"].(map[string]any); ok {
		delete(sigObj, "signatures")
	}
	raw, err := json.Marshal(clone)
	if err != nil {
		return nil, err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("JCS canonicalization: %w", err)
	}
	return canonical, nil
}

func deepCopyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch t := v.(type) {
		case map[string]any:
			out[k] = deepCopyMap(t)
		case []any:
			cp := make([]any, len(t))
			for i, e := range t {
				if em, ok := e.(map[string]any); ok {
					cp[i] = deepCopyMap(em)
				} else {
					cp[i] = e
				}
			}
			out[k] = cp
		default:
			out[k] = v
		}
	}
	return out
}

// feedToBytes serializes the feed document (pretty, deterministic) — the
// exact bytes that are hash-pinned as a TUF target.
func feedToBytes(doc *feedDoc) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep <p> etc. readable in the demo output
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeFeed serializes the feed document to path (pretty, deterministic).
func writeFeed(doc *feedDoc, path string) error {
	data, err := feedToBytes(doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// buildChannelFeed builds the public feed document for one channel,
// newest first. Items are signed per PROTOCOL §8.2: in editor-mode channels
// the editor keys are load-bearing (threshold, §9) and the channel-key
// signature is added for portability; in default channels the channel-key
// signature is attribution only.
func buildChannelFeed(keys map[string]*keyPair, ch channel) (*feedDoc, error) {
	items := make([]map[string]any, 0)
	for i := len(publicItems) - 1; i >= 0; i-- { // newest first
		it := publicItems[i]
		if it.Channel != ch.Name {
			continue
		}
		item := itemToMap(it)
		if ec, editor := editorChannels[ch.Name]; editor {
			for _, name := range ec.KeyNames {
				kp := keys[name]
				if err := addSignature(item, kp); err != nil {
					return nil, err
				}
			}
		}
		// channel-key signature: load-bearing in default mode (attribution),
		// portability extra in editor mode (§8.2: MAY be present).
		kp := keys[ch.Name]
		if err := addSignature(item, kp); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("channel %q has no items", ch.Name)
	}
	return &feedDoc{
		Version:     "https://jsonfeed.org/version/1.1",
		Title:       "Trezor — " + ch.DisplayName,
		HomePageURL: companyHome,
		FeedURL:     metadataOrigin + "/channels/" + ch.Name + "/feed.json",
		Description: ch.Description + ". Official announcements from Trezor Company s.r.o., authenticated via the Keryx protocol.",
		Icon:        logoURL,
		Favicon:     logoURL,
		Authors:     []feedAuthor{{Name: companyName, URL: companyHome}},
		Language:    "en",
		UserComment: "This feed is a signed broadcast. Items carry Ed25519 signatures in the _sig extension; generic JSON Feed readers may ignore them.",
		Sig:         &feedSig{About: sigAbout},
		Items:       items,
	}, nil
}

// buildPrivateFeed builds the self-authenticating capability feed for one
// order (PROTOCOL §10): a single JSON Feed document signed as a whole by
// the pattern entry's key. The top-level _sig carries channel/url/version/
// expires/signatures; item-level signatures are kept for portability only.
func buildPrivateFeed(keys map[string]*keyPair, token, feedURL string) (*feedDoc, error) {
	expired := false
	doc := &feedDoc{
		Version:     "https://jsonfeed.org/version/1.1",
		Title:       "Trezor — order #2026-0841 delivery",
		HomePageURL: companyHome,
		FeedURL:     feedURL,
		Description: "Private delivery notifications for order #2026-0841. Access by unguessable capability URL only.",
		Icon:        logoURL,
		Favicon:     logoURL,
		Authors:     []feedAuthor{{Name: companyName, URL: companyHome}},
		Language:    "en",
		Expired:     &expired,
		Sig: &feedSig{
			About:   sigAbout,
			Channel: trackingPatternEntry.Channel,
			URL:     feedURL,
			Version: 1,
			Expires: "2026-10-15T00:00:00Z", // the order window end (anti-freeze)
		},
		Items: []map[string]any{},
	}

	item := itemToMap(privateItem)
	item["url"] = metadataOrigin + "/channels/tracking/" + token + "/" + privateItem.Slug + ".html"
	// item-level signatures: uniform format, NOT load-bearing here (§10)
	kp := keys["tracking"]
	if err := addSignature(item, kp); err != nil {
		return nil, err
	}
	doc.Items = append(doc.Items, item)

	// whole-document signature: JCS of the document with the top-level
	// _sig.signatures field removed, by the pattern entry's key.
	docMap, err := docToMap(doc)
	if err != nil {
		return nil, err
	}
	canonical, err := canonicalBytesWithoutSignatures(docMap)
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(kp.Priv, canonical)
	doc.Sig.Signatures = []map[string]any{{
		"name":  kp.Name,
		"keyid": kp.KeyID,
		"sig":   base64.RawURLEncoding.EncodeToString(sig),
	}}
	return doc, nil
}

// docToMap converts a typed feedDoc to a plain map (for whole-doc signing).
func docToMap(doc *feedDoc) (map[string]any, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// verifyFeedFile loads a feed from disk and verifies every item. For
// public feeds `channelKey` is the channel role key (attribution) and
// `editor` carries the editor-mode requirement (nil = default mode); for
// private feeds `wholeDoc` triggers whole-document verification (§10).
func verifyFeedFile(path string, keys map[string]*keyPair, channelKey *keyPair, editor *editorConfig, wholeDoc *privateFeedCheck) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if wholeDoc != nil {
		if err := verifyPrivateDocument(doc, path, wholeDoc); err != nil {
			return err
		}
	}
	items, _ := doc["items"].([]any)
	if len(items) == 0 {
		return fmt.Errorf("%s: no items", path)
	}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: bad item", path)
		}
		id, _ := item["id"].(string)
		if err := verifyItemSignatures(item, keys, channelKey, editor); err != nil {
			return fmt.Errorf("%s: item %s: %w", path, id, err)
		}
	}
	fmt.Printf("  %s: %d items verified\n", path, len(items))
	return nil
}

// privateFeedCheck carries what a private feed must satisfy (§10).
type privateFeedCheck struct {
	Channel     string // pattern entry's channel
	Key         *keyPair
	FetchedURL  string // the capability URL actually fetched (must equal _sig.url)
	MaxVersion  int64  // client-side version memory (0 = none)
}

func verifyPrivateDocument(doc map[string]any, path string, check *privateFeedCheck) error {
	sigObj, ok := doc["_sig"].(map[string]any)
	if !ok {
		return fmt.Errorf("%s: missing top-level _sig", path)
	}
	if channel, _ := sigObj["channel"].(string); channel != check.Channel {
		return fmt.Errorf("%s: _sig.channel %q != pattern entry %q", path, channel, check.Channel)
	}
	if url, _ := sigObj["url"].(string); url != check.FetchedURL {
		return fmt.Errorf("%s: _sig.url %q != fetched URL %q", path, url, check.FetchedURL)
	}
	version, _ := sigObj["version"].(float64)
	if int64(version) < check.MaxVersion {
		return fmt.Errorf("%s: _sig.version %d older than last seen %d (rollback)", path, int64(version), check.MaxVersion)
	}
	canonical, err := canonicalBytesWithoutSignatures(doc)
	if err != nil {
		return err
	}
	sigs, _ := sigObj["signatures"].([]any)
	if len(sigs) == 0 {
		return fmt.Errorf("%s: no whole-document signature", path)
	}
	verified := 0
	for _, s := range sigs {
		se, ok := s.(map[string]any)
		if !ok {
			continue
		}
		keyid, _ := se["keyid"].(string)
		if keyid != check.Key.KeyID {
			continue
		}
		sigB64, _ := se["sig"].(string)
		rawSig, err := base64.RawURLEncoding.DecodeString(sigB64)
		if err != nil {
			continue
		}
		if ed25519.Verify(check.Key.Pub, canonical, rawSig) {
			verified++
		}
	}
	if verified == 0 {
		return fmt.Errorf("%s: whole-document signature invalid", path)
	}
	return nil
}

// verifyItemSignatures verifies one item per PROTOCOL §8.2/§9. In editor
// mode the editor signatures are load-bearing (threshold, unknown keyid →
// reject); in default mode signatures are optional attribution (unknown
// keyids ignored).
func verifyItemSignatures(item map[string]any, keys map[string]*keyPair, channelKey *keyPair, editor *editorConfig) error {
	sigObj, ok := item["_sig"].(map[string]any)
	if !ok {
		return fmt.Errorf("missing _sig")
	}
	canonical, err := canonicalBytesWithoutSignatures(item)
	if err != nil {
		return err
	}

	if editor != nil {
		valid := 0
		sigs, _ := sigObj["signatures"].([]any)
		if len(sigs) == 0 {
			return fmt.Errorf("editor mode: no signatures")
		}
		editorKeys := map[string]*keyPair{}
		for _, name := range editor.KeyNames {
			if kp, ok := keys[name]; ok {
				editorKeys[kp.KeyID] = kp
			}
		}
		for _, s := range sigs {
			se, ok := s.(map[string]any)
			if !ok {
				continue
			}
			keyid, _ := se["keyid"].(string)
			kp, ok := editorKeys[keyid]
			if !ok {
				continue // additional entry (e.g. channel-key attribution) — not load-bearing (§8.2)
			}
			sigB64, _ := se["sig"].(string)
			rawSig, err := base64.RawURLEncoding.DecodeString(sigB64)
			if err != nil {
				continue
			}
			if ed25519.Verify(kp.Pub, canonical, rawSig) {
				valid++
			}
		}
		if valid < editor.Threshold {
			return fmt.Errorf("editor mode: %d/%d valid editor signatures", valid, editor.Threshold)
		}
		return nil
	}

	// default mode: signatures optional; verify entries by the known key
	sigs, _ := sigObj["signatures"].([]any)
	for _, s := range sigs {
		se, ok := s.(map[string]any)
		if !ok {
			continue
		}
		keyid, _ := se["keyid"].(string)
		if keyid != channelKey.KeyID {
			continue // attribution by another (unknown) key — ignored
		}
		sigB64, _ := se["sig"].(string)
		rawSig, err := base64.RawURLEncoding.DecodeString(sigB64)
		if err != nil {
			continue
		}
		if !ed25519.Verify(channelKey.Pub, canonical, rawSig) {
			return fmt.Errorf("signature by channel key %s invalid", keyid)
		}
	}
	return nil
}
