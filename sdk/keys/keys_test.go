package keys_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/secure-systems-lab/go-securesystemslib/cjson"
	"github.com/v1b3coder/keryx/sdk/keys"
)

func TestKeyIDIsStandard(t *testing.T) {
	k, err := keys.Generate(keys.RoleChannel, "news")
	if err != nil {
		t.Fatal(err)
	}
	// keyid = SHA-256 of the canonical key object {keytype, scheme, keyval}
	// (spec/core.md §1) — the human-readable name is NOT part of it
	canonical, err := cjson.EncodeCanonical(map[string]any{
		"keytype": "ed25519",
		"scheme":  "ed25519",
		"keyval":  map[string]any{"public": hex.EncodeToString(k.Public())},
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256Sum(canonical)
	if k.KeyID() != sum {
		t.Fatalf("keyid %s != canonical key object hash %s", k.KeyID(), sum)
	}
	// the TUF key object carries the label but the keyid is unchanged
	if k.TUF().UnrecognizedFields["name"] != "news" {
		t.Fatalf("key object name = %v", k.TUF().UnrecognizedFields["name"])
	}
}

func TestDirStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := keys.NewDirStore(t.TempDir(), "")
	k, err := keys.Generate(keys.RoleAuthor, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(ctx, k); err != nil {
		t.Fatal(err)
	}
	got, err := store.Find(ctx, keys.RoleAuthor, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.KeyID() != k.KeyID() {
		t.Fatalf("keyid %s != %s", got.KeyID(), k.KeyID())
	}
	byID, err := store.Get(ctx, k.KeyID())
	if err != nil {
		t.Fatal(err)
	}
	if byID.Name != "alice" || byID.Role != keys.RoleAuthor {
		t.Fatalf("by keyid = %s/%s", byID.Name, byID.Role)
	}
	// a missing key is a typed error, never a silent zero key
	if _, err := store.Find(ctx, keys.RoleMaster, "nope"); !keys.IsNotFound(err) {
		t.Fatalf("missing key error = %v", err)
	}
	if err := store.Remove(ctx, k.KeyID()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, k.KeyID()); !keys.IsNotFound(err) {
		t.Fatal("removed key is still present")
	}
}

func TestEncryptedStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	plain := keys.NewDirStore(dir, "")
	k, err := keys.Generate(keys.RoleOps, "ops")
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.Add(ctx, k); err != nil {
		t.Fatal(err)
	}
	// a store with the wrong passphrase cannot read the seed
	enc := keys.NewDirStore(dir, "correct horse battery staple")
	if _, err := enc.Find(ctx, keys.RoleOps, "ops"); err == nil {
		t.Fatal("wrong passphrase decrypted the key")
	}
	if err := enc.Add(ctx, k); err != nil {
		t.Fatal(err)
	}
	got, err := enc.Find(ctx, keys.RoleOps, "ops")
	if err != nil {
		t.Fatal(err)
	}
	if got.KeyID() != k.KeyID() {
		t.Fatalf("keyid %s != %s", got.KeyID(), k.KeyID())
	}
}

func TestExportImport(t *testing.T) {
	ctx := context.Background()
	source := keys.NewDirStore(filepath.Join(t.TempDir(), "src"), "")
	for _, spec := range []struct {
		role keys.Role
		name string
	}{
		{keys.RoleMaster, "master"},
		{keys.RoleChannel, "news"},
	} {
		k, err := keys.Generate(spec.role, spec.name)
		if err != nil {
			t.Fatal(err)
		}
		if err := source.Add(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	data, err := keys.Export(ctx, source, nil, "bundle-pass")
	if err != nil {
		t.Fatal(err)
	}
	target := keys.NewDirStore(filepath.Join(t.TempDir(), "dst"), "bundle-pass")
	infos, err := keys.Import(ctx, target, data, "bundle-pass")
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 {
		t.Fatalf("imported %d keys", len(infos))
	}
	if _, err := target.Find(ctx, keys.RoleChannel, "news"); err != nil {
		t.Fatalf("imported channel key: %v", err)
	}
	// a wrong passphrase cannot import the encrypted bundle
	bad := keys.NewDirStore(filepath.Join(t.TempDir(), "bad"), "wrong")
	if _, err := keys.Import(ctx, bad, data); err == nil {
		t.Fatal("wrong passphrase imported the bundle")
	}
}

func TestPublicBundleCannotBeImported(t *testing.T) {
	ctx := context.Background()
	store := keys.NewDirStore(t.TempDir(), "")
	k, err := keys.Generate(keys.RoleAuthor, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(ctx, k); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(keys.ExportBundle{Version: 1, Keys: []keys.KeyFile{{
		Name: "alice", Role: keys.RoleAuthor, Public: hex.EncodeToString(k.Public()), KeyID: k.KeyID(),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	target := keys.NewDirStore(t.TempDir(), "")
	if _, err := keys.Import(ctx, target, data); err == nil {
		t.Fatal("imported a public-only bundle")
	}
}

func sha256Sum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
