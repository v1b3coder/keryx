// Package demoreseal re-points a copy of the demo repository at a local server
// for the browser/relay end-to-end runs. custom.repo_base is master-signed, so
// every released root version must be re-signed after the edit: version v is
// signed by the keys of v-1 and v, keeping the anchor chain walkable.
package demoreseal

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

// Root rewrites every released root version under dir/.well-known/keryx to point
// custom.repo_base at repoBase and re-signs it with the master seeds in keysDir.
func Root(dir, keysDir, repoBase string) error {
	anchor := filepath.Join(dir, ".well-known", "keryx")
	seeds, err := loadSeeds(keysDir)
	if err != nil {
		return err
	}
	roots, err := loadRoots(anchor)
	if err != nil {
		return err
	}
	var max int64
	for v := range roots {
		if v > max {
			max = v
		}
	}
	if max == 0 {
		return fmt.Errorf("%s: no root metadata", anchor)
	}
	for v := int64(1); v <= max; v++ {
		meta := roots[v]
		if meta == nil {
			return fmt.Errorf("%s: %d.root.json missing", anchor, v)
		}
		custom, _ := meta.Signed.UnrecognizedFields["custom"].(map[string]any)
		if custom == nil {
			return fmt.Errorf("%s: %d.root.json: custom missing", anchor, v)
		}
		custom["repo_base"] = repoBase
		meta.ClearSignatures()
		signed := map[string]bool{}
		for _, ver := range []int64{v - 1, v} {
			if ver < 1 {
				continue
			}
			prev := roots[ver]
			if prev == nil {
				return fmt.Errorf("%s: %d.root.json missing", anchor, ver)
			}
			role := prev.Signed.Roles[metadata.ROOT]
			if role == nil {
				return fmt.Errorf("%s: %d.root.json: root role missing", anchor, ver)
			}
			for _, keyid := range role.KeyIDs {
				if signed[keyid] {
					continue
				}
				signed[keyid] = true
				key := prev.Signed.Keys[keyid]
				if key == nil {
					return fmt.Errorf("%s: %d.root.json: key %s missing", anchor, ver, keyid)
				}
				pub, err := key.ToPublicKey()
				if err != nil {
					return fmt.Errorf("%s: %d.root.json: key %s: %w", anchor, ver, keyid, err)
				}
				edPub, ok := pub.(ed25519.PublicKey)
				if !ok {
					return fmt.Errorf("%s: %d.root.json: key %s: not ed25519", anchor, ver, keyid)
				}
				priv, ok := seeds[string(edPub)]
				if !ok {
					return fmt.Errorf("%s: no master seed for key %s (from %d.root.json)", keysDir, keyid, ver)
				}
				signer, err := signature.LoadSigner(priv, 0)
				if err != nil {
					return err
				}
				if _, err := meta.Sign(signer); err != nil {
					return fmt.Errorf("%s: %d.root.json: sign: %w", anchor, v, err)
				}
			}
		}
		out, err := meta.ToBytes(true)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(anchor, fmt.Sprintf("%d.root.json", v)), out, 0o644); err != nil {
			return err
		}
		if v == max {
			if err := os.WriteFile(filepath.Join(anchor, "root.json"), out, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadRoots parses every root metadata file in the anchor directory, keyed by
// the version inside the document.
func loadRoots(anchor string) (map[int64]*metadata.Metadata[metadata.RootType], error) {
	entries, err := os.ReadDir(anchor)
	if err != nil {
		return nil, err
	}
	roots := map[int64]*metadata.Metadata[metadata.RootType]{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (name != "root.json" && !strings.HasSuffix(name, ".root.json")) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(anchor, name))
		if err != nil {
			return nil, err
		}
		var meta metadata.Metadata[metadata.RootType]
		if _, err := meta.FromBytes(data); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if _, ok := roots[meta.Signed.Version]; !ok {
			roots[meta.Signed.Version] = &meta
		}
	}
	return roots, nil
}

// loadSeeds reads every plaintext seed in the keystore, keyed by the raw
// Ed25519 public key so a root role keyid can find its signer.
func loadSeeds(keysDir string) (map[string]ed25519.PrivateKey, error) {
	entries, err := os.ReadDir(keysDir)
	if err != nil {
		return nil, err
	}
	seeds := map[string]ed25519.PrivateKey{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(keysDir, e.Name()))
		if err != nil {
			return nil, err
		}
		var record struct {
			SeedHex string `json:"seed_hex"`
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			continue // not a key record
		}
		seed, err := hex.DecodeString(record.SeedHex)
		if err != nil || len(seed) != ed25519.SeedSize {
			continue // encrypted or not an Ed25519 seed
		}
		priv := ed25519.NewKeyFromSeed(seed)
		seeds[string(priv.Public().(ed25519.PublicKey))] = priv
	}
	return seeds, nil
}
