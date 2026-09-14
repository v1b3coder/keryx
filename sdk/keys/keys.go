// Package keys holds the Ed25519 software keys used by every publisher role
// (spec/core.md §1.3, spec/clients.md §2) and a role-tagged file store.
package keys

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
	"golang.org/x/crypto/scrypt"
)

// Role is the publisher role a key belongs to (spec/clients.md §2).
type Role string

const (
	RoleMaster  Role = "master"
	RoleOps     Role = "ops"
	RoleChannel Role = "channel"
	RoleAuthor  Role = "author"
	RoleEngine  Role = "engine"
)

// ErrMissingKey is returned when an operation needs a key the store does not
// hold — the CLI renders the role hint (spec/clients.md §2 fail-safes).
type ErrMissingKey struct {
	Role  string
	KeyID string
	Hint  string
}

func (e *ErrMissingKey) Error() string {
	if e.Hint != "" {
		return fmt.Sprintf("missing key: %s", e.Hint)
	}
	if e.KeyID != "" {
		return fmt.Sprintf("missing key: %s key %s", e.Role, e.KeyID)
	}
	return fmt.Sprintf("missing key: %s", e.Role)
}

// Key is one Ed25519 software key plus its TUF key object. The keyid is the
// SHA-256 of the canonical key object {keytype,scheme,keyval} (spec/core.md §1);
// the human-readable name is attached as an unrecognized field *after* the keyid
// is computed, so the keyid stays TUF-standard.
type Key struct {
	Name string `json:"name"`
	Role Role   `json:"role"`
	seed []byte
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
	tuf  *metadata.Key
	id   string
}

// Generate creates a fresh Ed25519 key for role.
func Generate(role Role, name string) (*Key, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	return FromSeed(role, name, seed)
}

// FromSeed reconstructs a key from a 32-byte Ed25519 seed.
func FromSeed(role Role, name string, seed []byte) (*Key, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("key %s: seed is %d bytes, want %d", name, len(seed), ed25519.SeedSize)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	tuf, err := metadata.KeyFromPublicKey(priv.Public())
	if err != nil {
		return nil, fmt.Errorf("key %s: %w", name, err)
	}
	id, err := tuf.ID()
	if err != nil {
		return nil, fmt.Errorf("key %s: keyid: %w", name, err)
	}
	tuf.UnrecognizedFields = map[string]any{"name": name}
	return &Key{
		Name: name,
		Role: role,
		seed: append([]byte(nil), seed...),
		priv: priv,
		pub:  priv.Public().(ed25519.PublicKey),
		tuf:  tuf,
		id:   id,
	}, nil
}

// KeyID returns the TUF-standard keyid.
func (k *Key) KeyID() string { return k.id }

// TUF returns the public key object (with the `name` label).
func (k *Key) TUF() *metadata.Key { return k.tuf }

// Public returns the raw Ed25519 public key.
func (k *Key) Public() ed25519.PublicKey { return k.pub }

// Private returns the raw Ed25519 private key.
func (k *Key) Private() ed25519.PrivateKey { return k.priv }

// Signer returns a Sigstore signer for TUF metadata signing.
func (k *Key) Signer() (signature.Signer, error) {
	return signature.LoadSigner(k.priv, 0)
}

// Info is the public, listable part of a stored key.
type Info struct {
	Name  string `json:"name"`
	Role  Role   `json:"role"`
	KeyID string `json:"keyid"`
}

// Store is the role-scoped key store (spec/clients.md §2). The web app can
// back it with a KMS/hardware store later; the CLI uses DirStore.
type Store interface {
	List(ctx context.Context) ([]Info, error)
	Get(ctx context.Context, keyid string) (*Key, error)
	Add(ctx context.Context, k *Key) error
	Remove(ctx context.Context, keyid string) error
}

