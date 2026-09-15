// Package publisher implements the publisher-side operations (design/tooling.md
// §3–§6). Every method loads the repo, verifies it, applies the change,
// re-signs, verifies the result, then writes — the CLI and the future web app
// both call these methods.
package publisher

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/v1b3coder/keryx/sdk/feed"
	"github.com/v1b3coder/keryx/sdk/keys"
	"github.com/v1b3coder/keryx/sdk/repo"
	"github.com/v1b3coder/keryx/sdk/tufrepo"
)

// Publisher is the SDK entry point: the repo is the only shared state
// (design/tooling.md §4) and every operation is deterministic.
type Publisher struct {
	Base   repo.Repo
	Anchor repo.Repo
	Keys   keys.Store
	Now    func() time.Time
	Exp    tufrepo.Expiries
}

// New returns a Publisher over the two output directories.
func New(base, anchor repo.Repo, ks keys.Store) *Publisher {
	return &Publisher{
		Base: base, Anchor: anchor, Keys: ks,
		Now: func() time.Time { return time.Now().UTC().Truncate(time.Second) },
		Exp: tufrepo.DefaultExpiries(),
	}
}

// Result is the outcome of a mutating operation.
type Result struct {
	Company  string   `json:"company,omitempty"`
	Channels []string `json:"channels,omitempty"`
	Version  int64    `json:"version,omitempty"`
	Message  string   `json:"message,omitempty"`
}

// InitParams configures a fresh repository.
type InitParams struct {
	RepoBase    string
	CompanyName string
	Logo        string // inline data URL or absolute HTTPS URL
	LogoSHA256  string // required when Logo is a linked URL
}

// Init generates master + ops keys, builds the full 4-role repo and writes
// the anchor + repo directories (spec/clients.md §2).
func (p *Publisher) Init(ctx context.Context, params InitParams) (Result, error) {
	if params.CompanyName == "" {
		return Result{}, fmt.Errorf("company name is required")
	}
	master, err := p.ensureKey(ctx, keys.RoleMaster, "master")
	if err != nil {
		return Result{}, err
	}
	ops, err := p.ensureKey(ctx, keys.RoleOps, "ops")
	if err != nil {
		return Result{}, err
	}
	st, err := tufrepo.NewState(p.now(), params.RepoBase, params.CompanyName)
	if err != nil {
		return Result{}, err
	}
	custom := st.Custom()
	if params.Logo != "" {
		custom["logo"] = params.Logo
		if strings.HasPrefix(params.Logo, "https://") {
			if params.LogoSHA256 == "" {
				return Result{}, fmt.Errorf("linked logo requires logo_sha256")
			}
			custom["logo_sha256"] = params.LogoSHA256
		}
	}
	for _, role := range []string{metadata.ROOT, metadata.TARGETS} {
		if err := st.Root.Signed.AddKey(master.TUF(), role); err != nil {
			return Result{}, err
		}
	}
	for _, role := range []string{metadata.SNAPSHOT, metadata.TIMESTAMP} {
		if err := st.Root.Signed.AddKey(ops.TUF(), role); err != nil {
			return Result{}, err
		}
	}
	if err := tufrepo.SignAndTag(st.Root, master); err != nil {
		return Result{}, err
	}
	if err := tufrepo.SignAndTag(st.Targets, master); err != nil {
		return Result{}, err
	}
	if err := st.BuildSnapshot(); err != nil {
		return Result{}, err
	}
	if err := tufrepo.SignAndTag(st.Snapshot, ops); err != nil {
		return Result{}, err
	}
	snapshotBytes, err := st.Snapshot.ToBytes(true)
	if err != nil {
		return Result{}, err
	}
	if err := st.BuildTimestamp(snapshotBytes); err != nil {
		return Result{}, err
	}
	if err := tufrepo.SignAndTag(st.Timestamp, ops); err != nil {
		return Result{}, err
	}
	if err := st.Verify(); err != nil {
		return Result{}, fmt.Errorf("init: %w", err)
	}
	if err := st.Write(ctx, p.Base, p.Anchor); err != nil {
		return Result{}, err
	}
	return Result{Company: params.CompanyName, Message: "repository initialized"}, nil
}

// loadVerified loads and verifies the current repo state.
func (p *Publisher) loadVerified(ctx context.Context) (*tufrepo.State, error) {
	st, err := tufrepo.Load(ctx, p.Base, p.Anchor)
	if err != nil {
		return nil, err
	}
	if err := st.Verify(); err != nil {
		return nil, fmt.Errorf("repository does not verify: %w", err)
	}
	return st, nil
}

// writeVerified verifies the resulting state and writes it.
func (p *Publisher) writeVerified(ctx context.Context, st *tufrepo.State) error {
	if err := st.Verify(); err != nil {
		return fmt.Errorf("result does not verify: %w", err)
	}
	return st.Write(ctx, p.Base, p.Anchor)
}

func (p *Publisher) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now().UTC().Truncate(time.Second)
}

func (p *Publisher) exp() tufrepo.Expiries {
	if p.Exp != (tufrepo.Expiries{}) {
		return p.Exp
	}
	return tufrepo.DefaultExpiries()
}

// key returns the store key matching role (and name when non-empty).
func (p *Publisher) key(ctx context.Context, role keys.Role, name string) (*keys.Key, error) {
	store, ok := p.Keys.(*keys.DirStore)
	if ok {
		return store.Find(ctx, role, name)
	}
	if name != "" {
		return p.Keys.Get(ctx, name)
	}
	infos, err := p.Keys.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, info := range infos {
		if info.Role == role {
			return p.Keys.Get(ctx, info.KeyID)
		}
	}
	return nil, &keys.ErrMissingKey{Role: string(role)}
}

