// Package scope builds the relay's uniform authorization scope table
// (relay/SPECIFICATION.md §3.1) from verified targets metadata.
//
// The table maps (company_id, scope_id) to authorized keys and threshold.
// Public entries come from channels.<channel> delegations; private entries come
// from individual custom.private_feed_patterns entries. Matching channel labels
// MUST NOT merge authority. Keys, thresholds and metadata versions are excluded
// from scope identity, so key rotation preserves scope_id and topics.
package scope

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/secure-systems-lab/go-securesystemslib/cjson"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

// salt is the static, public domain separator for scope_id derivation.
const salt = "keryx/relay/scope/v1|"

// Entry is one authorization scope: the keys allowed to sign wake-ups for the
// scope and their threshold.
type Entry struct {
	ScopeID   string
	Keys      map[string]ed25519.PublicKey
	Threshold int
}

// Table is the uniform (company_id, scope_id) -> Entry map.
type Table struct {
	entries map[string]*Entry
}

// Lookup returns the entry for scopeID, or nil when the scope is unknown.
func (t *Table) Lookup(scopeID string) (*Entry, bool) {
	if t == nil {
		return nil, false
	}
	e, ok := t.entries[scopeID]
	return e, ok
}

// Len returns the number of scopes in the table.
func (t *Table) Len() int {
	if t == nil {
		return 0
	}
	return len(t.entries)
}

// FromTargets builds the scope table from verified targets.json. Duplicate
// descriptors are rejected as ambiguous.
func FromTargets(targets *metadata.Metadata[metadata.TargetsType]) (*Table, error) {
	if targets == nil {
		return nil, fmt.Errorf("targets metadata missing")
	}
	t := &Table{entries: map[string]*Entry{}}

	dlg := targets.Signed.Delegations
	if dlg == nil {
		return nil, fmt.Errorf("targets.json: delegations missing")
	}
	for i := range dlg.Roles {
		role := &dlg.Roles[i]
		channel, ok := strings.CutPrefix(role.Name, "channels.")
		if !ok || strings.HasSuffix(channel, ".authors") {
			continue
		}
		if !validChannel(channel) {
			return nil, fmt.Errorf("delegation %q: invalid channel name", role.Name)
		}
		keys, err := delegationKeys(dlg, role)
		if err != nil {
			return nil, err
		}
		descriptor := map[string]any{"kind": "public", "channel": channel}
		if err := t.add(descriptor, keys, role.Threshold); err != nil {
			return nil, err
		}
	}

	patterns, _ := customMap(targets)["private_feed_patterns"].([]any)
	for i, p := range patterns {
		entry, ok := p.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("private_feed_patterns[%d]: not an object", i)
		}
		channel, _ := entry["channel"].(string)
		if !validChannel(channel) {
			return nil, fmt.Errorf("private_feed_patterns[%d]: invalid channel", i)
		}
		pattern, _ := entry["pattern"].(string)
		if pattern == "" {
			return nil, fmt.Errorf("private_feed_patterns[%d]: pattern missing", i)
		}
		keys, err := privateKeys(entry)
		if err != nil {
			return nil, fmt.Errorf("private_feed_patterns[%d]: %w", i, err)
		}
		threshold, err := intField(entry["threshold"])
		if err != nil {
			return nil, fmt.Errorf("private_feed_patterns[%d]: threshold: %w", i, err)
		}
		descriptor := map[string]any{"kind": "private", "channel": channel, "pattern": pattern}
		if err := t.add(descriptor, keys, threshold); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// PublicScopeID returns the scope_id for a public channel delegation
// descriptor. It is the same value FromTargets derives.
func PublicScopeID(channel string) (string, error) {
	if !validChannel(channel) {
		return "", fmt.Errorf("invalid channel name")
	}
	return descriptorID(map[string]any{"kind": "public", "channel": channel})
}

// PrivateScopeID returns the scope_id for a private pattern descriptor.
func PrivateScopeID(channel, pattern string) (string, error) {
	if !validChannel(channel) || pattern == "" {
		return "", fmt.Errorf("invalid private pattern descriptor")
	}
	return descriptorID(map[string]any{"kind": "private", "channel": channel, "pattern": pattern})
}

// descriptorID returns hex(sha256(salt + OLPC(descriptor))).
func descriptorID(descriptor map[string]any) (string, error) {
	canonical, err := cjson.EncodeCanonical(descriptor)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(salt), canonical...))
	return hex.EncodeToString(sum[:]), nil
}

// add derives the scope_id for descriptor and inserts the entry, rejecting
// duplicate descriptors as ambiguous.
func (t *Table) add(descriptor map[string]any, keys map[string]ed25519.PublicKey, threshold int) error {
	id, err := descriptorID(descriptor)
	if err != nil {
		return err
	}
	if _, dup := t.entries[id]; dup {
		return fmt.Errorf("duplicate scope descriptor for %v", descriptor)
	}
	t.entries[id] = &Entry{ScopeID: id, Keys: keys, Threshold: threshold}
	return nil
}

// delegationKeys resolves a channel delegation role's keys and threshold.
func delegationKeys(dlg *metadata.Delegations, role *metadata.DelegatedRole) (map[string]ed25519.PublicKey, error) {
	out := make(map[string]ed25519.PublicKey, len(role.KeyIDs))
	for _, keyid := range role.KeyIDs {
		key := dlg.Keys[keyid]
		if key == nil {
			return nil, fmt.Errorf("delegation %q: key object for %s missing", role.Name, keyid)
		}
		pub, err := ed25519Key(key)
		if err != nil {
			return nil, fmt.Errorf("delegation %q: key %s: %w", role.Name, keyid, err)
		}
		out[keyid] = pub
	}
	return out, nil
}

// privateKeys resolves a private_feed_patterns entry's keys. A keyid without its
// key object is a metadata error (§3.1).
func privateKeys(entry map[string]any) (map[string]ed25519.PublicKey, error) {
	rawKeys, _ := entry["keys"].(map[string]any)
	keyids, _ := entry["keyids"].([]any)
	out := make(map[string]ed25519.PublicKey, len(keyids))
	for _, kid := range keyids {
		keyid, _ := kid.(string)
		obj := rawKeys[keyid]
		if obj == nil {
			return nil, fmt.Errorf("key object for %s missing", keyid)
		}
		raw, err := json.Marshal(obj)
		if err != nil {
			return nil, err
		}
		var key metadata.Key
		if err := json.Unmarshal(raw, &key); err != nil {
			return nil, fmt.Errorf("key %s: %w", keyid, err)
		}
		pub, err := ed25519Key(&key)
		if err != nil {
			return nil, fmt.Errorf("key %s: %w", keyid, err)
		}
		out[keyid] = pub
	}
	return out, nil
}

func ed25519Key(key *metadata.Key) (ed25519.PublicKey, error) {
	pub, err := key.ToPublicKey()
	if err != nil {
		return nil, err
	}
	ed, ok := pub.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("key is not Ed25519")
	}
	return ed, nil
}

func customMap(targets *metadata.Metadata[metadata.TargetsType]) map[string]any {
	c, _ := targets.Signed.UnrecognizedFields["custom"].(map[string]any)
	if c == nil {
		c = map[string]any{}
	}
	return c
}

func intField(v any) (int, error) {
	switch n := v.(type) {
	case float64:
		if n != float64(int(n)) {
			return 0, fmt.Errorf("not an integer")
		}
		return int(n), nil
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, err
		}
		return int(i), nil
	case int:
		return n, nil
	case nil:
		return 1, nil
	}
	return 0, fmt.Errorf("not a number")
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
