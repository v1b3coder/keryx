package api

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v1b3coder/keryx/relay/internal/companytuf"
	"github.com/v1b3coder/keryx/relay/internal/netpolicy"
	"github.com/v1b3coder/keryx/relay/internal/push"
	"github.com/v1b3coder/keryx/relay/internal/pushtest"
	"github.com/v1b3coder/keryx/relay/internal/relay"
	"github.com/v1b3coder/keryx/relay/internal/store"
	"github.com/v1b3coder/keryx/relay/internal/topic"
)

const (
	testEndpoint = "http://127.0.0.1:9999/x"
	testAuth     = "BTBZMqHH6r4Tts7J_aSIgg"
)

func testP256DH(t *testing.T) string {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())
}

// validTopic returns a canonical 43-char base64url topic (32 zero bytes).
func validTopic() string { return base64.RawURLEncoding.EncodeToString(make([]byte, 32)) }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestServer(t *testing.T, debug bool) (*Server, *store.Store) {
	t.Helper()
	return newTestServerWith(t, debug, []string{"http://127.0.0.1:9999"}, false)
}

func newTestServerWith(t *testing.T, debug bool, origins []string, anyOrigin bool) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	policy := netpolicy.New()
	policy.AllowPrivate = true
	policy.AllowHTTP = true
	d := relay.New(st, nil, nil, relay.Options{FastPathMax: 1, Logger: discardLogger()})
	companies := companytuf.New(st, nil, companytuf.Options{Logger: discardLogger()})
	srv := New(st, d, companies, Options{
		Debug:               debug,
		DebugAPIKey:         "debug-secret",
		Policy:              policy,
		ApprovedPushOrigins: origins,
		PushOriginsAny:      anyOrigin,
		Logger:              discardLogger(),
	})
	return srv, st
}

func do(t *testing.T, h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRegistrationLifecycle(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()

	body := `{"endpoint":"` + testEndpoint + `","keys":{"p256dh":"` + testP256DH(t) + `","auth":"` + testAuth + `"},"topics":["` + validTopic() + `"]}`
	rec := do(t, h, http.MethodPost, "/v1/registrations", body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}
	var created struct {
		ID    string `json:"id"`
		Token string `json:"management_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || len(created.Token) != 43 {
		t.Fatalf("created = %+v", created)
	}
	// Duplicate endpoint conflicts and never reveals a token.
	rec = do(t, h, http.MethodPost, "/v1/registrations", body, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate = %d: %s", rec.Code, rec.Body)
	}
	// Mutation requires the management token.
	rec = do(t, h, http.MethodPut, "/v1/registrations/"+created.ID,
		`{"topics":["`+validTopic()+`"]}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated update = %d", rec.Code)
	}
	auth := map[string]string{"Authorization": "Bearer " + created.Token}
	rec = do(t, h, http.MethodPut, "/v1/registrations/"+created.ID,
		`{"topics":["`+validTopic()+`"]}`, auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodPost, "/v1/registrations/"+created.ID+"/heartbeat", "", auth)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("heartbeat = %d", rec.Code)
	}
	rec = do(t, h, http.MethodDelete, "/v1/registrations/"+created.ID, "", auth)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	rec = do(t, h, http.MethodDelete, "/v1/registrations/"+created.ID, "", auth)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete twice = %d", rec.Code)
	}
}

