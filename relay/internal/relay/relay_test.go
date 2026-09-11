package relay

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/v1b3coder/keryx/relay/internal/push"
	"github.com/v1b3coder/keryx/relay/internal/store"
	"github.com/v1b3coder/keryx/relay/internal/topic"
)

// fakes implement the leg interfaces.

type fakeFCM struct {
	err      error
	gotTopic string
	gotData  map[string]string
}

func (f *fakeFCM) Send(_ context.Context, topic string, data map[string]string) error {
	f.gotTopic = topic
	f.gotData = data
	return f.err
}

type fakeNtfy struct {
	err      error
	gotTopic string
	gotBody  []byte
}

func (f *fakeNtfy) Send(_ context.Context, topic string, payload []byte) error {
	f.gotTopic = topic
	f.gotBody = payload
	return f.err
}

type fakeWP struct {
	mu          sync.Mutex
	perEndpoint map[string]error // endpoint -> error (nil = sent)
	gotPayload  []byte
}

func (f *fakeWP) Send(_ context.Context, endpoint, p256dh, auth string, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotPayload = payload
	return f.perEndpoint[endpoint]
}

func newTestDispatcher(t *testing.T, fcm *fakeFCM, ntfy *fakeNtfy, wp *fakeWP) (*Dispatcher, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var fcmI FCMLeg
	if fcm != nil {
		fcmI = fcm
	}
	var ntfyI NtfyLeg
	if ntfy != nil {
		ntfyI = ntfy
	}
	var wpI WebPushLeg
	if wp != nil {
		wpI = wp
	}
	return New(st, fcmI, ntfyI, wpI, 4, slog.New(slog.DiscardHandler)), st
}

func TestPublishAllLegs(t *testing.T) {
	fcm := &fakeFCM{}
	ntfy := &fakeNtfy{}
	wp := &fakeWP{perEndpoint: map[string]error{}}
	d, st := newTestDispatcher(t, fcm, ntfy, wp)

	key, _ := st.CreatePublisher("Acme", "company.example", 60)
	pub, _ := st.LookupPublisher(key)
	h := topic.SourceHash(topic.Channel, "company.example|marketing")
	n, seq := 3, 7

	_, err := st.UpsertRegistration("https://push.example/1", "p", "a", "", []string{})
	if err != nil {
		t.Fatal(err)
	}
	// Register two devices on the topic: derive it first.
	tpc, _ := topic.Topic(topic.Channel, h)
	st.UpsertRegistration("https://push.example/1", "p", "a", "", []string{tpc})
	st.UpsertRegistration("https://push.example/2", "p", "a", "", []string{tpc})
	wp.perEndpoint = map[string]error{"https://push.example/1": nil, "https://push.example/2": nil}

	res, err := d.Publish(context.Background(), pub.ID, topic.Channel, h, &n, &seq)
	if err != nil {
		t.Fatal(err)
	}
	if res.FCM != 1 || res.Ntfy != 1 {
		t.Fatalf("fcm/ntfy = %d/%d, want 1/1", res.FCM, res.Ntfy)
	}
	if res.WebPush.Sent != 2 || res.WebPush.Failed != 0 || res.WebPush.Removed != 0 {
		t.Fatalf("webpush = %+v", res.WebPush)
	}
	if fcm.gotTopic != tpc {
		t.Fatalf("fcm topic = %q, want %q", fcm.gotTopic, tpc)
	}
	if fcm.gotData["v"] != "1" || fcm.gotData["n"] != "3" || fcm.gotData["seq"] != "7" {
		t.Fatalf("fcm data = %v", fcm.gotData)
	}
	if ntfy.gotTopic != tpc {
		t.Fatalf("ntfy topic = %q", ntfy.gotTopic)
	}
	if !errors.Is(push.ErrGone, push.ErrGone) {
		t.Fatal("sanity")
	}
	// Topic legs carry no t; WebPush does.
	var wpBody, ntfyBody map[string]any
	if err := jsonUnmarshal(wp.gotPayload, &wpBody); err != nil {
		t.Fatal(err)
	}
	if wpBody["t"] != tpc {
		t.Fatalf("webpush payload t = %v", wpBody["t"])
	}
	if err := jsonUnmarshal(ntfy.gotBody, &ntfyBody); err != nil {
		t.Fatal(err)
	}
	if _, has := ntfyBody["t"]; has {
		t.Fatalf("ntfy payload must not carry t: %v", ntfyBody)
	}
	if ntfyBody["v"] != float64(1) || ntfyBody["n"] != float64(3) {
		t.Fatalf("ntfy body = %v", ntfyBody)
	}
}