// keyByID returns the key with the given keyid.
func (p *Publisher) keyByID(ctx context.Context, keyid string) (*keys.Key, error) {
	return p.Keys.Get(ctx, keyid)
}

// ensureKey loads a key by role/name, generating and storing it when missing.
func (p *Publisher) ensureKey(ctx context.Context, role keys.Role, name string) (*keys.Key, error) {
	if store, ok := p.Keys.(*keys.DirStore); ok {
		k, err := store.Find(ctx, role, name)
		if err == nil {
			return k, nil
		}
		if !keys.IsNotFound(err) {
			return nil, err
		}
		k, err = keys.Generate(role, name)
		if err != nil {
			return nil, err
		}
		if err := p.Keys.Add(ctx, k); err != nil {
			return nil, err
		}
		return k, nil
	}
	return p.key(ctx, role, name)
}

// signFresh clears signatures, bumps the version and refreshes expires.
func signFresh[T metadata.Roles](meta *metadata.Metadata[T], expires time.Duration, now time.Time) {
	meta.ClearSignatures()
	switch m := any(&meta.Signed).(type) {
	case *metadata.RootType:
		m.Version++
		m.Expires = now.Add(expires)
	case *metadata.TargetsType:
		m.Version++
		m.Expires = now.Add(expires)
	case *metadata.SnapshotType:
		m.Version++
		m.Expires = now.Add(expires)
	case *metadata.TimestampType:
		m.Version++
		m.Expires = now.Add(expires)
	}
}

// PublishParams configures one publish.
type PublishParams struct {
	Channel string
	Item    map[string]any
}

// Publish verifies an item against the channel's authorization, writes it as a
// TUF target, re-signs the channel role metadata and freshness metadata
// (spec/clients.md §2). No master involvement.
func (p *Publisher) Publish(ctx context.Context, params PublishParams) (Result, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	channel := params.Channel
	if !validChannel(channel) {
		return Result{}, fmt.Errorf("invalid channel name %q", channel)
	}
	chMeta := st.Channels[channel]
	if chMeta == nil {
		return Result{}, fmt.Errorf("unknown channel %q", channel)
	}
	if err := feed.ValidateItem(params.Item); err != nil {
		return Result{}, err
	}
	id := feed.IDOf(params.Item)
	path := feed.TargetPath(channel, id)

	// authorization: authored channels verify the authors threshold; simple
	// channels are signed by the channel key here.
	authorKeys, authorThreshold, err := delegationKeys(st.Targets.Signed.Delegations, "channels."+channel+".authors")
	if err != nil {
		if st.HasAuthors(channel) {
			return Result{}, err
		}
		authorKeys, authorThreshold = nil, 0
	}
	channelKeys, channelThreshold, err := delegationKeys(st.Targets.Signed.Delegations, "channels."+channel)
	if err != nil {
		return Result{}, err
	}
	item := deepCopy(params.Item)
	if st.HasAuthors(channel) {
		if err := feed.VerifyItem(item, authorKeys, authorThreshold, channelKeys, channelThreshold); err != nil {
			return Result{}, fmt.Errorf("publish: %w", err)
		}
		// portability: add the channel-key signature (not load-bearing)
		chKey, err := p.channelKey(ctx, st, channel)
		if err != nil {
			return Result{}, err
		}
		if err := feed.SignItem(item, chKey); err != nil {
			return Result{}, err
		}
	} else {
		chKey, err := p.channelKey(ctx, st, channel)
		if err != nil {
			return Result{}, err
		}
		if err := feed.SignItem(item, chKey); err != nil {
			return Result{}, err
		}
		if err := feed.VerifyItem(item, nil, 0, channelKeys, channelThreshold); err != nil {
			return Result{}, err
		}
	}
	data, err := feed.Encode(item)
	if err != nil {
		return Result{}, err
	}
	st.Items[path] = data
	setTarget(chMeta, path, data)
	now := p.now()
	signFresh(chMeta, p.exp().Channel, now)
	chKey, err := p.channelKey(ctx, st, channel)
	if err != nil {
		return Result{}, err
	}
	if err := tufrepo.SignAndTag(chMeta, chKey); err != nil {
		return Result{}, err
	}
	if err := p.signFreshness(st, now); err != nil {
		return Result{}, err
	}
	if err := p.writeVerified(ctx, st); err != nil {
		return Result{}, err
	}
	return Result{Channels: []string{channel}, Version: chMeta.Signed.Version, Message: "published " + id}, nil
}

// Unpublish removes the item's index entry (absence = unpublished,
// spec/feeds.md §1.3) and re-signs the channel role + freshness.
func (p *Publisher) Unpublish(ctx context.Context, channel, id string) (Result, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	chMeta := st.Channels[channel]
	if chMeta == nil {
		return Result{}, fmt.Errorf("unknown channel %q", channel)
	}
	path := feed.TargetPath(channel, id)
	if _, ok := chMeta.Signed.Targets[path]; !ok {
		return Result{}, fmt.Errorf("channel %q: item %q is not published", channel, id)
	}
	delete(chMeta.Signed.Targets, path)
	delete(st.Items, path)
	now := p.now()
	signFresh(chMeta, p.exp().Channel, now)
	chKey, err := p.channelKey(ctx, st, channel)
	if err != nil {
		return Result{}, err
	}
	if err := tufrepo.SignAndTag(chMeta, chKey); err != nil {
		return Result{}, err
	}
	if err := p.signFreshness(st, now); err != nil {
		return Result{}, err
	}
	if err := p.writeVerified(ctx, st); err != nil {
		return Result{}, err
	}
	return Result{Channels: []string{channel}, Version: chMeta.Signed.Version, Message: "unpublished " + id}, nil
}

