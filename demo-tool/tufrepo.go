package main

import (
	"bytes"
	"crypto"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

// repoDir is the TUF repo base (root.json custom.repo_base) relative to the
// site directory: metadata + per-channel item files (spec/repository.md §1).
const repoDir = "keryx"

// itemTargetPath returns the TUF target path of one public item:
// channels/<name>/<id>.json (spec/feeds.md §1.1 — one item = one target).
func itemTargetPath(ch, id string) string {
	return "channels/" + ch + "/" + id + ".json"
}

// buildRepo creates the full TUF repository (root/targets/snapshot/timestamp
// + one delegated role per channel + optional authors roles,
// consistent_snapshot: false — the spec default) and writes it under
// siteDir/keryx/, plus the well-known root anchor at
// siteDir/.well-known/keryx/root.json (spec/core.md §1, spec/repository.md §1).
//
// Key split (spec/repository.md §1): the offline master key signs root +
// targets (authorization); the online ops key signs snapshot + timestamp
// (freshness) — no master involvement on any publish. Channel keys sign
// their own role metadata (channels.<name>.json), which pins that channel's
// item targets; author keys sign the authors role metadata
// (channels.<name>.authors.json) which authorizes item signing
// (spec/feeds.md §2). Root metadata lives ONLY at the well-known anchor —
// never in the repo base.
func buildRepo(keys map[string]*keyPair, siteDir string, items map[string][]byte) error {
	now := time.Now().UTC().Truncate(time.Second)
	dir := filepath.Join(siteDir, repoDir)
	// regenerate: drop stale metadata from previous runs
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// --- root.json: trust anchor. Master = root+targets; ops = snapshot+
	// timestamp. custom carries repo_base (single URL) + mode ("full").
	// consistent_snapshot: false (spec default) — metadata and targets are
	// served at their plain paths; only root keeps versioned N.root.json
	// files (TUF mandates them regardless of the flag).
	root := metadata.Root(now.AddDate(2, 0, 0))
	root.Signed.ConsistentSnapshot = false
	for _, role := range []string{metadata.ROOT, metadata.TARGETS} {
		if err := root.Signed.AddKey(keys["master"].Key, role); err != nil {
			return fmt.Errorf("adding master key to root role %s: %w", role, err)
		}
	}
	for _, role := range []string{metadata.SNAPSHOT, metadata.TIMESTAMP} {
		if err := root.Signed.AddKey(keys["ops"].Key, role); err != nil {
			return fmt.Errorf("adding ops key to root role %s: %w", role, err)
		}
	}
	root.Signed.UnrecognizedFields = map[string]any{
		"custom": map[string]any{
			"repo_base": repoBase,
			"mode":      "full",
		},
	}

	// --- targets.json: authorization (spec/repository.md §2). Channels =
	// delegated TUF roles (one per channel, paths channels/<name>/*,
	// terminating); authored channels get a sibling non-terminating authors
	// role (channels.<name>.authors) with the same paths — it authorizes
	// item signing and pins no targets. custom carries company identity,
	// channel display metadata and private-feed patterns — all master-signed.
	targets := metadata.Targets(now.AddDate(1, 0, 0))
	dlgKeys := map[string]*metadata.Key{}
	var dlgRoles []metadata.DelegatedRole
	for _, ch := range channels {
		kp := keys[ch.Name]
		dlgKeys[kp.KeyID] = kp.Key
		// Role name is namespaced `channels.<channel>` (spec/repository.md
		// §2): the channel name is used verbatim in paths and the payload,
		// the role name (and its <role>.json metadata file) is prefixed.
		dlgRoles = append(dlgRoles, metadata.DelegatedRole{
			Name:        "channels." + ch.Name,
			KeyIDs:      []string{kp.KeyID},
			Threshold:   1,
			Terminating: true,
			Paths:       []string{"channels/" + ch.Name + "/*"},
		})
	}
	for chName, ac := range authorChannels {
		var keyids []string
		for _, name := range ac.KeyNames {
			kp := keys[name]
			dlgKeys[kp.KeyID] = kp.Key
			keyids = append(keyids, kp.KeyID)
		}
		dlgRoles = append(dlgRoles, metadata.DelegatedRole{
			Name:        "channels." + chName + ".authors",
			KeyIDs:      keyids,
			Threshold:   ac.Threshold,
			Terminating: false,
			Paths:       []string{"channels/" + chName + "/*"},
		})
	}
	targets.Signed.Delegations = &metadata.Delegations{Keys: dlgKeys, Roles: dlgRoles}

	channelsCustom := map[string]any{}
	for _, ch := range channels {
		channelsCustom[ch.Name] = map[string]any{
			"display_name": ch.DisplayName,
			"description":  ch.Description,
		}
	}
	engine := keys["tracking"]
	targets.Signed.UnrecognizedFields = map[string]any{
		"custom": map[string]any{
			"company_name": companyName,
			"logo":         logoURL,
			"logo_sha256":  logoSHA256(),
			"channels":     channelsCustom,
			"private_feed_patterns": []any{
				map[string]any{
					"channel":      trackingPatternEntry.Channel,
					"pattern":      trackingPattern,
					"keys":         map[string]*metadata.Key{engine.KeyID: engine.Key},
					"keyids":       []string{engine.KeyID},
					"threshold":    1,
					"display_name": trackingPatternEntry.DisplayName,
					"purpose":      trackingPatternEntry.Purpose,
				},
			},
		},
	}

	masterSigner, err := signature.LoadSigner(keys["master"].Priv, crypto.Hash(0))
	if err != nil {
		return fmt.Errorf("loading master signer: %w", err)
	}
	opsSigner, err := signature.LoadSigner(keys["ops"].Priv, crypto.Hash(0))
	if err != nil {
		return fmt.Errorf("loading ops signer: %w", err)
	}
	if err := signAndTag(root, masterSigner, keys["master"]); err != nil {
		return fmt.Errorf("signing root: %w", err)
	}
	if err := signAndTag(targets, masterSigner, keys["master"]); err != nil {
		return fmt.Errorf("signing targets: %w", err)
	}

	// --- per-channel role metadata (channels.<name>.json): signed by the
	// channel key; pins that channel's item files (length + hashes). No
	// per-target custom (display metadata lives in master-signed
	// custom.channels — spec/repository.md §2/§3). A channel is a leaf:
	// delegations are always empty (spec/repository.md §2).
	channelMeta := map[string]*metadata.Metadata[metadata.TargetsType]{}
	for _, ch := range channels {
		role := metadata.Targets(now.AddDate(0, 6, 0))
		for path, itemBytes := range items {
			if !strings.HasPrefix(path, "channels/"+ch.Name+"/") {
				continue
			}
			sum := sha256.Sum256(itemBytes)
			role.Signed.Targets[path] = &metadata.TargetFiles{
				Length: int64(len(itemBytes)),
				Hashes: metadata.Hashes{"sha256": sum[:]},
			}
		}
		role.Signed.Delegations = &metadata.Delegations{Keys: map[string]*metadata.Key{}, Roles: []metadata.DelegatedRole{}}
		signer, err := signature.LoadSigner(keys[ch.Name].Priv, crypto.Hash(0))
		if err != nil {
			return err
		}
		if err := signAndTag(role, signer, keys[ch.Name]); err != nil {
			return fmt.Errorf("signing %s metadata: %w", ch.Name, err)
		}
		channelMeta[ch.Name] = role
	}

	// --- authors role metadata (channels.<name>.authors.json): signed by
	// the author keys per threshold, pins NO targets — it authorizes item
	// signing (spec/feeds.md §2). Pinned by snapshot.json.
	authorsMeta := map[string]*metadata.Metadata[metadata.TargetsType]{}
	for chName, ac := range authorChannels {
		role := metadata.Targets(now.AddDate(0, 6, 0))
		role.Signed.Delegations = &metadata.Delegations{Keys: map[string]*metadata.Key{}, Roles: []metadata.DelegatedRole{}}
		for _, name := range ac.KeyNames {
			signer, err := signature.LoadSigner(keys[name].Priv, crypto.Hash(0))
			if err != nil {
				return err
			}
			if err := signAndTag(role, signer, keys[name]); err != nil {
				return fmt.Errorf("signing %s authors metadata: %w", chName, err)
			}
		}
		authorsMeta[chName] = role
	}

	// --- snapshot/timestamp: pins metadata versions + hashes.
	metaFiles := map[string]*metadata.MetaFiles{}
	targetsBytes, err := targets.ToBytes(true)
	if err != nil {
		return err
	}
	metaFiles["targets.json"] = metaFilesFor(targets.Signed.Version, targetsBytes)
	for _, ch := range channels {
		b, err := channelMeta[ch.Name].ToBytes(true)
		if err != nil {
			return err
		}
		metaFiles["channels."+ch.Name+".json"] = metaFilesFor(channelMeta[ch.Name].Signed.Version, b)
	}
	for chName := range authorsMeta {
		b, err := authorsMeta[chName].ToBytes(true)
		if err != nil {
			return err
		}
		metaFiles["channels."+chName+".authors.json"] = metaFilesFor(authorsMeta[chName].Signed.Version, b)
	}
	snapshot := metadata.Snapshot(now.AddDate(0, 1, 0))
	snapshot.Signed.Meta = metaFiles
	if err := signAndTag(snapshot, opsSigner, keys["ops"]); err != nil {
		return fmt.Errorf("signing snapshot: %w", err)
	}
	snapshotBytes, err := snapshot.ToBytes(true)
	if err != nil {
		return err
	}
	timestamp := metadata.Timestamp(now.AddDate(0, 0, 7))
	timestamp.Signed.Meta["snapshot.json"] = metaFilesFor(snapshot.Signed.Version, snapshotBytes)
	if err := signAndTag(timestamp, opsSigner, keys["ops"]); err != nil {
		return fmt.Errorf("signing timestamp: %w", err)
	}

	// --- write everything. consistent_snapshot: false — plain paths only.
	// No root files in the repo base (spec/repository.md §1: root metadata is
	// published exclusively at the well-known anchor).
	write := func(name string, data []byte) error {
		return os.WriteFile(filepath.Join(dir, name), data, 0o644)
	}
	if err := write("targets.json", targetsBytes); err != nil {
		return err
	}
	for _, ch := range channels {
		b, err := channelMeta[ch.Name].ToBytes(true)
		if err != nil {
			return err
		}
		if err := write("channels."+ch.Name+".json", b); err != nil {
			return err
		}
	}
	for chName, role := range authorsMeta {
		b, err := role.ToBytes(true)
		if err != nil {
			return err
		}
		if err := write("channels."+chName+".authors.json", b); err != nil {
			return err
		}
	}
	if err := write("snapshot.json", snapshotBytes); err != nil {
		return err
	}
	timestampBytes, err := timestamp.ToBytes(true)
	if err != nil {
		return err
	}
	if err := write("timestamp.json", timestampBytes); err != nil {
		return err
	}
	// item target files: one file per item at its canonical path
	// (channels/<name>/<id>.json — spec/feeds.md §1.1)
	for path, itemBytes := range items {
		targetPath := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(targetPath, itemBytes, 0o644); err != nil {
			return err
		}
	}

	// --- root anchor at the join origin's well-known space (spec/core.md §3,
	// spec/repository.md §1): root.json + every N.root.json — the only place
	// root metadata is ever published.
	rootBytes, err := root.ToBytes(true)
	if err != nil {
		return err
	}
	wk := filepath.Join(siteDir, ".well-known", "keryx")
	if err := os.MkdirAll(wk, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(wk, "root.json"), rootBytes, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(wk, "1.root.json"), rootBytes, 0o644); err != nil {
		return err
	}
	return nil
}

// signAndTag signs meta with signer, then retags the appended signature's
// keyid to the keyid of the key object as it appears in the metadata. go-tuf's
// Sign computes the signature keyid from a bare key (without unrecognized
// fields), but our key objects carry a human-readable `name` (keys.go), so the
// signature keyid must be retagged to match the role keyids — otherwise
// verification finds no matching signature (the keyid is the SHA-256 of the
// key object as serialized, name included).
func signAndTag[T metadata.Roles](meta *metadata.Metadata[T], signer signature.Signer, kp *keyPair) error {
	if _, err := meta.Sign(signer); err != nil {
		return err
	}
	meta.Signatures[len(meta.Signatures)-1].KeyID = kp.KeyID
	return nil
}

// metaFilesFor builds a snapshot/timestamp meta entry (version + length + hashes).
func metaFilesFor(version int64, data []byte) *metadata.MetaFiles {
	sum := sha256.Sum256(data)
	return &metadata.MetaFiles{
		Version: version,
		Length:  int64(len(data)),
		Hashes:  metadata.Hashes{"sha256": sum[:]},
	}
}

// verifyRepo re-loads the repository from disk and checks the full metadata
// chain (root → timestamp → snapshot → targets → per-channel role metadata
// + authors role metadata), every item target hash/length, every item
// signature (authors role strict, single-author channel-key strict), the
// private capability feed as a whole, and the master-signed custom
// (company identity, channel display metadata, logo hash, patterns).
func verifyRepo(keys map[string]*keyPair, siteDir string) error {
	dir := filepath.Join(siteDir, repoDir)

	// root anchor: the ONLY source of root metadata — it lives at the
	// well-known space, never in the repo base (spec/repository.md §1).
	root, err := metadata.Root().FromFile(filepath.Join(siteDir, ".well-known", "keryx", "root.json"))
	if err != nil {
		return fmt.Errorf("loading root anchor: %w", err)
	}
	if err := root.VerifyDelegate(metadata.ROOT, root); err != nil {
		return fmt.Errorf("root self-signature: %w", err)
	}
	if rb, ok := root.Signed.UnrecognizedFields["custom"].(map[string]any); ok {
		if rb["repo_base"] != repoBase {
			return fmt.Errorf("root custom.repo_base = %v, want %s", rb["repo_base"], repoBase)
		}
		if rb["mode"] != "full" {
			return fmt.Errorf("root custom.mode = %v, want \"full\"", rb["mode"])
		}
	} else {
		return fmt.Errorf("root.json: custom.repo_base missing")
	}
	if root.Signed.ConsistentSnapshot {
		return fmt.Errorf("root.json: consistent_snapshot must be false (spec default)")
	}

	timestamp, err := metadata.Timestamp().FromFile(filepath.Join(dir, "timestamp.json"))
	if err != nil {
		return fmt.Errorf("loading timestamp.json: %w", err)
	}
	if err := root.VerifyDelegate(metadata.TIMESTAMP, timestamp); err != nil {
		return fmt.Errorf("timestamp signature (by root): %w", err)
	}
	snapshot, err := metadata.Snapshot().FromFile(filepath.Join(dir, "snapshot.json"))
	if err != nil {
		return fmt.Errorf("loading snapshot.json: %w", err)
	}
	if err := root.VerifyDelegate(metadata.SNAPSHOT, snapshot); err != nil {
		return fmt.Errorf("snapshot signature (by root): %w", err)
	}
	targets, err := metadata.Targets().FromFile(filepath.Join(dir, "targets.json"))
	if err != nil {
		return fmt.Errorf("loading targets.json: %w", err)
	}
	if err := root.VerifyDelegate(metadata.TARGETS, targets); err != nil {
		return fmt.Errorf("targets signature (by root): %w", err)
	}

	// snapshot/timestamp must reference the current versions + hashes.
	if got := timestamp.Signed.Meta["snapshot.json"].Version; got != snapshot.Signed.Version {
		return fmt.Errorf("timestamp references snapshot v%d, have v%d", got, snapshot.Signed.Version)
	}
	if got := snapshot.Signed.Meta["targets.json"].Version; got != targets.Signed.Version {
		return fmt.Errorf("snapshot references targets v%d, have v%d", got, targets.Signed.Version)
	}

	// targets.json: one channel role per public channel (terminating, paths
	// channels/<name>/*) + one non-terminating authors role per authored
	// channel; custom carries identity + channel display metadata +
	// private-feed patterns (all master-signed).
	roles := targets.Signed.Delegations.Roles
	custom, _ := targets.Signed.UnrecognizedFields["custom"].(map[string]any)
	if custom == nil {
		return fmt.Errorf("targets.json: custom missing")
	}
	if custom["company_name"] != companyName {
		return fmt.Errorf("targets.json: custom.company_name = %v, want %s", custom["company_name"], companyName)
	}
	if custom["logo"] != logoURL {
		return fmt.Errorf("targets.json: custom.logo = %v, want %s", custom["logo"], logoURL)
	}
	if got := asString(custom["logo_sha256"]); got != logoSHA256() {
		return fmt.Errorf("targets.json: custom.logo_sha256 = %v, want %s", custom["logo_sha256"], logoSHA256())
	}
	channelsCustom, _ := custom["channels"].(map[string]any)
	for _, ch := range channels {
		role := findRole(roles, "channels."+ch.Name)
		if role == nil {
			return fmt.Errorf("targets.json: missing delegation %q", "channels."+ch.Name)
		}
		if !role.Terminating {
			return fmt.Errorf("delegation %q: terminating must be true", ch.Name)
		}
		wantPath := "channels/" + ch.Name + "/*"
		if len(role.Paths) != 1 || role.Paths[0] != wantPath {
			return fmt.Errorf("delegation %q: paths %v, want [%s]", ch.Name, role.Paths, wantPath)
		}
		if role.Threshold != 1 || len(role.KeyIDs) != 1 {
			return fmt.Errorf("delegation %q: expected 1-of-1", ch.Name)
		}
		entry, _ := channelsCustom[ch.Name].(map[string]any)
		if entry == nil {
			return fmt.Errorf("targets.json: custom.channels.%s missing", ch.Name)
		}
		if asString(entry["display_name"]) != ch.DisplayName {
			return fmt.Errorf("custom.channels.%s: display_name = %v, want %s", ch.Name, entry["display_name"], ch.DisplayName)
		}
	}
	for chName, ac := range authorChannels {
		roleName := "channels." + chName + ".authors"
		role := findRole(roles, roleName)
		if role == nil {
			return fmt.Errorf("targets.json: missing delegation %q", roleName)
		}
		if role.Terminating {
			return fmt.Errorf("delegation %q: must not be terminating", roleName)
		}
		wantPath := "channels/" + chName + "/*"
		if len(role.Paths) != 1 || role.Paths[0] != wantPath {
			return fmt.Errorf("delegation %q: paths %v, want [%s]", roleName, role.Paths, wantPath)
		}
		if role.Threshold != ac.Threshold || len(role.KeyIDs) != len(ac.KeyNames) {
			return fmt.Errorf("delegation %q: expected %d-of-%d", roleName, ac.Threshold, len(ac.KeyNames))
		}
		// key separation (spec/feeds.md §2): author keyids MUST NOT
		// intersect the channel role's keyids.
		chRole := findRole(roles, "channels."+chName)
		if chRole == nil {
			return fmt.Errorf("targets.json: missing delegation %q", "channels."+chName)
		}
		for _, kid := range role.KeyIDs {
			for _, chKID := range chRole.KeyIDs {
				if kid == chKID {
					return fmt.Errorf("authors role %q: key %s is also the channel role key", roleName, kid)
				}
			}
		}
		// key publication (spec/repository.md §2): every keyid has its key
		// object in the delegation's keys map.
		dlgKeys := targets.Signed.Delegations.Keys
		for _, kid := range role.KeyIDs {
			if dlgKeys[kid] == nil {
				return fmt.Errorf("authors role %q: key object for %s missing", roleName, kid)
			}
		}
	}

	// private-feed patterns: entry present, key objects published.
	patterns, _ := custom["private_feed_patterns"].([]any)
	if len(patterns) != 1 {
		return fmt.Errorf("targets.json: private_feed_patterns: %d entries, want 1", len(patterns))
	}
	entry, _ := patterns[0].(map[string]any)
	if asString(entry["channel"]) != trackingPatternEntry.Channel {
		return fmt.Errorf("private_feed_patterns: channel = %v, want %s", entry["channel"], trackingPatternEntry.Channel)
	}
	if asString(entry["pattern"]) != trackingPattern {
		return fmt.Errorf("private_feed_patterns: pattern = %v, want %s", entry["pattern"], trackingPattern)
	}
	engine := keys["tracking"]
	entryKeys, _ := entry["keys"].(map[string]any)
	if entryKeys[engine.KeyID] == nil {
		return fmt.Errorf("private_feed_patterns: key object for %s missing", engine.KeyID)
	}

	// per-channel role metadata: signed by the channel key, pinned by
	// snapshot, pins the channel's item files. Files are named
	// channels.<channel>.json (spec/repository.md §2/§3).
	type roleCheck struct {
		meta *metadata.Metadata[metadata.TargetsType]
	}
	checks := map[string]roleCheck{}
	for _, ch := range channels {
		roleName := "channels." + ch.Name
		role, err := metadata.Targets().FromFile(filepath.Join(dir, roleName+".json"))
		if err != nil {
			return fmt.Errorf("loading %s.json: %w", roleName, err)
		}
		if err := targets.VerifyDelegate(roleName, role); err != nil {
			return fmt.Errorf("%s signature (by delegated role): %w", roleName, err)
		}
		info := snapshot.Signed.Meta[roleName+".json"]
		if info == nil {
			return fmt.Errorf("snapshot: meta.%s.json missing", roleName)
		}
		if info.Version != role.Signed.Version {
			return fmt.Errorf("snapshot references %s v%d, have v%d", roleName, info.Version, role.Signed.Version)
		}
		if len(role.Signed.Targets) == 0 {
			return fmt.Errorf("%s.json: no item targets", roleName)
		}
		checks[ch.Name] = roleCheck{meta: role}
	}

	// authors role metadata: signed by author keys per threshold, pinned by
	// snapshot, pins no targets (spec/feeds.md §2).
	for chName := range authorChannels {
		roleName := "channels." + chName + ".authors"
		role, err := metadata.Targets().FromFile(filepath.Join(dir, roleName+".json"))
		if err != nil {
			return fmt.Errorf("loading %s.json: %w", roleName, err)
		}
		if err := targets.VerifyDelegate(roleName, role); err != nil {
			return fmt.Errorf("%s signature (by delegated role): %w", roleName, err)
		}
		info := snapshot.Signed.Meta[roleName+".json"]
		if info == nil {
			return fmt.Errorf("snapshot: meta.%s.json missing", roleName)
		}
		if info.Version != role.Signed.Version {
			return fmt.Errorf("snapshot references %s v%d, have v%d", roleName, info.Version, role.Signed.Version)
		}
		if len(role.Signed.Targets) != 0 {
			return fmt.Errorf("%s.json: authors role must pin no targets", roleName)
		}
	}

	// item target bytes: length + sha256 + item signature + path/id
	// consistency (spec/feeds.md §1.1/§1.2).
	for _, ch := range channels {
		role := checks[ch.Name].meta
		var authorsCfg *authorConfig
		if ac, ok := authorChannels[ch.Name]; ok {
			authorsCfg = &ac
		}
		for path, info := range role.Signed.Targets {
			itemPath := filepath.Join(dir, filepath.FromSlash(path))
			itemBytes, err := os.ReadFile(itemPath)
			if err != nil {
				return err
			}
			if info.Length != int64(len(itemBytes)) {
				return fmt.Errorf("%s: length %d != metadata %d", path, len(itemBytes), info.Length)
			}
			sum := sha256.Sum256(itemBytes)
			if !bytes.Equal(sum[:], info.Hashes["sha256"]) {
				return fmt.Errorf("%s: sha256 mismatch", path)
			}
			fmt.Printf("  %s: %d bytes, sha256 %s\n", path, len(itemBytes), hex.EncodeToString(sum[:]))
			var item map[string]any
			if err := json.Unmarshal(itemBytes, &item); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			id := asString(item["id"])
			if id != filepath.Base(path[:len(path)-len(".json")]) {
				return fmt.Errorf("%s: item id %q != path segment", path, id)
			}
			if err := verifyItemSignatures(item, keys, keys[ch.Name], authorsCfg); err != nil {
				return fmt.Errorf("%s: item %s: %w", path, id, err)
			}
		}
	}

	// private capability feed: whole-document verification (spec/feeds.md §3)
	privFeeds, err := filepath.Glob(filepath.Join(siteDir, "channels", "tracking", "*", "feed.json"))
	if err != nil {
		return err
	}
	for _, pf := range privFeeds {
		url := metadataOrigin + strings.TrimPrefix(pf, siteDir)
		check := &privateFeedCheck{Channel: trackingPatternEntry.Channel, Key: engine, FetchedURL: url}
		data, err := os.ReadFile(pf)
		if err != nil {
			return err
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("%s: %w", pf, err)
		}
		if err := verifyPrivateDocument(doc, pf, check); err != nil {
			return err
		}
		fmt.Printf("  %s: document verified\n", strings.TrimPrefix(pf, siteDir))
	}
	return nil
}

// findRole returns the delegated role with the given name.
func findRole(roles []metadata.DelegatedRole, name string) *metadata.DelegatedRole {
	for i := range roles {
		if roles[i].Name == name {
			return &roles[i]
		}
	}
	return nil
}