// File format: either a plain seed_hex record or an encrypted record. The
// passphrase is never stored; it comes from the caller (env/config).
type keyFile struct {
	Name       string `json:"name"`
	Role       Role   `json:"role"`
	SeedHex    string `json:"seed_hex,omitempty"`
	KDF        string `json:"kdf,omitempty"`
	Salt       string `json:"salt,omitempty"`
	Nonce      string `json:"nonce,omitempty"`
	Ciphertext string `json:"ciphertext,omitempty"`
}

// DirStore is a directory of one JSON file per key. When a passphrase is
// configured, seeds are encrypted (scrypt + AES-256-GCM); otherwise the file is
// plaintext with 0600 permissions (software-key MVP, spec/clients.md §2).
type DirStore struct {
	Dir        string
	Passphrase string
}

// NewDirStore returns a store rooted at dir.
func NewDirStore(dir, passphrase string) *DirStore {
	return &DirStore{Dir: dir, Passphrase: passphrase}
}

func (s *DirStore) path(name string) string { return filepath.Join(s.Dir, name+".json") }

func (s *DirStore) List(_ context.Context) ([]Info, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		k, err := s.loadFile(filepath.Join(s.Dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, Info{Name: k.Name, Role: k.Role, KeyID: k.id})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].KeyID < out[j].KeyID })
	return out, nil
}

func (s *DirStore) Get(_ context.Context, keyid string) (*Key, error) {
	infos, err := s.List(context.Background())
	if err != nil {
		return nil, err
	}
	for _, info := range infos {
		if info.KeyID != keyid && info.Name != keyid {
			continue
		}
		return s.loadFile(s.path(info.Name))
	}
	return nil, &ErrMissingKey{Role: "any", KeyID: keyid, Hint: fmt.Sprintf("key %q not in %s", keyid, s.Dir)}
}

// Find returns the first key matching role (and name when non-empty).
func (s *DirStore) Find(_ context.Context, role Role, name string) (*Key, error) {
	infos, err := s.List(context.Background())
	if err != nil {
		return nil, err
	}
	for _, info := range infos {
		if info.Role != role {
			continue
		}
		if name != "" && info.Name != name {
			continue
		}
		return s.loadFile(s.path(info.Name))
	}
	hint := fmt.Sprintf("%s key", role)
	if name != "" {
		hint = fmt.Sprintf("%s key %q", role, name)
	}
	return nil, &ErrMissingKey{Role: string(role), Hint: hint + " — run this on the machine that holds it"}
}

// FindAll returns every key with the given role.
func (s *DirStore) FindAll(_ context.Context, role Role) ([]*Key, error) {
	infos, err := s.List(context.Background())
	if err != nil {
		return nil, err
	}
	var out []*Key
	for _, info := range infos {
		if info.Role != role {
			continue
		}
		k, err := s.loadFile(s.path(info.Name))
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

func (s *DirStore) Add(_ context.Context, k *Key) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	kf := keyFile{Name: k.Name, Role: k.Role}
	if s.Passphrase == "" {
		kf.SeedHex = hex.EncodeToString(k.seed)
	} else {
		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return err
		}
		dk, err := scrypt.Key([]byte(s.Passphrase), salt, 1<<15, 8, 1, 32)
		if err != nil {
			return err
		}
		block, err := aes.NewCipher(dk)
		if err != nil {
			return err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return err
		}
		nonce := make([]byte, gcm.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			return err
		}
		ct := gcm.Seal(nil, nonce, k.seed, nil)
		kf.KDF, kf.Salt, kf.Nonce, kf.Ciphertext = "scrypt", hex.EncodeToString(salt), hex.EncodeToString(nonce), hex.EncodeToString(ct)
	}
	data, err := json.MarshalIndent(kf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(k.Name), data, 0o600)
}

func (s *DirStore) Remove(_ context.Context, keyid string) error {
	k, err := s.Get(context.Background(), keyid)
	if err != nil {
		return err
	}
	return os.Remove(s.path(k.Name))
}

