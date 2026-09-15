// Package store implements the relay's SQLite persistence
// (relay/SPECIFICATION.md §7). Two databases, versioned independently:
//
//   - the main database holds company TUF trust state and the bounded
//     event_log (trust, publishing and metadata-refresh writes);
//   - the registry database holds registrations and registration_topics
//     (write-heavy device churn).
//
// Both are WAL-mode and the relay refuses to start on a mismatched schema
// version.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/v1b3coder/keryx/relay/internal/tufclient"
)

// SchemaVersion is the current schema version of both databases.
const SchemaVersion = 1

// memSeq names in-memory databases uniquely so the two stores never share one.
var memSeq atomic.Int64

// MaxTopicsPerRegistration is the per-subscription topic cap (§5.3).
const MaxTopicsPerRegistration = 200

var (
	// ErrNotFound is returned when a row does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned when an endpoint is already registered.
	ErrConflict = errors.New("endpoint already registered")
	// ErrUnauthorized is returned for a missing/invalid management token.
	ErrUnauthorized = errors.New("invalid management token")
	// ErrTooManyTopics is returned when a registration exceeds the topic cap.
	ErrTooManyTopics = fmt.Errorf("registration: at most %d topics", MaxTopicsPerRegistration)
	// ErrSchemaMismatch is returned on a database schema version mismatch;
	// the relay must refuse to start (§7).
	ErrSchemaMismatch = errors.New("database schema version mismatch")
)

// Event statuses (§5.1.1, §7).
const (
	StatusPending    = "pending"
	StatusComplete   = "complete"
	StatusSuperseded = "superseded"
)

// FCM leg results (§5.1, §7).
const (
	FCMDisabled   = "disabled"
	FCMAccepted   = "accepted"
	FCMFailed     = "failed"
	FCMSuppressed = "suppressed"
)

// CompanyTUF is one company's persisted TUF trust state (§7).
type CompanyTUF struct {
	CompanyID      string
	Root           []byte
	RootVersion    int64
	Targets        []byte
	TargetsVersion int64
	TargetsExpires time.Time
	RefreshedAt    time.Time
	NextRefreshAt  time.Time
}

// Event is one accepted publish's audit row (§7).
type Event struct {
	ID               int64
	RequestID        string
	RequestExpiresAt time.Time
	CompanyID        string
	ScopeID          string
	Status           string
	SupersededByID   *int64
	Topic            string
	FCM              string
	WebPushAttempted int
	WebPushSent      int
	WebPushFailed    int
	WebPushDead      int
	At               time.Time
	CompletedAt      *time.Time
}

// Registration is one endpoint registration (§7).
type Registration struct {
	ID        string
	Endpoint  string
	P256DH    string
	Auth      string
	Source    string
	CreatedAt time.Time
	LastSeen  time.Time
	UserAgent string
}

// Store holds both SQLite databases.
type Store struct {
	main     *sql.DB
	registry *sql.DB
}

// Open opens (creating if needed) both databases and migrates them to the
// current schema version, refusing to start on a mismatch.
func Open(mainPath, registryPath string) (*Store, error) {
	main, err := openDB(mainPath)
	if err != nil {
		return nil, fmt.Errorf("main db: %w", err)
	}
	if err := migrate(main, mainSchema); err != nil {
		main.Close()
		return nil, fmt.Errorf("main db: %w", err)
	}
	registry, err := openDB(registryPath)
	if err != nil {
		main.Close()
		return nil, fmt.Errorf("registry db: %w", err)
	}
	if err := migrate(registry, registrySchema); err != nil {
		main.Close()
		registry.Close()
		return nil, fmt.Errorf("registry db: %w", err)
	}
	return &Store{main: main, registry: registry}, nil
}

// Close closes both databases.
func (s *Store) Close() error {
	return errors.Join(s.main.Close(), s.registry.Close())
}