// ChannelSpec configures a new channel.
type ChannelSpec struct {
	Name        string
	DisplayName string
	Description string
	Simple      bool
	Threshold   int      // authors-role threshold (default 1)
	KeyID       string   // existing channel key; generated when empty
	Authors     []string // existing author keyids; a key is generated when empty
}

// ChannelAdd creates the channel delegation, its role metadata (the index) and
// its display metadata; authored by default (spec/feeds.md §2.1).
func (p *Publisher) ChannelAdd(ctx context.Context, spec ChannelSpec) (Result, error) {
	if !validChannel(spec.Name) {
		return Result{}, fmt.Errorf("invalid channel name %q", spec.Name)
	}
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	if st.Channels[spec.Name] != nil {
		return Result{}, fmt.Errorf("channel %q already exists", spec.Name)
	}
	chKey, err := p.resolveChannelKey(ctx, spec)
	if err != nil {
		return Result{}, err
	}
	roleName := "channels." + spec.Name
	if err := addDelegation(st.Targets, roleName, []*metadata.Key{chKey.TUF()}, 1, []string{"channels/" + spec.Name + "/*"}, true); err != nil {
		return Result{}, err
	}
	chMeta := metadata.Targets(p.now().Add(p.exp().Channel))
	chMeta.Signed.Delegations = emptyDelegations()
	st.Channels[spec.Name] = chMeta

	if !spec.Simple {
		authorKeys, err := p.resolveAuthorKeys(ctx, spec)
		if err != nil {
			return Result{}, err
		}
		authRole := roleName + ".authors"
		threshold := spec.Threshold
		if threshold < 1 {
			threshold = 1
		}
		if err := addDelegation(st.Targets, authRole, tufKeys(authorKeys), threshold, []string{"channels/" + spec.Name + "/*"}, false); err != nil {
			return Result{}, err
		}
		authMeta := metadata.Targets(p.now().Add(p.exp().Authors))
		authMeta.Signed.Delegations = emptyDelegations()
		for _, k := range authorKeys {
			if err := tufrepo.SignAndTag(authMeta, k); err != nil {
				return Result{}, err
			}
		}
		st.Authors[spec.Name] = authMeta
	}
	display := map[string]any{}
	if spec.DisplayName != "" {
		display["display_name"] = spec.DisplayName
	}
	if spec.Description != "" {
		display["description"] = spec.Description
	}
	setChannelDisplay(st, spec.Name, display)
	now := p.now()
	signFresh(st.Targets, p.exp().Targets, now)
	master, err := p.key(ctx, keys.RoleMaster, "")
	if err != nil {
		return Result{}, err
	}
	if err := tufrepo.SignAndTag(st.Targets, master); err != nil {
		return Result{}, err
	}
	if err := tufrepo.SignAndTag(chMeta, chKey); err != nil {
		return Result{}, err
	}
	if err := p.signFreshness(st, now); err != nil {
		return Result{}, err
	}
	if err := p.writeVerified(ctx, st); err != nil {
		return Result{}, err
	}
	return Result{Channels: []string{spec.Name}, Version: st.Targets.Signed.Version, Message: "channel added"}, nil
}

// ChannelRemove drops the channel delegation and its metadata.
func (p *Publisher) ChannelRemove(ctx context.Context, name string) (Result, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	if st.Channels[name] == nil {
		return Result{}, fmt.Errorf("unknown channel %q", name)
	}
	removeDelegation(st.Targets, "channels."+name)
	removeDelegation(st.Targets, "channels."+name+".authors")
	delete(st.Channels, name)
	delete(st.Authors, name)
	deleteChannelDisplay(st, name)
	now := p.now()
	signFresh(st.Targets, p.exp().Targets, now)
	master, err := p.key(ctx, keys.RoleMaster, "")
	if err != nil {
		return Result{}, err
	}
	if err := tufrepo.SignAndTag(st.Targets, master); err != nil {
		return Result{}, err
	}
	if err := p.signFreshness(st, now); err != nil {
		return Result{}, err
	}
	if err := p.writeVerified(ctx, st); err != nil {
		return Result{}, err
	}
	return Result{Channels: []string{name}, Version: st.Targets.Signed.Version, Message: "channel removed"}, nil
}

// ChannelSet updates master-signed channel display metadata.
func (p *Publisher) ChannelSet(ctx context.Context, name, displayName, description string) (Result, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	if st.Channels[name] == nil {
		return Result{}, fmt.Errorf("unknown channel %q", name)
	}
	display := map[string]any{}
	if displayName != "" {
		display["display_name"] = displayName
	}
	if description != "" {
		display["description"] = description
	}
	setChannelDisplay(st, name, display)
	now := p.now()
	signFresh(st.Targets, p.exp().Targets, now)
	master, err := p.key(ctx, keys.RoleMaster, "")
	if err != nil {
		return Result{}, err
	}
	if err := tufrepo.SignAndTag(st.Targets, master); err != nil {
		return Result{}, err
	}
	if err := p.signFreshness(st, now); err != nil {
		return Result{}, err
	}
	if err := p.writeVerified(ctx, st); err != nil {
		return Result{}, err
	}
	return Result{Channels: []string{name}, Version: st.Targets.Signed.Version, Message: "channel updated"}, nil
}

