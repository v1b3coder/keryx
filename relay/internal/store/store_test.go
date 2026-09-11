package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenWalAndVersion(t *testing.T) {
	s := openTest(t)
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	var version int
	if err := s.db.QueryRow(`SELECT version FROM schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != SchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, SchemaVersion)
	}
}

func TestOpenRefusesMismatchedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE schema_version SET version = ?`, SchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	s.Close()

	_, err = Open(path)
	if !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("Open on mismatched version = %v, want ErrSchemaMismatch", err)
	}
}

func TestPublisherLifecycle(t *testing.T) {
	s := openTest(t)
	key, err := s.CreatePublisher("Acme", "company.example", 120)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.LookupPublisher(key)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Acme" || p.CompanyID != "company.example" || p.RatePerMin != 120 {
		t.Fatalf("publisher = %+v", p)
	}
	if _, err := s.LookupPublisher("wrong-key"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("LookupPublisher(wrong key) = %v, want ErrUnknownKey", err)
	}
	// Key is never stored in plaintext.
	var hash string
	if err := s.db.QueryRow(`SELECT api_key_hash FROM publishers WHERE id = ?`, p.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash == key || hash == "" {
		t.Fatalf("api_key_hash not hashed: %q", hash)
	}
	if HashAPIKey(key) != hash {
		t.Fatalf("HashAPIKey mismatch")
	}
}

func TestRegistrationUpsertAndReplace(t *testing.T) {
	s := openTest(t)
	endpoint := "https://push.example.com/abc"
	id, err := s.UpsertRegistration(endpoint, "p256dh1", "auth1", "ua", []string{"n-b-aaa", "n-o-bbb"})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("empty id")
	}

	// Same endpoint again: replaces in place, same id, topics replaced.
	id2, err := s.UpsertRegistration(endpoint, "p256dh2", "auth2", "ua2", []string{"n-b-ccc"})
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id {
		t.Fatalf("replace by endpoint changed id: %q -> %q", id, id2)
	}
	regs, err := s.RegistrationsForTopic("n-b-aaa")
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 0 {
		t.Fatalf("old topic still present: %d", len(regs))
	}
	regs, err = s.RegistrationsForTopic("n-b-ccc")
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 || regs[0].P256DH != "p256dh2" {
		t.Fatalf("regs = %+v", regs)
	}
}

func TestRegistrationValidation(t *testing.T) {
	s := openTest(t)
	if _, err := s.UpsertRegistration("http://insecure.example/x", "p", "a", "", nil); err == nil {
		t.Fatal("accepted non-https endpoint")
	}
	if _, err := s.UpsertRegistration("https://push.example/x", "", "a", "", nil); err == nil {
		t.Fatal("accepted empty p256dh")
	}
	if _, err := s.UpsertRegistration("https://push.example/x", "p", "", "", nil); err == nil {
		t.Fatal("accepted empty auth")
	}
}

func TestRegistrationTopicCap(t *testing.T) {
	s := openTest(t)
	topics := make([]string, MaxTopicsPerRegistration+1)
	for i := range topics {
		topics[i] = "n-b-" + pad(i)
	}
	if _, err := s.UpsertRegistration("https://push.example/x", "p", "a", "", topics); !errors.Is(err, ErrTooManyTopics) {
		t.Fatalf("Upsert = %v, want ErrTooManyTopics", err)
	}
	id, err := s.UpsertRegistration("https://push.example/x", "p", "a", "", topics[:MaxTopicsPerRegistration])
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceRegistrationTopics(id, topics); !errors.Is(err, ErrTooManyTopics) {
		t.Fatalf("Replace = %v, want ErrTooManyTopics", err)
	}
}

func TestRegistrationReplaceAndDelete(t *testing.T) {
	s := openTest(t)
	id, err := s.UpsertRegistration("https://push.example/x", "p", "a", "", []string{"n-b-aaa"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceRegistrationTopics(id, []string{"n-b-bbb", "n-b-ccc"}); err != nil {
		t.Fatal(err)
	}
	if regs, _ := s.RegistrationsForTopic("n-b-aaa"); len(regs) != 0 {
		t.Fatal("old topic survived replace")
	}
	if regs, _ := s.RegistrationsForTopic("n-b-bbb"); len(regs) != 1 {
		t.Fatal("new topic missing")
	}
	if err := s.ReplaceRegistrationTopics("no-such-id", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Replace unknown = %v, want ErrNotFound", err)
	}
	if err := s.DeleteRegistration(id); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRegistration(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete again = %v, want ErrNotFound", err)
	}
	// Cascade removed topics too.
	if regs, _ := s.RegistrationsForTopic("n-b-bbb"); len(regs) != 0 {
		t.Fatal("topics not cascaded on delete")
	}
}

func TestRegistrationsForTopic(t *testing.T) {
	s := openTest(t)
	for i := 0; i < 3; i++ {
		if _, err := s.UpsertRegistration(
			"https://push.example/"+pad(i), "p", "a", "", []string{"n-b-shared", "n-b-" + pad(i)},
		); err != nil {
			t.Fatal(err)
		}
	}
	regs, err := s.RegistrationsForTopic("n-b-shared")
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 3 {
		t.Fatalf("shared topic regs = %d, want 3", len(regs))
	}
	regs, err = s.RegistrationsForTopic("n-b-none")
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 0 {
		t.Fatalf("unknown topic regs = %d", len(regs))
	}
}

func TestEventLogAndPrune(t *testing.T) {
	s := openTest(t)
	key, err := s.CreatePublisher("Acme", "company.example", 60)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.LookupPublisher(key)
	if err := s.LogEvent(p.ID, "channel", "n-b-aaa", 1, 1, 3, 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.LogEvent(p.ID, "order", "n-o-bbb", 0, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	// Prune with a huge retention keeps rows; zero retention removes all.
	n, err := s.PruneEventLog(30 * 24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("prune removed %d with 30d retention", n)
	}
	n, err = s.PruneEventLog(0)
	if err != nil || n != 0 {
		t.Fatalf("prune(0) = %d, %v", n, err)
	}
}

func pad(i int) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 43)
	for i := range out {
		out[i] = 'a'
	}
	copy(out, []byte("abc"))
	// vary the tail so topics differ
	for j := 0; j < 8; j++ {
		out[42-j] = hexdigits[(i>>(j*4))&0xf]
	}
	return string(out)
}
