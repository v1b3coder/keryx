// Package publisher implements the publisher-side operations (design/tooling.md
// §3–§6). Every method loads the repo, verifies it, applies the change,
// re-signs, verifies the result, then writes — the CLI and the future web app
// both call these methods.
package publisher

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/v1b3coder/keryx/sdk/ceremony"
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
	// AllowLocalHTTP permits plain-HTTP loopback/RFC 1918 linked media (the
	// local demo; never set in production).
	AllowLocalHTTP bool
	// GenerateKeys lets a ceremony command mint keys it does not hold. When
	// false, a missing key is a typed error instead of a silent generation.
	GenerateKeys bool
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
	master, err := p.keyForInit(ctx, keys.RoleMaster, "master")
	if err != nil {
		return Result{}, err
	}
	ops, err := p.keyForInit(ctx, keys.RoleOps, "ops")
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
	rootBytes, err := st.Root.ToBytes(true)
	if err != nil {
		return Result{}, err
	}
	st.Roots[st.Root.Signed.Version] = rootBytes
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

// key returns the unique store key matching role (and name when non-empty).
func (p *Publisher) key(ctx context.Context, role keys.Role, name string) (*keys.Key, error) {
	return p.Keys.Find(ctx, role, name)
}

// keyByID returns the key with the given keyid.
func (p *Publisher) keyByID(ctx context.Context, keyid string) (*keys.Key, error) {
	return p.Keys.Get(ctx, keyid)
}

// masterKey resolves the key currently authorized for the root role (which also
// signs targets). After a root rotation the store holds several master keys, so the
// authorized keyid from the current root decides — never a name/ordering guess.
func (p *Publisher) masterKey(ctx context.Context, st *tufrepo.State) (*keys.Key, error) {
	role := st.Root.Signed.Roles[metadata.ROOT]
	if role == nil || len(role.KeyIDs) == 0 {
		return nil, &keys.ErrMissingKey{Role: "master", Hint: "root.json has no root role key"}
	}
	return p.keyByID(ctx, role.KeyIDs[0])
}

// opsKey resolves the key currently authorized for snapshot/timestamp.
func (p *Publisher) opsKey(ctx context.Context, st *tufrepo.State) (*keys.Key, error) {
	role := st.Root.Signed.Roles[metadata.SNAPSHOT]
	if role == nil || len(role.KeyIDs) == 0 {
		return nil, &keys.ErrMissingKey{Role: "ops", Hint: "root.json has no snapshot role key"}
	}
	return p.keyByID(ctx, role.KeyIDs[0])
}

// Generate creates a fresh key for role and stores it. Ceremony commands that
// mint keys call it explicitly; signing paths never generate implicitly.
func (p *Publisher) Generate(ctx context.Context, role keys.Role, name string) (*keys.Key, error) {
	k, err := keys.Generate(role, name)
	if err != nil {
		return nil, err
	}
	if err := p.Keys.Add(ctx, k); err != nil {
		return nil, err
	}
	return k, nil
}