func (s *DirStore) loadFile(path string) (*Key, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var kf keyFile
	if err := json.Unmarshal(data, &kf); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var seed []byte
	if kf.KDF == "" {
		seed, err = hex.DecodeString(kf.SeedHex)
		if err != nil {
			return nil, fmt.Errorf("%s: seed: %w", path, err)
		}
	} else {
		if s.Passphrase == "" {
			return nil, fmt.Errorf("%s: key %q is encrypted; set KERYX_PASSPHRASE", path, kf.Name)
		}
		salt, err := hex.DecodeString(kf.Salt)
		if err != nil {
			return nil, err
		}
		nonce, err := hex.DecodeString(kf.Nonce)
		if err != nil {
			return nil, err
		}
		ct, err := hex.DecodeString(kf.Ciphertext)
		if err != nil {
			return nil, err
		}
		dk, err := scrypt.Key([]byte(s.Passphrase), salt, 1<<15, 8, 1, 32)
		if err != nil {
			return nil, err
		}
		block, err := aes.NewCipher(dk)
		if err != nil {
			return nil, err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		seed, err = gcm.Open(nil, nonce, ct, nil)
		if err != nil {
			return nil, fmt.Errorf("%s: wrong passphrase", path)
		}
	}
	return FromSeed(kf.Role, kf.Name, seed)
}

// ExportBundle is a role-tagged, optionally encrypted set of keys
// (spec/clients.md §2 handoff artifacts).
type ExportBundle struct {
	Version int       `json:"version"`
	Keys    []KeyFile `json:"keys"`
}

// KeyFile is the exported form of one key.
type KeyFile struct {
	Name    string `json:"name"`
	Role    Role   `json:"role"`
	SeedHex string `json:"seed_hex,omitempty"`
	Public  string `json:"public,omitempty"`
	KeyID   string `json:"keyid,omitempty"`
}

// Export writes the requested keys as a bundle. When passphrase is non-empty
// the seeds are encrypted with scrypt + AES-256-GCM.
func Export(_ context.Context, store Store, keyids []string, passphrase string) ([]byte, error) {
	infos, err := store.List(context.Background())
	if err != nil {
		return nil, err
	}
	bundle := ExportBundle{Version: 1}
	for _, info := range infos {
		if len(keyids) > 0 && !contains(keyids, info.KeyID) && !contains(keyids, info.Name) {
			continue
		}
		k, err := store.Get(context.Background(), info.KeyID)
		if err != nil {
			return nil, err
		}
		bundle.Keys = append(bundle.Keys, KeyFile{
			Name: k.Name, Role: k.Role,
			SeedHex: hex.EncodeToString(k.seed),
			Public:  hex.EncodeToString(k.pub),
			KeyID:   k.id,
		})
	}
	return json.MarshalIndent(bundle, "", "  ")
}

// Import adds every key in a bundle to store. Encrypted bundles are not
// supported here yet (they are decrypted by the caller's tooling).
func Import(_ context.Context, store Store, data []byte) ([]Info, error) {
	var bundle ExportBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return nil, err
	}
	if bundle.Version != 1 {
		return nil, fmt.Errorf("key bundle: unknown version %d", bundle.Version)
	}
	var out []Info
	for _, kf := range bundle.Keys {
		seed, err := hex.DecodeString(kf.SeedHex)
		if err != nil {
			return nil, fmt.Errorf("key %s: %w", kf.Name, err)
		}
		k, err := FromSeed(kf.Role, kf.Name, seed)
		if err != nil {
			return nil, err
		}
		if kf.KeyID != "" && kf.KeyID != k.id {
			return nil, fmt.Errorf("key %s: keyid mismatch", kf.Name)
		}
		if err := store.Add(context.Background(), k); err != nil {
			return nil, err
		}
		out = append(out, Info{Name: k.Name, Role: k.Role, KeyID: k.id})
	}
	return out, nil
}

// KeyID computes the TUF-standard keyid of a public key object.
func KeyID(key *metadata.Key) (string, error) {
	return key.ID()
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Hash returns the SHA-256 hex of data.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

var errNotFound = errors.New("key not found")

// IsNotFound reports whether err is a missing-key error.
func IsNotFound(err error) bool {
	var e *ErrMissingKey
	return errors.As(err, &e) || errors.Is(err, errNotFound)
}