// ChannelMode switches a channel between authored and simple mode. The mode
// change MUST re-sign the channel's published items with the new mode's keys
// (spec/feeds.md §2.1).
func (p *Publisher) ChannelMode(ctx context.Context, name, mode string) (Result, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	chMeta := st.Channels[name]
	if chMeta == nil {
		return Result{}, fmt.Errorf("unknown channel %q", name)
	}
	switch mode {
	case "authored":
		if st.HasAuthors(name) {
			return Result{}, fmt.Errorf("channel %q is already authored", name)
		}
		author, err := p.ensureKey(ctx, keys.RoleAuthor, name+"-author")
		if err != nil {
			return Result{}, err
		}
		roleName := "channels." + name + ".authors"
		if err := addDelegation(st.Targets, roleName, []*metadata.Key{author.TUF()}, 1, []string{"channels/" + name + "/*"}, false); err != nil {
			return Result{}, err
		}
		authMeta := metadata.Targets(p.now().Add(p.exp().Authors))
		authMeta.Signed.Delegations = emptyDelegations()
		if err := tufrepo.SignAndTag(authMeta, author); err != nil {
			return Result{}, err
		}
		st.Authors[name] = authMeta
		// re-sign the channel's items with the author key (+ channel key)
		chKey, err := p.channelKey(ctx, st, name)
		if err != nil {
			return Result{}, err
		}
		if err := p.resignItems(st, name, []*keys.Key{author, chKey}); err != nil {
			return Result{}, err
		}
	case "simple":
		if !st.HasAuthors(name) {
			return Result{}, fmt.Errorf("channel %q is already simple", name)
		}
		removeDelegation(st.Targets, "channels."+name+".authors")
		delete(st.Authors, name)
		chKey, err := p.channelKey(ctx, st, name)
		if err != nil {
			return Result{}, err
		}
		if err := p.resignItems(st, name, []*keys.Key{chKey}); err != nil {
			return Result{}, err
		}
	default:
		return Result{}, fmt.Errorf("unknown mode %q (want authored|simple)", mode)
	}
	now := p.now()
	signFresh(st.Targets, p.exp().Targets, now)
	master, err := p.key(ctx, keys.RoleMaster, "")
	if err != nil {
		return Result{}, err
	}
	if err := tufrepo.SignAndTag(st.Targets, master); err != nil {
		return Result{}, err
	}
	chKey, err := p.channelKey(ctx, st, name)
	if err != nil {
		return Result{}, err
	}
	signFresh(chMeta, p.exp().Channel, now)
	if err := tufrepo.SignAndTag(chMeta, chKey); err != nil {
		return Result{}, err
	}
	if err := p.signFreshness(st, now); err != nil {
		return Result{}, err
	}
	if err := p.writeVerified(ctx, st); err != nil {
		return Result{}, err
	}
	return Result{Channels: []string{name}, Version: st.Targets.Signed.Version, Message: "mode " + mode}, nil
}

// AuthorAdd adds an author keyid to an authored channel's delegation and
// re-signs the authors role metadata (spec/repository.md §5).
func (p *Publisher) AuthorAdd(ctx context.Context, channel, keyid string) (Result, error) {
	return p.authorChange(ctx, channel, keyid, false)
}

// AuthorRevoke drops an author keyid; it refuses to remove the last author
// (use `channel mode <channel> simple` instead, spec/feeds.md §2.1).
func (p *Publisher) AuthorRevoke(ctx context.Context, channel, keyid string) (Result, error) {
	return p.authorChange(ctx, channel, keyid, true)
}

func (p *Publisher) authorChange(ctx context.Context, channel, keyid string, revoke bool) (Result, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	roleName := "channels." + channel + ".authors"
	role := st.Delegation(roleName)
	if role == nil {
		return Result{}, fmt.Errorf("channel %q is not authored (use channel mode %s authored)", channel, channel)
	}
	key, err := p.keyByID(ctx, keyid)
	if err != nil {
		return Result{}, err
	}
	if revoke {
		if !contains(role.KeyIDs, keyid) {
			return Result{}, fmt.Errorf("author %s is not listed on channel %q", keyid, channel)
		}
		if len(role.KeyIDs) <= 1 {
			return Result{}, fmt.Errorf("cannot remove the last author; use channel mode %s simple", channel)
		}
		role.KeyIDs = removeString(role.KeyIDs, keyid)
	} else {
		if contains(role.KeyIDs, keyid) {
			return Result{}, fmt.Errorf("author %s is already listed on channel %q", keyid, channel)
		}
		if chRole := st.Delegation("channels." + channel); chRole != nil && contains(chRole.KeyIDs, keyid) {
			return Result{}, fmt.Errorf("key %s is the channel role key; key separation is required", keyid)
		}
		role.KeyIDs = append(role.KeyIDs, keyid)
	}
	st.Targets.Signed.Delegations.Keys[keyid] = key.TUF()
	authMeta := st.Authors[channel]
	authMeta.ClearSignatures()
	now := p.now()
	authMeta.Signed.Expires = now.Add(p.exp().Authors)
	for _, kid := range role.KeyIDs {
		k, err := p.keyByID(ctx, kid)
		if err != nil {
			return Result{}, err
		}
		if err := tufrepo.SignAndTag(authMeta, k); err != nil {
			return Result{}, err
		}
	}
	signFresh(st.Targets, p.exp().Targets, now)
	master, err := p.key(ctx, keys.RoleMaster, "")
	if err != nil {
		return Result{}, err
	}
	if err := tufrepo.SignAndTag(st.Targets, master); err != nil {
		return Result{}, err
	}
	if err := p.signFreshness(st, now); err != nil {
		return Result{}, err
	}
	if err := p.writeVerified(ctx, st); err != nil {
		return Result{}, err
	}
	action := "added"
	if revoke {
		action = "revoked"
	}
	return Result{Channels: []string{channel}, Version: st.Targets.Signed.Version, Message: "author " + action}, nil
}

