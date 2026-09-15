// Package feed implements public channel items and private capability
// documents (spec/feeds.md). Canonicalization is securesystemslib canonical
// JSON (OLPC) — the same canonicalization as TUF metadata (spec/core.md §1).
package feed

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/secure-systems-lab/go-securesystemslib/cjson"
	"github.com/v1b3coder/keryx/sdk/keys"
)

// Sig is one item signature entry (spec/feeds.md §1.1).
type Sig struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

// Attachment is an item attachment (spec/feeds.md §1.1).
type Attachment struct {
	Name        string `json:"name,omitempty"`
	URL         string `json:"url"`
	MimeType    string `json:"mime_type,omitempty"`
	SizeInBytes int64  `json:"size_in_bytes,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
}

var idRe = regexp.MustCompile(`^[a-z0-9-_]+$`)

// TargetPath returns the TUF target path of one public item.
func TargetPath(channel, id string) string {
	return "channels/" + channel + "/" + id + ".json"
}

// IDOf returns an item's id.
func IDOf(obj map[string]any) string {
	s, _ := obj["id"].(string)
	return s
}

// Decode parses item/document JSON preserving numbers as json.Number so OLPC
// canonicalization is lossless and re-signing is byte-exact.
func Decode(data []byte) (map[string]any, error) {
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// Encode serializes an item/document deterministically (pretty, no HTML
// escaping). The TUF hash pins these bytes; OLPC is escaping-independent.
func Encode(obj map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(obj); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CanonicalBytes returns the OLPC canonical bytes of obj with its `sig` field
// removed — exactly what the publisher signs (spec/feeds.md §1.2).
func CanonicalBytes(obj map[string]any) ([]byte, error) {
	clone := deepCopy(obj)
	delete(clone, "sig")
	return cjson.EncodeCanonical(clone)
}

// SignItem appends {keyid, sig} to obj's `sig` array. Calling it repeatedly
// accumulates threshold signatures (spec/feeds.md §2).
func SignItem(obj map[string]any, k *keys.Key) error {
	canonical, err := CanonicalBytes(obj)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(k.Private(), canonical)
	sigs, _ := obj["sig"].([]any)
	obj["sig"] = append(sigs, map[string]any{
		"keyid": k.KeyID(),
		"sig":   base64.RawURLEncoding.EncodeToString(sig),
	})
	return nil
}

// SignatureEntries returns the item's `sig` entries.
func SignatureEntries(obj map[string]any) []Sig {
	sigs, _ := obj["sig"].([]any)
	out := make([]Sig, 0, len(sigs))
	for _, s := range sigs {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		keyid, _ := m["keyid"].(string)
		sig, _ := m["sig"].(string)
		out = append(out, Sig{KeyID: keyid, Sig: sig})
	}
	return out
}

// VerifyItem verifies one item per spec/feeds.md §1.2. In an authored channel
// the author signatures are load-bearing (threshold); otherwise the channel role
// keys are. Entries by unknown keys are ignored; a known keyid whose signature
// does not verify rejects the item.
func VerifyItem(
	obj map[string]any,
	authorKeys map[string]ed25519.PublicKey, authorThreshold int,
	channelKeys map[string]ed25519.PublicKey, channelThreshold int,
) error {
	canonical, err := CanonicalBytes(obj)
	if err != nil {
		return err
	}
	sigs := SignatureEntries(obj)
	if len(sigs) == 0 {
		return fmt.Errorf("item: no signatures")
	}
	authored := len(authorKeys) > 0
	if authored {
		valid := 0
		for _, s := range sigs {
			pub, ok := authorKeys[s.KeyID]
			if !ok {
				continue
			}
			raw, err := base64.RawURLEncoding.DecodeString(s.Sig)
			if err != nil || !ed25519.Verify(pub, canonical, raw) {
				return fmt.Errorf("item: signature by author key %s invalid", s.KeyID)
			}
			valid++
		}
		if valid < max(1, authorThreshold) {
			return fmt.Errorf("item: %d/%d valid author signatures", valid, max(1, authorThreshold))
		}
		return nil
	}
	valid := 0
	for _, s := range sigs {
		pub, ok := channelKeys[s.KeyID]
		if !ok {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(s.Sig)
		if err != nil || !ed25519.Verify(pub, canonical, raw) {
			return fmt.Errorf("item: signature by channel key %s invalid", s.KeyID)
		}
		valid++
	}
	if valid < max(1, channelThreshold) {
		return fmt.Errorf("item: %d/%d valid channel signatures", valid, max(1, channelThreshold))
	}
	return nil
}

// ValidateItem checks the item schema (spec/feeds.md §1.1). Signature presence
// and validity are separate (VerifyItem); this is the shape check the publisher
// applies before writing.
func ValidateItem(obj map[string]any) error {
	return ValidateItemOptions(obj, false)
}

// ValidateItemOptions allows a plain-HTTP local-dev origin for linked media
// when allowLocalHTTP is set (a documented dev-only exception, spec/feeds.md §1.1).
func ValidateItemOptions(obj map[string]any, allowLocalHTTP bool) error {
	id, _ := obj["id"].(string)
	if id == "" || !idRe.MatchString(id) {
		return fmt.Errorf("item: id %q must match [a-z0-9-_]+", id)
	}
	if _, ok := obj["title"].(string); !ok {
		return fmt.Errorf("item %s: title is required", id)
	}
	if _, ok := obj["content_html"].(string); !ok {
		return fmt.Errorf("item %s: content_html is required", id)
	}
	pub, ok := obj["date_published"].(string)
	if !ok {
		return fmt.Errorf("item %s: date_published is required", id)
	}
	if _, err := time.Parse(time.RFC3339, pub); err != nil {
		return fmt.Errorf("item %s: date_published: %w", id, err)
	}
	if mod, ok := obj["date_modified"].(string); ok {
		if _, err := time.Parse(time.RFC3339, mod); err != nil {
			return fmt.Errorf("item %s: date_modified: %w", id, err)
		}
	}
	if img, ok := obj["image"].(string); ok {
		if strings.HasPrefix(img, "data:") {
			if !strings.Contains(img, ";base64,") {
				return fmt.Errorf("item %s: image data URL must be base64", id)
			}
		} else {
			if !linkedAllowed(img, allowLocalHTTP) {
				return fmt.Errorf("item %s: image must be a data URL or absolute HTTPS URL", id)
			}
			if sum, _ := obj["image_sha256"].(string); sum == "" {
				return fmt.Errorf("item %s: linked image requires image_sha256", id)
			}
		}
	}
	if atts, ok := obj["attachments"].([]any); ok {
		for i, a := range atts {
			m, ok := a.(map[string]any)
			if !ok {
				return fmt.Errorf("item %s: attachment %d is not an object", id, i)
			}
			url, _ := m["url"].(string)
			if !linkedAllowed(url, allowLocalHTTP) {
				return fmt.Errorf("item %s: attachment %d url must be absolute HTTPS", id, i)
			}
			if sum, _ := m["sha256"].(string); sum != "" && len(sum) != 64 {
				return fmt.Errorf("item %s: attachment %d sha256 must be lowercase hex", id, i)
			}
		}
	}
	return nil
}

// NewToken returns a 128-bit unguessable capability token (spec/feeds.md §3):
// 16 CSPRNG bytes, base64url without padding (22 chars).
func NewToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// IsToken reports whether s looks like a capability token.
func IsToken(s string) bool {
	if len(s) != 22 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && len(raw) == 16
}

// linkedAllowed reports whether a linked resource URL is acceptable: HTTPS,
// or plain-HTTP loopback/RFC 1918 in the local-dev exception.
func linkedAllowed(raw string, allowLocalHTTP bool) bool {
	if strings.HasPrefix(raw, "https://") {
		return true
	}
	if !allowLocalHTTP || !strings.HasPrefix(raw, "http://") {
		return false
	}
	host := strings.TrimPrefix(raw, "http://")
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		return false
	}
	nums := make([]int, 4)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return false
		}
		nums[i] = n
	}
	a, b := nums[0], nums[1]
	return a == 10 || (a == 172 && b >= 16 && b <= 31) || (a == 192 && b == 168)
}

func deepCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return deepCopy(t)
	case []any:
		cp := make([]any, len(t))
		for i, e := range t {
			cp[i] = deepCopyValue(e)
		}
		return cp
	default:
		return v
	}
}

// HashBytes returns the lowercase-hex SHA-256 of data.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
