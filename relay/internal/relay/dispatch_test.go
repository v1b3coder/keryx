package relay

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/v1b3coder/keryx/relay/internal/push"
	"github.com/v1b3coder/keryx/relay/internal/store"
)

func TestDispatcherIdle(t *testing.T) {
	st, err := store.Open(":memory:", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d := New(st, nil, nil, Options{
		FastPathMax:   1,
		MaxConcurrent: 1,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if !d.Idle() {
		t.Fatal("fresh dispatcher is not idle")
	}

	// Two registrations push the topic past the fast path, so the publish is
	// queued instead of dispatched inline.
	const topic = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for i := 0; i < 2; i++ {
		endpoint := fmt.Sprintf("https://push.example/%d", i)
		if _, _, err := st.CreateRegistration(endpoint, "p256dh", "auth", "pwa", "", []string{topic}, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	d.sem <- struct{}{} // hold the only dispatch slot so the queued work cannot finish
	out, err := d.Publish(context.Background(), "company.example", "scope", topic, 1, []byte(`{"v":1}`))
	if err != nil || !out.Async {
		t.Fatalf("publish = %+v, %v", out, err)
	}
	if d.Idle() {
		t.Fatal("dispatcher with queued work reports idle")
	}
	<-d.sem

	deadline := time.Now().Add(2 * time.Second)
	for !d.Idle() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !d.Idle() {
		t.Fatal("dispatcher never drained to idle")
	}
}

func TestSendToTopic(t *testing.T) {
	st, err := store.Open(":memory:", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// No FCM leg: the topic leg reports disabled.
	d := New(st, nil, nil, Options{FastPathMax: 1, Logger: logger})
	if err := d.SendToTopic(context.Background(), "topic", []byte(`{"v":1}`)); !errors.Is(err, ErrTopicLegDisabled) {
		t.Fatalf("disabled leg: %v", err)
	}

	// FCM leg: the payload goes out under message.data.test.
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_in": 3600})
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	d = New(st, testFCM(t, srv), nil, Options{FastPathMax: 1, Logger: logger})
	if err := d.SendToTopic(context.Background(), "topic", []byte(`{"v":1,"test":true,"nonce":"n"}`)); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["message"].(map[string]any)["data"].(map[string]any)["test"]; !ok {
		t.Fatalf("body = %+v: no test key", got)
	}
}

// testFCM builds an FCM leg whose token and publish calls both go to srv.
func testFCM(t *testing.T, srv *httptest.Server) *push.FCM {
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

// rewriteTransport sends every request to one test server.
type rewriteTransport struct{ target *url.URL }

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = rt.target.Scheme
	req.URL.Host = rt.target.Host
	return http.DefaultTransport.RoundTrip(req)
}