func TestRegistrationValidation(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()
	p256 := testP256DH(t)
	auth := testAuth
	topic := validTopic()

	tests := []struct {
		name string
		body string
		code int
	}{
		{"ok", `{"endpoint":"` + testEndpoint + `","keys":{"p256dh":"` + p256 + `","auth":"` + auth + `"},"topics":[]}`, http.StatusOK},
		{"http endpoint", `{"endpoint":"http://evil.example/x","keys":{"p256dh":"` + p256 + `","auth":"` + auth + `"},"topics":[]}`, http.StatusBadRequest},
		{"unapproved origin", `{"endpoint":"https://evil.example/x","keys":{"p256dh":"` + p256 + `","auth":"` + auth + `"},"topics":[]}`, http.StatusBadRequest},
		{"bad p256dh", `{"endpoint":"` + testEndpoint + `","keys":{"p256dh":"AAAA","auth":"` + auth + `"},"topics":[]}`, http.StatusBadRequest},
		{"bad auth", `{"endpoint":"` + testEndpoint + `","keys":{"p256dh":"` + p256 + `","auth":"AAAA"},"topics":[]}`, http.StatusBadRequest},
		{"bad topic", `{"endpoint":"` + testEndpoint + `","keys":{"p256dh":"` + p256 + `","auth":"` + auth + `"},"topics":["n-abc"]}`, http.StatusBadRequest},
		{"unknown field", `{"endpoint":"` + testEndpoint + `","keys":{"p256dh":"` + p256 + `","auth":"` + auth + `"},"topics":[],"x":1}`, http.StatusBadRequest},
		{"duplicate member", `{"endpoint":"` + testEndpoint + `","endpoint":"` + testEndpoint + `","keys":{"p256dh":"` + p256 + `","auth":"` + auth + `"},"topics":[]}`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, "/v1/registrations", tc.body, nil)
			if rec.Code != tc.code {
				t.Fatalf("code = %d, want %d: %s", rec.Code, tc.code, rec.Body)
			}
		})
	}
	_ = topic
}

func TestPushOriginMatching(t *testing.T) {
	tests := []struct {
		entry, origin string
		want          bool
	}{
		{"https://ntfy.sh", "https://ntfy.sh", true},
		{"https://ntfy.sh", "https://evil.example", false},
		{"https://*.push.apple.com", "https://web.push.apple.com", true},
		{"https://*.push.apple.com", "https://push.apple.com", true},
		{"https://*.push.apple.com", "https://evilpush.apple.com", false},
		{"https://*.push.apple.com", "https://push.apple.com.evil.example", false},
		{"https://*.push.apple.com", "http://web.push.apple.com", false},
		{"https://*.notify.windows.com", "https://wns2-par02p.notify.windows.com", true},
	}
	for _, tc := range tests {
		if got := originMatches(tc.entry, tc.origin); got != tc.want {
			t.Errorf("originMatches(%q, %q) = %v, want %v", tc.entry, tc.origin, got, tc.want)
		}
	}

	// The seed list must approve the real browser push services.
	srv, _ := newTestServer(t, false)
	for _, origin := range []string{
		"https://web.push.apple.com",
		"https://wns2-par02p.notify.windows.com",
		"https://jmt17.google.com",
		"https://fcm.googleapis.com",
		"https://updates.push.services.mozilla.com",
		"https://ntfy.sh",
	} {
		if !srv.pushOriginApproved(origin) {
			t.Errorf("seed list does not approve %s", origin)
		}
	}
}

func TestAnyPushOriginMode(t *testing.T) {
	srv, _ := newTestServerWith(t, false, nil, true)
	h := srv.Handler()
	body := `{"endpoint":"http://127.0.0.1:9999/x","keys":{"p256dh":"` + testP256DH(t) + `","auth":"` + testAuth + `"},"topics":[]}`
	rec := do(t, h, http.MethodPost, "/v1/registrations", body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("any mode = %d: %s", rec.Code, rec.Body)
	}
	// The outbound policy still applies: non-HTTPS endpoints stay rejected.
	body = `{"endpoint":"ftp://127.0.0.1:9999/x","keys":{"p256dh":"` + testP256DH(t) + `","auth":"` + testAuth + `"},"topics":[]}`
	rec = do(t, h, http.MethodPost, "/v1/registrations", body, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("any mode non-https = %d: %s", rec.Code, rec.Body)
	}
}

