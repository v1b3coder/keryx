package relay

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

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