func TestPublishLegErrors(t *testing.T) {
	tests := []struct {
		name     string
		fcmErr   error
		ntfyErr  error
		wantFCM  int
		wantNtfy int
	}{
		{name: "fcm empty topic counts dispatched", fcmErr: push.ErrEmptyTopic, wantFCM: 1, wantNtfy: 1},
		{name: "fcm credentials rejected", fcmErr: push.ErrCredentials, wantFCM: 0, wantNtfy: 1},
		{name: "fcm transient failed", fcmErr: errors.New("boom"), wantFCM: 0, wantNtfy: 1},
		{name: "ntfy failed", ntfyErr: errors.New("boom"), wantFCM: 1, wantNtfy: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fcm := &fakeFCM{err: tc.fcmErr}
			ntfy := &fakeNtfy{err: tc.ntfyErr}
			d, st := newTestDispatcher(t, fcm, ntfy, nil)
			key, _ := st.CreatePublisher("Acme", "company.example", 60)
			pub, _ := st.LookupPublisher(key)
			h := topic.SourceHash(topic.Channel, "company.example|marketing")
			res, err := d.Publish(context.Background(), pub.ID, topic.Channel, h, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if res.FCM != tc.wantFCM || res.Ntfy != tc.wantNtfy {
				t.Fatalf("fcm/ntfy = %d/%d, want %d/%d", res.FCM, res.Ntfy, tc.wantFCM, tc.wantNtfy)
			}
		})
	}
}

func TestPublishNoLegs(t *testing.T) {
	d, st := newTestDispatcher(t, nil, nil, nil)
	key, _ := st.CreatePublisher("Acme", "company.example", 60)
	pub, _ := st.LookupPublisher(key)
	h := topic.SourceHash(topic.Channel, "company.example|marketing")
	res, err := d.Publish(context.Background(), pub.ID, topic.Channel, h, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.FCM != 0 || res.Ntfy != 0 || res.WebPush != (WebPushResult{}) {
		t.Fatalf("result = %+v", res)
	}
}

func TestPublishWebPushRemoval(t *testing.T) {
	wp := &fakeWP{perEndpoint: map[string]error{
		"https://push.example/ok":   nil,
		"https://push.example/dead": push.ErrGone,
		"https://push.example/bad":  errors.New("boom"),
	}}
	d, st := newTestDispatcher(t, nil, nil, wp)
	key, _ := st.CreatePublisher("Acme", "company.example", 60)
	pub, _ := st.LookupPublisher(key)
	h := topic.SourceHash(topic.Channel, "company.example|marketing")
	tpc, _ := topic.Topic(topic.Channel, h)
	st.UpsertRegistration("https://push.example/ok", "p", "a", "", []string{tpc})
	st.UpsertRegistration("https://push.example/dead", "p", "a", "", []string{tpc})
	st.UpsertRegistration("https://push.example/bad", "p", "a", "", []string{tpc})

	res, err := d.Publish(context.Background(), pub.ID, topic.Channel, h, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.WebPush.Sent != 1 || res.WebPush.Failed != 1 || res.WebPush.Removed != 1 {
		t.Fatalf("webpush = %+v", res.WebPush)
	}
	// The dead registration must have been deleted from the registry.
	if regs, _ := st.RegistrationsForTopic(tpc); len(regs) != 2 {
		t.Fatalf("registrations after removal = %d, want 2", len(regs))
	}
	if _, err := st.RegistrationsForTopic(tpc); err != nil {
		t.Fatal(err)
	}
	// Verify the dead one is gone by endpoint lookup: delete by id instead.
	regs, _ := st.RegistrationsForTopic(tpc)
	for _, r := range regs {
		if r.Endpoint == "https://push.example/dead" {
			t.Fatalf("dead registration still present")
		}
	}
}

func TestPublishEventLog(t *testing.T) {
	fcm := &fakeFCM{}
	ntfy := &fakeNtfy{}
	d, st := newTestDispatcher(t, fcm, ntfy, nil)
	key, _ := st.CreatePublisher("Acme", "company.example", 60)
	pub, _ := st.LookupPublisher(key)
	h := topic.SourceHash(topic.Order, "A9xQr5bDgWz4m2nPqK8tLc")
	if _, err := d.Publish(context.Background(), pub.ID, topic.Order, h, nil, nil); err != nil {
		t.Fatal(err)
	}
	var kind, tpc string
	var fcmN, ntfyN int
	e, err := st.LatestEvent()
	if err != nil {
		t.Fatal(err)
	}
	kind, tpc, fcmN, ntfyN = e.Kind, e.Topic, e.FCM, e.Ntfy
	if kind != "order" || fcmN != 1 || ntfyN != 1 {
		t.Fatalf("event log = kind %q fcm %d ntfy %d", kind, fcmN, ntfyN)
	}
	if want, _ := topic.Topic(topic.Order, h); tpc != want {
		t.Fatalf("event topic = %q, want %q", tpc, want)
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