// PatternSpec configures a private-feed pattern entry.
type PatternSpec struct {
	Channel     string
	Pattern     string
	KeyID       string
	Threshold   int
	DisplayName string
	Purpose     string
}

// PatternAdd adds a master-signed private-feed pattern entry
// (spec/feeds.md §3).
func (p *Publisher) PatternAdd(ctx context.Context, spec PatternSpec) (Result, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	key, err := p.keyByID(ctx, spec.KeyID)
	if err != nil {
		return Result{}, err
	}
	threshold := spec.Threshold
	if threshold < 1 {
		threshold = 1
	}
	entry := map[string]any{
		"channel":   spec.Channel,
		"pattern":   spec.Pattern,
		"keys":      map[string]*metadata.Key{key.KeyID(): key.TUF()},
		"keyids":    []string{key.KeyID()},
		"threshold": threshold,
	}
	if spec.DisplayName != "" {
		entry["display_name"] = spec.DisplayName
	}
	if spec.Purpose != "" {
		entry["purpose"] = spec.Purpose
	}
	custom := st.Custom()
	patterns, _ := custom["private_feed_patterns"].([]any)
	custom["private_feed_patterns"] = append(patterns, entry)
	now := p.now()
	signFresh(st.Targets, p.exp().Targets, now)
	master, err := p.key(ctx, keys.RoleMaster, "")
	if err != nil {
		return Result{}, err
	}
	if err := tufrepo.SignAndTag(st.Targets, master); err != nil {
		return Result{}, err
	}
	if err := p.signFreshness(st, now); err != nil {
		return Result{}, err
	}
	if err := p.writeVerified(ctx, st); err != nil {
		return Result{}, err
	}
	return Result{Channels: []string{spec.Channel}, Version: st.Targets.Signed.Version, Message: "pattern added"}, nil
}

// PatternRemove drops the pattern entry for a channel.
func (p *Publisher) PatternRemove(ctx context.Context, channel string) (Result, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	custom := st.Custom()
	patterns, _ := custom["private_feed_patterns"].([]any)
	var kept []any
	found := false
	for _, e := range patterns {
		if m, ok := e.(map[string]any); ok && m["channel"] == channel {
			found = true
			continue
		}
		kept = append(kept, e)
	}
	if !found {
		return Result{}, fmt.Errorf("no private-feed pattern for channel %q", channel)
	}
	custom["private_feed_patterns"] = kept
	now := p.now()
	signFresh(st.Targets, p.exp().Targets, now)
	master, err := p.key(ctx, keys.RoleMaster, "")
	if err != nil {
		return Result{}, err
	}
	if err := tufrepo.SignAndTag(st.Targets, master); err != nil {
		return Result{}, err
	}
	if err := p.signFreshness(st, now); err != nil {
		return Result{}, err
	}
	if err := p.writeVerified(ctx, st); err != nil {
		return Result{}, err
	}
	return Result{Channels: []string{channel}, Version: st.Targets.Signed.Version, Message: "pattern removed"}, nil
}

// CompanySet is the identity ceremony: master-signed company name and/or logo.
func (p *Publisher) CompanySet(ctx context.Context, name, logo, logoSHA256 string) (Result, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	custom := st.Custom()
	if name != "" {
		custom["company_name"] = name
	}
	if logo != "" {
		if strings.HasPrefix(logo, "https://") {
			if logoSHA256 == "" {
				return Result{}, fmt.Errorf("linked logo requires logo_sha256")
			}
			custom["logo"] = logo
			custom["logo_sha256"] = logoSHA256
		} else {
			custom["logo"] = logo
			delete(custom, "logo_sha256")
		}
	}
	now := p.now()
	signFresh(st.Targets, p.exp().Targets, now)
	master, err := p.key(ctx, keys.RoleMaster, "")
	if err != nil {
		return Result{}, err
	}
	if err := tufrepo.SignAndTag(st.Targets, master); err != nil {
		return Result{}, err
	}
	if err := p.signFreshness(st, now); err != nil {
		return Result{}, err
	}
	if err := p.writeVerified(ctx, st); err != nil {
		return Result{}, err
	}
	return Result{Company: st.CompanyName(), Version: st.Targets.Signed.Version, Message: "company updated"}, nil
}

// RotateChannelKey adds a new channel key alongside the old (threshold-1
// overlap), re-signs the channel role metadata with old+new and, in simple
// mode, re-signs the channel's items with the new key (spec/repository.md §5).
func (p *Publisher) RotateChannelKey(ctx context.Context, channel string) (Result, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	chMeta := st.Channels[channel]
	if chMeta == nil {
		return Result{}, fmt.Errorf("unknown channel %q", channel)
	}
	newKey, err := p.ensureKey(ctx, keys.RoleChannel, channel+"-"+shortID(p.now()))
	if err != nil {
		return Result{}, err
	}
	roleName := "channels." + channel
	role := st.Delegation(roleName)
	if role == nil {
		return Result{}, fmt.Errorf("channel %q: delegation missing", channel)
	}
	role.KeyIDs = append(role.KeyIDs, newKey.KeyID())
	st.Targets.Signed.Delegations.Keys[newKey.KeyID()] = newKey.TUF()
	if !st.HasAuthors(channel) {
		if err := p.resignItems(st, channel, []*keys.Key{newKey}); err != nil {
			return Result{}, err
		}
	}
	now := p.now()
	signFresh(chMeta, p.exp().Channel, now)
	for _, kid := range role.KeyIDs {
		k, err := p.keyByID(ctx, kid)
		if err != nil {
			return Result{}, err
		}
		if err := tufrepo.SignAndTag(chMeta, k); err != nil {
			return Result{}, err
		}
	}
	signFresh(st.Targets, p.exp().Targets, now)
	master, err := p.key(ctx, keys.RoleMaster, "")
	if err != nil {
		return Result{}, err
	}
	if err := tufrepo.SignAndTag(st.Targets, master); err != nil {
		return Result{}, err
	}
	if err := p.signFreshness(st, now); err != nil {
		return Result{}, err
	}
	if err := p.writeVerified(ctx, st); err != nil {
		return Result{}, err
	}
	return Result{Channels: []string{channel}, Version: st.Targets.Signed.Version, Message: "channel key rotated (overlap)"}, nil
}