// keyForInit returns the key for role/name, generating and storing it when the
// store holds none. Only the bootstrap (init) uses it: a second init on the same
// workspace reuses the keys it created, so the output stays deterministic.
func (p *Publisher) keyForInit(ctx context.Context, role keys.Role, name string) (*keys.Key, error) {
	k, err := p.Keys.Find(ctx, role, name)
	if err == nil {
		return k, nil
	}
	if !keys.IsNotFound(err) {
		return nil, err
	}
	return p.Generate(ctx, role, name)
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
	if err := feed.ValidateItemOptions(params.Item, p.AllowLocalHTTP); err != nil {
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

// mutation is a master-signed targets.json change plus the channel-level
// work the applier finishes (design/tooling.md §3.5). The single-step path
// applies it locally; the two-step path stages it for a CI/ops machine.
type mutation struct {
	apply func(st *tufrepo.State) error
	steps []ceremony.Step
	keys  []*keys.Key
	msg   string
	chans []string
}

// runMutation applies a mutation to the live repo and finishes it locally
// (the single-step default, design/tooling.md §3.5).
func (p *Publisher) runMutation(ctx context.Context, m mutation) (Result, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := m.apply(st); err != nil {
		return Result{}, err
	}
	if err := p.applySteps(ctx, st, m.steps, m.keys); err != nil {
		return Result{}, err
	}
	now := p.now()
	signFresh(st.Targets, p.exp().Targets, now)
	master, err := p.masterKey(ctx, st)
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
	return Result{Company: st.CompanyName(), Channels: m.chans, Version: st.Targets.Signed.Version, Message: m.msg}, nil
}

// stageMutation master-signs targets.json and writes the handoff bundle without
// touching the live repo (the strict two-step path, design/tooling.md §3.5).
func (p *Publisher) stageMutation(ctx context.Context, m mutation, out, passphrase string) (Result, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := m.apply(st); err != nil {
		return Result{}, err
	}
	now := p.now()
	signFresh(st.Targets, p.exp().Targets, now)
	master, err := p.masterKey(ctx, st)
	if err != nil {
		return Result{}, err
	}
	if err := tufrepo.SignAndTag(st.Targets, master); err != nil {
		return Result{}, err
	}
	targetsBytes, err := st.Targets.ToBytes(true)
	if err != nil {
		return Result{}, err
	}
	bundle := &ceremony.Bundle{Version: 1, Targets: targetsBytes, Steps: m.steps, Keys: keyFiles(m.keys)}
	if err := bundle.EncodeKeys(passphrase); err != nil {
		return Result{}, err
	}
	if err := ceremony.Write(out, bundle); err != nil {
		return Result{}, err
	}
	return Result{Channels: m.chans, Version: st.Targets.Signed.Version, Message: "staged " + m.msg}, nil
}

// applySteps finishes the channel-level work of a ceremony bundle: it creates or
// drops channel/authors role metadata, re-signs items and installs nothing else.
func (p *Publisher) applySteps(ctx context.Context, st *tufrepo.State, steps []ceremony.Step, ks []*keys.Key) error {
	byID := map[string]*keys.Key{}
	for _, k := range ks {
		byID[k.KeyID()] = k
	}
	keyFor := func(kid string) (*keys.Key, error) {
		if k := byID[kid]; k != nil {
			return k, nil
		}
		// fall back to the local store (single-step path holds the old keys)
		if k, err := p.keyByID(ctx, kid); err == nil {
			return k, nil
		}
		return nil, &keys.ErrMissingKey{Role: "ceremony", KeyID: kid, Hint: "key " + kid + " missing from the ceremony bundle"}
	}
	for _, step := range steps {
		switch step.Kind {
		case "channel-create":
			k, err := keyFor(step.KeyID)
			if err != nil {
				return err
			}
			chMeta := metadata.Targets(p.now().Add(p.exp().Channel))
			chMeta.Signed.Delegations = emptyDelegations()
			if err := tufrepo.SignAndTag(chMeta, k); err != nil {
				return err
			}
			st.Channels[step.Channel] = chMeta
		case "authors-create":
			authMeta := metadata.Targets(p.now().Add(p.exp().Authors))
			authMeta.Signed.Delegations = emptyDelegations()
			for _, kid := range step.KeyIDs {
				k, err := keyFor(kid)
				if err != nil {
					return err
				}
				if err := tufrepo.SignAndTag(authMeta, k); err != nil {
					return err
				}
			}
			st.Authors[step.Channel] = authMeta
		case "channel-remove":
			delete(st.Channels, step.Channel)
			delete(st.Authors, step.Channel)
		case "authors-remove":
			delete(st.Authors, step.Channel)
		case "channel-mode":
			if step.Mode == "authored" {
				authMeta := metadata.Targets(p.now().Add(p.exp().Authors))
				authMeta.Signed.Delegations = emptyDelegations()
				for _, kid := range step.KeyIDs {
					k, err := keyFor(kid)
					if err != nil {
						return err
					}
					if err := tufrepo.SignAndTag(authMeta, k); err != nil {
						return err
					}
				}
				st.Authors[step.Channel] = authMeta
			} else {
				delete(st.Authors, step.Channel)
			}
			signers, err := keyList(step.KeyIDs, keyFor)
			if err != nil {
				return err
			}
			if err := p.resignItems(st, step.Channel, signers); err != nil {
				return err
			}
			// the item hashes changed: re-sign the channel role metadata too
			chMeta := st.Channels[step.Channel]
			signFresh(chMeta, p.exp().Channel, p.now())
			for _, kid := range step.KeyIDs {
				k, err := keyFor(kid)
				if err != nil {
					return err
				}
				if err := tufrepo.SignAndTag(chMeta, k); err != nil {
					return err
				}
			}
		case "channel-keys":
			signers, err := keyList(step.KeyIDs, keyFor)
			if err != nil {
				return err
			}
			if step.Resign {
				if err := p.resignItems(st, step.Channel, signers); err != nil {
					return err
				}
			}
			// re-sign the channel role metadata over the (possibly re-signed) items
			chMeta := st.Channels[step.Channel]
			signFresh(chMeta, p.exp().Channel, p.now())
			for _, kid := range step.KeyIDs {
				k, err := keyFor(kid)
				if err != nil {
					return err
				}
				if err := tufrepo.SignAndTag(chMeta, k); err != nil {
					return err
				}
			}
		case "resign-items":
			signers, err := keyList(step.KeyIDs, keyFor)
			if err != nil {
				return err
			}
			if err := p.resignItems(st, step.Channel, signers); err != nil {
				return err
			}
			chMeta := st.Channels[step.Channel]
			signFresh(chMeta, p.exp().Channel, p.now())
			for _, kid := range step.KeyIDs {
				k, err := keyFor(kid)
				if err != nil {
					return err
				}
				if err := tufrepo.SignAndTag(chMeta, k); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("ceremony: unknown step %q", step.Kind)
		}
	}
	return nil
}

func keyList(keyids []string, keyFor func(string) (*keys.Key, error)) ([]*keys.Key, error) {
	out := make([]*keys.Key, 0, len(keyids))
	for _, kid := range keyids {
		k, err := keyFor(kid)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

// Apply verifies a ceremony bundle (master signature, version monotonicity,
// delegation invariants) and finishes it on the machine that holds the channel
// and ops keys (design/tooling.md §3.5).
func (p *Publisher) Apply(ctx context.Context, dir, passphrase string) (Result, error) {
	b, err := ceremony.Read(dir)
	if err != nil {
		return Result{}, err
	}
	if err := b.DecodeKeys(passphrase); err != nil {
		return Result{}, err
	}
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	bundleTargets, err := metadata.Targets().FromBytes(b.Targets)
	if err != nil {
		return Result{}, fmt.Errorf("bundle targets: %w", err)
	}
	if err := st.Root.VerifyDelegate(metadata.TARGETS, bundleTargets); err != nil {
		return Result{}, fmt.Errorf("bundle targets signature: %w", err)
	}
	if bundleTargets.Signed.Version <= st.Targets.Signed.Version {
		return Result{}, fmt.Errorf("bundle targets v%d is not newer than live v%d", bundleTargets.Signed.Version, st.Targets.Signed.Version)
	}
	st.Targets = bundleTargets
	ks := make([]*keys.Key, 0, len(b.Keys))
	for _, kf := range b.Keys {
		seed, err := hex.DecodeString(kf.SeedHex)
		if err != nil {
			return Result{}, fmt.Errorf("bundle key %s: %w", kf.Name, err)
		}
		k, err := keys.FromSeed(kf.Role, kf.Name, seed)
		if err != nil {
			return Result{}, err
		}
		if err := p.Keys.Add(ctx, k); err != nil {
			return Result{}, err
		}
		ks = append(ks, k)
	}
	if err := p.applySteps(ctx, st, b.Steps, ks); err != nil {
		return Result{}, err
	}
	now := p.now()
	if err := p.signFreshness(st, now); err != nil {
		return Result{}, err
	}
	if err := p.writeVerified(ctx, st); err != nil {
		return Result{}, err
	}
	return Result{Version: st.Targets.Signed.Version, Message: "ceremony applied"}, nil
}

func keyFiles(ks []*keys.Key) []keys.KeyFile {
	out := make([]keys.KeyFile, 0, len(ks))
	for _, k := range ks {
		out = append(out, keys.KeyFile{
			Name: k.Name, Role: k.Role,
			SeedHex: hex.EncodeToString(k.Seed()),
			Public:  hex.EncodeToString(k.Public()),
			KeyID:   k.KeyID(),
		})
	}
	return out
}

// ChannelAdd creates the channel delegation, its role metadata (the index) and
// its display metadata; authored by default (spec/feeds.md §2.1).
func (p *Publisher) ChannelAdd(ctx context.Context, spec ChannelSpec) (Result, error) {
	m, err := p.channelAddMutation(ctx, spec)
	if err != nil {
		return Result{}, err
	}
	return p.runMutation(ctx, m)
}

// StageChannelAdd master-signs the channel addition and writes the handoff
// bundle (design/tooling.md §3.5, strict two-step ceremony).
func (p *Publisher) StageChannelAdd(ctx context.Context, spec ChannelSpec, out, passphrase string) (Result, error) {
	m, err := p.channelAddMutation(ctx, spec)
	if err != nil {
		return Result{}, err
	}
	return p.stageMutation(ctx, m, out, passphrase)
}

func (p *Publisher) channelAddMutation(ctx context.Context, spec ChannelSpec) (mutation, error) {
	if !validChannel(spec.Name) {
		return mutation{}, fmt.Errorf("invalid channel name %q", spec.Name)
	}
	st, err := p.loadVerified(ctx)
	if err != nil {
		return mutation{}, err
	}
	if st.Channels[spec.Name] != nil {
		return mutation{}, fmt.Errorf("channel %q already exists", spec.Name)
	}
	chKey, err := p.resolveChannelKey(ctx, spec)
	if err != nil {
		return mutation{}, err
	}
	var authorKeys []*keys.Key
	if !spec.Simple {
		authorKeys, err = p.resolveAuthorKeys(ctx, spec)
		if err != nil {
			return mutation{}, err
		}
	}
	threshold := spec.Threshold
	if threshold < 1 {
		threshold = 1
	}
	display := map[string]any{}
	if spec.DisplayName != "" {
		display["display_name"] = spec.DisplayName
	}
	if spec.Description != "" {
		display["description"] = spec.Description
	}
	steps := []ceremony.Step{{Kind: "channel-create", Channel: spec.Name, KeyID: chKey.KeyID()}}
	allKeys := []*keys.Key{chKey}
	if !spec.Simple {
		keyids := make([]string, 0, len(authorKeys))
		for _, k := range authorKeys {
			keyids = append(keyids, k.KeyID())
			allKeys = append(allKeys, k)
		}
		steps = append(steps, ceremony.Step{Kind: "authors-create", Channel: spec.Name, KeyIDs: keyids})
	}
	return mutation{
		apply: func(st *tufrepo.State) error {
			roleName := "channels." + spec.Name
			if err := addDelegation(st.Targets, roleName, []*metadata.Key{chKey.TUF()}, 1, []string{"channels/" + spec.Name + "/*"}, true); err != nil {
				return err
			}
			if !spec.Simple {
				if err := addDelegation(st.Targets, roleName+".authors", tufKeys(authorKeys), threshold, []string{"channels/" + spec.Name + "/*"}, false); err != nil {
					return err
				}
			}
			setChannelDisplay(st, spec.Name, display)
			return nil
		},
		steps: steps,
		keys:  allKeys,
		msg:   "channel added",
		chans: []string{spec.Name},
	}, nil
}

// ChannelRemove drops the channel delegation and its metadata.
func (p *Publisher) ChannelRemove(ctx context.Context, name string) (Result, error) {
	m, err := p.channelRemoveMutation(ctx, name)
	if err != nil {
		return Result{}, err
	}
	return p.runMutation(ctx, m)
}

// StageChannelRemove master-signs the channel removal and writes the handoff bundle.
func (p *Publisher) StageChannelRemove(ctx context.Context, name, out, passphrase string) (Result, error) {
	m, err := p.channelRemoveMutation(ctx, name)
	if err != nil {
		return Result{}, err
	}
	return p.stageMutation(ctx, m, out, passphrase)
}

func (p *Publisher) channelRemoveMutation(ctx context.Context, name string) (mutation, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return mutation{}, err
	}
	if st.Channels[name] == nil {
		return mutation{}, fmt.Errorf("unknown channel %q", name)
	}
	return mutation{
		apply: func(st *tufrepo.State) error {
			removeDelegation(st.Targets, "channels."+name)
			removeDelegation(st.Targets, "channels."+name+".authors")
			deleteChannelDisplay(st, name)
			// the channel's items are no longer published
			for path := range st.Items {
				if strings.HasPrefix(path, "channels/"+name+"/") {
					delete(st.Items, path)
				}
			}
			return nil
		},
		steps: []ceremony.Step{{Kind: "channel-remove", Channel: name}},
		msg:   "channel removed",
		chans: []string{name},
	}, nil
}

// ChannelSet updates master-signed channel display metadata.
func (p *Publisher) ChannelSet(ctx context.Context, name, displayName, description string) (Result, error) {
	m, err := p.channelSetMutation(ctx, name, displayName, description)
	if err != nil {
		return Result{}, err
	}
	return p.runMutation(ctx, m)
}

// StageChannelSet stages the master-signed display metadata change.
func (p *Publisher) StageChannelSet(ctx context.Context, name, displayName, description, out, passphrase string) (Result, error) {
	m, err := p.channelSetMutation(ctx, name, displayName, description)
	if err != nil {
		return Result{}, err
	}
	return p.stageMutation(ctx, m, out, passphrase)
}

func (p *Publisher) channelSetMutation(ctx context.Context, name, displayName, description string) (mutation, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return mutation{}, err
	}
	if st.Channels[name] == nil {
		return mutation{}, fmt.Errorf("unknown channel %q", name)
	}
	display := map[string]any{}
	if displayName != "" {
		display["display_name"] = displayName
	}
	if description != "" {
		display["description"] = description
	}
	return mutation{
		apply: func(st *tufrepo.State) error { setChannelDisplay(st, name, display); return nil },
		msg:   "channel updated",
		chans: []string{name},
	}, nil
}

// ChannelMode switches a channel between authored and simple mode. The mode
// change MUST re-sign the channel's published items with the new mode's keys
// (spec/feeds.md §2.1).
func (p *Publisher) ChannelMode(ctx context.Context, name, mode string) (Result, error) {
	m, err := p.channelModeMutation(ctx, name, mode)
	if err != nil {
		return Result{}, err
	}
	return p.runMutation(ctx, m)
}

// StageChannelMode stages the master-signed mode change.
func (p *Publisher) StageChannelMode(ctx context.Context, name, mode, out, passphrase string) (Result, error) {
	m, err := p.channelModeMutation(ctx, name, mode)
	if err != nil {
		return Result{}, err
	}
	return p.stageMutation(ctx, m, out, passphrase)
}

func (p *Publisher) channelModeMutation(ctx context.Context, name, mode string) (mutation, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return mutation{}, err
	}
	if st.Channels[name] == nil {
		return mutation{}, fmt.Errorf("unknown channel %q", name)
	}
	chKey, err := p.channelKey(ctx, st, name)
	if err != nil {
		return mutation{}, err
	}
	switch mode {
	case "authored":
		if st.HasAuthors(name) {
			return mutation{}, fmt.Errorf("channel %q is already authored", name)
		}
		author, err := p.authorKey(ctx, name)
		if err != nil {
			return mutation{}, err
		}
		return mutation{
			apply: func(st *tufrepo.State) error {
				roleName := "channels." + name + ".authors"
				return addDelegation(st.Targets, roleName, []*metadata.Key{author.TUF()}, 1, []string{"channels/" + name + "/*"}, false)
			},
			steps: []ceremony.Step{{
				Kind: "channel-mode", Channel: name, Mode: "authored",
				KeyIDs: []string{author.KeyID(), chKey.KeyID()},
			}},
			keys:  []*keys.Key{author, chKey},
			msg:   "mode authored",
			chans: []string{name},
		}, nil
	case "simple":
		if !st.HasAuthors(name) {
			return mutation{}, fmt.Errorf("channel %q is already simple", name)
		}
		return mutation{
			apply: func(st *tufrepo.State) error {
				removeDelegation(st.Targets, "channels."+name+".authors")
				return nil
			},
			steps: []ceremony.Step{{
				Kind: "channel-mode", Channel: name, Mode: "simple",
				KeyIDs: []string{chKey.KeyID()},
			}},
			keys:  []*keys.Key{chKey},
			msg:   "mode simple",
			chans: []string{name},
		}, nil
	default:
		return mutation{}, fmt.Errorf("unknown mode %q (want authored|simple)", mode)
	}
}

// AuthorAdd adds an author keyid to an authored channel's delegation and
// re-signs the authors role metadata (spec/repository.md §5).
func (p *Publisher) AuthorAdd(ctx context.Context, channel, keyid string) (Result, error) {
	m, err := p.authorChangeMutation(ctx, channel, keyid, false)
	if err != nil {
		return Result{}, err
	}
	return p.runMutation(ctx, m)
}

// AuthorRevoke drops an author keyid; it refuses to remove the last author
// (use `channel mode <channel> simple` instead, spec/feeds.md §2.1).
func (p *Publisher) AuthorRevoke(ctx context.Context, channel, keyid string) (Result, error) {
	m, err := p.authorChangeMutation(ctx, channel, keyid, true)
	if err != nil {
		return Result{}, err
	}
	return p.runMutation(ctx, m)
}

// StageAuthorAdd stages the master-signed author addition.
func (p *Publisher) StageAuthorAdd(ctx context.Context, channel, keyid, out, passphrase string) (Result, error) {
	m, err := p.authorChangeMutation(ctx, channel, keyid, false)
	if err != nil {
		return Result{}, err
	}
	return p.stageMutation(ctx, m, out, passphrase)
}

// StageAuthorRevoke stages the master-signed author revocation.
func (p *Publisher) StageAuthorRevoke(ctx context.Context, channel, keyid, out, passphrase string) (Result, error) {
	m, err := p.authorChangeMutation(ctx, channel, keyid, true)
	if err != nil {
		return Result{}, err
	}
	return p.stageMutation(ctx, m, out, passphrase)
}

func (p *Publisher) authorChangeMutation(ctx context.Context, channel, keyid string, revoke bool) (mutation, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return mutation{}, err
	}
	roleName := "channels." + channel + ".authors"
	role := st.Delegation(roleName)
	if role == nil {
		return mutation{}, fmt.Errorf("channel %q is not authored (use channel mode %s authored)", channel, channel)
	}
	key, err := p.keyByID(ctx, keyid)
	if err != nil {
		return mutation{}, err
	}
	if revoke {
		if !contains(role.KeyIDs, keyid) {
			return mutation{}, fmt.Errorf("author %s is not listed on channel %q", keyid, channel)
		}
		if len(role.KeyIDs) <= 1 {
			return mutation{}, fmt.Errorf("cannot remove the last author; use channel mode %s simple", channel)
		}
	} else {
		if contains(role.KeyIDs, keyid) {
			return mutation{}, fmt.Errorf("author %s is already listed on channel %q", keyid, channel)
		}
		if chRole := st.Delegation("channels." + channel); chRole != nil && contains(chRole.KeyIDs, keyid) {
			return mutation{}, fmt.Errorf("key %s is the channel role key; key separation is required", keyid)
		}
	}
	action := "added"
	if revoke {
		action = "revoked"
	}
	steps := []ceremony.Step{{
		Kind: "authors-create", Channel: channel, KeyIDs: append([]string(nil), role.KeyIDs...),
	}}
	mutationKeys := []*keys.Key{key}
	if revoke {
		// spec/repository.md §5: items signed by the revoked author MUST be
		// re-signed with the remaining author key(s) during the overlap,
		// otherwise they are dropped on the next client fetch.
		remaining := removeString(role.KeyIDs, keyid)
		if len(remaining) < role.Threshold {
			return mutation{}, fmt.Errorf("cannot meet the %d-of-%d authors threshold after revoking %s; add a replacement author first", role.Threshold, len(role.KeyIDs), keyid)
		}
		chKey, err := p.channelKey(ctx, st, channel)
		if err != nil {
			return mutation{}, err
		}
		signers := append(append([]string(nil), remaining...), chKey.KeyID())
		steps = []ceremony.Step{
			{Kind: "authors-create", Channel: channel, KeyIDs: remaining},
			{Kind: "resign-items", Channel: channel, KeyIDs: signers},
		}
		for _, kid := range remaining {
			k, err := p.keyByID(ctx, kid)
			if err != nil {
				return mutation{}, err
			}
			mutationKeys = append(mutationKeys, k)
		}
	}
	return mutation{
		apply: func(st *tufrepo.State) error {
			role := st.Delegation(roleName)
			if revoke {
				role.KeyIDs = removeString(role.KeyIDs, keyid)
			} else {
				role.KeyIDs = append(role.KeyIDs, keyid)
				st.Targets.Signed.Delegations.Keys[keyid] = key.TUF()
			}
			return nil
		},
		steps: steps,
		keys:  mutationKeys,
		msg:   "author " + action,
		chans: []string{channel},
	}, nil
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
	m, err := p.patternAddMutation(ctx, spec)
	if err != nil {
		return Result{}, err
	}
	return p.runMutation(ctx, m)
}

// StagePatternAdd stages the master-signed pattern addition.
func (p *Publisher) StagePatternAdd(ctx context.Context, spec PatternSpec, out, passphrase string) (Result, error) {
	m, err := p.patternAddMutation(ctx, spec)
	if err != nil {
		return Result{}, err
	}
	return p.stageMutation(ctx, m, out, passphrase)
}

func (p *Publisher) patternAddMutation(ctx context.Context, spec PatternSpec) (mutation, error) {
	key, err := p.keyByID(ctx, spec.KeyID)
	if err != nil {
		return mutation{}, err
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
	return mutation{
		apply: func(st *tufrepo.State) error {
			custom := st.Custom()
			patterns, _ := custom["private_feed_patterns"].([]any)
			custom["private_feed_patterns"] = append(patterns, entry)
			return nil
		},
		msg:   "pattern added",
		chans: []string{spec.Channel},
	}, nil
}

// PatternRemove drops the pattern entry for a channel.
func (p *Publisher) PatternRemove(ctx context.Context, channel string) (Result, error) {
	m, err := p.patternRemoveMutation(ctx, channel)
	if err != nil {
		return Result{}, err
	}
	return p.runMutation(ctx, m)
}

// StagePatternRemove stages the master-signed pattern removal.
func (p *Publisher) StagePatternRemove(ctx context.Context, channel, out, passphrase string) (Result, error) {
	m, err := p.patternRemoveMutation(ctx, channel)
	if err != nil {
		return Result{}, err
	}
	return p.stageMutation(ctx, m, out, passphrase)
}

func (p *Publisher) patternRemoveMutation(ctx context.Context, channel string) (mutation, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return mutation{}, err
	}
	custom := st.Custom()
	patterns, _ := custom["private_feed_patterns"].([]any)
	found := false
	for _, e := range patterns {
		if m, ok := e.(map[string]any); ok && m["channel"] == channel {
			found = true
			break
		}
	}
	if !found {
		return mutation{}, fmt.Errorf("no private-feed pattern for channel %q", channel)
	}
	return mutation{
		apply: func(st *tufrepo.State) error {
			custom := st.Custom()
			patterns, _ := custom["private_feed_patterns"].([]any)
			var kept []any
			for _, e := range patterns {
				if m, ok := e.(map[string]any); ok && m["channel"] == channel {
					continue
				}
				kept = append(kept, e)
			}
			custom["private_feed_patterns"] = kept
			return nil
		},
		msg:   "pattern removed",
		chans: []string{channel},
	}, nil
}

// CompanySet is the identity ceremony: master-signed company name and/or logo.
func (p *Publisher) CompanySet(ctx context.Context, name, logo, logoSHA256 string) (Result, error) {
	m, err := p.companySetMutation(ctx, name, logo, logoSHA256)
	if err != nil {
		return Result{}, err
	}
	return p.runMutation(ctx, m)
}

// StageCompanySet stages the master-signed identity change.
func (p *Publisher) StageCompanySet(ctx context.Context, name, logo, logoSHA256, out, passphrase string) (Result, error) {
	m, err := p.companySetMutation(ctx, name, logo, logoSHA256)
	if err != nil {
		return Result{}, err
	}
	return p.stageMutation(ctx, m, out, passphrase)
}

func (p *Publisher) companySetMutation(_ context.Context, name, logo, logoSHA256 string) (mutation, error) {
	if logo != "" && strings.HasPrefix(logo, "https://") && logoSHA256 == "" {
		return mutation{}, fmt.Errorf("linked logo requires logo_sha256")
	}
	return mutation{
		apply: func(st *tufrepo.State) error {
			custom := st.Custom()
			if name != "" {
				custom["company_name"] = name
			}
			if logo != "" {
				custom["logo"] = logo
				if strings.HasPrefix(logo, "https://") {
					custom["logo_sha256"] = logoSHA256
				} else {
					delete(custom, "logo_sha256")
				}
			}
			return nil
		},
		msg: "company updated",
	}, nil
}

// RotateChannelKey adds a new channel key alongside the old (threshold-1
// overlap), re-signs the channel role metadata with old+new and, in simple
// mode, re-signs the channel's items with the new key (spec/repository.md §5).
func (p *Publisher) RotateChannelKey(ctx context.Context, channel string) (Result, error) {
	return p.RotateChannelKeyOptions(ctx, channel, false)
}

// RotateChannelKeyOptions also pre-announces the next key when announceNext is
// set (spec/repository.md §5, RECOMMENDED).
func (p *Publisher) RotateChannelKeyOptions(ctx context.Context, channel string, announceNext bool) (Result, error) {
	m, err := p.rotateChannelKeyMutation(ctx, channel, announceNext)
	if err != nil {
		return Result{}, err
	}
	return p.runMutation(ctx, m)
}

// StageChannelKeyRotate stages the master-signed overlap rotation.
func (p *Publisher) StageChannelKeyRotate(ctx context.Context, channel, out, passphrase string, announceNext bool) (Result, error) {
	m, err := p.rotateChannelKeyMutation(ctx, channel, announceNext)
	if err != nil {
		return Result{}, err
	}
	return p.stageMutation(ctx, m, out, passphrase)
}

// RevokeChannelKeyReissue revokes a channel key and reissues in the same
// update (spec/repository.md §5 compromise response): the new key replaces the
// old one and the channel role metadata is re-signed with it.
func (p *Publisher) RevokeChannelKeyReissue(ctx context.Context, channel, keyid string) (Result, error) {
	m, err := p.reissueChannelKeyMutation(ctx, channel, keyid)
	if err != nil {
		return Result{}, err
	}
	return p.runMutation(ctx, m)
}

func (p *Publisher) reissueChannelKeyMutation(ctx context.Context, channel, keyid string) (mutation, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return mutation{}, err
	}
	role := st.Delegation("channels." + channel)
	if role == nil {
		return mutation{}, fmt.Errorf("unknown channel %q", channel)
	}
	if !contains(role.KeyIDs, keyid) {
		return mutation{}, fmt.Errorf("key %s is not on channel %q", keyid, channel)
	}
	newKey, err := p.Generate(ctx, keys.RoleChannel, channel+"-"+shortID(p.now()))
	if err != nil {
		return mutation{}, err
	}
	remaining := append(removeString(role.KeyIDs, keyid), newKey.KeyID())
	return mutation{
		apply: func(st *tufrepo.State) error {
			role := st.Delegation("channels." + channel)
			ids := removeString(role.KeyIDs, keyid)
			if !contains(ids, newKey.KeyID()) {
				ids = append(ids, newKey.KeyID())
			}
			role.KeyIDs = ids
			delete(st.Targets.Signed.Delegations.Keys, keyid)
			st.Targets.Signed.Delegations.Keys[newKey.KeyID()] = newKey.TUF()
			return nil
		},
		steps: []ceremony.Step{{
			Kind: "channel-keys", Channel: channel, KeyIDs: remaining, Resign: !st.HasAuthors(channel),
		}},
		keys:  []*keys.Key{newKey},
		msg:   "channel key revoked and reissued",
		chans: []string{channel},
	}, nil
}

