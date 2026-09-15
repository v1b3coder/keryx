package feed_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"

	"github.com/v1b3coder/keryx/sdk/feed"
	"github.com/v1b3coder/keryx/sdk/keys"
)

func item(id string) map[string]any {
	return map[string]any{
		"id":             id,
		"title":          "Title " + id,
		"content_html":   "<p>Body</p>",
		"date_published": "2026-03-14T10:00:00Z",
	}
}

func signer(t *testing.T, role keys.Role, name string) *keys.Key {
	t.Helper()
	k, err := keys.Generate(role, name)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func pubOf(ks ...*keys.Key) map[string]ed25519.PublicKey {
	out := map[string]ed25519.PublicKey{}
	for _, k := range ks {
		out[k.KeyID()] = k.Public()
	}
	return out
}

func TestOLPCAuthoredThreshold(t *testing.T) {
	alice := signer(t, keys.RoleAuthor, "alice")
	bob := signer(t, keys.RoleAuthor, "bob")
	channel := signer(t, keys.RoleChannel, "news")
	it := item("hello")
	for _, k := range []*keys.Key{alice, bob} {
		if err := feed.SignItem(it, k); err != nil {
			t.Fatal(err)
		}
	}
	if err := feed.VerifyItem(it, pubOf(alice, bob), 2, pubOf(channel), 1); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// below threshold → rejected
	if err := feed.VerifyItem(it, map[string]ed25519.PublicKey{alice.KeyID(): alice.Public()}, 2, pubOf(channel), 1); err == nil {
		t.Fatal("verified below the authors threshold")
	}
	// a known author keyid with a broken signature rejects the item
	bad := deepCopy(it)
	sigs := bad["sig"].([]any)
	sigs[0].(map[string]any)["sig"] = base64.RawURLEncoding.EncodeToString(make([]byte, 64))
	if err := feed.VerifyItem(bad, pubOf(alice), 2, pubOf(channel), 1); err == nil {
		t.Fatal("accepted a broken author signature")
	}
	// tampering with the content rejects the item
	tampered := deepCopy(it)
	tampered["title"] = "Tampered"
	if err := feed.VerifyItem(tampered, pubOf(alice), 2, pubOf(channel), 1); err == nil {
		t.Fatal("accepted a tampered item")
	}
}

func TestOLPCUnknownKeyIgnored(t *testing.T) {
	channel := signer(t, keys.RoleChannel, "news")
	other := signer(t, keys.RoleChannel, "other")
	it := item("hello")
	if err := feed.SignItem(it, channel); err != nil {
		t.Fatal(err)
	}
	// an extra entry by an unknown key is ignored (attribution only)
	if err := feed.SignItem(it, other); err != nil {
		t.Fatal(err)
	}
	if err := feed.VerifyItem(it, nil, 0, pubOf(channel), 1); err != nil {
		t.Fatalf("verify with unknown extra key: %v", err)
	}
	// a known keyid with a failing signature rejects the item
	bad := deepCopy(it)
	for _, s := range bad["sig"].([]any) {
		e := s.(map[string]any)
		if e["keyid"] == channel.KeyID() {
			e["sig"] = "AAAA"
		}
	}
	if err := feed.VerifyItem(bad, nil, 0, pubOf(channel), 1); err == nil {
		t.Fatal("accepted a broken channel signature")
	}
	// missing signatures reject the item in both modes
	if err := feed.VerifyItem(item("unsigned"), nil, 0, pubOf(channel), 1); err == nil {
		t.Fatal("accepted an unsigned item")
	}
}

func TestValidateItemImageRules(t *testing.T) {
	it := item("hello")
	it["image"] = "https://cdn.example.com/a.jpg"
	if err := feed.ValidateItem(it); err == nil {
		t.Fatal("accepted a linked image without image_sha256")
	}
	it["image_sha256"] = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := feed.ValidateItem(it); err != nil {
		t.Fatalf("linked image with hash: %v", err)
	}
	it["image"] = "data:image/png;base64,AAAA"
	delete(it, "image_sha256")
	if err := feed.ValidateItem(it); err != nil {
		t.Fatalf("inline image: %v", err)
	}
	// the id charset is the channel alphabet
	it["id"] = "Bad.Id"
	if err := feed.ValidateItem(it); err == nil {
		t.Fatal("accepted an invalid id")
	}
	// attachments must be absolute HTTPS
	it["id"] = "hello"
	it["attachments"] = []any{map[string]any{"url": "http://cdn.example.com/a.pdf"}}
	if err := feed.ValidateItem(it); err == nil {
		t.Fatal("accepted an HTTP attachment")
	}
}

func TestPrivateDocumentRoundTrip(t *testing.T) {
	engine := signer(t, keys.RoleEngine, "tracking")
	url := "https://eshop.example.com/channels/tracking/abcdefghijklmnopqrstuv/feed.json"
	expires := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	doc, err := feed.BuildPrivateDocument(engine, url, "tracking", []map[string]any{item("order-1")}, expires)
	if err != nil {
		t.Fatal(err)
	}
	ver, err := feed.VerifyPrivateDocument(doc, pubOf(engine), 1, url, "tracking", 0)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ver.Version != 1 || ver.Closed {
		t.Fatalf("verification = %+v", ver)
	}
	// absence = removed: an update drops the item and bumps the version
	updated, err := feed.UpdatePrivateDocument(engine, doc, nil, expires.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ver2, err := feed.VerifyPrivateDocument(updated, pubOf(engine), 1, url, "tracking", 1)
	if err != nil {
		t.Fatalf("verify update: %v", err)
	}
	if ver2.Version != 2 {
		t.Fatalf("update version = %d", ver2.Version)
	}
	if items, _ := updated["items"].([]any); len(items) != 0 {
		t.Fatalf("updated items = %v", items)
	}
	// rollback is rejected via version memory
	if _, err := feed.VerifyPrivateDocument(updated, pubOf(engine), 1, url, "tracking", 3); err == nil {
		t.Fatal("accepted a rollback")
	}
	// a document served for another URL is rejected (cross-order mix-up)
	if _, err := feed.VerifyPrivateDocument(updated, pubOf(engine), 1, url+"x", "tracking", 0); err == nil {
		t.Fatal("accepted a document for the wrong URL")
	}
	// expired: true closes the feed but keeps it verified
	closed, err := feed.ExpirePrivateDocument(engine, updated)
	if err != nil {
		t.Fatal(err)
	}
	ver3, err := feed.VerifyPrivateDocument(closed, pubOf(engine), 1, url, "tracking", 2)
	if err != nil {
		t.Fatalf("verify expire: %v", err)
	}
	if !ver3.Closed {
		t.Fatal("expired feed is not reported closed")
	}
	// a tampered document breaks the whole-document signature
	tampered := deepCopy(doc)
	tampered["channel"] = "marketing"
	if _, err := feed.VerifyPrivateDocument(tampered, pubOf(engine), 1, url, "tracking", 0); err == nil {
		t.Fatal("accepted a tampered document")
	}
}

func TestToken(t *testing.T) {
	tok, err := feed.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if !feed.IsToken(tok) {
		t.Fatalf("token %q is not a valid capability token", tok)
	}
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || len(raw) != 16 {
		t.Fatalf("token decodes to %d bytes", len(raw))
	}
	if feed.IsToken("short") || feed.IsToken("!!!!!!!!!!!!!!!!!!!!!!") {
		t.Fatal("accepted a malformed token")
	}
}

func deepCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch t := v.(type) {
		case map[string]any:
			out[k] = deepCopy(t)
		case []any:
			cp := make([]any, len(t))
			for i, e := range t {
				if em, ok := e.(map[string]any); ok {
					cp[i] = deepCopy(em)
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