// RevokeChannelKey drops a keyid from the channel delegation and re-signs
// the channel role metadata with the remaining keys.
func (p *Publisher) RevokeChannelKey(ctx context.Context, channel, keyid string) (Result, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	role := st.Delegation("channels." + channel)
	if role == nil {
		return Result{}, fmt.Errorf("unknown channel %q", channel)
	}
	if !contains(role.KeyIDs, keyid) {
		return Result{}, fmt.Errorf("key %s is not on channel %q", keyid, channel)
	}
	if len(role.KeyIDs) <= 1 {
		return Result{}, fmt.Errorf("cannot revoke the last channel key")
	}
	role.KeyIDs = removeString(role.KeyIDs, keyid)
	delete(st.Targets.Signed.Delegations.Keys, keyid)
	chMeta := st.Channels[channel]
	now := p.now()
	signFresh(chMeta, p.exp().Channel, now)
	for _, kid := range role.KeyIDs {
		k, err := p.keyByID(ctx, kid)
		if err != nil {
			return Result{}, err
		}
		if err := tufrepo.SignAndTag(chMeta, k); err != nil {
			return Result{}, err
		}
	}
	signFresh(st.Targets, p.exp().Targets, now)
	master, err := p.key(ctx, keys.RoleMaster, "")
	if err != nil {
		return Result{}, err
	}
	if err := tufrepo.SignAndTag(st.Targets, master); err != nil {
		return Result{}, err
	}
	if err := p.signFreshness(st, now); err != nil {
		return Result{}, err
	}
	if err := p.writeVerified(ctx, st); err != nil {
		return Result{}, err
	}
	return Result{Channels: []string{channel}, Version: st.Targets.Signed.Version, Message: "channel key revoked"}, nil
}

// RotateRoot generates a new master key, builds root v+1 signed by the
// previous master keys and self-signed by the new master, and writes it to the
// anchor only (spec/repository.md §1/§5).
func (p *Publisher) RotateRoot(ctx context.Context, announceNext bool) (Result, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	old, err := p.key(ctx, keys.RoleMaster, "")
	if err != nil {
		return Result{}, err
	}
	newMaster, err := p.ensureKey(ctx, keys.RoleMaster, "master-"+shortID(p.now()))
	if err != nil {
		return Result{}, err
	}
	// rebuild root v+1: new master key for root+targets, current ops key
	next := metadata.Root(st.Root.Signed.Expires)
	next.Signed.Version = st.Root.Signed.Version + 1
	next.Signed.ConsistentSnapshot = false
	next.Signed.UnrecognizedFields = map[string]any{
		"custom": map[string]any{"repo_base": st.RepoBase(), "mode": st.Mode()},
	}
	if announceNext {
		next.Signed.UnrecognizedFields["next_key"] = map[string]any{"keyid": newMaster.KeyID()}
	}
	for _, role := range []string{metadata.ROOT, metadata.TARGETS} {
		if err := next.Signed.AddKey(newMaster.TUF(), role); err != nil {
			return Result{}, err
		}
	}
	for _, role := range []string{metadata.SNAPSHOT, metadata.TIMESTAMP} {
		ops, err := p.key(ctx, keys.RoleOps, "")
		if err != nil {
			return Result{}, err
		}
		if err := next.Signed.AddKey(ops.TUF(), role); err != nil {
			return Result{}, err
		}
	}
	if err := tufrepo.SignAndTag(next, old); err != nil {
		return Result{}, err
	}
	if err := tufrepo.SignAndTag(next, newMaster); err != nil {
		return Result{}, err
	}
	st.Root = next
	// targets.json was signed by the previous master key; the new root
	// authorizes only the new key, so re-sign it during the ceremony
	if err := tufrepo.SignAndTag(st.Targets, newMaster); err != nil {
		return Result{}, err
	}
	// the targets hash changed — refresh snapshot/timestamp (ops key)
	if err := p.signFreshness(st, p.now()); err != nil {
		return Result{}, err
	}
	if err := st.Verify(); err != nil {
		return Result{}, fmt.Errorf("rotated root does not verify: %w", err)
	}
	if err := st.Write(ctx, p.Base, p.Anchor); err != nil {
		return Result{}, err
	}
	return Result{Version: next.Signed.Version, Message: "root rotated"}, nil
}

// RefreshTimestamp re-signs timestamp with a fresh expiry (the cron line).
func (p *Publisher) RefreshTimestamp(ctx context.Context) (Result, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	ops, err := p.key(ctx, keys.RoleOps, "")
	if err != nil {
		return Result{}, err
	}
	now := p.now()
	if err := st.RefreshTimestamp(ops, p.exp().Timestamp, now); err != nil {
		return Result{}, err
	}
	if err := p.writeVerified(ctx, st); err != nil {
		return Result{}, err
	}
	return Result{Version: st.Timestamp.Signed.Version, Message: "timestamp refreshed"}, nil
}

