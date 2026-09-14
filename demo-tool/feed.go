package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/secure-systems-lab/go-securesystemslib/cjson"
)

// This file implements item signing/verification (spec/feeds.md §1.2) and
// the private capability-feed document (spec/feeds.md §3).
//
// Canonicalization is securesystemslib canonical JSON (OLPC) — the same
// canonicalization TUF metadata uses (spec/core.md §1: one
// canonicalization for the whole protocol, delegated to the TUF library).
// Item signatures are raw 64-byte Ed25519 over the OLPC bytes of the item
// object with its `sig` field removed; `sig` entries are {keyid, sig}
// (base64url, no padding). There is no `_sig` object anywhere.

// itemToMap renders a demoItem as the published item object (without `sig`).
// Fields follow spec/feeds.md §1.1: id, title, content_html, image (+
// image_sha256 when linked), date_published, date_modified, tags, language,
// attachments. Deliberately absent: content_text, summary, url, authors.
func itemToMap(it demoItem) map[string]any {
	m := map[string]any{
		"id":             it.ID,
		"title":          it.Title,
		"content_html":   it.ContentHTML,
		"date_published": it.Published,
	}
	if it.Image != "" {
		// Linked image: image_sha256 is REQUIRED (spec/feeds.md §1.1) — the
		// app verifies the fetched bytes before rendering.
		m["image"] = metadataOrigin + "/media/img/" + it.Image
		m["image_sha256"] = mediaSHA256(it.Image)
	}
	if it.DateModified != "" {
		m["date_modified"] = it.DateModified
	}
	if len(it.Tags) > 0 {
		m["tags"] = it.Tags
	}
	if it.Language != "" {
		m["language"] = it.Language
	}
	if len(it.Attachments) > 0 {
		m["attachments"] = it.Attachments
	}
	return m
}

// canonicalItemBytes returns the OLPC canonical bytes of the item object
// with its `sig` field removed — exactly what the publisher signed.
func canonicalItemBytes(obj map[string]any) ([]byte, error) {
	clone := deepCopyMap(obj)
	delete(clone, "sig")
	return cjson.EncodeCanonical(clone)
}

// addSignature computes the OLPC canonical bytes of obj with its `sig`
// field removed, signs them with Ed25519 and appends {keyid, sig}
// (base64url, no padding) to the `sig` array. Calling it multiple times
// accumulates threshold signatures.
func addSignature(obj map[string]any, kp *keyPair) error {
	canonical, err := canonicalItemBytes(obj)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(kp.Priv, canonical)
	sigs, _ := obj["sig"].([]any)
	obj["sig"] = append(sigs, map[string]any{
		"keyid": kp.KeyID,
		"sig":   base64.RawURLEncoding.EncodeToString(sig),
	})
	return nil
}

// buildChannelItem builds one signed public item object. In an authored
// channel (spec/feeds.md §2) the author keys are load-bearing (threshold)
// and the channel-key signature is added for portability (MAY be present,
// not load-bearing); in a single-author channel the channel role key signs
// the item (spec/feeds.md §1.2 — all items are signed).
func buildChannelItem(keys map[string]*keyPair, it demoItem) (map[string]any, error) {
	item := itemToMap(it)
	if ac, authored := authorChannels[it.Channel]; authored {
		for _, name := range ac.KeyNames {
			kp := keys[name]
			if err := addSignature(item, kp); err != nil {
				return nil, err
			}
		}
	}
	// channel-key signature: load-bearing in a single-author channel,
	// portability extra in an authored channel (§1.2/§2).
	kp := keys[it.Channel]
	if err := addSignature(item, kp); err != nil {
		return nil, err
	}
	return item, nil
}

// encodeJSON serializes v (pretty, deterministic, no HTML escaping — the
// demo output stays readable; the TUF hash pins these bytes and OLPC
// canonicalization is order/escaping-independent for verification).
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// itemToBytes serializes an item object (pretty, deterministic).
func itemToBytes(item map[string]any) ([]byte, error) {
	return encodeJSON(item)
}

