package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func writeMetadata(t *testing.T, path string, version int64) {
	t.Helper()
	if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"signed":{"version":%d}}`, version)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLocalVersions(t *testing.T) {
	dir := t.TempDir()
	writeMetadata(t, filepath.Join(dir, "timestamp.json"), 4)
	writeMetadata(t, filepath.Join(dir, "snapshot.json"), 5)
	writeMetadata(t, filepath.Join(dir, "targets.json"), 6)
	writeMetadata(t, filepath.Join(dir, "channels.security.json"), 7)
	got, err := LocalVersions(dir, "security")
	if err != nil {
		t.Fatal(err)
	}
	if got != (Versions{Timestamp: 4, Snapshot: 5, Targets: 6, Role: 7}) {
		t.Fatalf("versions = %+v", got)
	}
}

func TestWaitForDeployPollsUntilLive(t *testing.T) {
	pollInterval = 20 * time.Millisecond
	defer func() { pollInterval = 2 * time.Second }()
	var version atomic.Int64
	version.Store(5)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"signed": map[string]any{"version": version.Load()}})
	}))
	defer srv.Close()
	// the deploy propagates shortly after the wait starts
	go func() {
		time.Sleep(30 * time.Millisecond)
		version.Store(6)
	}()
	want := Versions{Timestamp: 6, Snapshot: 6, Targets: 6, Role: 6}
	got, err := WaitForDeploy(context.Background(), srv.Client(), srv.URL, "security", want, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AtLeast(want) {
		t.Fatalf("versions = %+v", got)
	}
}

func TestWaitForDeployTimesOut(t *testing.T) {
	pollInterval = 20 * time.Millisecond
	defer func() { pollInterval = 2 * time.Second }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"signed": map[string]any{"version": 5}})
	}))
	defer srv.Close()
	start := time.Now()
	_, err := WaitForDeploy(context.Background(), srv.Client(), srv.URL, "security", Versions{Timestamp: 6}, 200*time.Millisecond)
	if err == nil {
		t.Fatal("accepted a deploy that never propagated")
	}
	elapsed := time.Since(start)
	if elapsed < 150*time.Millisecond {
		t.Fatal("returned before the timeout")
	}
	if elapsed > time.Second {
		t.Fatalf("overshot the timeout: %s", elapsed)
	}
}

func TestWaitForDeployURLsAreUnique(t *testing.T) {
	var mu sync.Mutex
	var urls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		urls = append(urls, r.URL.RequestURI())
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"signed": map[string]any{"version": 6}})
	}))
	defer srv.Close()
	_, err := WaitForDeploy(context.Background(), srv.Client(), srv.URL, "security",
		Versions{Timestamp: 6, Snapshot: 6, Targets: 6, Role: 6}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(urls) != 4 {
		t.Fatalf("fetched %d files: %v", len(urls), urls)
	}
	for _, u := range urls {
		if !strings.Contains(u, "?t=") {
			t.Fatalf("metadata fetched without a cache-busting nonce: %s", u)
		}
	}
}
