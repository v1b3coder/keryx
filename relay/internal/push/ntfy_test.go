package push

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNtfySend(t *testing.T) {
	var path, contentType string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		contentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	n, err := NewNtfy(srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	n.backoff = func(int) time.Duration { return 0 }
	if err := n.Send(context.Background(), "n-b-abc", []byte(`{"v":1,"n":3}`)); err != nil {
		t.Fatal(err)
	}
	if path != "/n-b-abc" {
		t.Fatalf("path = %q", path)
	}
	if !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("content-type = %q", contentType)
	}
	if body["v"] != float64(1) || body["n"] != float64(3) {
		t.Fatalf("body = %v", body)
	}
	if _, hasT := body["t"]; hasT {
		t.Fatalf("topic-based leg must not carry t: %v", body)
	}
}

func TestNtfyErrors(t *testing.T) {
	tests := []struct {
		name      string
		statuses  []int
		wantErr   bool
		wantCalls int32
	}{
		{name: "ok", statuses: []int{200}, wantCalls: 1},
		{name: "4xx final", statuses: []int{400}, wantErr: true, wantCalls: 1},
		{name: "5xx retry then ok", statuses: []int{500, 503, 200}, wantCalls: 3},
		{name: "5xx exhausted", statuses: []int{500, 502, 503}, wantErr: true, wantCalls: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := int(calls.Add(1))
				if n > len(tc.statuses) {
					w.WriteHeader(500)
					return
				}
				w.WriteHeader(tc.statuses[n-1])
			}))
			defer srv.Close()
			n, err := NewNtfy(srv.URL, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			n.backoff = func(int) time.Duration { return 0 }
			err = n.Send(context.Background(), "n-b-a", []byte(`{"v":1}`))
			if tc.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("err = %v", err)
			}
			if calls.Load() != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", calls.Load(), tc.wantCalls)
			}
		})
	}
}

func TestNtfyValidation(t *testing.T) {
	if _, err := NewNtfy("not-a-url", nil); err == nil {
		t.Fatal("accepted invalid base")
	}
	if _, err := NewNtfy("ftp://ntfy.example", nil); err == nil {
		t.Fatal("accepted non-http base")
	}
	if n, err := NewNtfy("https://ntfy.example/", nil); err != nil || n.base != "https://ntfy.example" {
		t.Fatalf("base = %q, err = %v", n.base, err)
	}
}