func openDB(path string) (*sql.DB, error) {
	dsn := path
	if path == ":memory:" {
		dsn = fmt.Sprintf("file:relay-mem-%d?mode=memory&cache=shared", memSeq.Add(1))
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
	return db, nil
}

const mainSchema = `
CREATE TABLE IF NOT EXISTS schema_version (
  version INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS company_tuf (
  company_id      TEXT PRIMARY KEY,
  root            BLOB NOT NULL,
  root_version    INTEGER NOT NULL,
  chain_state     TEXT NOT NULL,
  refreshed_at    TEXT,
  next_refresh_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS event_log (
  id                INTEGER PRIMARY KEY,
  request_id        TEXT NOT NULL UNIQUE,
  request_expires_at TEXT NOT NULL,
  company_id        TEXT NOT NULL,
  scope_id          TEXT NOT NULL,
  status            TEXT NOT NULL,
  superseded_by_id  INTEGER,
  topic             TEXT NOT NULL,
  fcm               TEXT,
  webpush_attempted INTEGER,
  webpush_sent      INTEGER,
  webpush_failed    INTEGER,
  webpush_dead      INTEGER,
  at                TEXT NOT NULL,
  completed_at      TEXT
);
`

const registrySchema = `
CREATE TABLE IF NOT EXISTS schema_version (
  version INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS registrations (
  id                    TEXT PRIMARY KEY,
  endpoint              TEXT NOT NULL UNIQUE,
  p256dh                TEXT NOT NULL,
  auth                  TEXT NOT NULL,
  management_token_hash TEXT NOT NULL,
  source                TEXT NOT NULL DEFAULT 'pwa',
  created_at            TEXT NOT NULL,
  last_seen             TEXT NOT NULL,
  user_agent            TEXT
);
CREATE TABLE IF NOT EXISTS registration_topics (
  registration_id TEXT NOT NULL REFERENCES registrations(id) ON DELETE CASCADE,
  topic           TEXT NOT NULL,
  PRIMARY KEY (registration_id, topic)
);
CREATE INDEX IF NOT EXISTS idx_registration_topics_topic ON registration_topics(topic);
`

// migrate creates the schema if absent and refuses a mismatched version.
func migrate(db *sql.DB, schema string) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	var version int
	err := db.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		_, err := db.Exec("INSERT INTO schema_version (version) VALUES (?)", SchemaVersion)
		return err
	}
	if err != nil {
		return err
	}
	if version != SchemaVersion {
		return fmt.Errorf("%w: have %d, want %d", ErrSchemaMismatch, version, SchemaVersion)
	}
	return nil
}

// --- company TUF state ---

// Company returns the persisted TUF state for companyID.
func (s *Store) Company(companyID string) (*CompanyTUF, error) {
	var c CompanyTUF
	var root []byte
	var chain string
	var refreshed, next sql.NullString
	err := s.main.QueryRow(`SELECT company_id, root, root_version, chain_state, refreshed_at, next_refresh_at
		FROM company_tuf WHERE company_id = ?`, companyID).
		Scan(&c.CompanyID, &root, &c.RootVersion, &chain, &refreshed, &next)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Root = root
	var state struct {
		Targets        []byte `json:"targets"`
		TargetsVersion int64  `json:"targets_version"`
		TargetsExpires string `json:"targets_expires"`
	}
	if err := json.Unmarshal([]byte(chain), &state); err != nil {
		return nil, fmt.Errorf("company %s: chain_state: %w", companyID, err)
	}
	c.Targets = state.Targets
	c.TargetsVersion = state.TargetsVersion
	if state.TargetsExpires != "" {
		if c.TargetsExpires, err = time.Parse(time.RFC3339, state.TargetsExpires); err != nil {
			return nil, fmt.Errorf("company %s: targets_expires: %w", companyID, err)
		}
	}
	if refreshed.Valid {
		if c.RefreshedAt, err = time.Parse(time.RFC3339, refreshed.String); err != nil {
			return nil, err
		}
	}
	if c.NextRefreshAt, err = time.Parse(time.RFC3339, next.String); err != nil {
		return nil, err
	}
	return &c, nil
}

// State returns the TUF client state for a persisted company row.
func (c *CompanyTUF) State() *tufclient.State {
	return &tufclient.State{
		CompanyID:      c.CompanyID,
		Root:           c.Root,
		RootVersion:    c.RootVersion,
		Targets:        c.Targets,
		TargetsVersion: c.TargetsVersion,
		TargetsExpires: c.TargetsExpires,
		RefreshedAt:    c.RefreshedAt,
	}
}

