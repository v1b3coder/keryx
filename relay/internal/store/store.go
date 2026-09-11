// Package store implements the relay's SQLite persistence (SPECIFICATION §7):
// publishers, WebPush registrations + followed topics, and the bounded
// event_log. The database is WAL-mode, single file, versioned — the relay
// refuses to start on a mismatched schema version.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SchemaVersion is the current database schema version.
// v2: event_log.kind dropped (generic relay — no channel/order type marker).
const SchemaVersion = 2

// MaxTopicsPerRegistration is the per-subscription topic cap (§5.3).
const MaxTopicsPerRegistration = 200

var (
	// ErrUnknownKey is returned for an API key with no publisher record.
	ErrUnknownKey = errors.New("unknown API key")
	// ErrNotFound is returned when a registration id does not exist.
	ErrNotFound = errors.New("not found")
	// ErrTooManyTopics is returned when a registration exceeds the topic cap.
	ErrTooManyTopics = fmt.Errorf("registration: at most %d topics", MaxTopicsPerRegistration)
	// ErrSchemaMismatch is returned when the DB schema version is not
	// SchemaVersion; the relay must refuse to start (§7).
	ErrSchemaMismatch = errors.New("database schema version mismatch")
)

// Publisher is a company (or department/partner engine) with an API key.
type Publisher struct {
	ID         int64
	Name       string
	CompanyID  string
	RatePerMin int
	CreatedAt  time.Time
}

// Registration is a WebPush subscription (one per PWA install).
type Registration struct {
	ID        string
	Endpoint  string
	P256DH    string
	Auth      string
	CreatedAt time.Time
	LastSeen  time.Time
	UserAgent string
}