func TestRemoteIP(t *testing.T) {
	srv, _ := newTestServer(t, false)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	if got := srv.remoteIP(req); got != "203.0.113.9" {
		t.Fatalf("peer IP = %q", got)
	}
	srv.clientIPHeader = "Fly-Client-IP"
	req.Header.Set("Fly-Client-IP", "198.51.100.7")
	if got := srv.remoteIP(req); got != "198.51.100.7" {
		t.Fatalf("header IP = %q", got)
	}
	req.Header.Set("Fly-Client-IP", "not-an-ip")
	if got := srv.remoteIP(req); got != "203.0.113.9" {
		t.Fatalf("invalid header IP = %q", got)
	}
	req.Header.Set("Fly-Client-IP", "")
	if got := srv.remoteIP(req); got != "203.0.113.9" {
		t.Fatalf("empty header IP = %q", got)
	}
}

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t, false)
	rec := do(t, srv.Handler(), http.MethodGet, "/healthz", "", nil)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"status":"ok"`)) {
		t.Fatalf("healthz = %d: %s", rec.Code, rec.Body)
	}
	srvDebug, _ := newTestServer(t, true)
	rec = do(t, srvDebug.Handler(), http.MethodGet, "/healthz", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("debug healthz = %d", rec.Code)
	}
}

func TestLastActivity(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()
	before := srv.LastActivity()
	rec := do(t, h, http.MethodGet, "/healthz", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rec.Code)
	}
	if got := srv.LastActivity(); got.After(before) {
		t.Fatalf("health check counted as activity: %v -> %v", before, got)
	}
	rec = do(t, h, http.MethodPost, "/v1/registrations", `{}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("registration = %d", rec.Code)
	}
	if got := srv.LastActivity(); !got.After(before) {
		t.Fatal("request did not count as activity")
	}
}