// Companies returns every known company's persisted TUF state.
func (s *Store) Companies() ([]*CompanyTUF, error) {
	rows, err := s.main.Query(`SELECT company_id FROM company_tuf ORDER BY company_id`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	out := make([]*CompanyTUF, 0, len(ids))
	for _, id := range ids {
		c, err := s.Company(id)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// SaveRoot persists verified root progress for a company, creating the row if
// needed. It is called for every verified root, so a later metadata failure
// never loses root progress (§5.2). A new row is immediately eligible for a
// scheduled refresh.
func (s *Store) SaveRoot(companyID string, root []byte, version int64, now time.Time) error {
	_, err := s.main.Exec(`INSERT INTO company_tuf (company_id, root, root_version, chain_state, next_refresh_at)
		VALUES (?, ?, ?, '{}', ?)
		ON CONFLICT(company_id) DO UPDATE SET root = excluded.root, root_version = excluded.root_version`,
		companyID, root, version, nowString(now))
	return err
}

// SaveState persists a completed refresh.
func (s *Store) SaveState(state *tufclient.State, nextRefreshAt time.Time) error {
	chain, err := json.Marshal(map[string]any{
		"targets":         state.Targets,
		"targets_version": state.TargetsVersion,
		"targets_expires": state.TargetsExpires.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	_, err = s.main.Exec(`INSERT INTO company_tuf
		(company_id, root, root_version, chain_state, refreshed_at, next_refresh_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(company_id) DO UPDATE SET
			root = excluded.root,
			root_version = excluded.root_version,
			chain_state = excluded.chain_state,
			refreshed_at = excluded.refreshed_at,
			next_refresh_at = excluded.next_refresh_at`,
		state.CompanyID, state.Root, state.RootVersion, string(chain),
		nowString(state.RefreshedAt), nowString(nextRefreshAt))
	return err
}

// ReserveRefresh atomically reserves the next eligible synchronization attempt
// for a known company (§5.2): it returns the reserved time (now when an attempt
// may start immediately) and never moves an existing reservation later.
func (s *Store) ReserveRefresh(companyID string, now time.Time, interval time.Duration) (time.Time, error) {
	tx, err := s.main.Begin()
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	var next string
	err = tx.QueryRow(`SELECT next_refresh_at FROM company_tuf WHERE company_id = ?`, companyID).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, err
	}
	reserved, err := time.Parse(time.RFC3339, next)
	if err != nil {
		return time.Time{}, err
	}
	if !now.Before(reserved) {
		reserved = now
		if _, err := tx.Exec(`UPDATE company_tuf SET next_refresh_at = ? WHERE company_id = ?`,
			nowString(now.Add(interval)), companyID); err != nil {
			return time.Time{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return time.Time{}, err
	}
	return reserved, nil
}

// --- event_log ---

// AcceptEvent writes the acceptance audit row and returns its id, the raw
// status capability and the capability expiry (§5.1, §5.1.1).
func (s *Store) AcceptEvent(companyID, scopeID, topic string, ttl time.Duration, now time.Time) (int64, string, time.Time, error) {
	requestID, err := randomToken()
	if err != nil {
		return 0, "", time.Time{}, err
	}
	expires := now.Add(ttl)
	res, err := s.main.Exec(`INSERT INTO event_log
		(request_id, request_expires_at, company_id, scope_id, status, topic, at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		requestID, nowString(expires), companyID, scopeID, StatusPending, topic, nowString(now))
	if err != nil {
		return 0, "", time.Time{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, "", time.Time{}, err
	}
	return id, requestID, expires, nil
}

// CompleteEvent records the per-leg results of a finished dispatch.
func (s *Store) CompleteEvent(id int64, fcm string, attempted, sent, failed, dead int, now time.Time) error {
	_, err := s.main.Exec(`UPDATE event_log SET
		status = ?, fcm = ?, webpush_attempted = ?, webpush_sent = ?,
		webpush_failed = ?, webpush_dead = ?, completed_at = ?
		WHERE id = ?`,
		StatusComplete, fcm, attempted, sent, failed, dead, nowString(now), id)
	return err
}

// SupersedeEvent marks a pending request superseded by successorID (§5.1).
func (s *Store) SupersedeEvent(id, successorID int64, now time.Time) error {
	_, err := s.main.Exec(`UPDATE event_log SET status = ?, superseded_by_id = ?, completed_at = ?
		WHERE id = ?`, StatusSuperseded, successorID, nowString(now), id)
	return err
}

// EventByRequestID looks up one event by its raw status capability. Expired
// capabilities return ErrNotFound (§5.1.1).
func (s *Store) EventByRequestID(requestID string, now time.Time) (*Event, error) {
	e, err := s.eventByRequestID(requestID, now)
	if err != nil {
		return nil, err
	}
	if !now.IsZero() && !now.Before(e.RequestExpiresAt) {
		return nil, ErrNotFound
	}
	return e, nil
}

func (s *Store) eventByRequestID(requestID string, now time.Time) (*Event, error) {
	var e Event
	var expires, at string
	var completed sql.NullString
	var superseded sql.NullInt64
	var fcm sql.NullString
	var attempted, sent, failed, dead sql.NullInt64
	err := s.main.QueryRow(`SELECT id, request_id, request_expires_at, company_id, scope_id, status,
		superseded_by_id, topic, fcm, webpush_attempted, webpush_sent, webpush_failed, webpush_dead, at, completed_at
		FROM event_log WHERE request_id = ?`, requestID).
		Scan(&e.ID, &e.RequestID, &expires, &e.CompanyID, &e.ScopeID, &e.Status,
			&superseded, &e.Topic, &fcm, &attempted, &sent, &failed, &dead, &at, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if e.RequestExpiresAt, err = time.Parse(time.RFC3339, expires); err != nil {
		return nil, err
	}
	if e.At, err = time.Parse(time.RFC3339, at); err != nil {
		return nil, err
	}
	if completed.Valid {
		t, err := time.Parse(time.RFC3339, completed.String)
		if err != nil {
			return nil, err
		}
		e.CompletedAt = &t
	}
	if superseded.Valid {
		e.SupersededByID = &superseded.Int64
	}
	e.FCM = fcm.String
	e.WebPushAttempted = int(attempted.Int64)
	e.WebPushSent = int(sent.Int64)
	e.WebPushFailed = int(failed.Int64)
	e.WebPushDead = int(dead.Int64)
	return &e, nil
}

// DeleteEvent removes an acceptance row that could not be admitted (full
// queue): replay state and existing queued work are left unchanged (§5.1).
func (s *Store) DeleteEvent(id int64) error {
	_, err := s.main.Exec(`DELETE FROM event_log WHERE id = ?`, id)
	return err
}

// EventByID reads one event row by its row id (used to resolve a successor
// capability for a superseded request, §5.1.1). It does not enforce the
// capability expiry: the successor's own expiry is returned to the caller.
func (s *Store) EventByID(id int64) (*Event, error) {
	var requestID string
	err := s.main.QueryRow(`SELECT request_id FROM event_log WHERE id = ?`, id).Scan(&requestID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.eventByRequestID(requestID, time.Time{})
}

// ClosePendingEvents closes rows left pending by a restart with legs recorded as
// failed (§7).
func (s *Store) ClosePendingEvents(now time.Time) (int64, error) {
	res, err := s.main.Exec(`UPDATE event_log SET status = ?, fcm = COALESCE(fcm, ?), completed_at = ?
		WHERE status = ?`, StatusComplete, FCMFailed, nowString(now), StatusPending)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneEventLog removes rows older than retention.
func (s *Store) PruneEventLog(retention time.Duration, now time.Time) (int64, error) {
	res, err := s.main.Exec(`DELETE FROM event_log WHERE at < ?`, nowString(now.Add(-retention)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- registrations ---

// CreateRegistration stores a new registration and returns its id and management
// token. An existing endpoint returns ErrConflict without overwriting (§5.3).
func (s *Store) CreateRegistration(endpoint, p256dh, auth, source, userAgent string, topics []string, now time.Time) (string, string, error) {
	if len(topics) > MaxTopicsPerRegistration {
		return "", "", ErrTooManyTopics
	}
	token, err := randomToken()
	if err != nil {
		return "", "", err
	}
	id := newUUID()
	tx, err := s.registry.Begin()
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO registrations
		(id, endpoint, p256dh, auth, management_token_hash, source, created_at, last_seen, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, endpoint, p256dh, auth, hashToken(token), source, nowString(now), nowString(now), userAgent)
	if err != nil {
		if isUnique(err) {
			return "", "", ErrConflict
		}
		return "", "", err
	}
	if err := replaceTopics(tx, id, topics); err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return id, token, nil
}

// UpdateRegistration authenticates with managementToken, then replaces the topic
// set and optionally the endpoint and keys. An endpoint owned by another
// registration returns ErrConflict without modifying either record (§5.3).
func (s *Store) UpdateRegistration(id, managementToken string, endpoint, p256dh, auth *string, topics []string, now time.Time) error {
	if len(topics) > MaxTopicsPerRegistration {
		return ErrTooManyTopics
	}
	tx, err := s.registry.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := authenticate(tx, id, managementToken); err != nil {
		return err
	}
	if endpoint != nil {
		if p256dh == nil || auth == nil {
			return errors.New("endpoint and keys must be replaced together")
		}
		var owner string
		err := tx.QueryRow(`SELECT id FROM registrations WHERE endpoint = ?`, *endpoint).Scan(&owner)
		if err == nil && owner != id {
			return ErrConflict
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(`UPDATE registrations SET endpoint = ?, p256dh = ?, auth = ? WHERE id = ?`,
			*endpoint, *p256dh, *auth, id); err != nil {
			if isUnique(err) {
				return ErrConflict
			}
			return err
		}
	}
	if err := replaceTopics(tx, id, topics); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE registrations SET last_seen = ? WHERE id = ?`, nowString(now), id); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteRegistration removes an installation's registration.
func (s *Store) DeleteRegistration(id, managementToken string) error {
	tx, err := s.registry.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := authenticate(tx, id, managementToken); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM registrations WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// HeartbeatRegistration bumps last_seen (§5.3).
func (s *Store) HeartbeatRegistration(id, managementToken string, now time.Time) error {
	tx, err := s.registry.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := authenticate(tx, id, managementToken); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE registrations SET last_seen = ? WHERE id = ?`, nowString(now), id); err != nil {
		return err
	}
	return tx.Commit()
}

// TopicCount returns the number of registrations following topic.
func (s *Store) TopicCount(topic string) (int, error) {
	var n int
	err := s.registry.QueryRow(`SELECT COUNT(*) FROM registration_topics WHERE topic = ?`, topic).Scan(&n)
	return n, err
}

// ForEachRegistrationForTopic streams the registrations following topic in
// batches, never loading the whole fan-out into memory (§6.2). The callback is
// invoked in keyset order; returning an error stops the walk.
func (s *Store) ForEachRegistrationForTopic(ctx context.Context, topic string, batch int, fn func(Registration) error) error {
	if batch <= 0 {
		batch = 500
	}
	last := ""
	for {
		rows, err := s.registry.QueryContext(ctx, `SELECT r.id, r.endpoint, r.p256dh, r.auth, r.source, r.created_at, r.last_seen, COALESCE(r.user_agent, '')
			FROM registrations r
			JOIN registration_topics t ON t.registration_id = r.id
			WHERE t.topic = ? AND r.id > ?
			ORDER BY r.id LIMIT ?`, topic, last, batch)
		if err != nil {
			return err
		}
		n := 0
		for rows.Next() {
			var r Registration
			var created, seen string
			if err := rows.Scan(&r.ID, &r.Endpoint, &r.P256DH, &r.Auth, &r.Source, &created, &seen, &r.UserAgent); err != nil {
				rows.Close()
				return err
			}
			if r.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
				rows.Close()
				return err
			}
			if r.LastSeen, err = time.Parse(time.RFC3339, seen); err != nil {
				rows.Close()
				return err
			}
			last = r.ID
			n++
			if err := fn(r); err != nil {
				rows.Close()
				return err
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if n < batch {
			return nil
		}
	}
}

// SweepRegistrations removes registrations whose last_seen is older than ttl.
func (s *Store) SweepRegistrations(ttl time.Duration, now time.Time) (int64, error) {
	res, err := s.registry.Exec(`DELETE FROM registrations WHERE last_seen < ?`, nowString(now.Add(-ttl)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func authenticate(tx *sql.Tx, id, managementToken string) error {
	var stored string
	err := tx.QueryRow(`SELECT management_token_hash FROM registrations WHERE id = ?`, id).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if stored != hashToken(managementToken) {
		return ErrUnauthorized
	}
	return nil
}

func replaceTopics(tx *sql.Tx, id string, topics []string) error {
	if _, err := tx.Exec(`DELETE FROM registration_topics WHERE registration_id = ?`, id); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, t := range topics {
		if seen[t] {
			continue
		}
		seen[t] = true
		if _, err := tx.Exec(`INSERT INTO registration_topics (registration_id, topic) VALUES (?, ?)`, id, t); err != nil {
			return err
		}
	}
	return nil
}

// --- helpers ---

// HashToken returns the SHA-256 hex hash of a management token; only the hash
// is stored at rest (§5.3).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func hashToken(token string) string { return HashToken(token) }

// randomToken returns a fresh 256-bit unpadded base64url capability.
func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func newUUID() string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

func nowString(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "constraint failed")
}