// Store is the relay's SQLite-backed store.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and migrates it
// to the current schema version. It refuses to open a database with a
// mismatched schema version.
func Open(path string) (*Store, error) {
	dsn := path
	if path == ":memory:" {
		dsn = "file:relay-mem?mode=memory&cache=shared"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer; SQLite WAL + our usage
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	var exists int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_version'`,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check schema: %w", err)
	}
	if exists == 0 {
		// Fresh database: create the current schema and stamp the version.
		if _, err := s.db.Exec(schemaV2); err != nil {
			return fmt.Errorf("create schema: %w", err)
		}
		if _, err := s.db.Exec(`INSERT INTO schema_version (version) VALUES (?)`, SchemaVersion); err != nil {
			return fmt.Errorf("set schema version: %w", err)
		}
		return nil
	}
	var version int
	if err := s.db.QueryRow(`SELECT version FROM schema_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > SchemaVersion {
		return fmt.Errorf("%w: database is version %d, relay requires %d", ErrSchemaMismatch, version, SchemaVersion)
	}
	for v := version; v < SchemaVersion; v++ {
		stmt, ok := migrations[v]
		if !ok {
			return fmt.Errorf("%w: no migration path from version %d", ErrSchemaMismatch, v)
		}
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate %d -> %d: %w", v, v+1, err)
		}
	}
	if _, err := s.db.Exec(`UPDATE schema_version SET version = ?`, SchemaVersion); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

// migrations maps a version to the SQL that upgrades it to version+1.
var migrations = map[int]string{
	1: `ALTER TABLE event_log DROP COLUMN kind`, // generic relay: no type marker
}

// schemaV2 is the current schema (§7, event_log.kind removed).
const schemaV2 = `
CREATE TABLE schema_version (
  version INTEGER NOT NULL
);

CREATE TABLE publishers (
  id            INTEGER PRIMARY KEY,
  api_key_hash  TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL,
  company_id    TEXT NOT NULL,
  rate_per_min  INTEGER NOT NULL DEFAULT 60,
  created_at    TEXT NOT NULL
);

CREATE TABLE registrations (
  id         TEXT PRIMARY KEY,
  endpoint   TEXT NOT NULL UNIQUE,
  p256dh     TEXT NOT NULL,
  auth       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  last_seen  TEXT NOT NULL,
  user_agent TEXT
);

CREATE TABLE registration_topics (
  registration_id TEXT NOT NULL REFERENCES registrations(id) ON DELETE CASCADE,
  topic           TEXT NOT NULL,
  PRIMARY KEY (registration_id, topic)
);
CREATE INDEX idx_registration_topics_topic ON registration_topics(topic);

CREATE TABLE event_log (
  id          INTEGER PRIMARY KEY,
  publisher_id INTEGER NOT NULL,
  topic       TEXT NOT NULL,
  fcm         INTEGER, ntfy INTEGER,
  webpush_sent INTEGER, webpush_failed INTEGER, webpush_removed INTEGER,
  at          TEXT NOT NULL
);
`

// schemaV1 is the v1 schema (event_log had a kind column); kept for the
// migration path and its test.
const schemaV1 = `
CREATE TABLE schema_version (
  version INTEGER NOT NULL
);

CREATE TABLE publishers (
  id            INTEGER PRIMARY KEY,
  api_key_hash  TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL,
  company_id    TEXT NOT NULL,
  rate_per_min  INTEGER NOT NULL DEFAULT 60,
  created_at    TEXT NOT NULL
);

CREATE TABLE registrations (
  id         TEXT PRIMARY KEY,
  endpoint   TEXT NOT NULL UNIQUE,
  p256dh     TEXT NOT NULL,
  auth       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  last_seen  TEXT NOT NULL,
  user_agent TEXT
);

CREATE TABLE registration_topics (
  registration_id TEXT NOT NULL REFERENCES registrations(id) ON DELETE CASCADE,
  topic           TEXT NOT NULL,
  PRIMARY KEY (registration_id, topic)
);
CREATE INDEX idx_registration_topics_topic ON registration_topics(topic);

CREATE TABLE event_log (
  id          INTEGER PRIMARY KEY,
  publisher_id INTEGER NOT NULL,
  kind        TEXT NOT NULL,
  topic       TEXT NOT NULL,
  fcm         INTEGER, ntfy INTEGER,
  webpush_sent INTEGER, webpush_failed INTEGER, webpush_removed INTEGER,
  at          TEXT NOT NULL
);
`

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// HashAPIKey returns the sha256hex stored for an API key (the key itself is
// shown once at provisioning and never stored).
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// NewAPIKey generates a fresh API key: 32 random bytes, base64url no pad.
func NewAPIKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// CreatePublisher provisions a publisher and returns its API key (shown once).
func (s *Store) CreatePublisher(name, companyID string, ratePerMin int) (string, error) {
	key, err := NewAPIKey()
	if err != nil {
		return "", err
	}
	if _, err := s.db.Exec(
		`INSERT INTO publishers (api_key_hash, name, company_id, rate_per_min, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		HashAPIKey(key), name, companyID, ratePerMin, nowString(),
	); err != nil {
		return "", err
	}
	return key, nil
}

// LookupPublisher resolves an API key to its publisher record.
func (s *Store) LookupPublisher(apiKey string) (*Publisher, error) {
	row := s.db.QueryRow(
		`SELECT id, name, company_id, rate_per_min, created_at FROM publishers WHERE api_key_hash = ?`,
		HashAPIKey(apiKey),
	)
	return scanPublisher(row)
}

// PublisherByID fetches a publisher by id.
func (s *Store) PublisherByID(id int64) (*Publisher, error) {
	row := s.db.QueryRow(
		`SELECT id, name, company_id, rate_per_min, created_at FROM publishers WHERE id = ?`, id,
	)
	return scanPublisher(row)
}

// ListPublishers returns all publishers, newest first.
func (s *Store) ListPublishers() ([]Publisher, error) {
	rows, err := s.db.Query(
		`SELECT id, name, company_id, rate_per_min, created_at FROM publishers ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Publisher
	for rows.Next() {
		p, err := scanPublisher(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanPublisher(row scanner) (*Publisher, error) {
	var p Publisher
	var created string
	if err := row.Scan(&p.ID, &p.Name, &p.CompanyID, &p.RatePerMin, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUnknownKey
		}
		return nil, err
	}
	p.CreatedAt, _ = parseTime(created)
	return &p, nil
}

// UpsertRegistration registers (or replaces by endpoint) a WebPush
// subscription with its followed topics. Returns the registration id.
func (s *Store) UpsertRegistration(endpoint, p256dh, auth, userAgent string, topics []string) (string, error) {
	if len(topics) > MaxTopicsPerRegistration {
		return "", ErrTooManyTopics
	}
	if err := validateSubscription(endpoint, p256dh, auth); err != nil {
		return "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	now := nowString()
	id := newUUID()
	_, err = tx.Exec(
		`INSERT INTO registrations (id, endpoint, p256dh, auth, created_at, last_seen, user_agent)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(endpoint) DO UPDATE SET
		   p256dh = excluded.p256dh, auth = excluded.auth,
		   last_seen = excluded.last_seen, user_agent = excluded.user_agent`,
		id, endpoint, p256dh, auth, now, now, nullable(userAgent),
	)
	if err != nil {
		return "", err
	}
	// Resolve the actual id (an existing registration keeps its id on replace)
	// and only then replace its topic set.
	var regID string
	if err := tx.QueryRow(`SELECT id FROM registrations WHERE endpoint = ?`, endpoint).Scan(&regID); err != nil {
		return "", err
	}
	if err := replaceTopicsTx(tx, regID, topics); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return regID, nil
}

// ReplaceRegistrationTopics replaces the followed-topic set of a registration.
func (s *Store) ReplaceRegistrationTopics(id string, topics []string) error {
	if len(topics) > MaxTopicsPerRegistration {
		return ErrTooManyTopics
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists string
	err = tx.QueryRow(`SELECT id FROM registrations WHERE id = ?`, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := replaceTopicsTx(tx, id, topics); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE registrations SET last_seen = ? WHERE id = ?`, nowString(), id); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceTopicsTx(tx *sql.Tx, id string, topics []string) error {
	if _, err := tx.Exec(`DELETE FROM registration_topics WHERE registration_id = ?`, id); err != nil {
		return err
	}
	for _, t := range topics {
		if _, err := tx.Exec(
			`INSERT INTO registration_topics (registration_id, topic) VALUES (?, ?)`, id, t,
		); err != nil {
			return err
		}
	}
	return nil
}

// DeleteRegistration removes a registration (and, via ON DELETE CASCADE, its
// topics). Returns ErrNotFound if the id is unknown.
func (s *Store) DeleteRegistration(id string) error {
	res, err := s.db.Exec(`DELETE FROM registrations WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RegistrationsForTopic returns all registrations following topic.
func (s *Store) RegistrationsForTopic(topic string) ([]Registration, error) {
	rows, err := s.db.Query(
		`SELECT r.id, r.endpoint, r.p256dh, r.auth, r.created_at, r.last_seen, r.user_agent
		 FROM registrations r
		 JOIN registration_topics t ON t.registration_id = r.id
		 WHERE t.topic = ?`, topic,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Registration
	for rows.Next() {
		var r Registration
		var created, last string
		var ua sql.NullString
		if err := rows.Scan(&r.ID, &r.Endpoint, &r.P256DH, &r.Auth, &created, &last, &ua); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = parseTime(created)
		r.LastSeen, _ = parseTime(last)
		if ua.Valid {
			r.UserAgent = ua.String
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LogEvent appends an event_log row (audit/abuse record: hashes only).
func (s *Store) LogEvent(publisherID int64, topic string, fcm, ntfy int, wpSent, wpFailed, wpRemoved int) error {
	_, err := s.db.Exec(
		`INSERT INTO event_log (publisher_id, topic, fcm, ntfy, webpush_sent, webpush_failed, webpush_removed, at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		publisherID, topic, fcm, ntfy, wpSent, wpFailed, wpRemoved, nowString(),
	)
	return err
}

// EventLogEntry is one row of the bounded audit/abuse record (§7).
type EventLogEntry struct {
	PublisherID    int64
	Topic          string
	FCM, Ntfy      int
	WebPushSent    int
	WebPushFailed  int
	WebPushRemoved int
	At             time.Time
}

// LatestEvent returns the most recent event_log row (audit/inspection aid).
func (s *Store) LatestEvent() (*EventLogEntry, error) {
	row := s.db.QueryRow(`SELECT publisher_id, topic, fcm, ntfy,
		webpush_sent, webpush_failed, webpush_removed, at
		FROM event_log ORDER BY id DESC LIMIT 1`)
	var e EventLogEntry
	var at string
	if err := row.Scan(&e.PublisherID, &e.Topic, &e.FCM, &e.Ntfy,
		&e.WebPushSent, &e.WebPushFailed, &e.WebPushRemoved, &at); err != nil {
		return nil, err
	}
	e.At, _ = parseTime(at)
	return &e, nil
}

// PruneEventLog deletes event_log rows older than retention and returns the
// number removed.
func (s *Store) PruneEventLog(retention time.Duration) (int64, error) {
	if retention <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().Add(-retention).Format(time.RFC3339)
	res, err := s.db.Exec(`DELETE FROM event_log WHERE at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func validateSubscription(endpoint, p256dh, auth string) error {
	if !strings.HasPrefix(endpoint, "https://") {
		return errors.New("endpoint: must be an https:// URL")
	}
	if p256dh == "" || auth == "" {
		return errors.New("keys: p256dh and auth are required")
	}
	return nil
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nowString() string { return time.Now().UTC().Format(time.RFC3339) }

func parseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }
