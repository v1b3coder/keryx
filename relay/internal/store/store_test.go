package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/v1b3coder/keryx/relay/internal/tufclient"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(":memory:", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestCompanyStateRoundTrip(t *testing.T) {
	st := openTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	root := []byte(`{"signed":{"version":1}}`)

	if _, err := st.Company("company.example"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown company: err = %v", err)
	}
	// Root progress is persisted immediately, even before a full refresh.
	if err := st.SaveRoot("company.example", root, 1, now); err != nil {
		t.Fatal(err)
	}
	c, err := st.Company("company.example")
	if err != nil {
		t.Fatal(err)
	}
	if c.RootVersion != 1 || string(c.Root) != string(root) {
		t.Fatalf("company = %+v", c)
	}
	// A completed refresh persists targets and refreshed_at.
	targets := []byte(`{"signed":{"_type":"targets"}}`)
	state := &tufclient.State{
		CompanyID:      "company.example",
		Root:           root,
		RootVersion:    2,
		Targets:        targets,
		TargetsVersion: 3,
		TargetsExpires: now.Add(24 * time.Hour),
		RefreshedAt:    now,
	}
	if err := st.SaveState(state, now.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	c, err = st.Company("company.example")
	if err != nil {
		t.Fatal(err)
	}
	if c.RootVersion != 2 || c.TargetsVersion != 3 || string(c.Targets) != string(targets) {
		t.Fatalf("company = %+v", c)
	}
	if got := c.State().CompanyID; got != "company.example" {
		t.Fatalf("state company = %q", got)
	}
	companies, err := st.Companies()
	if err != nil || len(companies) != 1 {
		t.Fatalf("companies = %v, %v", companies, err)
	}
}

