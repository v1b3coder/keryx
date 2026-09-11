package push

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// writeServiceAccount creates a temp service-account JSON with a fresh RSA key.
func writeServiceAccount(t *testing.T, projectID string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemBlock := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	sa := map[string]string{
		"type":         "service_account",
		"project_id":   projectID,
		"client_email": "relay@example.iam.gserviceaccount.com",
		"private_key":  string(pemBlock),
		"token_uri":    "https://oauth2.googleapis.com/token",
	}
	raw, _ := json.Marshal(sa)
	path := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNewFCMValidation(t *testing.T) {
	if _, err := NewFCM(filepath.Join(t.TempDir(), "missing.json"), nil); err == nil {
		t.Fatal("accepted missing file")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte(`{}`), 0o600)
	if _, err := NewFCM(bad, nil); err == nil {
		t.Fatal("accepted empty service account")
	}
}

func TestFCMTokenCaching(t *testing.T) {
	var tokenCalls atomic.Int32
	var sendCalls atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls.Add(1)
		r.ParseForm()
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			t.Errorf("bad grant_type %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("assertion") == "" {
			t.Error("missing assertion")
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-1", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	var fcmSrv *httptest.Server
	fcmSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sendCalls.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer tok-1" {
			t.Errorf("authorization = %q", got)
		}
		if !strings.Contains(r.URL.Path, "/projects/proj-x/messages:send") {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body struct {
			Message struct {
				Topic   string            `json:"topic"`
				Data    map[string]string `json:"data"`
				Android struct {
					Priority string `json:"priority"`
				} `json:"android"`
			} `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Message.Topic != "n-b-abc" {
			t.Errorf("topic = %q", body.Message.Topic)
		}
		if body.Message.Data["v"] != "1" || body.Message.Data["n"] != "3" {
			t.Errorf("data = %v", body.Message.Data)
		}
		if body.Message.Android.Priority != "high" {
			t.Errorf("priority = %q", body.Message.Android.Priority)
		}
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]string{"name": "projects/proj-x/messages/1"})
	}))
	defer fcmSrv.Close()

	f, err := NewFCM(writeServiceAccount(t, "proj-x"), fcmSrv.Client())
	if err != nil {
		t.Fatal(err)
	}
	f.tokenURI = tokenSrv.URL // exercise token fetching against httptest
	f.endpoint = fcmSrv.URL
	f.backoff = func(int) time.Duration { return 0 }

	for i := 0; i < 2; i++ {
		if err := f.Send(context.Background(), "n-b-abc", map[string]string{"v": "1", "n": "3"}); err != nil {
			t.Fatal(err)
		}
	}
	if tokenCalls.Load() != 1 {
		t.Fatalf("token fetches = %d, want 1 (cached)", tokenCalls.Load())
	}
	if sendCalls.Load() != 2 {
		t.Fatalf("sends = %d", sendCalls.Load())
	}
}

func TestFCMTokenRefreshNearExpiry(t *testing.T) {
	var tokenCalls atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-2", "expires_in": 60})
	}))
	defer tokenSrv.Close()

	fcmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer fcmSrv.Close()

	f, err := NewFCM(writeServiceAccount(t, "proj-x"), fcmSrv.Client())
	if err != nil {
		t.Fatal(err)
	}
	f.tokenURI = tokenSrv.URL
	f.endpoint = fcmSrv.URL
	f.backoff = func(int) time.Duration { return 0 }

	// First send: token with 60s expiry. Immediately after, remaining < 5min,
	// so the next send must refetch (no caching window).
	f.Send(context.Background(), "n-b-a", nil)
	f.Send(context.Background(), "n-b-a", nil)
	if tokenCalls.Load() != 2 {
		t.Fatalf("token fetches = %d, want 2 (no cache within 5min of expiry)", tokenCalls.Load())
	}
}

func TestFCMSendClassifications(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{"ok", 200, `{"name":"m/1"}`, nil},
		{"empty topic 404", 404, `{"error":{"code":404,"status":"NOT_FOUND"}}`, ErrEmptyTopic},
		{"invalid argument", 400, `{"error":{"code":400,"status":"INVALID_ARGUMENT"}}`, ErrEmptyTopic},
		{"credentials", 401, `{"error":{"status":"UNAUTHENTICATED"}}`, ErrCredentials},
		{"forbidden", 403, `{"error":{"status":"PERMISSION_DENIED"}}`, ErrCredentials},
		{"other 4xx", 400, `{"error":{"status":"UNKNOWN"}}`, nil}, // expect generic error
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_in": 3600})
			}))
			defer tokenSrv.Close()
			f, err := NewFCM(writeServiceAccount(t, "proj-x"), srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			f.tokenURI = tokenSrv.URL
			f.endpoint = srv.URL
			f.backoff = func(int) time.Duration { return 0 }
			err = f.Send(context.Background(), "n-b-a", map[string]string{"v": "1"})
			if tc.wantErr != nil && err != tc.wantErr {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && err != nil && tc.status != 400 {
				t.Fatalf("err = %v, want nil", err)
			}
			if tc.status == 400 && tc.body == `{"error":{"status":"UNKNOWN"}}` && err == nil {
				t.Fatal("expected generic error")
			}
		})
	}
}

func TestFCMRetriesTransient(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_in": 3600})
	}))
	defer tokenSrv.Close()
	f, err := NewFCM(writeServiceAccount(t, "proj-x"), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	f.tokenURI = tokenSrv.URL
	f.endpoint = srv.URL
	f.backoff = func(int) time.Duration { return 0 }
	if err := f.Send(context.Background(), "n-b-a", map[string]string{"v": "1"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
}