func (p *Publisher) rotateChannelKeyMutation(ctx context.Context, channel string, announceNext bool) (mutation, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return mutation{}, err
	}
	if st.Channels[channel] == nil {
		return mutation{}, fmt.Errorf("unknown channel %q", channel)
	}
	newKey, err := p.Generate(ctx, keys.RoleChannel, channel+"-"+shortID(p.now()))
	if err != nil {
		return mutation{}, err
	}
	role := st.Delegation("channels." + channel)
	if role == nil {
		return mutation{}, fmt.Errorf("channel %q: delegation missing", channel)
	}
	all := append(append([]string(nil), role.KeyIDs...), newKey.KeyID())
	return mutation{
		apply: func(st *tufrepo.State) error {
			role := st.Delegation("channels." + channel)
			if !contains(role.KeyIDs, newKey.KeyID()) {
				role.KeyIDs = append(role.KeyIDs, newKey.KeyID())
			}
			st.Targets.Signed.Delegations.Keys[newKey.KeyID()] = newKey.TUF()
			if announceNext {
				// pre-announced rotation: a signed next_key record ahead
				// of time (spec/repository.md §5)
				chMeta := st.Channels[channel]
				if chMeta.Signed.UnrecognizedFields == nil {
					chMeta.Signed.UnrecognizedFields = map[string]any{}
				}
				chMeta.Signed.UnrecognizedFields["next_key"] = map[string]any{"keyid": newKey.KeyID()}
			}
			return nil
		},
		steps: []ceremony.Step{{
			Kind: "channel-keys", Channel: channel, KeyIDs: all, Resign: !st.HasAuthors(channel),
		}},
		keys:  []*keys.Key{newKey},
		msg:   "channel key rotated (overlap)",
		chans: []string{channel},
	}, nil
}