// Validate performs the full check (spec/clients.md §2) without writing.
func (p *Publisher) Validate(ctx context.Context) (Result, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Company:  st.CompanyName(),
		Channels: st.ChannelNames(),
		Version:  st.Targets.Signed.Version,
		Message:  "ok",
	}, nil
}

// signFreshness re-signs snapshot + timestamp with the ops key and bumps
// their versions.
func (p *Publisher) signFreshness(st *tufrepo.State, now time.Time) error {
	ops, err := p.key(context.Background(), keys.RoleOps, "")
	if err != nil {
		return err
	}
	if err := st.Prepare(); err != nil {
		return err
	}
	signFresh(st.Snapshot, p.exp().Snapshot, now)
	if err := tufrepo.SignAndTag(st.Snapshot, ops); err != nil {
		return err
	}
	snapshotBytes, err := st.Snapshot.ToBytes(true)
	if err != nil {
		return err
	}
	signFresh(st.Timestamp, p.exp().Timestamp, now)
	st.Timestamp.Signed.Meta = map[string]*metadata.MetaFiles{
		"snapshot.json": metaFilesFor(st.Snapshot.Signed.Version, snapshotBytes),
	}
	return tufrepo.SignAndTag(st.Timestamp, ops)
}

func (p *Publisher) channelKey(ctx context.Context, st *tufrepo.State, channel string) (*keys.Key, error) {
	role := st.Delegation("channels." + channel)
	if role == nil {
		return nil, fmt.Errorf("channel %q: delegation missing", channel)
	}
	return p.keyByID(ctx, role.KeyIDs[0])
}

func (p *Publisher) resolveChannelKey(ctx context.Context, spec ChannelSpec) (*keys.Key, error) {
	if spec.KeyID != "" {
		return p.keyByID(ctx, spec.KeyID)
	}
	return p.ensureKey(ctx, keys.RoleChannel, spec.Name)
}

func (p *Publisher) resolveAuthorKeys(ctx context.Context, spec ChannelSpec) ([]*keys.Key, error) {
	if len(spec.Authors) > 0 {
		out := make([]*keys.Key, 0, len(spec.Authors))
		for _, kid := range spec.Authors {
			k, err := p.keyByID(ctx, kid)
			if err != nil {
				return nil, err
			}
			out = append(out, k)
		}
		return out, nil
	}
	k, err := p.ensureKey(ctx, keys.RoleAuthor, spec.Name+"-author")
	if err != nil {
		return nil, err
	}
	return []*keys.Key{k}, nil
}

// resignItems re-signs every item of a channel with keys, updating the pinned
// hashes in the channel role metadata (spec/feeds.md §2.1/§2 rotation).
func (p *Publisher) resignItems(st *tufrepo.State, channel string, ks []*keys.Key) error {
	chMeta := st.Channels[channel]
	for path, data := range st.Items {
		if !strings.HasPrefix(path, "channels/"+channel+"/") {
			continue
		}
		item, err := feed.Decode(data)
		if err != nil {
			return err
		}
		item["sig"] = []any{}
		for _, k := range ks {
			if err := feed.SignItem(item, k); err != nil {
				return err
			}
		}
		out, err := feed.Encode(item)
		if err != nil {
			return err
		}
		st.Items[path] = out
		setTarget(chMeta, path, out)
	}
	return nil
}

func delegationKeys(dlg *metadata.Delegations, roleName string) (map[string]ed25519.PublicKey, int, error) {
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
			return nil, 0, err
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

func addDelegation(targets *metadata.Metadata[metadata.TargetsType], name string, ks []*metadata.Key, threshold int, paths []string, terminating bool) error {
	if findRole(targets.Signed.Delegations.Roles, name) != nil {
		return fmt.Errorf("delegation %q already exists", name)
	}
	keyids := make([]string, 0, len(ks))
	for _, k := range ks {
		id, err := k.ID()
		if err != nil {
			return err
		}
		keyids = append(keyids, id)
		targets.Signed.Delegations.Keys[id] = k
	}
	targets.Signed.Delegations.Roles = append(targets.Signed.Delegations.Roles, metadata.DelegatedRole{
		Name: name, KeyIDs: keyids, Threshold: threshold, Paths: paths, Terminating: terminating,
	})
	return nil
}

func removeDelegation(targets *metadata.Metadata[metadata.TargetsType], name string) {
	roles := targets.Signed.Delegations.Roles
	for i := range roles {
		if roles[i].Name == name {
			targets.Signed.Delegations.Roles = append(roles[:i], roles[i+1:]...)
			return
		}
	}
}

func emptyDelegations() *metadata.Delegations {
	return &metadata.Delegations{Keys: map[string]*metadata.Key{}, Roles: []metadata.DelegatedRole{}}
}

func tufKeys(ks []*keys.Key) []*metadata.Key {
	out := make([]*metadata.Key, 0, len(ks))
	for _, k := range ks {
		out = append(out, k.TUF())
	}
	return out
}

func setTarget(meta *metadata.Metadata[metadata.TargetsType], path string, data []byte) {
	sum := sha256Sum(data)
	meta.Signed.Targets[path] = &metadata.TargetFiles{
		Length: int64(len(data)),
		Hashes: metadata.Hashes{"sha256": sum[:]},
	}
}

func setChannelDisplay(st *tufrepo.State, channel string, display map[string]any) {
	custom := st.Custom()
	chs, _ := custom["channels"].(map[string]any)
	if chs == nil {
		chs = map[string]any{}
		custom["channels"] = chs
	}
	chs[channel] = display
}

func deleteChannelDisplay(st *tufrepo.State, channel string) {
	custom := st.Custom()
	if chs, ok := custom["channels"].(map[string]any); ok {
		delete(chs, channel)
	}
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

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func removeString(xs []string, s string) []string {
	out := xs[:0]
	for _, x := range xs {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func shortID(t time.Time) string {
	return fmt.Sprintf("%d", t.Unix())
}

func deepCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch t := v.(type) {
		case map[string]any:
			out[k] = deepCopy(t)
		case []any:
			cp := make([]any, len(t))
			for i, e := range t {
				if em, ok := e.(map[string]any); ok {
					cp[i] = deepCopy(em)
				} else {
					cp[i] = e
				}
			}
			out[k] = cp
		default:
			out[k] = v
		}
	}
	return out
}

// ChannelInfo describes a channel for `pub channel list`.
type ChannelInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	Mode        string `json:"mode"`
	KeyIDs      []string `json:"keyids"`
	Items       int    `json:"items"`
}

// Channels returns the channel list.
func (p *Publisher) Channels(ctx context.Context) ([]ChannelInfo, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return nil, err
	}
	var out []ChannelInfo
	for _, name := range st.ChannelNames() {
		role := st.Delegation("channels." + name)
		display := st.ChannelDisplay(name)
		displayName, _ := display["display_name"].(string)
		description, _ := display["description"].(string)
		mode := "simple"
		if st.HasAuthors(name) {
			mode = "authored"
		}
		out = append(out, ChannelInfo{
			Name: name, DisplayName: displayName, Description: description,
			Mode: mode, KeyIDs: role.KeyIDs, Items: len(st.Channels[name].Signed.Targets),
		})
	}
	return out, nil
}

