// Package tufrepo builds, loads and verifies the Keryx TUF repository
// (spec/repository.md) using go-tuf v2 as the crypto core. Root metadata
// lives exclusively at the well-known anchor; everything else in the repo base.
package tufrepo

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/trustedmetadata"
	"github.com/v1b3coder/keryx/sdk/feed"
	"github.com/v1b3coder/keryx/sdk/keys"
	"github.com/v1b3coder/keryx/sdk/repo"
)

// Expiries are the metadata lifetimes. The reference defaults follow
// spec/repository.md §5 (root 10 years — a last-resort backstop refreshed
// only by a master ceremony; timestamp 24–72 h).
type Expiries struct {
	Root      time.Duration
	Targets   time.Duration
	Snapshot  time.Duration
	Timestamp time.Duration
	Channel   time.Duration
	Authors   time.Duration
}

// DefaultExpiries returns the reference lifetimes.
func DefaultExpiries() Expiries {
	return Expiries{
		Root:      10 * 365 * 24 * time.Hour,
		Targets:   365 * 24 * time.Hour,
		Snapshot:  30 * 24 * time.Hour,
		Timestamp: 48 * time.Hour,
		Channel:   180 * 24 * time.Hour,
		Authors:   180 * 24 * time.Hour,
	}
}

// State is the whole repository in memory. It is the only shared state
// (design/tooling.md §4): versions, items and custom all derive from it.
type State struct {
	Root      *metadata.Metadata[metadata.RootType]
	Targets   *metadata.Metadata[metadata.TargetsType]
	Snapshot  *metadata.Metadata[metadata.SnapshotType]
	Timestamp *metadata.Metadata[metadata.TimestampType]
	Channels  map[string]*metadata.Metadata[metadata.TargetsType]
	Authors   map[string]*metadata.Metadata[metadata.TargetsType]
	Items     map[string][]byte // target path -> item bytes
	Roots     map[int64][]byte  // root version -> raw bytes (anchor only)
}

// NewState builds an empty full-mode repository.
func NewState(now time.Time, repoBase, companyName string) (*State, error) {
	e := DefaultExpiries()
	root := metadata.Root(now.Add(e.Root))
	root.Signed.ConsistentSnapshot = false
	root.Signed.UnrecognizedFields = map[string]any{
		"custom": map[string]any{"repo_base": repoBase, "mode": "full"},
	}
	targets := metadata.Targets(now.Add(e.Targets))
	targets.Signed.Delegations = &metadata.Delegations{Keys: map[string]*metadata.Key{}, Roles: []metadata.DelegatedRole{}}
	targets.Signed.UnrecognizedFields = map[string]any{"custom": map[string]any{
		"company_name":          companyName,
		"channels":              map[string]any{},
		"private_feed_patterns": []any{},
	}}
	snapshot := metadata.Snapshot(now.Add(e.Snapshot))
	snapshot.Signed.Meta = map[string]*metadata.MetaFiles{}
	timestamp := metadata.Timestamp(now.Add(e.Timestamp))
	timestamp.Signed.Meta = map[string]*metadata.MetaFiles{}
	return &State{
		Root: root, Targets: targets, Snapshot: snapshot, Timestamp: timestamp,
		Channels: map[string]*metadata.Metadata[metadata.TargetsType]{},
		Authors:  map[string]*metadata.Metadata[metadata.TargetsType]{},
		Items:    map[string][]byte{},
		Roots:    map[int64][]byte{},
	}, nil
}

// customMap returns the master-signed custom object, creating it if needed.
func customMap(t *metadata.Metadata[metadata.TargetsType]) map[string]any {
	if t.Signed.UnrecognizedFields == nil {
		t.Signed.UnrecognizedFields = map[string]any{}
	}
	c, _ := t.Signed.UnrecognizedFields["custom"].(map[string]any)
	if c == nil {
		c = map[string]any{}
		t.Signed.UnrecognizedFields["custom"] = c
	}
	return c
}