// RevokeChannelKey drops a keyid from the channel delegation and re-signs
// the channel role metadata with the remaining keys.
func (p *Publisher) RevokeChannelKey(ctx context.Context, channel, keyid string) (Result, error) {
	m, err := p.revokeChannelKeyMutation(ctx, channel, keyid)
	if err != nil {
		return Result{}, err
	}
	return p.runMutation(ctx, m)
}

// StageChannelKeyRevoke stages the master-signed revocation.
func (p *Publisher) StageChannelKeyRevoke(ctx context.Context, channel, keyid, out, passphrase string) (Result, error) {
	m, err := p.revokeChannelKeyMutation(ctx, channel, keyid)
	if err != nil {
		return Result{}, err
	}
	return p.stageMutation(ctx, m, out, passphrase)
}

func (p *Publisher) revokeChannelKeyMutation(ctx context.Context, channel, keyid string) (mutation, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return mutation{}, err
	}
	role := st.Delegation("channels." + channel)
	if role == nil {
		return mutation{}, fmt.Errorf("unknown channel %q", channel)
	}
	if !contains(role.KeyIDs, keyid) {
		return mutation{}, fmt.Errorf("key %s is not on channel %q", keyid, channel)
	}
	if len(role.KeyIDs) <= 1 {
		return mutation{}, fmt.Errorf("cannot revoke the last channel key")
	}
	remaining := removeString(role.KeyIDs, keyid)
	return mutation{
		apply: func(st *tufrepo.State) error {
			role := st.Delegation("channels." + channel)
			role.KeyIDs = removeString(role.KeyIDs, keyid)
			delete(st.Targets.Signed.Delegations.Keys, keyid)
			return nil
		},
		steps: []ceremony.Step{{Kind: "channel-keys", Channel: channel, KeyIDs: remaining}},
		msg:   "channel key revoked",
		chans: []string{channel},
	}, nil
}

