package feed

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/v1b3coder/keryx/sdk/keys"
)

// PrivateDocument is one signed capability-feed document (spec/feeds.md §3).
// It is a single signed document, NOT a JSON Feed document and NOT a TUF target.
type PrivateDocument struct {
	V       int              `json:"v"`
	Channel string           `json:"channel"`
	URL     string           `json:"url"`
	Version int64            `json:"version"`
	Expires string           `json:"expires"`
	Expired bool             `json:"expired"`
	Items   []any            `json:"items"`
	Sig     []Sig            `json:"sig,omitempty"`
}

// BuildPrivateDocument builds and whole-document-signs a new capability feed.
// The engine key is the single authority for its pattern entry (spec/feeds.md §3);
// items use the public item format without `sig`.
func BuildPrivateDocument(engine *keys.Key, feedURL, channel string, items []map[string]any, expires time.Time) (map[string]any, error) {
	doc, err := privateToMap(&PrivateDocument{
		V:       1,
		Channel: channel,
		URL:     feedURL,
		Version: 1,
		Expires: expires.UTC().Format(time.RFC3339),
		Expired: false,
		Items:   stripItemSigs(items),
	})
	if err != nil {
		return nil, err
	}
	if err := SignDocument(doc, engine); err != nil {
		return nil, err
	}
	return doc, nil
}

// UpdatePrivateDocument rewrites a document with a new item set: version+1 and a
// refreshed `expires` (spec/feeds.md §3 — absence = removed).
func UpdatePrivateDocument(engine *keys.Key, prev map[string]any, items []map[string]any, expires time.Time) (map[string]any, error) {
	next := int64(1)
	if n, ok := asInt(prev["version"]); ok {
		next = n + 1
	}
	doc := deepCopy(prev)
	doc["version"] = next
	doc["expires"] = expires.UTC().Format(time.RFC3339)
	doc["expired"] = false
	doc["items"] = stripItemSigs(items)
	delete(doc, "sig")
	if err := SignDocument(doc, engine); err != nil {
		return nil, err
	}
	return doc, nil
}

// ExpirePrivateDocument sets `expired: true` (MANDATORY at window end,
// spec/feeds.md §3) and version+1; the app stops polling and keeps cached items.
func ExpirePrivateDocument(engine *keys.Key, prev map[string]any) (map[string]any, error) {
	next := int64(1)
	if n, ok := asInt(prev["version"]); ok {
		next = n + 1
	}
	doc := deepCopy(prev)
	doc["version"] = next
	doc["expired"] = true
	delete(doc, "sig")
	if err := SignDocument(doc, engine); err != nil {
		return nil, err
	}
	return doc, nil
}

// SignDocument signs the whole document with its `sig` field removed (OLPC).
func SignDocument(doc map[string]any, k *keys.Key) error {
	canonical, err := CanonicalBytes(doc)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(k.Private(), canonical)
	sigs, _ := doc["sig"].([]any)
	doc["sig"] = append(sigs, map[string]any{
		"keyid": k.KeyID(),
		"sig":   base64.RawURLEncoding.EncodeToString(sig),
	})
	return nil
}

// PrivateVerification is the result of verifying a capability feed.
type PrivateVerification struct {
	Version int64
	Closed  bool
}

// VerifyPrivateDocument verifies a fetched document per spec/feeds.md §3:
// whole-document signature (threshold), `channel` == entry channel, `url` ==
// fetched URL, and `version` not older than last seen.
func VerifyPrivateDocument(doc map[string]any, entryKeys map[string]ed25519.PublicKey, threshold int, fetchedURL, channel string, lastVersion int64) (PrivateVerification, error) {
	v, ok := asInt(doc["v"])
	if !ok || v != 1 {
		return PrivateVerification{}, fmt.Errorf("private feed: unknown schema version %v", doc["v"])
	}
	if got, _ := doc["channel"].(string); got != channel {
		return PrivateVerification{}, fmt.Errorf("private feed: channel %q != pattern entry %q", got, channel)
	}
	if got, _ := doc["url"].(string); got != fetchedURL {
		return PrivateVerification{}, fmt.Errorf("private feed: url %q != fetched URL %q", got, fetchedURL)
	}
	version, ok := asInt(doc["version"])
	if !ok {
		return PrivateVerification{}, fmt.Errorf("private feed: version missing")
	}
	if version < lastVersion {
		return PrivateVerification{}, fmt.Errorf("private feed: version %d older than last seen %d (rollback?)", version, lastVersion)
	}
	canonical, err := CanonicalBytes(doc)
	if err != nil {
		return PrivateVerification{}, err
	}
	valid := 0
	for _, s := range SignatureEntries(doc) {
		pub, ok := entryKeys[s.KeyID]
		if !ok {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(s.Sig)
		if err != nil || !ed25519.Verify(pub, canonical, raw) {
			continue
		}
		valid++
	}
	if valid < max(1, threshold) {
		return PrivateVerification{}, fmt.Errorf("private feed: %d/%d valid whole-document signatures", valid, max(1, threshold))
	}
	closed, _ := doc["expired"].(bool)
	return PrivateVerification{Version: version, Closed: closed}, nil
}

// ValidatePrivateDocument checks the document shape and that items carry no
// per-item `sig` (the document signature covers everything).
func ValidatePrivateDocument(doc map[string]any) error {
	if v, ok := asInt(doc["v"]); !ok || v != 1 {
		return fmt.Errorf("private feed: v must be 1")
	}
	if channel, _ := doc["channel"].(string); channel == "" {
		return fmt.Errorf("private feed: channel is required")
	}
	if url, _ := doc["url"].(string); url == "" {
		return fmt.Errorf("private feed: url is required")
	}
	if _, ok := asInt(doc["version"]); !ok {
		return fmt.Errorf("private feed: version is required")
	}
	if expires, _ := doc["expires"].(string); expires != "" {
		if _, err := time.Parse(time.RFC3339, expires); err != nil {
			return fmt.Errorf("private feed: expires: %w", err)
		}
	} else {
		return fmt.Errorf("private feed: expires is required")
	}
	items, _ := doc["items"].([]any)
	for i, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			return fmt.Errorf("private feed: item %d is not an object", i)
		}
		if _, ok := m["sig"]; ok {
			return fmt.Errorf("private feed: item %d must not carry sig", i)
		}
		if err := ValidateItem(m); err != nil {
			return err
		}
	}
	return nil
}

func privateToMap(d *PrivateDocument) (map[string]any, error) {
	data, err := Encode(map[string]any{
		"v": d.V, "channel": d.Channel, "url": d.URL, "version": d.Version,
		"expires": d.Expires, "expired": d.Expired, "items": d.Items,
	})
	if err != nil {
		return nil, err
	}
	return Decode(data)
}

func stripItemSigs(items []map[string]any) []any {
	out := make([]any, 0, len(items))
	for _, it := range items {
		cp := deepCopy(it)
		delete(cp, "sig")
		out = append(out, cp)
	}
	return out
}

func asInt(v any) (int64, bool) {
	switch t := v.(type) {
	case json.Number:
		n, err := t.Int64()
		return n, err == nil
	case float64:
		return int64(t), true
	case int:
		return int64(t), true
	case int64:
		return t, true
	}
	return 0, false
}