func TestPublishValidation(t *testing.T) {
	srv, st := newTestServer(t, false)
	h := srv.Handler()
	h64 := strings.Repeat("a", 64)
	s64 := strings.Repeat("z", 64)
	sig := `[{"keyid":"` + strings.Repeat("c", 64) + `","sig":"AAAA"}]`

	// Unknown company: 404, no TOFU on the publish path.
	rec := do(t, h, http.MethodPost, "/v1/publish",
		`{"v":1,"company_id":"company.example","scope_id":"`+h64+`","h":"`+h64+`","seq":1,"sig":`+sig+`}`, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown company = %d: %s", rec.Code, rec.Body)
	}
	// A known company without usable authorization fails closed with 503.
	if err := st.SaveRoot("company.example", []byte("root"), 1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := srv.companies.Load(); err != nil {
		t.Fatal(err)
	}
	rec = do(t, h, http.MethodPost, "/v1/publish",
		`{"v":1,"company_id":"company.example","scope_id":"`+h64+`","h":"`+h64+`","seq":1,"sig":`+sig+`}`, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("known company without table = %d: %s", rec.Code, rec.Body)
	}
	// Bad bodies.
	for _, body := range []string{
		`{}`,
		`{"v":2,"company_id":"company.example","scope_id":"` + h64 + `","h":"` + h64 + `","seq":1,"sig":` + sig + `}`,
		`{"v":1,"company_id":"Company.Example","scope_id":"` + h64 + `","h":"` + h64 + `","seq":1,"sig":` + sig + `}`,
		`{"v":1,"company_id":"company.example","scope_id":"` + s64 + `","h":"` + h64 + `","seq":1,"sig":` + sig + `}`,
		`{"v":1,"company_id":"company.example","scope_id":"` + h64 + `","h":"` + h64 + `","seq":0,"sig":` + sig + `}`,
		`{"v":1,"company_id":"company.example","scope_id":"` + h64 + `","h":"` + h64 + `","seq":1,"sig":[]}`,
	} {
		rec := do(t, h, http.MethodPost, "/v1/publish", body, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s = %d: %s", body, rec.Code, rec.Body)
		}
	}
	// A seq implausibly far in the future is rejected.
	rec = do(t, h, http.MethodPost, "/v1/publish",
		`{"v":1,"company_id":"company.example","scope_id":"`+h64+`","h":"`+h64+`","seq":9007199254740991,"sig":`+sig+`}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("future seq = %d: %s", rec.Code, rec.Body)
	}
}

func TestProbeUnknown(t *testing.T) {
	srv, _ := newTestServer(t, false)
	rec := do(t, srv.Handler(), http.MethodGet, "/v1/publishes/"+validTopic(), "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("probe = %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache-control = %q", cc)
	}
}

func TestCompanyRefresh(t *testing.T) {
	srv, _ := newTestServer(t, false)
	h := srv.Handler()
	rec := do(t, h, http.MethodPost, "/v1/companies/company.example/refresh", "", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("refresh = %d: %s", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodPost, "/v1/companies/Company.Example/refresh", "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("noncanonical refresh = %d", rec.Code)
	}
}

func TestDebugModeIsolation(t *testing.T) {
	srv, _ := newTestServer(t, true)
	h := srv.Handler()

	// Production publish and company-sync routes are not mounted.
	rec := do(t, h, http.MethodPost, "/v1/publish", `{}`, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("production publish in debug mode = %d", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/v1/companies/company.example/refresh", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("company refresh in debug mode = %d", rec.Code)
	}
	// Debug publish requires its configured API key.
	h64 := strings.Repeat("a", 64)
	derived, err := topic.Derive("company.example", h64, h64)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"v":1,"company_id":"company.example","scope_id":"` + h64 + `","h":"` + h64 + `","seq":1}`
	rec = do(t, h, http.MethodPost, "/debug/v1/publish", body, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated debug publish = %d", rec.Code)
	}
	// A small fan-out dispatches synchronously.
	rec = do(t, h, http.MethodPost, "/debug/v1/publish", body,
		map[string]string{"Authorization": "Bearer debug-secret"})
	if rec.Code != http.StatusOK {
		t.Fatalf("debug publish = %d: %s", rec.Code, rec.Body)
	}
	// Two registrations exceed the fast-path threshold, so the publish is
	// accepted asynchronously and can be probed by its capability.
	for i := 0; i < 2; i++ {
		reg := `{"endpoint":"http://127.0.0.1:9999/r` + string(rune('a'+i)) + `","keys":{"p256dh":"` + testP256DH(t) + `","auth":"` + testAuth + `"},"topics":["` + derived + `"]}`
		rec := do(t, h, http.MethodPost, "/v1/registrations", reg, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("registration = %d: %s", rec.Code, rec.Body)
		}
	}
	// A higher seq is accepted asynchronously (a replacement always uses the
	// async response) and can be probed by its capability.
	body = `{"v":1,"company_id":"company.example","scope_id":"` + h64 + `","h":"` + h64 + `","seq":2}`
	rec = do(t, h, http.MethodPost, "/debug/v1/publish", body,
		map[string]string{"Authorization": "Bearer debug-secret"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("async debug publish = %d: %s", rec.Code, rec.Body)
	}
	var accepted struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	// The debug status capability works without an API key and never caches.
	var probe *httptest.ResponseRecorder
	for i := 0; i < 100; i++ {
		probe = do(t, h, http.MethodGet, "/debug/v1/publishes/"+accepted.RequestID, "", nil)
		if probe.Code == http.StatusOK && bytes.Contains(probe.Body.Bytes(), []byte(`"status":"complete"`)) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if probe.Code != http.StatusOK || !bytes.Contains(probe.Body.Bytes(), []byte(`"status":"complete"`)) {
		t.Fatalf("probe = %d: %s", probe.Code, probe.Body)
	}
	if cc := probe.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache-control = %q", cc)
	}
}

// decryptRFC8291 decrypts the single-record aes128gcm body with the UA key.
func decryptRFC8291(t *testing.T, ua *ecdh.PrivateKey, auth, body []byte) []byte {
	t.Helper()
	plain, err := pushtest.DecryptRFC8291(ua, auth, body)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}

func TestRegistrationSelfTest(t *testing.T) {
	st, err := store.Open(":memory:", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	// a fake push service: it records the RFC 8291 body and returns 201
	var mu sync.Mutex
	var body []byte
	pushSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		body = b
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer pushSrv.Close()

	policy := netpolicy.New()
	policy.AllowPrivate = true
	policy.AllowHTTP = true
	vapid, _ := ecdh.P256().GenerateKey(rand.Reader)
	wp, _, err := push.NewWebPush(base64.RawURLEncoding.EncodeToString(vapid.Bytes()),
		"mailto:ops@example.com", time.Hour, policy.HTTPClient(false))
	if err != nil {
		t.Fatal(err)
	}
	d := relay.New(st, nil, wp, relay.Options{Logger: discardLogger()})
	srv := New(st, d, companytuf.New(st, nil, companytuf.Options{Logger: discardLogger()}), Options{
		Policy:              policy,
		ApprovedPushOrigins: []string{pushSrv.URL},
		Logger:              discardLogger(),
	})
	h := srv.Handler()

	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)
	regBody, _ := json.Marshal(map[string]any{
		"endpoint": pushSrv.URL + "/push",
		"keys": map[string]string{
			"p256dh": base64.RawURLEncoding.EncodeToString(ua.PublicKey().Bytes()),
			"auth":   base64.RawURLEncoding.EncodeToString(auth),
		},
		"topics": []string{validTopic()},
	})
	rec := do(t, h, http.MethodPost, "/v1/registrations", string(regBody), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}
	var created struct {
		ID    string `json:"id"`
		Token string `json:"management_token"`
	}
	json.Unmarshal(rec.Body.Bytes(), &created)

	rec = do(t, h, http.MethodPost, "/v1/registrations/"+created.ID+"/test", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token = %d", rec.Code)
	}
	authHeader := map[string]string{"Authorization": "Bearer " + created.Token}
	rec = do(t, h, http.MethodPost, "/v1/registrations/"+created.ID+"/test", "", authHeader)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("test = %d: %s", rec.Code, rec.Body)
	}
	var result struct {
		Nonce     string `json:"nonce"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Nonce) != 43 || result.ExpiresAt == "" {
		t.Fatalf("result = %s", rec.Body)
	}
	mu.Lock()
	got := body
	mu.Unlock()
	if len(got) < 86 {
		t.Fatalf("push service received %d bytes", len(got))
	}
	// the payload is the §4.3 JSON, not a wake-up: decrypt it as in the e2e test
	plain := decryptRFC8291(t, ua, auth, got)
	var testPayload struct {
		V     int    `json:"v"`
		Test  bool   `json:"test"`
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal(plain, &testPayload); err != nil {
		t.Fatal(err)
	}
	if testPayload.V != 1 || !testPayload.Test || testPayload.Nonce != result.Nonce {
		t.Fatalf("payload = %s", plain)
	}

	rec = do(t, h, http.MethodPost, "/v1/registrations/missing/test", "", authHeader)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing = %d", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/v1/registrations/"+created.ID+"/test", "", map[string]string{"Authorization": "Bearer wrong"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d", rec.Code)
	}
}

// rewriteTransport sends every request to one test server: the FCM leg's OAuth2
// token call and its publish call both go there.
type rewriteTransport struct{ target *url.URL }

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = rt.target.Scheme
	req.URL.Host = rt.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

// fakeFCMServer answers the OAuth2 token call and the FCM publish call, and
// records every published message.
func fakeFCMServer(t *testing.T, published *[]map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_in": 3600})
			return
		}
		var msg map[string]any
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Errorf("decode FCM body: %v", err)
		}
		*published = append(*published, msg)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"name":"projects/keryx-test/messages/1"}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeFCM builds an FCM leg whose token_uri and publish endpoint both point
// at the test server.
func fakeFCM(t *testing.T, srv *httptest.Server) *push.FCM {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	sa := map[string]string{
		"type":         "service_account",
		"project_id":   "keryx-test",
		"client_email": "relay@keryx-test.iam.gserviceaccount.com",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"token_uri":    srv.URL + "/token",
	}
	raw, _ := json.Marshal(sa)
	path := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	fcm, err := push.NewFCM(path, &http.Client{Transport: rewriteTransport{target: target}})
	if err != nil {
		t.Fatal(err)
	}
	return fcm
}

// fcmTestServer builds an API server whose dispatcher has the given FCM leg.
func fcmTestServer(t *testing.T, fcm *push.FCM, ttl time.Duration) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	policy := netpolicy.New()
	policy.AllowPrivate = true
	policy.AllowHTTP = true
	d := relay.New(st, fcm, nil, relay.Options{FastPathMax: 1, Logger: discardLogger()})
	companies := companytuf.New(st, nil, companytuf.Options{Logger: discardLogger()})
	srv := New(st, d, companies, Options{
		Policy:  policy,
		TestTTL: ttl,
		Logger:  discardLogger(),
	})
	return srv, st
}

func TestFCMTestHandshake(t *testing.T) {
	var published []map[string]any
	fcmSrv := fakeFCMServer(t, &published)
	srv, _ := fcmTestServer(t, fakeFCM(t, fcmSrv), time.Minute)
	h := srv.Handler()

	rec := do(t, h, "POST", "/v1/fcm/test", "", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}
	var created struct {
		TestID    string `json:"test_id"`
		Topic     string `json:"topic"`
		Nonce     string `json:"nonce"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"test_id": created.TestID, "topic": created.Topic, "nonce": created.Nonce} {
		if len(value) != 43 {
			t.Fatalf("%s = %q: want 43 chars", name, value)
		}
	}
	if created.ExpiresAt == "" {
		t.Fatal("no expires_at")
	}

	rec = do(t, h, "POST", "/v1/fcm/test/"+created.TestID+"/ready", "", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("ready = %d: %s", rec.Code, rec.Body)
	}
	if len(published) != 1 {
		t.Fatalf("published = %d, want 1", len(published))
	}
	message, _ := published[0]["message"].(map[string]any)
	if message["topic"] != created.Topic {
		t.Fatalf("message = %+v: wrong topic", message)
	}
	raw, _ := message["data"].(map[string]any)["test"].(string)
	var payload struct {
		V     int    `json:"v"`
		Test  bool   `json:"test"`
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.V != 1 || !payload.Test || payload.Nonce != created.Nonce {
		t.Fatalf("payload = %+v, want nonce %q", payload, created.Nonce)
	}
}

func TestFCMTestUnknownCapability(t *testing.T) {
	var published []map[string]any
	fcmSrv := fakeFCMServer(t, &published)
	srv, _ := fcmTestServer(t, fakeFCM(t, fcmSrv), time.Minute)
	h := srv.Handler()
	rec := do(t, h, "POST", "/v1/fcm/test/"+strings.Repeat("A", 43)+"/ready", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("ready = %d: %s", rec.Code, rec.Body)
	}
	if len(published) != 0 {
		t.Fatalf("published = %d, want 0", len(published))
	}
}

func TestFCMTestExpiredCapability(t *testing.T) {
	var published []map[string]any
	fcmSrv := fakeFCMServer(t, &published)
	srv, _ := fcmTestServer(t, fakeFCM(t, fcmSrv), time.Nanosecond)
	h := srv.Handler()
	rec := do(t, h, "POST", "/v1/fcm/test", "", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}
	var created struct {
		TestID string `json:"test_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	rec = do(t, h, "POST", "/v1/fcm/test/"+created.TestID+"/ready", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("ready = %d: %s", rec.Code, rec.Body)
	}
	if len(published) != 0 {
		t.Fatalf("published = %d, want 0", len(published))
	}
}

func TestFCMTestLegDisabled(t *testing.T) {
	srv, _ := fcmTestServer(t, nil, time.Minute)
	h := srv.Handler()
	rec := do(t, h, "POST", "/v1/fcm/test", "", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}
	var created struct {
		TestID string `json:"test_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	rec = do(t, h, "POST", "/v1/fcm/test/"+created.TestID+"/ready", "", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready = %d: %s", rec.Code, rec.Body)
	}
}
