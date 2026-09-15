// Package tufclient implements the relay's per-company TUF client
// (relay/SPECIFICATION.md §5.2, §5.5): TOFU bootstrap of the well-known
// root anchor for an unknown domain, standard TUF root rotation and metadata
// verification for known domains, and derivation of the authorization scope
// table from verified targets.json. It never fetches content.
package tufclient

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/trustedmetadata"

	"github.com/v1b3coder/keryx/relay/internal/netpolicy"
	"github.com/v1b3coder/keryx/relay/internal/scope"
)

// State is the persisted per-company trust state.
type State struct {
	CompanyID      string
	Root           []byte
	RootVersion    int64
	Targets        []byte
	TargetsVersion int64
	TargetsExpires time.Time
	RefreshedAt    time.Time
}

// Client runs the TUF client workflow against company repositories.
type Client struct {
	Policy           *netpolicy.Policy
	MaxRootRotations int64
	MaxMetadataBytes int64

	// TestWellKnown overrides the well-known anchor base for a company
	// (map[companyID]baseURL). It exists so a local HTTPS end-to-end run
	// can serve a demo repository without owning the domain; it MUST be
	// empty in production.
	TestWellKnown map[string]string
}

// New returns a client with conservative defaults.
func New(policy *netpolicy.Policy) *Client {
	return &Client{Policy: policy, MaxRootRotations: 32, MaxMetadataBytes: 8 << 20}
}

// Refresh bootstraps an unknown domain by TOFU or updates a known one through
// standard TUF verification, then derives the authorization table. prev may be
// nil for an unknown company. On error the returned state carries any verified
// root progress (so the caller can persist it) and is nil only when no trusted
// root exists at all.
func (c *Client) Refresh(ctx context.Context, companyID string, prev *State) (*State, error) {
	if c.Policy == nil {
		c.Policy = netpolicy.New()
	}
	if c.MaxRootRotations <= 0 {
		c.MaxRootRotations = 32
	}
	if c.MaxMetadataBytes <= 0 {
		c.MaxMetadataBytes = 8 << 20
	}

	state := &State{CompanyID: companyID}
	wellKnown := "https://" + companyID + "/.well-known/keryx/"
	if base, ok := c.TestWellKnown[companyID]; ok {
		wellKnown = base
	}
	if prev != nil {
		state.Root = prev.Root
		state.RootVersion = prev.RootVersion
		state.Targets = prev.Targets
		state.TargetsVersion = prev.TargetsVersion
		state.TargetsExpires = prev.TargetsExpires
		state.RefreshedAt = prev.RefreshedAt
	}
	if len(state.Root) == 0 {
		root, err := c.fetch(ctx, wellKnown+"root.json")
		if err != nil {
			return nil, fmt.Errorf("company %s: bootstrap root: %w", companyID, err)
		}
		state.Root = root
	}

	trusted, err := trustedmetadata.New(state.Root)
	if err != nil {
		return nil, fmt.Errorf("company %s: trusted root: %w", companyID, err)
	}
	state.RootVersion = trusted.Root.Signed.Version

	// Root rotation: standard TUF walks the sequential N.root.json chain from
	// the well-known anchor, persisting each verified root immediately so a
	// later metadata failure never loses root progress.
	lower := trusted.Root.Signed.Version + 1
	upper := lower + c.MaxRootRotations
	for v := lower; v < upper; v++ {
		data, err := c.fetch(ctx, fmt.Sprintf("%s%d.root.json", wellKnown, v))
		if errors.Is(err, netpolicy.ErrNotFound) {
			break
		}
		if err != nil {
			return state, fmt.Errorf("company %s: root v%d: %w", companyID, v, err)
		}
		if _, err := trusted.UpdateRoot(data); err != nil {
			return state, fmt.Errorf("company %s: root v%d: %w", companyID, v, err)
		}
		state.Root = data
		state.RootVersion = trusted.Root.Signed.Version
	}

	if trusted.Root.Signed.ConsistentSnapshot {
		return state, fmt.Errorf("company %s: consistent_snapshot is not supported", companyID)
	}
	repoBase, err := repoBase(trusted.Root)
	if err != nil {
		return state, fmt.Errorf("company %s: %w", companyID, err)
	}

	timestamp, err := c.fetch(ctx, repoBase+"timestamp.json")
	if err != nil {
		return state, fmt.Errorf("company %s: timestamp: %w", companyID, err)
	}
	if _, err := trusted.UpdateTimestamp(timestamp); err != nil {
		return state, fmt.Errorf("company %s: timestamp: %w", companyID, err)
	}

	snapshot, err := c.fetch(ctx, repoBase+"snapshot.json")
	if err != nil {
		return state, fmt.Errorf("company %s: snapshot: %w", companyID, err)
	}
	if _, err := trusted.UpdateSnapshot(snapshot, false); err != nil {
		return state, fmt.Errorf("company %s: snapshot: %w", companyID, err)
	}

	targetsBytes, err := c.fetch(ctx, repoBase+"targets.json")
	if err != nil {
		return state, fmt.Errorf("company %s: targets: %w", companyID, err)
	}
	targets, err := trusted.UpdateTargets(targetsBytes)
	if err != nil {
		return state, fmt.Errorf("company %s: targets: %w", companyID, err)
	}
	if _, err := scope.FromTargets(targets); err != nil {
		return state, fmt.Errorf("company %s: %w", companyID, err)
	}

	state.Targets = targetsBytes
	state.TargetsVersion = targets.Signed.Version
	state.TargetsExpires = targets.Signed.Expires
	state.RefreshedAt = time.Now().UTC()
	return state, nil
}

// Table derives the authorization table from a persisted targets blob.
func (s *State) Table() (*scope.Table, error) {
	if len(s.Targets) == 0 {
		return nil, fmt.Errorf("company %s: no verified targets", s.CompanyID)
	}
	var zero metadata.Metadata[metadata.TargetsType]
	targets, err := zero.FromBytes(s.Targets)
	if err != nil {
		return nil, fmt.Errorf("company %s: targets: %w", s.CompanyID, err)
	}
	return scope.FromTargets(targets)
}

// repoBase reads the master-signed custom.repo_base from a verified root.
func repoBase(root *metadata.Metadata[metadata.RootType]) (string, error) {
	custom, _ := root.Signed.UnrecognizedFields["custom"].(map[string]any)
	base, _ := custom["repo_base"].(string)
	if base == "" {
		return "", errors.New("root.json: custom.repo_base missing")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", errors.New("root.json: custom.repo_base must be an https URL")
	}
	if base[len(base)-1] != '/' {
		base += "/"
	}
	return base, nil
}

// fetch downloads one metadata file through the outbound policy, bounded to
// MaxMetadataBytes.
func (c *Client) fetch(ctx context.Context, raw string) ([]byte, error) {
	return c.Policy.Get(ctx, raw, c.MaxMetadataBytes)
}
