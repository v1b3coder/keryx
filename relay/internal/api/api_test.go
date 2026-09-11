package api

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/v1b3coder/keryx/relay/internal/relay"
	"github.com/v1b3coder/keryx/relay/internal/store"
	"github.com/v1b3coder/keryx/relay/internal/topic"
)

// fakes implementing the (unexported) relay leg interfaces.

type apiFakeFCM struct {
	mu      sync.Mutex
	err     error
	gate    chan struct{} // if set, Send blocks until closed
	started chan struct{} // closed on first Send
}

func (f *apiFakeFCM) Send(_ context.Context, _ string, _ map[string]string) error {
	if f.started != nil {
		f.mu.Lock()
		if f.started != nil {
			close(f.started)
			f.started = nil
		}
		f.mu.Unlock()
	}
	if f.gate != nil {
		<-f.gate
	}
	return f.err
}

type apiFakeNtfy struct{ err error }

func (f *apiFakeNtfy) Send(_ context.Context, _ string, _ []byte) error { return f.err }

type apiFakeWP struct {
	mu    sync.Mutex
	perEp map[string]error
}

func (f *apiFakeWP) Send(_ context.Context, endpoint, _, _ string, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.perEp[endpoint]
}

type testEnv struct {
	server *httptest.Server
	store  *store.Store
	apiKey string
}

// newEnv builds a full API server with fake legs and provisions one publisher.
func newEnv(t *testing.T, opts Options, fcm *apiFakeFCM, ntfy *apiFakeNtfy, wp *apiFakeWP, ratePerMin int) *testEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key, err := st.CreatePublisher("Acme", "company.example", ratePerMin)
	if err != nil {
		t.Fatal(err)
	}
	d := relay.New(st, fcm, ntfy, wp, 4, slog.New(slog.DiscardHandler))
	opts.Logger = slog.New(slog.DiscardHandler)
	srv := New(st, d, opts)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &testEnv{server: ts, store: st, apiKey: key}
}

