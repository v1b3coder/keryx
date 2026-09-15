package api

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/v1b3coder/keryx/relay/internal/companytuf"
	"github.com/v1b3coder/keryx/relay/internal/netpolicy"
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
		ApprovedPushOrigins: []string{"http://127.0.0.1:9999"},
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