// writeJSON writes obj to path (pretty, deterministic).
func writeJSON(obj any, path string) error {
	data, err := encodeJSON(obj)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
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

// buildPrivateDocument builds the signed capability-feed document for one
// order (spec/feeds.md §3): a single signed document, NOT a TUF target and
// NOT a JSON Feed document. Items use the public item format WITHOUT `sig`
// — the document signature covers everything. The whole document is signed
// with its `sig` field removed (OLPC).
func buildPrivateDocument(keys map[string]*keyPair, feedURL string) (map[string]any, error) {
	item := itemToMap(privateItem)
	// private items are never standalone: no `sig` field (spec/feeds.md §3)
	delete(item, "sig")
	doc := map[string]any{
		"v":       1,
		"channel": trackingPatternEntry.Channel,
		"url":     feedURL,
		"version": 1,
		"expires": "2026-10-15T00:00:00Z", // the order window end (anti-freeze)
		"expired": false,
		"items":   []any{item},
	}
	// whole-document signature by the pattern entry's key (the engine key)
	if err := addSignature(doc, keys["tracking"]); err != nil {
		return nil, err
	}
	return doc, nil
}

// verifyItemSignatures verifies one item per spec/feeds.md §1.2. In an
// authored channel the author signatures are load-bearing (threshold; a
// known author keyid whose signature fails → reject); in a single-author
// channel at least `threshold` entries must verify against the channel role
// keyids. Entries by unknown keys are ignored (attribution only).
func verifyItemSignatures(item map[string]any, keys map[string]*keyPair, channelKey *keyPair, authors *authorConfig) error {
	sigs, _ := item["sig"].([]any)
	if len(sigs) == 0 {
		return fmt.Errorf("no signatures")
	}
	canonical, err := canonicalItemBytes(item)
	if err != nil {
		return err
	}

	if authors != nil {
		authorKeys := map[string]*keyPair{}
		for _, name := range authors.KeyNames {
			if kp, ok := keys[name]; ok {
				authorKeys[kp.KeyID] = kp
			}
		}
		valid := 0
		for _, s := range sigs {
			se, ok := s.(map[string]any)
			if !ok {
				continue
			}
			keyid, _ := se["keyid"].(string)
			kp, ok := authorKeys[keyid]
			if !ok {
				continue // additional entry (e.g. channel-key attribution) — not load-bearing (§2)
			}
			rawSig, err := base64.RawURLEncoding.DecodeString(asString(se["sig"]))
			if err != nil || !ed25519.Verify(kp.Pub, canonical, rawSig) {
				return fmt.Errorf("signature by author key %s invalid", keyid)
			}
			valid++
		}
		if valid < authors.Threshold {
			return fmt.Errorf("%d/%d valid author signatures", valid, authors.Threshold)
		}
		return nil
	}

	// single-author channel: at least `threshold` (1) entries verify against
	// the channel role keyids; a known keyid whose signature fails → reject.
	valid := 0
	for _, s := range sigs {
		se, ok := s.(map[string]any)
		if !ok {
			continue
		}
		keyid, _ := se["keyid"].(string)
		if keyid != channelKey.KeyID {
			continue // unknown key — ignored (attribution only)
		}
		rawSig, err := base64.RawURLEncoding.DecodeString(asString(se["sig"]))
		if err != nil || !ed25519.Verify(channelKey.Pub, canonical, rawSig) {
			return fmt.Errorf("signature by channel key %s invalid", keyid)
		}
		valid++
	}
	if valid == 0 {
		return fmt.Errorf("no valid channel-key signature")
	}
	return nil
}

// privateFeedCheck carries what a private feed must satisfy (spec/feeds.md §3).
type privateFeedCheck struct {
	Channel    string // pattern entry's channel (must equal doc `channel`)
	Key        *keyPair
	FetchedURL string // the capability URL actually fetched (must equal doc `url`)
	MaxVersion int64  // client-side version memory (0 = none)
}

// verifyPrivateDocument verifies the whole-document signature and the
// channel/url/version bindings (spec/feeds.md §3).
func verifyPrivateDocument(doc map[string]any, path string, check *privateFeedCheck) error {
	if v, _ := doc["v"].(float64); v != 1 {
		return fmt.Errorf("%s: unknown schema version %v", path, doc["v"])
	}
	if channel, _ := doc["channel"].(string); channel != check.Channel {
		return fmt.Errorf("%s: channel %q != pattern entry %q", path, channel, check.Channel)
	}
	if url, _ := doc["url"].(string); url != check.FetchedURL {
		return fmt.Errorf("%s: url %q != fetched URL %q", path, url, check.FetchedURL)
	}
	version, _ := doc["version"].(float64)
	if int64(version) < check.MaxVersion {
		return fmt.Errorf("%s: version %d older than last seen %d (rollback)", path, int64(version), check.MaxVersion)
	}
	canonical, err := canonicalItemBytes(doc)
	if err != nil {
		return err
	}
	sigs, _ := doc["sig"].([]any)
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
		rawSig, err := base64.RawURLEncoding.DecodeString(asString(se["sig"]))
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

// asString normalizes a JSON-decoded value into a string.
func asString(v any) string {
	s, _ := v.(string)
	return s
}