// Custom returns the master-signed custom map of targets.json.
func (s *State) Custom() map[string]any { return customMap(s.Targets) }

// RepoBase returns root custom.repo_base.
func (s *State) RepoBase() string {
	v, _ := customRoot(s.Root)["repo_base"].(string)
	return v
}

// Mode returns root custom.mode (default "full").
func (s *State) Mode() string {
	v, _ := customRoot(s.Root)["mode"].(string)
	if v == "" {
		return "full"
	}
	return v
}

// CompanyName returns custom.company_name.
func (s *State) CompanyName() string {
	v, _ := s.Custom()["company_name"].(string)
	return v
}

// Logo returns custom.logo.
func (s *State) Logo() string {
	v, _ := s.Custom()["logo"].(string)
	return v
}

// ChannelDisplay returns custom.channels[channel] as a map.
func (s *State) ChannelDisplay(channel string) map[string]any {
	chs, _ := s.Custom()["channels"].(map[string]any)
	if chs == nil {
		return nil
	}
	m, _ := chs[channel].(map[string]any)
	return m
}

// PrivatePatterns returns custom.private_feed_patterns.
func (s *State) PrivatePatterns() []any {
	v, _ := s.Custom()["private_feed_patterns"].([]any)
	return v
}

// Delegation returns the delegation named roleName, or nil.
func (s *State) Delegation(roleName string) *metadata.DelegatedRole {
	if s.Targets.Signed.Delegations == nil {
		return nil
	}
	for i := range s.Targets.Signed.Delegations.Roles {
		if s.Targets.Signed.Delegations.Roles[i].Name == roleName {
			return &s.Targets.Signed.Delegations.Roles[i]
		}
	}
	return nil
}

// HasAuthors reports whether the channel is authored (default mode).
func (s *State) HasAuthors(channel string) bool {
	return s.Delegation("channels."+channel+".authors") != nil
}