// RotateRoot generates a new master key, builds root v+1 signed by the
// previous master keys and self-signed by the new master, and writes it to the
// anchor only (spec/repository.md §1/§5).
func (p *Publisher) RotateRoot(ctx context.Context, announceNext bool) (Result, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	old, err := p.masterKey(ctx, st)
	if err != nil {
		return Result{}, err
	}
	// always mint a fresh key: reusing the current master would sign the new
	// root twice with the same key
	newMaster, err := p.Generate(ctx, keys.RoleMaster, "master-"+shortID(p.now())+"-"+randSuffix())
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
		ops, err := p.opsKey(ctx, st)
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
	nextBytes, err := next.ToBytes(true)
	if err != nil {
		return Result{}, err
	}
	st.Roots[next.Signed.Version] = nextBytes
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
	return p.RefreshTimestampExpires(ctx, p.exp().Timestamp)
}

// RefreshTimestampExpires re-signs timestamp with a caller-chosen expiry
// (design/tooling.md §5: `pub refresh-timestamp [--expires 48h]`).
func (p *Publisher) RefreshTimestampExpires(ctx context.Context, expires time.Duration) (Result, error) {
	if expires <= 0 {
		expires = p.exp().Timestamp
	}
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	ops, err := p.opsKey(ctx, st)
	if err != nil {
		return Result{}, err
	}
	now := p.now()
	if err := st.RefreshTimestamp(ops, expires, now); err != nil {
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

// ValidateStrict also rejects expired metadata (design/tooling.md §5).
func (p *Publisher) ValidateStrict(ctx context.Context) (Result, error) {
	st, err := p.loadVerified(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := st.Expired(p.now()); err != nil {
		return Result{}, err
	}
	return Result{
		Company:  st.CompanyName(),
		Channels: st.ChannelNames(),
		Version:  st.Targets.Signed.Version,
		Message:  "ok (strict)",
	}, nil
}

// signFreshness re-signs snapshot + timestamp with the ops key and bumps
// their versions.
func (p *Publisher) signFreshness(st *tufrepo.State, now time.Time) error {
	ops, err := p.opsKey(context.Background(), st)
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
	if p.GenerateKeys {
		return p.Generate(ctx, keys.RoleChannel, spec.Name)
	}
	return p.key(ctx, keys.RoleChannel, spec.Name)
}

// authorKey resolves the author key for a channel, generating one when the
// caller opted in (GenerateKeys); otherwise it must already be in the store.
func (p *Publisher) authorKey(ctx context.Context, channel string) (*keys.Key, error) {
	if p.GenerateKeys {
		return p.Generate(ctx, keys.RoleAuthor, channel+"-author")
	}
	return p.key(ctx, keys.RoleAuthor, channel+"-author")
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
	k, err := p.authorKey(ctx, spec.Name)
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

func randSuffix() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "x"
	}
	return hex.EncodeToString(b)
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
