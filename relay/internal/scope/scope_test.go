package scope

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func newKey(t *testing.T) *metadata.Key {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := metadata.KeyFromPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func targetsWith(delegations *metadata.Delegations, custom map[string]any) *metadata.Metadata[metadata.TargetsType] {
	return &metadata.Metadata[metadata.TargetsType]{
		Signed: metadata.TargetsType{
			Type:               "targets",
			Delegations:        delegations,
			UnrecognizedFields: map[string]any{"custom": custom},
		},
	}
}

func publicRole(t *testing.T, channel string, threshold int) (*metadata.DelegatedRole, *metadata.Key) {
	t.Helper()
	k := newKey(t)
	role := &metadata.DelegatedRole{
		Name:      "channels." + channel,
		KeyIDs:    []string{mustID(t, k)},
		Threshold: threshold,
		Paths:     []string{"channels/" + channel + "/*"},
	}
	return role, k
}

func mustID(t *testing.T, k *metadata.Key) string {
	t.Helper()
	id, err := k.ID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPublicScopeStableAcrossKeys(t *testing.T) {
	role, key := publicRole(t, "news", 1)
	tbl, err := FromTargets(targetsWith(&metadata.Delegations{
		Keys:  map[string]*metadata.Key{mustID(t, key): key},
		Roles: []metadata.DelegatedRole{*role},
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := tbl.Lookup(onlyScope(t, tbl))
	if !ok {
		t.Fatal("scope missing")
	}
	if entry.Threshold != 1 || len(entry.Keys) != 1 {
		t.Fatalf("entry = %+v", entry)
	}

	// Rotating the key must not change scope_id (keys are excluded).
	role2, key2 := publicRole(t, "news", 1)
	tbl2, err := FromTargets(targetsWith(&metadata.Delegations{
		Keys:  map[string]*metadata.Key{mustID(t, key2): key2},
		Roles: []metadata.DelegatedRole{*role2},
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if onlyScope(t, tbl2) != onlyScope(t, tbl) {
		t.Fatal("scope_id changed on key rotation")
	}
}

func TestPublicAndPrivateLabelsIsolated(t *testing.T) {
	role, key := publicRole(t, "tracking", 1)
	privKey := newKey(t)
	privID := mustID(t, privKey)
	custom := map[string]any{
		"private_feed_patterns": []any{
			map[string]any{
				"channel":   "tracking",
				"pattern":   "https://company.example/channels/tracking/*/feed.json",
				"threshold": 1,
				"keyids":    []any{privID},
				"keys":      map[string]any{privID: keyToAny(t, privKey)},
			},
		},
	}
	tbl, err := FromTargets(targetsWith(&metadata.Delegations{
		Keys:  map[string]*metadata.Key{mustID(t, key): key},
		Roles: []metadata.DelegatedRole{*role},
	}, custom))
	if err != nil {
		t.Fatal(err)
	}
	if tbl.Len() != 2 {
		t.Fatalf("scopes = %d, want 2", tbl.Len())
	}
}

func TestDuplicateDescriptorRejected(t *testing.T) {
	role, key := publicRole(t, "news", 1)
	_, err := FromTargets(targetsWith(&metadata.Delegations{
		Keys:  map[string]*metadata.Key{mustID(t, key): key},
		Roles: []metadata.DelegatedRole{*role, *role},
	}, nil))
	if err == nil {
		t.Fatal("accepted duplicate descriptor")
	}
}

func TestPrivateKeyObjectRequired(t *testing.T) {
	custom := map[string]any{
		"private_feed_patterns": []any{
			map[string]any{
				"channel":   "tracking",
				"pattern":   "https://company.example/channels/tracking/*/feed.json",
				"threshold": 1,
				"keyids":    []any{"deadbeef"},
				"keys":      map[string]any{},
			},
		},
	}
	_, err := FromTargets(targetsWith(&metadata.Delegations{
		Keys:  map[string]*metadata.Key{},
		Roles: []metadata.DelegatedRole{},
	}, custom))
	if err == nil {
		t.Fatal("accepted a keyid without its key object")
	}
}

func TestMissingDelegationKeyRejected(t *testing.T) {
	role := &metadata.DelegatedRole{
		Name:      "channels.news",
		KeyIDs:    []string{"deadbeef"},
		Threshold: 1,
		Paths:     []string{"channels/news/*"},
	}
	_, err := FromTargets(targetsWith(&metadata.Delegations{
		Keys:  map[string]*metadata.Key{},
		Roles: []metadata.DelegatedRole{*role},
	}, nil))
	if err == nil {
		t.Fatal("accepted a delegation keyid without its key object")
	}
}

func keyToAny(t *testing.T, k *metadata.Key) any {
	t.Helper()
	return map[string]any{
		"keytype": k.Type,
		"scheme":  k.Scheme,
		"keyval":  map[string]any{"public": k.Value.PublicKey},
	}
}

func onlyScope(t *testing.T, tbl *Table) string {
	t.Helper()
	for id := range tbl.entries {
		return id
	}
	t.Fatal("empty table")
	return ""
}
