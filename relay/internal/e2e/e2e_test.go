// Package e2e exercises the full relay path end to end against the registered
// demo repository: TOFU + TUF verification, scope derivation, a real signed
// wake-up, and RFC 8291 WebPush delivery to a fake push service.
package e2e

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"

	"github.com/v1b3coder/keryx/relay/internal/api"
	"github.com/v1b3coder/keryx/relay/internal/companytuf"
	"github.com/v1b3coder/keryx/relay/internal/netpolicy"
	"github.com/v1b3coder/keryx/relay/internal/push"
	"github.com/v1b3coder/keryx/relay/internal/relay"
	"github.com/v1b3coder/keryx/relay/internal/scope"
	"github.com/v1b3coder/keryx/relay/internal/store"
	"github.com/v1b3coder/keryx/relay/internal/topic"
	"github.com/v1b3coder/keryx/relay/internal/tufclient"
	"github.com/v1b3coder/keryx/relay/internal/wakeup"
)

// demoDir locates the registered demo repository.
func demoDir() string {
	if dir := os.Getenv("KERYX_DEMO_DIR"); dir != "" {
		return dir
	}
	return filepath.Join("..", "..", "..", "..", "keryx-demo")
}

// resealRoot copies the demo repository, points custom.repo_base at the local
// HTTPS server and re-signs the root with the demo's master key.
func resealRoot(t *testing.T, src, dst, repoBase string) {
	t.Helper()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dst, "keys", "master.json")
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		SeedHex string `json:"seed_hex"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	seed, err := hex.DecodeString(record.SeedHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("master seed: %v", err)
	}
	priv := ed25519.NewKeyFromSeed(seed)

	for _, name := range []string{"root.json", "1.root.json"} {
		path := filepath.Join(dst, ".well-known", "keryx", name)
		rootBytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var root metadata.Metadata[metadata.RootType]
		if _, err := root.FromBytes(rootBytes); err != nil {
			t.Fatal(err)
		}
		custom, _ := root.Signed.UnrecognizedFields["custom"].(map[string]any)
		if custom == nil {
			t.Fatalf("%s: custom missing", name)
		}
		custom["repo_base"] = repoBase
		root.ClearSignatures()
		signer, err := signature.LoadSigner(priv, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := root.Sign(signer); err != nil {
			t.Fatal(err)
		}
		out, err := root.ToBytes(true)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// channelKey reads a demo channel key's Ed25519 private key.
func channelKey(t *testing.T, dir, channel string) (ed25519.PrivateKey, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "keys", channel+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		SeedHex string `json:"seed_hex"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	seed, err := hex.DecodeString(record.SeedHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("channel seed: %v", err)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	key, err := metadata.KeyFromPublicKey(priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	id, err := key.ID()
	if err != nil {
		t.Fatal(err)
	}
	return priv, id
}

// fakePushService is a stand-in for the browser vendor's push service: it
// records the encrypted RFC 8291 request so the test can decrypt and verify it.
type fakePushService struct {
	mu      sync.Mutex
	body    []byte
	headers http.Header
	ttl     string
	urgency string
	ready   chan struct{}
}

func (f *fakePushService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	f.mu.Lock()
	f.body = body
	f.headers = r.Header.Clone()
	f.ttl = r.Header.Get("TTL")
	f.urgency = r.Header.Get("Urgency")
	f.mu.Unlock()
	select {
	case <-f.ready:
	default:
		close(f.ready)
	}
	w.WriteHeader(http.StatusCreated)
}

// decryptRFC8291 decrypts the single-record aes128gcm body with the UA key.
func decryptRFC8291(t *testing.T, ua *ecdh.PrivateKey, auth, body []byte) []byte {
	t.Helper()
	if len(body) < 86 {
		t.Fatalf("body too short: %d", len(body))
	}
	salt := body[:16]
	if rs := body[16:20]; rs[0] != 0 || rs[1] != 0 || rs[2] != 0x10 || rs[3] != 0 {
		t.Fatalf("bad rs: %x", rs)
	}
	idlen := body[20]
	if idlen != 65 {
		t.Fatalf("bad keyid length: %d", idlen)
	}
	asPub := body[21 : 21+65]
	ct := body[21+65:]

	asPoint, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := ua.ECDH(asPoint)
	if err != nil {
		t.Fatal(err)
	}
	prkKey, err := hkdf.Extract(sha256.New, shared, auth)
	if err != nil {
		t.Fatal(err)
	}
	info := append([]byte("WebPush: info\x00"), ua.PublicKey().Bytes()...)
	info = append(info, asPub...)
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(info), 32)
	if err != nil {
		t.Fatal(err)
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		t.Fatal(err)
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		t.Fatalf("missing padding delimiter")
	}
	return plain[:len(plain)-1]
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestEndToEndDemoRepository(t *testing.T) {
	src := demoDir()
	if _, err := os.Stat(filepath.Join(src, ".well-known", "keryx", "root.json")); err != nil {
		t.Skipf("demo repository not present at %s: %v", src, err)
	}
	logger := discardLogger()

	// Serve a copy of the demo repository over local HTTPS with a test CA,
	// pointed at itself via custom.repo_base.
	serveDir := t.TempDir()
	repoSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	repoBase := repoSrv.URL + "/keryx/"
	repoSrv.Config.Handler = http.FileServer(http.Dir(serveDir))
	defer repoSrv.Close()
	resealRoot(t, src, serveDir, repoBase)

	pool := x509Pool(t, repoSrv.Certificate())

	// The fake push service stands in for the browser vendor's push service.
	fake := &fakePushService{ready: make(chan struct{})}
	pushSrv := httptest.NewTLSServer(fake)
	defer pushSrv.Close()
	pushOrigin := pushSrv.URL

	policy := netpolicy.New()
	policy.AllowPrivate = true
	policy.RootCAs = pool

	st, err := store.Open(":memory:", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	companyID := "127.0.0.1"
	client := tufclient.New(policy)
	client.TestWellKnown = map[string]string{
		companyID: repoSrv.URL + "/.well-known/keryx/",
	}
	companies := companytuf.New(st, client, companytuf.Options{Logger: logger})
	if err := companies.Load(); err != nil {
		t.Fatal(err)
	}
	companies.Start()
	defer companies.Stop()

	vapid, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wp, _, err := push.NewWebPush(
		base64.RawURLEncoding.EncodeToString(vapid.Bytes()),
		"mailto:ops@example.com", time.Hour, policy.HTTPClient(false))
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := relay.New(st, nil, wp, relay.Options{Logger: logger})
	srv := api.New(st, dispatcher, companies, api.Options{
		Policy:              policy,
		ApprovedPushOrigins: []string{pushOrigin},
		Logger:              logger,
	})
	handler := srv.Handler()

	// 1. Register the demo repository (unsigned hint → TOFU + TUF update).
	rec := post(t, handler, "/v1/companies/"+companyID+"/refresh", "", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("refresh = %d: %s", rec.Code, rec.Body)
	}
	waitForRefresh(t, st, companyID)

	// 2. Derive the public "security" scope and its topic from the verified
	// demo targets, exactly as the app and publisher tooling do.
	persisted, err := st.Company(companyID)
	if err != nil {
		t.Fatal(err)
	}
	table, err := persisted.State().Table()
	if err != nil {
		t.Fatal(err)
	}
	scopeID, err := scope.PublicScopeID("security")
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := table.Lookup(scopeID)
	if !ok {
		t.Fatalf("security scope %s missing from %d scopes", scopeID, table.Len())
	}
	priv, keyID := channelKey(t, serveDir, "security")
	if _, ok := entry.Keys[keyID]; !ok {
		t.Fatalf("channel key %s is not authorized for scope %s", keyID, scopeID)
	}
	h := topic.SourceHash(companyID + "|security")
	derived, err := topic.Derive(companyID, scopeID, h)
	if err != nil {
		t.Fatal(err)
	}

	// 3. Register the PWA's WebPush subscription.
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	regBody, _ := json.Marshal(map[string]any{
		"endpoint": pushSrv.URL + "/push",
		"keys": map[string]string{
			"p256dh": base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes()),
			"auth":   base64.RawURLEncoding.EncodeToString(auth),
		},
		"topics": []string{derived},
	})
	rec = post(t, handler, "/v1/registrations", string(regBody), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("registration = %d: %s", rec.Code, rec.Body)
	}

	// 4. Sign and publish a wake-up as the security channel key.
	seq := time.Now().Unix()
	msg, err := wakeup.SignedBytes(1, derived, seq)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, msg)
	pubBody, _ := json.Marshal(map[string]any{
		"v":          1,
		"company_id": companyID,
		"scope_id":   scopeID,
		"h":          h,
		"seq":        seq,
		"sig": []map[string]string{
			{"keyid": keyID, "sig": base64.RawURLEncoding.EncodeToString(sig)},
		},
	})
	rec = post(t, handler, "/v1/publish", string(pubBody), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("publish = %d: %s", rec.Code, rec.Body)
	}
	var result struct {
		Topic      string `json:"topic"`
		Suppressed bool   `json:"suppressed"`
		Providers  struct {
			WebPush struct {
				Sent int `json:"sent"`
			} `json:"webpush"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Topic != derived || result.Providers.WebPush.Sent != 1 {
		t.Fatalf("publish result = %s", rec.Body)
	}

	// 5. The push service must have received one RFC 8291 payload that
	// decrypts to the signed wake-up envelope.
	select {
	case <-fake.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("push service received nothing")
	}
	fake.mu.Lock()
	body, headers := fake.body, fake.headers
	ttl, urgency := fake.ttl, fake.urgency
	fake.mu.Unlock()
	if ttl != "3600" || urgency != "normal" {
		t.Fatalf("ttl = %q urgency = %q", ttl, urgency)
	}
	if !strings.HasPrefix(headers.Get("Authorization"), "vapid t=") {
		t.Fatalf("authorization = %q", headers.Get("Authorization"))
	}
	plain := decryptRFC8291(t, uaPriv, auth, body)
	w, err := wakeup.Parse(plain)
	if err != nil {
		t.Fatalf("decrypted payload: %v", err)
	}
	if w.V != 1 || w.T != derived || w.Seq != seq {
		t.Fatalf("wake-up = %+v", w)
	}
	if err := wakeup.Verify(w.V, w.T, w.Seq, w.Sig, entry.Keys, entry.Threshold); err != nil {
		t.Fatalf("wake-up signature: %v", err)
	}
	if w.Sig[0].KeyID != keyID {
		t.Fatalf("wake-up keyid = %s", w.Sig[0].KeyID)
	}

	// Emit the cross-stack fixture consumed by the PWA's relay tests when
	// KERYX_WRITE_FIXTURES is set (derivation + verified wake-up).
	if os.Getenv("KERYX_WRITE_FIXTURES") == "1" {
		writeFixture(t, map[string]any{
			"companyId": companyID,
			"scopeId":   scopeID,
			"h":         h,
			"topic":     derived,
			"seq":       seq,
			"wakeup":    json.RawMessage(plain),
			"keys":      keyList(entry),
			"threshold": entry.Threshold,
		})
	}

	// 6. A replayed seq is suppressed and never dispatched again.
	rec = post(t, handler, "/v1/publish", string(pubBody), nil)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"suppressed":true`)) {
		t.Fatalf("replay = %d: %s", rec.Code, rec.Body)
	}
}

func post(t *testing.T, h http.Handler, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func waitForRefresh(t *testing.T, st *store.Store, companyID string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := st.Company(companyID); err == nil && !c.RefreshedAt.IsZero() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("company refresh did not complete")
}

func x509Pool(t *testing.T, cert *x509.Certificate) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool
}

// keyList returns the scope's authorized keys as {keyid, pub} hex records.
func keyList(entry *scope.Entry) []map[string]string {
	out := make([]map[string]string, 0, len(entry.Keys))
	for keyid, pub := range entry.Keys {
		out = append(out, map[string]string{"keyid": keyid, "pub": hex.EncodeToString(pub)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["keyid"] < out[j]["keyid"] })
	return out
}

// writeFixture writes the cross-stack fixture into the PWA's test fixtures.
func writeFixture(t *testing.T, v any) {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "app", "src", "lib", "__fixtures__")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "relay-e2e.json"), append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
