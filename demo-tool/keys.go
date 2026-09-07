package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

// keyPair is one local software key (Ed25519) used for the demo.
type keyPair struct {
	Name  string
	Priv  ed25519.PrivateKey
	Pub   ed25519.PublicKey
	Key   *metadata.Key // TUF key object (public part)
	KeyID string
}

type keyFile struct {
	Name string `json:"name"`
	Seed string `json:"seed_hex"` // 32-byte seed, hex
}

// keyNames enumerates all demo keys: master (root+targets), ops
// (snapshot+timestamp), one per public channel (delegated roles), the
// private-feed engine key (tracking) and the editor keys (PROTOCOL §9).
var keyNames = []string{
	"master", "ops",
	"security", "news", "insights",
	"tracking",
	"editor-security-a", "editor-security-b",
}

// loadOrCreateKeys loads demo keys from dir, generating new ones when missing.
func loadOrCreateKeys(dir string, names []string) (map[string]*keyPair, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	keys := map[string]*keyPair{}
	for _, name := range names {
		path := filepath.Join(dir, name+".json")
		var kf keyFile
		if data, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(data, &kf); err != nil {
				return nil, fmt.Errorf("key %s: %w", name, err)
			}
		} else if os.IsNotExist(err) {
			seed := make([]byte, ed25519.SeedSize)
			if _, err := rand.Read(seed); err != nil {
				return nil, err
			}
			kf = keyFile{Name: name, Seed: hex.EncodeToString(seed)}
			data, err := json.MarshalIndent(kf, "", "  ")
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				return nil, err
			}
			fmt.Printf("generated new demo key: %s\n", name)
		} else {
			return nil, err
		}
		seed, err := hex.DecodeString(kf.Seed)
		if err != nil {
			return nil, fmt.Errorf("key %s: %w", name, err)
		}
		priv := ed25519.NewKeyFromSeed(seed)
		tufKey, err := metadata.KeyFromPublicKey(priv.Public())
		if err != nil {
			return nil, err
		}
		// Human-readable label on the key object (a TUF "unrecognized"
		// field, ignored by clients). The keyid stays the normative SHA-256
		// hash of the key — but because it hashes the whole key object, the
		// label must be set BEFORE the keyid is computed.
		tufKey.UnrecognizedFields = map[string]any{"name": name}
		keyID, err := tufKey.ID()
		if err != nil {
			return nil, err
		}
		keys[name] = &keyPair{
			Name: name, Priv: priv, Pub: priv.Public().(ed25519.PublicKey),
			Key: tufKey, KeyID: keyID,
		}
	}
	return keys, nil
}