// ChannelNames returns the public channel names, sorted.
func (s *State) ChannelNames() []string {
	out := make([]string, 0, len(s.Channels))
	for name := range s.Channels {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Load reads and parses the repository from base + anchor. It does not verify
// signatures; call Verify for that.
func Load(_ context.Context, base, anchor repo.Repo) (*State, error) {
	st := &State{
		Channels: map[string]*metadata.Metadata[metadata.TargetsType]{},
		Authors:  map[string]*metadata.Metadata[metadata.TargetsType]{},
		Items:    map[string][]byte{},
		Roots:    map[int64][]byte{},
	}
	rootBytes, err := anchor.Read(context.Background(), "root.json")
	if err != nil {
		return nil, fmt.Errorf("root anchor: %w", err)
	}
	root, err := metadata.Root().FromBytes(rootBytes)
	if err != nil {
		return nil, fmt.Errorf("root.json: %w", err)
	}
	st.Root = root
	st.Roots[root.Signed.Version] = rootBytes
	// every released N.root.json (TUF mandate). The versioned file is
	// authoritative for the chain walk; root.json is the same bytes and is the
	// fallback for repositories that predate the versioned file.
	for v := int64(1); v <= root.Signed.Version; v++ {
		data, err := anchor.Read(context.Background(), fmt.Sprintf("%d.root.json", v))
		if err == nil {
			st.Roots[v] = data
		}
	}
	if err := loadMeta(base, "targets.json", &st.Targets); err != nil {
		return nil, err
	}
	if err := loadMeta(base, "snapshot.json", &st.Snapshot); err != nil {
		return nil, err
	}
	if err := loadMeta(base, "timestamp.json", &st.Timestamp); err != nil {
		return nil, err
	}
	if st.Targets.Signed.Delegations == nil {
		return nil, fmt.Errorf("targets.json: delegations missing")
	}
	for _, role := range st.Targets.Signed.Delegations.Roles {
		if !strings.HasPrefix(role.Name, "channels.") {
			continue
		}
		rest := strings.TrimPrefix(role.Name, "channels.")
		if strings.HasSuffix(rest, ".authors") {
			channel := strings.TrimSuffix(rest, ".authors")
			var m *metadata.Metadata[metadata.TargetsType]
			if err := loadMeta(base, role.Name+".json", &m); err != nil {
				return nil, err
			}
			st.Authors[channel] = m
			continue
		}
		var m *metadata.Metadata[metadata.TargetsType]
		if err := loadMeta(base, role.Name+".json", &m); err != nil {
			return nil, err
		}
		st.Channels[rest] = m
	}
	for _, ch := range st.Channels {
		for path := range ch.Signed.Targets {
			data, err := base.Read(context.Background(), path)
			if err != nil {
				return nil, fmt.Errorf("item %s: %w", path, err)
			}
			st.Items[path] = data
		}
	}
	return st, nil
}

func loadMeta[T metadata.Roles](base repo.Repo, name string, out **metadata.Metadata[T]) error {
	data, err := base.Read(context.Background(), name)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	var zero metadata.Metadata[T]
	meta, err := zero.FromBytes(data)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	*out = meta
	return nil
}

// Prepare fills the snapshot/timestamp cross-references from the current
// metadata versions; call before Verify on a freshly built repo.
func (s *State) Prepare() error {
	if err := s.BuildSnapshot(); err != nil {
		return err
	}
	snapshotBytes, err := s.Snapshot.ToBytes(true)
	if err != nil {
		return err
	}
	return s.BuildTimestamp(snapshotBytes)
}

// Write writes the whole state: metadata + items to the repo base, and every
// root version to the anchor (spec/repository.md §1 — root only at the anchor).
func (s *State) Write(_ context.Context, base, anchor repo.Repo) error {
	ctx := context.Background()
	if err := s.BuildSnapshot(); err != nil {
		return err
	}
	write := func(r repo.Repo, name string, data []byte) error {
		return r.Write(ctx, name, data)
	}
	targetsBytes, err := s.Targets.ToBytes(true)
	if err != nil {
		return err
	}
	if err := write(base, "targets.json", targetsBytes); err != nil {
		return err
	}
	for name, m := range s.Channels {
		b, err := m.ToBytes(true)
		if err != nil {
			return err
		}
		if err := write(base, "channels."+name+".json", b); err != nil {
			return err
		}
	}
	for name, m := range s.Authors {
		b, err := m.ToBytes(true)
		if err != nil {
			return err
		}
		if err := write(base, "channels."+name+".authors.json", b); err != nil {
			return err
		}
	}
	snapshotBytes, err := s.Snapshot.ToBytes(true)
	if err != nil {
		return err
	}
	if err := write(base, "snapshot.json", snapshotBytes); err != nil {
		return err
	}
	if err := s.BuildTimestamp(snapshotBytes); err != nil {
		return err
	}
	timestampBytes, err := s.Timestamp.ToBytes(true)
	if err != nil {
		return err
	}
	if err := write(base, "timestamp.json", timestampBytes); err != nil {
		return err
	}
	// drop metadata for channels that no longer exist: a removed channel's
	// role files are no longer pinned by snapshot.json, but a stale copy must
	// not be published by deploy
	if existing, err := base.List(ctx, "."); err == nil {
		for _, name := range existing {
			baseName := name
			if i := strings.LastIndex(baseName, "/"); i >= 0 {
				baseName = baseName[i+1:]
			}
			if strings.HasPrefix(baseName, "channels.") && strings.HasSuffix(baseName, ".json") {
				if !s.knownMetadata(baseName) {
					if err := base.Remove(ctx, name); err != nil {
						return err
					}
				}
				continue
			}
			// item target files are channels/<name>/<id>.json
			if strings.HasPrefix(name, "channels/") {
				if _, ok := s.Items[name]; !ok {
					if err := base.Remove(ctx, name); err != nil {
						return err
					}
				}
			}
		}
	}
	for path, data := range s.Items {
		if err := write(base, path, data); err != nil {
			return err
		}
	}
	rootBytes, err := s.Root.ToBytes(true)
	if err != nil {
		return err
	}
	s.Roots[s.Root.Signed.Version] = rootBytes
	for v, data := range s.Roots {
		if err := write(anchor, fmt.Sprintf("%d.root.json", v), data); err != nil {
			return err
		}
	}
	return write(anchor, "root.json", rootBytes)
}

// buildSnapshot pins every non-root metadata file at its current version.
func (s *State) BuildSnapshot() error {
	meta := map[string]*metadata.MetaFiles{}
	put := func(name string, data []byte, version int64) {
		meta[name] = metaFilesFor(version, data)
	}
	targetsBytes, err := s.Targets.ToBytes(true)
	if err != nil {
		return err
	}
	put("targets.json", targetsBytes, s.Targets.Signed.Version)
	for name, m := range s.Channels {
		b, err := m.ToBytes(true)
		if err != nil {
			return err
		}
		put("channels."+name+".json", b, m.Signed.Version)
	}
	for name, m := range s.Authors {
		b, err := m.ToBytes(true)
		if err != nil {
			return err
		}
		put("channels."+name+".authors.json", b, m.Signed.Version)
	}
	s.Snapshot.Signed.Meta = meta
	return nil
}

// buildTimestamp pins the snapshot at its current version.
func (s *State) BuildTimestamp(snapshotBytes []byte) error {
	s.Timestamp.Signed.Meta = map[string]*metadata.MetaFiles{
		"snapshot.json": metaFilesFor(s.Snapshot.Signed.Version, snapshotBytes),
	}
	return nil
}

// RefreshTimestamp re-signs timestamp with a fresh expiry and pins the current
// snapshot (the cron line, spec/clients.md §2).
func (s *State) RefreshTimestamp(ops *keys.Key, expires time.Duration, now time.Time) error {
	snapshotBytes, err := s.Snapshot.ToBytes(true)
	if err != nil {
		return err
	}
	version := s.Timestamp.Signed.Version
	s.Timestamp = metadata.Timestamp(now.Add(expires))
	// the constructor starts at v1: a refresh must move the version forward,
	// never back — clients reject a lower timestamp version as a rollback
	s.Timestamp.Signed.Version = version + 1
	if err := s.BuildTimestamp(snapshotBytes); err != nil {
		return err
	}
	return SignAndTag(s.Timestamp, ops)
}

// SignAndTag signs meta and retags the signature keyid to the key's
// TUF-standard keyid (our key objects carry a human-readable `name`).
func SignAndTag[T metadata.Roles](meta *metadata.Metadata[T], k *keys.Key) error {
	signer, err := k.Signer()
	if err != nil {
		return err
	}
	if _, err := meta.Sign(signer); err != nil {
		return err
	}
	meta.Signatures[len(meta.Signatures)-1].KeyID = k.KeyID()
	return nil
}

func metaFilesFor(version int64, data []byte) *metadata.MetaFiles {
	sum := sha256.Sum256(data)
	return &metadata.MetaFiles{Version: version, Length: int64(len(data)), Hashes: metadata.Hashes{"sha256": sum[:]}}
}

// keySet resolves a delegation's keyids to raw Ed25519 public keys.
func keySet(dlg *metadata.Delegations, roleName string) (map[string]ed25519.PublicKey, int, error) {
	if dlg == nil {
		return nil, 0, fmt.Errorf("no delegations")
	}
	role := findRole(dlg.Roles, roleName)
	if role == nil {
		return nil, 0, fmt.Errorf("delegation %q not found", roleName)
	}
	out := map[string]ed25519.PublicKey{}
	for _, keyid := range role.KeyIDs {
		key := dlg.Keys[keyid]
		if key == nil {
			return nil, 0, fmt.Errorf("delegation %q: key object for %s missing", roleName, keyid)
		}
		pub, err := key.ToPublicKey()
		if err != nil {
			return nil, 0, fmt.Errorf("delegation %q: key %s: %w", roleName, keyid, err)
		}
		ed, ok := pub.(ed25519.PublicKey)
		if !ok {
			return nil, 0, fmt.Errorf("delegation %q: key %s is not Ed25519", roleName, keyid)
		}
		out[keyid] = ed
	}
	return out, role.Threshold, nil
}

func findRole(roles []metadata.DelegatedRole, name string) *metadata.DelegatedRole {
	for i := range roles {
		if roles[i].Name == name {
			return &roles[i]
		}
	}
	return nil
}

// Verify performs the full check of spec/clients.md §2: root chain, the
// timestamp → snapshot → targets → channel role chain, delegation invariants,
// item hash/length + signature rules, and master-signed custom.
func (s *State) Verify() error {
	if err := s.verifyRootChain(); err != nil {
		return err
	}
	if err := s.Root.VerifyDelegate(metadata.ROOT, s.Root); err != nil {
		return fmt.Errorf("root self-signature: %w", err)
	}
	if s.Root.Signed.ConsistentSnapshot {
		return fmt.Errorf("root.json: consistent_snapshot must be false")
	}
	if base, _ := customRoot(s.Root)["repo_base"].(string); base == "" {
		return fmt.Errorf("root.json: custom.repo_base missing")
	}
	if mode, _ := customRoot(s.Root)["mode"].(string); mode != "" && mode != "full" {
		return fmt.Errorf("root.json: custom.mode %q not supported", mode)
	}
	if err := s.Root.VerifyDelegate(metadata.TIMESTAMP, s.Timestamp); err != nil {
		return fmt.Errorf("timestamp signature (by root): %w", err)
	}
	if err := s.Root.VerifyDelegate(metadata.SNAPSHOT, s.Snapshot); err != nil {
		return fmt.Errorf("snapshot signature (by root): %w", err)
	}
	if err := s.Root.VerifyDelegate(metadata.TARGETS, s.Targets); err != nil {
		return fmt.Errorf("targets signature (by root): %w", err)
	}
	// snapshot/timestamp cross-references (version + hashes)
	if info := s.Timestamp.Signed.Meta["snapshot.json"]; info == nil {
		return fmt.Errorf("timestamp.json: meta.snapshot.json missing")
	} else {
		b, err := s.Snapshot.ToBytes(true)
		if err != nil {
			return err
		}
		if err := info.VerifyLengthHashes(b); err != nil {
			return fmt.Errorf("timestamp → snapshot: %w", err)
		}
		if info.Version != s.Snapshot.Signed.Version {
			return fmt.Errorf("timestamp references snapshot v%d, have v%d", info.Version, s.Snapshot.Signed.Version)
		}
	}
	targetsBytes, err := s.Targets.ToBytes(true)
	if err != nil {
		return err
	}
	if info := s.Snapshot.Signed.Meta["targets.json"]; info == nil {
		return fmt.Errorf("snapshot.json: meta.targets.json missing")
	} else {
		if err := info.VerifyLengthHashes(targetsBytes); err != nil {
			return fmt.Errorf("snapshot → targets: %w", err)
		}
		if info.Version != s.Targets.Signed.Version {
			return fmt.Errorf("snapshot references targets v%d, have v%d", info.Version, s.Targets.Signed.Version)
		}
	}

	// targets.json delegation invariants + custom
	if err := validateTargetsCustom(s.Targets); err != nil {
		return err
	}
	dlg := s.Targets.Signed.Delegations
	if dlg == nil {
		return fmt.Errorf("targets.json: delegations missing")
	}
	for _, role := range dlg.Roles {
		if !strings.HasPrefix(role.Name, "channels.") {
			return fmt.Errorf("delegation %q: role name must be channels.<channel>", role.Name)
		}
		rest := strings.TrimPrefix(role.Name, "channels.")
		if strings.HasSuffix(rest, ".authors") {
			channel := strings.TrimSuffix(rest, ".authors")
			if !validChannel(channel) {
				return fmt.Errorf("delegation %q: invalid channel name", role.Name)
			}
			if role.Terminating {
				return fmt.Errorf("authors role %q must not be terminating", role.Name)
			}
			want := "channels/" + channel + "/*"
			if len(role.Paths) != 1 || role.Paths[0] != want {
				return fmt.Errorf("authors role %q: paths %v, want [%s]", role.Name, role.Paths, want)
			}
			continue
		}
		if !validChannel(rest) {
			return fmt.Errorf("delegation %q: invalid channel name", role.Name)
		}
		if !role.Terminating {
			return fmt.Errorf("delegation %q: terminating must be true", role.Name)
		}
		want := "channels/" + rest + "/*"
		if len(role.Paths) != 1 || role.Paths[0] != want {
			return fmt.Errorf("delegation %q: paths %v, want [%s]", role.Name, role.Paths, want)
		}
		if role.Threshold < 1 || len(role.KeyIDs) == 0 {
			return fmt.Errorf("delegation %q: empty threshold/keyids", role.Name)
		}
		for _, keyid := range role.KeyIDs {
			if dlg.Keys[keyid] == nil {
				return fmt.Errorf("delegation %q: key object for %s missing", role.Name, keyid)
			}
		}
	}
	// key separation: authors keyids MUST NOT intersect channel role keyids
	for _, role := range dlg.Roles {
		rest, ok := strings.CutPrefix(role.Name, "channels.")
		if !ok || !strings.HasSuffix(rest, ".authors") {
			continue
		}
		channel := strings.TrimSuffix(rest, ".authors")
		chRole := findRole(dlg.Roles, "channels."+channel)
		if chRole == nil {
			return fmt.Errorf("authors role %q: channel role missing", role.Name)
		}
		for _, kid := range role.KeyIDs {
			for _, chKID := range chRole.KeyIDs {
				if kid == chKID {
					return fmt.Errorf("authors role %q: key %s is also the channel role key", role.Name, kid)
				}
			}
		}
	}
	// channel roles: signature + snapshot pin + item targets + item signatures
	for _, role := range dlg.Roles {
		rest, ok := strings.CutPrefix(role.Name, "channels.")
		if !ok || strings.HasSuffix(rest, ".authors") {
			continue
		}
		channel := rest
		chMeta := s.Channels[channel]
		if chMeta == nil {
			return fmt.Errorf("channel %q: role metadata missing", channel)
		}
		if err := s.Targets.VerifyDelegate(role.Name, chMeta); err != nil {
			return fmt.Errorf("%s signature: %w", role.Name, err)
		}
		b, err := chMeta.ToBytes(true)
		if err != nil {
			return err
		}
		info := s.Snapshot.Signed.Meta[role.Name+".json"]
		if info == nil {
			return fmt.Errorf("snapshot: meta.%s.json missing", role.Name)
		}
		if err := info.VerifyLengthHashes(b); err != nil {
			return fmt.Errorf("snapshot → %s: %w", role.Name, err)
		}
		if info.Version != chMeta.Signed.Version {
			return fmt.Errorf("snapshot references %s v%d, have v%d", role.Name, info.Version, chMeta.Signed.Version)
		}
		if err := s.verifyChannelItems(channel, chMeta); err != nil {
			return err
		}
	}
	// authors roles: signature + snapshot pin + no targets
	for _, role := range dlg.Roles {
		rest, ok := strings.CutPrefix(role.Name, "channels.")
		if !ok || !strings.HasSuffix(rest, ".authors") {
			continue
		}
		channel := strings.TrimSuffix(rest, ".authors")
		authMeta := s.Authors[channel]
		if authMeta == nil {
			return fmt.Errorf("authors role %q: role metadata missing", role.Name)
		}
		if err := s.Targets.VerifyDelegate(role.Name, authMeta); err != nil {
			return fmt.Errorf("%s signature: %w", role.Name, err)
		}
		if len(authMeta.Signed.Targets) != 0 {
			return fmt.Errorf("%s: authors role must pin no targets", role.Name)
		}
		b, err := authMeta.ToBytes(true)
		if err != nil {
			return err
		}
		info := s.Snapshot.Signed.Meta[role.Name+".json"]
		if info == nil {
			return fmt.Errorf("snapshot: meta.%s.json missing", role.Name)
		}
		if err := info.VerifyLengthHashes(b); err != nil {
			return fmt.Errorf("snapshot → %s: %w", role.Name, err)
		}
		if info.Version != authMeta.Signed.Version {
			return fmt.Errorf("snapshot references %s v%d, have v%d", role.Name, info.Version, authMeta.Signed.Version)
		}
	}
	// private-feed patterns: key publication rule
	if err := validatePrivatePatterns(s.Targets); err != nil {
		return err
	}
	return nil
}

// Expired returns an error when any metadata has expired (strict
// validation, design/tooling.md §5 command surface).
func (s *State) Expired(now time.Time) error {
	check := func(name string, exp time.Time) error {
		if exp.Before(now) {
			return fmt.Errorf("%s expired %s", name, exp.UTC().Format(time.RFC3339))
		}
		return nil
	}
	if err := check("root.json", s.Root.Signed.Expires); err != nil {
		return err
	}
	if err := check("targets.json", s.Targets.Signed.Expires); err != nil {
		return err
	}
	if err := check("snapshot.json", s.Snapshot.Signed.Expires); err != nil {
		return err
	}
	if err := check("timestamp.json", s.Timestamp.Signed.Expires); err != nil {
		return err
	}
	for name, m := range s.Channels {
		if err := check("channels."+name+".json", m.Signed.Expires); err != nil {
			return err
		}
	}
	for name, m := range s.Authors {
		if err := check("channels."+name+".authors.json", m.Signed.Expires); err != nil {
			return err
		}
	}
	return nil
}

func (s *State) verifyChannelItems(channel string, chMeta *metadata.Metadata[metadata.TargetsType]) error {
	channelKeys, channelThreshold, err := keySet(s.Targets.Signed.Delegations, "channels."+channel)
	if err != nil {
		return err
	}
	var authorKeys map[string]ed25519.PublicKey
	var authorThreshold int
	if s.HasAuthors(channel) {
		authorKeys, authorThreshold, err = keySet(s.Targets.Signed.Delegations, "channels."+channel+".authors")
		if err != nil {
			return err
		}
	}
	for path, info := range chMeta.Signed.Targets {
		if !strings.HasPrefix(path, "channels/"+channel+"/") {
			return fmt.Errorf("%s: target %s outside channel namespace", channel, path)
		}
		data, ok := s.Items[path]
		if !ok {
			return fmt.Errorf("%s: item %s missing", channel, path)
		}
		if err := info.VerifyLengthHashes(data); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		obj, err := feed.Decode(data)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		id := feed.IDOf(obj)
		want := strings.TrimSuffix(strings.TrimPrefix(path, "channels/"+channel+"/"), ".json")
		if id != want {
			return fmt.Errorf("%s: item id %q != path segment %q", path, id, want)
		}
		if err := feed.VerifyItem(obj, authorKeys, authorThreshold, channelKeys, channelThreshold); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

// verifyRootChain walks every released N.root.json from version 1 to the current
// root, checking the TUF root-update rule: each version is signed by the previous
// root's keys (per its threshold) and self-signed by its own keys. A root that
// fails this would make compliant clients suspend (spec/repository.md §1/§5), so the
// publisher must never write one.
//
// The per-step verification is go-tuf's own trustedmetadata.UpdateRoot (the same
// primitive its client Updater uses), so no root-update crypto is hand-rolled here.
func (s *State) verifyRootChain() error {
	maxV := s.Root.Signed.Version
	first, ok := s.Roots[1]
	if !ok {
		return fmt.Errorf("root chain: 1.root.json missing")
	}
	trusted, err := trustedmetadata.New(first)
	if err != nil {
		return fmt.Errorf("root chain: 1.root.json: %w", err)
	}
	if trusted.Root.Signed.Version != 1 {
		return fmt.Errorf("root chain: 1.root.json declares version %d", trusted.Root.Signed.Version)
	}
	for v := int64(2); v <= maxV; v++ {
		data, ok := s.Roots[v]
		if !ok {
			return fmt.Errorf("root chain: %d.root.json missing", v)
		}
		if _, err := trusted.UpdateRoot(data); err != nil {
			return fmt.Errorf("root chain: v%d: %w", v, err)
		}
	}
	if trusted.Root.Signed.Version != maxV {
		return fmt.Errorf("root chain: current root v%d not reached (chain stops at v%d)", maxV, trusted.Root.Signed.Version)
	}
	return nil
}

// knownMetadata reports whether a channels.<name>[.authors].json file belongs to
// the current state.
func (s *State) knownMetadata(baseName string) bool {
	rest := strings.TrimSuffix(strings.TrimPrefix(baseName, "channels."), ".json")
	if strings.HasSuffix(rest, ".authors") {
		return s.Authors[strings.TrimSuffix(rest, ".authors")] != nil
	}
	return s.Channels[rest] != nil
}

func customRoot(root *metadata.Metadata[metadata.RootType]) map[string]any {
	c, _ := root.Signed.UnrecognizedFields["custom"].(map[string]any)
	if c == nil {
		c = map[string]any{}
	}
	return c
}

func validChannel(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func validateTargetsCustom(targets *metadata.Metadata[metadata.TargetsType]) error {
	c := customMap(targets)
	if _, ok := c["company_name"].(string); !ok {
		return fmt.Errorf("targets.json: custom.company_name missing")
	}
	if logo, ok := c["logo"].(string); ok && logo != "" {
		if strings.HasPrefix(logo, "data:") {
			if !strings.Contains(logo, ";base64,") {
				return fmt.Errorf("targets.json: custom.logo data URL must be base64")
			}
		} else if strings.HasPrefix(logo, "https://") {
			if sum, _ := c["logo_sha256"].(string); sum == "" {
				return fmt.Errorf("targets.json: linked custom.logo requires logo_sha256")
			}
		} else {
			return fmt.Errorf("targets.json: custom.logo must be a data URL or absolute HTTPS URL")
		}
	}
	chs, _ := c["channels"].(map[string]any)
	for name := range chs {
		if !validChannel(name) {
			return fmt.Errorf("targets.json: custom.channels.%s: invalid channel name", name)
		}
	}
	return nil
}

func validatePrivatePatterns(targets *metadata.Metadata[metadata.TargetsType]) error {
	patterns, _ := customMap(targets)["private_feed_patterns"].([]any)
	for i, p := range patterns {
		entry, ok := p.(map[string]any)
		if !ok {
			return fmt.Errorf("private_feed_patterns[%d]: not an object", i)
		}
		if channel, _ := entry["channel"].(string); !validChannel(channel) {
			return fmt.Errorf("private_feed_patterns[%d]: invalid channel", i)
		}
		if pattern, _ := entry["pattern"].(string); pattern == "" {
			return fmt.Errorf("private_feed_patterns[%d]: pattern missing", i)
		}
		keyids, _ := entry["keyids"].([]any)
		keys, _ := entry["keys"].(map[string]any)
		for _, kid := range keyids {
			id, _ := kid.(string)
			if keys[id] == nil {
				return fmt.Errorf("private_feed_patterns[%d]: key object for %s missing", i, id)
			}
		}
	}
	return nil
}