func TestReserveRefreshCoalesces(t *testing.T) {
	st := openTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := st.SaveRoot("company.example", []byte("root"), 1, now); err != nil {
		t.Fatal(err)
	}
	reserved, err := st.ReserveRefresh("company.example", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !reserved.Equal(now) {
		t.Fatalf("first reservation = %v, want %v", reserved, now)
	}
	// A second hint during the interval must not move the reservation later.
	later := now.Add(10 * time.Second)
	reserved, err = st.ReserveRefresh("company.example", later, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !reserved.Equal(now.Add(time.Minute)) {
		t.Fatalf("coalesced reservation = %v, want %v", reserved, now.Add(time.Minute))
	}
	// The reserved time is persisted, so it survives a restart.
	c, err := st.Company("company.example")
	if err != nil {
		t.Fatal(err)
	}
	if !c.NextRefreshAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("next_refresh_at = %v", c.NextRefreshAt)
	}
	if _, err := st.ReserveRefresh("unknown.example", now, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown reserve: err = %v", err)
	}
}

func TestEventLifecycle(t *testing.T) {
	st := openTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	topic := strings.Repeat("a", 43)

	id, requestID, expires, err := st.AcceptEvent("company.example", strings.Repeat("b", 64), topic, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(requestID) != 43 || !expires.Equal(now.Add(time.Hour)) {
		t.Fatalf("request_id = %q expires = %v", requestID, expires)
	}
	e, err := st.EventByRequestID(requestID, now)
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != StatusPending || e.Topic != topic {
		t.Fatalf("event = %+v", e)
	}
	if err := st.CompleteEvent(id, FCMAccepted, 10, 8, 1, 1, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	e, err = st.EventByRequestID(requestID, now)
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != StatusComplete || e.FCM != FCMAccepted ||
		e.WebPushAttempted != 10 || e.WebPushSent != 8 || e.WebPushFailed != 1 || e.WebPushDead != 1 {
		t.Fatalf("event = %+v", e)
	}
	// Expired capabilities return 404-equivalent ErrNotFound.
	if _, err := st.EventByRequestID(requestID, now.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired probe: err = %v", err)
	}
}

func TestEventSupersession(t *testing.T) {
	st := openTest(t)
	now := time.Now().UTC()
	topic := strings.Repeat("a", 43)
	id1, req1, _, err := st.AcceptEvent("c", strings.Repeat("b", 64), topic, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	id2, req2, _, err := st.AcceptEvent("c", strings.Repeat("b", 64), topic, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SupersedeEvent(id1, id2, now); err != nil {
		t.Fatal(err)
	}
	e, err := st.EventByRequestID(req1, now)
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != StatusSuperseded || e.SupersededByID == nil || *e.SupersededByID != id2 {
		t.Fatalf("event = %+v", e)
	}
	next, err := st.EventByID(id2)
	if err != nil {
		t.Fatal(err)
	}
	if next.RequestID != req2 {
		t.Fatalf("successor request_id = %q, want %q", next.RequestID, req2)
	}
}

func TestClosePendingAndPrune(t *testing.T) {
	st := openTest(t)
	now := time.Now().UTC()
	_, req, _, err := st.AcceptEvent("c", strings.Repeat("b", 64), strings.Repeat("a", 43), time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := st.ClosePendingEvents(now); err != nil || n != 1 {
		t.Fatalf("closed = %d, %v", n, err)
	}
	e, err := st.EventByRequestID(req, now)
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != StatusComplete || e.FCM != FCMFailed {
		t.Fatalf("event = %+v", e)
	}
	if n, err := st.PruneEventLog(time.Nanosecond, now.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("pruned = %d, %v", n, err)
	}
}

func TestRegistrationLifecycle(t *testing.T) {
	st := openTest(t)
	now := time.Now().UTC()
	topics := []string{strings.Repeat("a", 43), strings.Repeat("b", 43)}
	id, token, err := st.CreateRegistration("https://push.example/x", "p256dh", "auth", "pwa", "ua", topics, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 43 {
		t.Fatalf("management token = %q", token)
	}
	// The endpoint is unique: a second creation conflicts and never overwrites.
	if _, _, err := st.CreateRegistration("https://push.example/x", "p", "a", "pwa", "ua", nil, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate endpoint: err = %v", err)
	}
	// Wrong token is unauthorized; unknown id is not found.
	if err := st.HeartbeatRegistration(id, "wrong", now); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong token: err = %v", err)
	}
	if err := st.HeartbeatRegistration("nope", token, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: err = %v", err)
	}
	// A valid heartbeat bumps last_seen.
	if err := st.HeartbeatRegistration(id, token, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, err := st.TopicCount(topics[0]); err != nil || n != 1 {
		t.Fatalf("topic count = %d, %v", n, err)
	}
	// Replace the topic set.
	if err := st.UpdateRegistration(id, token, nil, nil, nil, []string{strings.Repeat("c", 43)}, now); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.TopicCount(topics[0]); n != 0 {
		t.Fatalf("old topic count = %d", n)
	}
	if n, _ := st.TopicCount(strings.Repeat("c", 43)); n != 1 {
		t.Fatalf("new topic count = %d", n)
	}
	if err := st.DeleteRegistration(id, token); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRegistration(id, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: err = %v", err)
	}
}

func TestUpdateRegistrationEndpointConflict(t *testing.T) {
	st := openTest(t)
	now := time.Now().UTC()
	id1, tok1, err := st.CreateRegistration("https://push.example/a", "p", "a", "pwa", "ua", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = st.CreateRegistration("https://push.example/b", "p", "a", "pwa", "ua", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	// Taking another registration's endpoint conflicts without modifying
	// either record.
	ep, p, a := "https://push.example/b", "p2", "a2"
	if err := st.UpdateRegistration(id1, tok1, &ep, &p, &a, nil, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("endpoint conflict: err = %v", err)
	}
	if err := st.HeartbeatRegistration(id1, tok1, now); err != nil {
		t.Fatalf("record modified by a rejected update: %v", err)
	}
	// Endpoint and keys must be supplied together.
	ep = "https://push.example/c"
	if err := st.UpdateRegistration(id1, tok1, &ep, nil, nil, nil, now); err == nil {
		t.Fatal("accepted endpoint without keys")
	}
}

func TestForEachRegistrationForTopicBatches(t *testing.T) {
	st := openTest(t)
	now := time.Now().UTC()
	topic := strings.Repeat("a", 43)
	for i := 0; i < 5; i++ {
		if _, _, err := st.CreateRegistration("https://push.example/"+string(rune('a'+i)), "p", "a", "pwa", "ua", []string{topic}, now); err != nil {
			t.Fatal(err)
		}
	}
	var seen int
	err := st.ForEachRegistrationForTopic(context.Background(), topic, 2, func(r Registration) error {
		seen++
		return nil
	})
	if err != nil || seen != 5 {
		t.Fatalf("seen = %d, err = %v", seen, err)
	}
	// A callback error stops the walk.
	err = st.ForEachRegistrationForTopic(context.Background(), topic, 2, func(r Registration) error {
		return errors.New("stop")
	})
	if err == nil {
		t.Fatal("callback error not propagated")
	}
}

func TestSweepRegistrations(t *testing.T) {
	st := openTest(t)
	old := time.Now().UTC().Add(-48 * time.Hour)
	id, token, err := st.CreateRegistration("https://push.example/x", "p", "a", "pwa", "ua", nil, old)
	if err != nil {
		t.Fatal(err)
	}
	_ = id
	_ = token
	n, err := st.SweepRegistrations(24*time.Hour, time.Now().UTC())
	if err != nil || n != 1 {
		t.Fatalf("swept = %d, %v", n, err)
	}
}

func TestTooManyTopics(t *testing.T) {
	st := openTest(t)
	topics := make([]string, MaxTopicsPerRegistration+1)
	for i := range topics {
		topics[i] = strings.Repeat("a", 43)
	}
	if _, _, err := st.CreateRegistration("https://push.example/x", "p", "a", "pwa", "ua", topics, time.Now().UTC()); !errors.Is(err, ErrTooManyTopics) {
		t.Fatalf("err = %v", err)
	}
}

func TestRegistrationForManagement(t *testing.T) {
	st := openTest(t)
	now := time.Now().UTC()
	topic := strings.Repeat("a", 43)
	id, token, err := st.CreateRegistration("https://push.example/1", "p256dh", "auth", "pwa", "", []string{topic}, now)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := st.RegistrationForManagement(id, token)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if reg.Endpoint != "https://push.example/1" || reg.P256DH != "p256dh" || reg.Auth != "auth" {
		t.Fatalf("registration = %+v", reg)
	}
	if !reg.CreatedAt.Equal(now.Truncate(time.Second)) || !reg.LastSeen.Equal(now.Truncate(time.Second)) {
		t.Fatalf("timestamps = %v / %v", reg.CreatedAt, reg.LastSeen)
	}
	if _, err := st.RegistrationForManagement(id, "wrong"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong token = %v", err)
	}
	if _, err := st.RegistrationForManagement("missing", token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing id = %v", err)
	}
}