// AuthorList lists the author keyids of a channel.
func (p *Publisher) AuthorList(ctx context.Context, channel string) ([]string, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return nil, err
	}
	role := st.Delegation("channels." + channel + ".authors")
	if role == nil {
		return nil, nil
	}
	return role.KeyIDs, nil
}

// ItemIDs returns the currently published item ids of a channel.
func (p *Publisher) ItemIDs(ctx context.Context, channel string) ([]string, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return nil, err
	}
	chMeta := st.Channels[channel]
	if chMeta == nil {
		return nil, fmt.Errorf("unknown channel %q", channel)
	}
	var out []string
	for path := range chMeta.Signed.Targets {
		out = append(out, strings.TrimSuffix(strings.TrimPrefix(path, "channels/"+channel+"/"), ".json"))
	}
	sort.Strings(out)
	return out, nil
}

// JoinOrigin returns the repo base origin (for `pub join-url`/`qr`).
func (p *Publisher) JoinOrigin(ctx context.Context) (string, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return "", err
	}
	return st.RepoBase(), nil
}

// PrivatePatterns returns the master-signed private-feed patterns.
func (p *Publisher) PrivatePatterns(ctx context.Context) ([]map[string]any, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, e := range st.PrivatePatterns() {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// BuildPrivateDocument signs a new capability feed with the engine key.
func (p *Publisher) BuildPrivateDocument(ctx context.Context, keyid, feedURL, channel string, items []map[string]any, expires time.Time) (map[string]any, error) {
	key, err := p.keyByID(ctx, keyid)
	if err != nil {
		return nil, err
	}
	return feed.BuildPrivateDocument(key, feedURL, channel, items, expires)
}

// UpdatePrivateDocument rewrites a capability feed (version+1).
func (p *Publisher) UpdatePrivateDocument(ctx context.Context, keyid string, prev map[string]any, items []map[string]any, expires time.Time) (map[string]any, error) {
	key, err := p.keyByID(ctx, keyid)
	if err != nil {
		return nil, err
	}
	return feed.UpdatePrivateDocument(key, prev, items, expires)
}

// ExpirePrivateDocument closes a capability feed (expired: true).
func (p *Publisher) ExpirePrivateDocument(ctx context.Context, keyid string, prev map[string]any) (map[string]any, error) {
	key, err := p.keyByID(ctx, keyid)
	if err != nil {
		return nil, err
	}
	return feed.ExpirePrivateDocument(key, prev)
}

// EncodeItem serializes an item deterministically.
func EncodeItem(item map[string]any) ([]byte, error) { return feed.Encode(item) }

// DecodeItem parses an item.
func DecodeItem(data []byte) (map[string]any, error) { return feed.Decode(data) }

// VerifyItem verifies an item's signatures.
func VerifyItem(item map[string]any, authorKeys map[string]ed25519.PublicKey, authorThreshold int, channelKeys map[string]ed25519.PublicKey, channelThreshold int) error {
	return feed.VerifyItem(item, authorKeys, authorThreshold, channelKeys, channelThreshold)
}

// ValidateItem checks an item's schema.
func ValidateItem(item map[string]any) error { return feed.ValidateItem(item) }

// CanonicalItemBytes returns the OLPC bytes the publisher signs.
func CanonicalItemBytes(item map[string]any) ([]byte, error) { return feed.CanonicalBytes(item) }

// ItemID returns the item id.
func ItemID(item map[string]any) string { return feed.IDOf(item) }

// TargetPath returns the item's TUF target path.
func TargetPath(channel, id string) string { return feed.TargetPath(channel, id) }

func metaFilesFor(version int64, data []byte) *metadata.MetaFiles {
	sum := sha256.Sum256(data)
	return &metadata.MetaFiles{Version: version, Length: int64(len(data)), Hashes: metadata.Hashes{"sha256": sum[:]}}
}

func sha256Sum(data []byte) [32]byte { return sha256.Sum256(data) }