func (e *testEnv) do(method, path string, body any, auth, appKey string) *http.Response {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, e.server.URL+path, &buf)
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	if appKey != "" {
		req.Header.Set("X-App-Key", appKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	return resp
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer resp.Body.Close()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func goodH(t *testing.T) string {
	t.Helper()
	return topic.SourceHash("company.example|marketing")
}

func TestPublishAuth(t *testing.T) {
	e := newEnv(t, Options{}, &apiFakeFCM{}, &apiFakeNtfy{}, nil, 60)
	body := map[string]any{"v": 1, "h": goodH(t)}

	resp := e.do("POST", "/v1/publish", body, "", "")
	if resp.StatusCode != 401 {
		t.Fatalf("no auth = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
	resp = e.do("POST", "/v1/publish", body, "wrong-key", "")
	if resp.StatusCode != 401 {
		t.Fatalf("bad key = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
	resp = e.do("POST", "/v1/publish", body, e.apiKey, "")
	if resp.StatusCode != 200 {
		t.Fatalf("good key = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPublishValidation(t *testing.T) {
	e := newEnv(t, Options{}, &apiFakeFCM{}, &apiFakeNtfy{}, nil, 60)
	cases := []struct {
		name string
		body map[string]any
	}{
		{"missing v", map[string]any{"h": goodH(t)}},
		{"wrong v", map[string]any{"v": 2, "h": goodH(t)}},
		{"short h", map[string]any{"v": 1, "kind": "channel", "h": "short"}},
		{"bad h charset", map[string]any{"v": 1, "kind": "channel", "h": "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!"}},
		{"negative n", map[string]any{"v": 1, "kind": "channel", "h": goodH(t), "n": -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := e.do("POST", "/v1/publish", tc.body, e.apiKey, "")
			defer resp.Body.Close()
			if resp.StatusCode != 400 {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}

func TestPublishRateLimit(t *testing.T) {
	e := newEnv(t, Options{}, &apiFakeFCM{}, &apiFakeNtfy{}, nil, 1) // 1/min, burst 2
	body := map[string]any{"v": 1, "h": goodH(t)}
	for i := 0; i < 2; i++ {
		resp := e.do("POST", "/v1/publish", body, e.apiKey, "")
		if resp.StatusCode != 200 {
			t.Fatalf("request %d = %d, want 200", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp := e.do("POST", "/v1/publish", body, e.apiKey, "")
	defer resp.Body.Close()
	if resp.StatusCode != 429 {
		t.Fatalf("third request = %d, want 429", resp.StatusCode)
	}
}

func TestPublishHappyPath(t *testing.T) {
	fcm := &apiFakeFCM{}
	ntfy := &apiFakeNtfy{}
	wp := &apiFakeWP{perEp: map[string]error{
		"https://push.example/a": nil,
		"https://push.example/b": nil,
	}}
	e := newEnv(t, Options{}, fcm, ntfy, wp, 60)
	h := goodH(t)
	tpc, _ := topic.Topic(h)
	e.store.UpsertRegistration("https://push.example/a", "p", "a", "", []string{tpc})
	e.store.UpsertRegistration("https://push.example/b", "p", "a", "", []string{tpc})

	resp := e.do("POST", "/v1/publish", map[string]any{
		"v": 1, "h": h, "n": 3, "seq": 8,
	}, e.apiKey, "")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	out := decode[struct {
		Topic     string `json:"topic"`
		Delivered struct {
			FCM     int `json:"fcm"`
			Ntfy    int `json:"ntfy"`
			WebPush struct {
				Sent    int `json:"sent"`
				Failed  int `json:"failed"`
				Removed int `json:"removed"`
			} `json:"webpush"`
		} `json:"delivered"`
	}](t, resp)
	if out.Topic != tpc {
		t.Fatalf("topic = %q, want %q", out.Topic, tpc)
	}
	if out.Delivered.FCM != 1 || out.Delivered.Ntfy != 1 {
		t.Fatalf("fcm/ntfy = %d/%d", out.Delivered.FCM, out.Delivered.Ntfy)
	}
	if out.Delivered.WebPush.Sent != 2 || out.Delivered.WebPush.Failed != 0 || out.Delivered.WebPush.Removed != 0 {
		t.Fatalf("webpush = %+v", out.Delivered.WebPush)
	}
}

func TestPublish202UnderLoad(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{})
	fcm := &apiFakeFCM{gate: gate, started: started}
	e := newEnv(t, Options{MaxConcurrent: 1, QueueSize: 8}, fcm, &apiFakeNtfy{}, nil, 60)

	body := map[string]any{"v": 1, "h": goodH(t)}
	firstDone := make(chan int, 1)
	go func() {
		resp := e.do("POST", "/v1/publish", body, e.apiKey, "")
		firstDone <- resp.StatusCode
		resp.Body.Close()
	}()
	// Wait until the first request holds the only slot (fcm gate blocks it).
	<-started
	resp := e.do("POST", "/v1/publish", body, e.apiKey, "")
	if resp.StatusCode != 202 {
		t.Fatalf("under load = %d, want 202", resp.StatusCode)
	}
	resp.Body.Close()
	close(gate)
	if code := <-firstDone; code != 200 {
		t.Fatalf("first request = %d, want 200", code)
	}
}

func TestRegistrationLifecycle(t *testing.T) {
	e := newEnv(t, Options{}, &apiFakeFCM{}, &apiFakeNtfy{}, nil, 60)
	t1 := deriveTopic(t, "company.example|a")
	t2 := deriveTopic(t, "tok1")
	t3 := deriveTopic(t, "company.example|b")
	reg := map[string]any{
		"endpoint": "https://push.example/abc",
		"keys":     map[string]string{"p256dh": p256dhB64(t), "auth": authB64(t)},
		"topics":   []string{t1, t2},
	}
	resp := e.do("POST", "/v1/registrations", reg, "", "")
	if resp.StatusCode != 200 {
		t.Fatalf("create = %d, want 200", resp.StatusCode)
	}
	out := decode[map[string]string](t, resp)
	id := out["id"]
	if id == "" {
		t.Fatal("empty id")
	}
	// Replace topics.
	resp = e.do("PUT", "/v1/registrations/"+id, map[string]any{"topics": []string{t3}}, "", "")
	if resp.StatusCode != 200 {
		t.Fatalf("put = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	if regs, _ := e.store.RegistrationsForTopic(t1); len(regs) != 0 {
		t.Fatal("old topic survived PUT")
	}
	if regs, _ := e.store.RegistrationsForTopic(t2); len(regs) != 0 {
		t.Fatal("second old topic survived PUT")
	}
	if regs, _ := e.store.RegistrationsForTopic(t3); len(regs) != 1 {
		t.Fatal("new topic missing after PUT")
	}
	// Unknown id.
	resp = e.do("PUT", "/v1/registrations/no-such", map[string]any{"topics": []string{}}, "", "")
	if resp.StatusCode != 404 {
		t.Fatalf("put unknown = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	// Delete.
	resp = e.do("DELETE", "/v1/registrations/"+id, nil, "", "")
	if resp.StatusCode != 204 {
		t.Fatalf("delete = %d, want 204", resp.StatusCode)
	}
	resp.Body.Close()
	resp = e.do("DELETE", "/v1/registrations/"+id, nil, "", "")
	if resp.StatusCode != 404 {
		t.Fatalf("delete again = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestRegistrationValidation(t *testing.T) {
	e := newEnv(t, Options{}, &apiFakeFCM{}, &apiFakeNtfy{}, nil, 60)
	valid := map[string]any{
		"endpoint": "https://push.example/abc",
		"keys":     map[string]string{"p256dh": p256dhB64(t), "auth": authB64(t)},
		"topics":   []string{validTopic("n-")},
	}
	cases := []struct {
		name string
		mut  func(m map[string]any)
	}{
		{"http endpoint", func(m map[string]any) { m["endpoint"] = "http://push.example/x" }},
		{"no host", func(m map[string]any) { m["endpoint"] = "https://" }},
		{"bad p256dh", func(m map[string]any) { m["keys"] = map[string]string{"p256dh": "AAAA", "auth": authB64(t)} }},
		{"bad auth", func(m map[string]any) { m["keys"] = map[string]string{"p256dh": p256dhB64(t), "auth": "AAAA"} }},
		{"bad topic", func(m map[string]any) { m["topics"] = []string{"n-x-" + strings.Repeat("a", 43)} }},
		{"topic too long", func(m map[string]any) { m["topics"] = []string{"n-" + string(bytes.Repeat([]byte("a"), 43)) + "x"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{
				"endpoint": valid["endpoint"],
				"keys":     valid["keys"],
				"topics":   valid["topics"],
			}
			tc.mut(m)
			resp := e.do("POST", "/v1/registrations", m, "", "")
			defer resp.Body.Close()
			if resp.StatusCode != 400 {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
	// Too many topics.
	topics := make([]string, store.MaxTopicsPerRegistration+1)
	for i := range topics {
		topics[i] = "n-" + fmt.Sprintf("%043d", i)
	}
	resp := e.do("POST", "/v1/registrations", map[string]any{
		"endpoint": "https://push.example/abc",
		"keys":     map[string]string{"p256dh": p256dhB64(t), "auth": authB64(t)},
		"topics":   topics,
	}, "", "")
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("too many topics = %d, want 400", resp.StatusCode)
	}
}

func TestAppKeyGate(t *testing.T) {
	e := newEnv(t, Options{AppKey: "secret"}, &apiFakeFCM{}, &apiFakeNtfy{}, nil, 60)
	reg := map[string]any{
		"endpoint": "https://push.example/abc",
		"keys":     map[string]string{"p256dh": p256dhB64(t), "auth": authB64(t)},
		"topics":   []string{},
	}
	resp := e.do("POST", "/v1/registrations", reg, "", "")
	if resp.StatusCode != 401 {
		t.Fatalf("no app key = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
	resp = e.do("POST", "/v1/registrations", reg, "", "wrong")
	if resp.StatusCode != 401 {
		t.Fatalf("wrong app key = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
	resp = e.do("POST", "/v1/registrations", reg, "", "secret")
	if resp.StatusCode != 200 {
		t.Fatalf("right app key = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestRegistrationIPThrottle(t *testing.T) {
	e := newEnv(t, Options{RegPerMin: 2, RegBurst: 2}, &apiFakeFCM{}, &apiFakeNtfy{}, nil, 60)
	reg := map[string]any{
		"endpoint": "https://push.example/abc",
		"keys":     map[string]string{"p256dh": p256dhB64(t), "auth": authB64(t)},
		"topics":   []string{},
	}
	for i := 0; i < 2; i++ {
		resp := e.do("POST", "/v1/registrations", reg, "", "")
		if resp.StatusCode != 200 {
			t.Fatalf("request %d = %d, want 200", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp := e.do("POST", "/v1/registrations", reg, "", "")
	defer resp.Body.Close()
	if resp.StatusCode != 429 {
		t.Fatalf("third = %d, want 429", resp.StatusCode)
	}
}

func validTopic(prefix string) string { return prefix + strings.Repeat("A", 43) }

func deriveTopic(t *testing.T, input string) string {
	t.Helper()
	h := topic.SourceHash(input)
	tpc, err := topic.Topic(h)
	if err != nil {
		t.Fatal(err)
	}
	return tpc
}

func p256dhB64(t *testing.T) string {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())
}

func authB64(t *testing.T) string {
	t.Helper()
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x01}, 16))
}
