// Package ceremony is the operator → CI handoff (design/tooling.md §3.5): the
// offline ceremony machine produces a master-signed targets.json plus a step
// manifest and the new keys; the CI/ops machine verifies and finishes it.
package ceremony

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/v1b3coder/keryx/sdk/keys"
	"golang.org/x/crypto/scrypt"
)

// Step is one pending channel-level action the applier must perform after the
// master-signed targets.json is merged.
type Step struct {
	Kind     string   `json:"kind"` // channel-create|channel-remove|channel-mode|authors-create|authors-remove|resign-items
	Channel  string   `json:"channel"`
	KeyID    string   `json:"keyid,omitempty"`   // channel key (create)
	KeyIDs   []string `json:"keyids,omitempty"` // author keys (create/resign)
	Mode     string   `json:"mode,omitempty"`    // authored|simple (mode)
	Resign   bool     `json:"resign,omitempty"`  // re-sign items (channel-keys)
	Terminal bool     `json:"terminal,omitempty"`
}

// Bundle is the serialized handoff artifact. Targets is the master-signed
// targets.json; Keys are the new keys the applier needs (encrypted at rest when
// a passphrase is configured).
type Bundle struct {
	Version int              `json:"version"`
	Targets []byte           `json:"targets"`
	Steps   []Step           `json:"steps"`
	Keys    []keys.KeyFile   `json:"keys"`
	Enc     bool             `json:"enc,omitempty"`
	Salt    string           `json:"salt,omitempty"`
	Nonce   string           `json:"nonce,omitempty"`
	Ciphertext string       `json:"ciphertext,omitempty"`
}

// Write serializes a bundle to dir (bundle.json).
func Write(dir string, b *Bundle) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "bundle.json"), data, 0o600)
}

// Read loads a bundle from dir.
func Read(dir string) (*Bundle, error) {
	data, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		return nil, err
	}
	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	if b.Version != 1 {
		return nil, fmt.Errorf("bundle: unknown version %d", b.Version)
	}
	return &b, nil
}

// EncodeKeys encrypts the bundle's key seeds with a passphrase (scrypt +
// AES-GCM) so the bundle can travel over an untrusted channel.
func (b *Bundle) EncodeKeys(passphrase string) error {
	if passphrase == "" || b.Enc {
		return nil
	}
	raw, err := json.Marshal(b.Keys)
	if err != nil {
		return err
	}
	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	key, err := scrypt.Key([]byte(passphrase), salt, 1<<15, 8, 1, 32)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	ct := gcm.Seal(nil, nonce, raw, nil)
	b.Enc = true
	b.Salt = base64.StdEncoding.EncodeToString(salt)
	b.Nonce = base64.StdEncoding.EncodeToString(nonce)
	b.Ciphertext = base64.StdEncoding.EncodeToString(ct)
	b.Keys = nil
	return nil
}

// DecodeKeys decrypts the bundle's key seeds with a passphrase.
func (b *Bundle) DecodeKeys(passphrase string) error {
	if !b.Enc {
		return nil
	}
	if passphrase == "" {
		return fmt.Errorf("bundle keys are encrypted; pass --bundle-passphrase")
	}
	salt, err := base64.StdEncoding.DecodeString(b.Salt)
	if err != nil {
		return err
	}
	nonce, err := base64.StdEncoding.DecodeString(b.Nonce)
	if err != nil {
		return err
	}
	ct, err := base64.StdEncoding.DecodeString(b.Ciphertext)
	if err != nil {
		return err
	}
	key, err := scrypt.Key([]byte(passphrase), salt, 1<<15, 8, 1, 32)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	raw, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return fmt.Errorf("bundle: wrong passphrase")
	}
	return json.Unmarshal(raw, &b.Keys)
}
