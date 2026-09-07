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
// site directory: metadata + per-channel feed target files (PROTOCOL §3).
const repoDir = "keryx"

// channelFeedPath returns the TUF target path of a channel's public feed:
// channels/<name>/feed.json (PROTOCOL §3/§8).
func channelFeedPath(name string) string {
	return "channels/" + name + "/feed.json"
}

// buildRepo creates the full TUF repository (root/targets/snapshot/timestamp
// + one delegated role per channel, consistent snapshots, versioned files)
// and writes it under siteDir/keryx/, plus the well-known root anchor at
// siteDir/.well-known/keryx/root.json (PROTOCOL §2/§3).
//
// Key split (PROTOCOL §3): the offline master key signs root + targets
// (authorization); the online ops key signs snapshot + timestamp (freshness)
// — no master involvement on any publish. Channel keys sign their own role
// metadata (<channel>.json), which pins that channel's feed target.
func buildRepo(keys map[string]*keyPair, siteDir string, feeds map[string][]byte) error {
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
	root := metadata.Root(now.AddDate(2, 0, 0))
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

	// --- targets.json: authorization (PROTOCOL §4). Channels = delegated
	// TUF roles (one per channel, paths channels/<name>/*, terminating).
	// custom carries company identity, editor mode (§9) and private-feed
	// patterns (§10) — all master-signed.
	targets := metadata.Targets(now.AddDate(1, 0, 0))
	dlgKeys := map[string]*metadata.Key{}
	var dlgRoles []metadata.DelegatedRole
	for _, ch := range channels {
		kp := keys[ch.Name]
		dlgKeys[kp.KeyID] = kp.Key
		dlgRoles = append(dlgRoles, metadata.DelegatedRole{
			Name:        ch.Name,
			KeyIDs:      []string{kp.KeyID},
			Threshold:   1,
			Terminating: true,
			Paths:       []string{"channels/" + ch.Name + "/*"},
		})
	}
	targets.Signed.Delegations = &metadata.Delegations{Keys: dlgKeys, Roles: dlgRoles}

	editorMode := map[string]any{}
	for chName, ec := range editorChannels {
		entryKeys := map[string]*metadata.Key{}
		var keyids []string
		for _, name := range ec.KeyNames {
			kp := keys[name]
			entryKeys[kp.KeyID] = kp.Key
			keyids = append(keyids, kp.KeyID)
		}
		editorMode[chName] = map[string]any{
			"keys":      entryKeys,
			"keyids":    keyids,
			"threshold": ec.Threshold,
		}
	}
	engine := keys["tracking"]
	targets.Signed.UnrecognizedFields = map[string]any{
		"custom": map[string]any{
			"company_name": companyName,
			"logo":         logoURL,
			"editor_mode":  editorMode,
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

	// --- per-channel role metadata (<channel>.json): signed by the channel
	// key; pins that channel's feed target (length + hashes + display
	// metadata). Delegations are always empty — a channel is a leaf (§5).
	channelMeta := map[string]*metadata.Metadata[metadata.TargetsType]{}
	for _, ch := range channels {
		feedBytes := feeds[ch.Name]
		sum := sha256.Sum256(feedBytes)
		customRaw, err := json.Marshal(map[string]any{
			"display_name": ch.DisplayName,
			"description":  ch.Description,
		})
		if err != nil {
			return err
		}
		role := metadata.Targets(now.AddDate(0, 6, 0))
		role.Signed.Targets[channelFeedPath(ch.Name)] = &metadata.TargetFiles{
			Length: int64(len(feedBytes)),
			Hashes: metadata.Hashes{"sha256": sum[:]},
			Custom: (*json.RawMessage)(&customRaw),
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

	// --- snapshot/timestamp: pins metadata versions + hashes.
	metaFiles := map[string]*metadata.MetaFiles{}
	bytesOf := func(m *metadata.Metadata[metadata.TargetsType]) ([]byte, error) {
		return m.ToBytes(true)
	}
	targetsBytes, err := targets.ToBytes(true)
	if err != nil {
		return err
	}
	metaFiles["targets.json"] = metaFilesFor(targets.Signed.Version, targetsBytes)
	for _, ch := range channels {
		b, err := bytesOf(channelMeta[ch.Name])
		if err != nil {
			return err
		}
		metaFiles[ch.Name+".json"] = metaFilesFor(channelMeta[ch.Name].Signed.Version, b)
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

	// --- write everything (consistent-snapshot naming: versioned + unversioned).
	write := func(name string, data []byte) error {
		return os.WriteFile(filepath.Join(dir, name), data, 0o644)
	}
	rootBytes, err := root.ToBytes(true)
	if err != nil {
		return err
	}
	if err := write("root.json", rootBytes); err != nil {
		return err
	}
	if err := write("1.root.json", rootBytes); err != nil {
		return err
	}
	if err := write("targets.json", targetsBytes); err != nil {
		return err
	}
	if err := write("1.targets.json", targetsBytes); err != nil {
		return err
	}
	for _, ch := range channels {
		b, err := channelMeta[ch.Name].ToBytes(true)
		if err != nil {
			return err
		}
		if err := write(ch.Name+".json", b); err != nil {
			return err
		}
		if err := write("1."+ch.Name+".json", b); err != nil {
			return err
		}
	}
	if err := write("snapshot.json", snapshotBytes); err != nil {
		return err
	}
	if err := write("1.snapshot.json", snapshotBytes); err != nil {
		return err
	}
	timestampBytes, err := timestamp.ToBytes(true)
	if err != nil {
		return err
	}
	if err := write("timestamp.json", timestampBytes); err != nil {
		return err
	}
	// target files: canonical path + consistent-snapshot hash-prefixed copy
	for _, ch := range channels {
		feedBytes := feeds[ch.Name]
		sum := sha256.Sum256(feedBytes)
		chDir := filepath.Join(dir, "channels", ch.Name)
		if err := os.MkdirAll(chDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(chDir, "feed.json"), feedBytes, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(chDir, hex.EncodeToString(sum[:])+".feed.json"), feedBytes, 0o644); err != nil {
			return err
		}
	}

	// --- root anchor at the join origin's well-known space (PROTOCOL §2).
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
// chain (root → timestamp → snapshot → targets → per-channel role metadata),
// every feed target hash/length, every feed item signature (editor mode
// strict, default attribution) and the private capability feed as a whole.
func verifyRepo(keys map[string]*keyPair, siteDir string) error {
	dir := filepath.Join(siteDir, repoDir)

	// root anchor (well-known) and repo-base root must be byte-identical
	anchor, err := os.ReadFile(filepath.Join(siteDir, ".well-known", "keryx", "root.json"))
	if err != nil {
		return fmt.Errorf("loading well-known root anchor: %w", err)
	}
	baseRoot, err := os.ReadFile(filepath.Join(dir, "root.json"))
	if err != nil {
		return fmt.Errorf("loading repo base root.json: %w", err)
	}
	if !bytes.Equal(anchor, baseRoot) {
		return fmt.Errorf("root anchor != repo base root.json")
	}

	root, err := metadata.Root().FromFile(filepath.Join(dir, "root.json"))
	if err != nil {
		return fmt.Errorf("loading root.json: %w", err)
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

	// targets.json: delegations = one role per public channel, terminating,
	// paths channels/<name>/*; custom carries identity + editor mode +
	// private-feed patterns (all master-signed).
	roles := targets.Signed.Delegations.Roles
	if len(roles) != len(channels) {
		return fmt.Errorf("targets.json: %d delegations, want %d", len(roles), len(channels))
	}
	editorMode, _ := targets.Signed.UnrecognizedFields["custom"].(map[string]any)["editor_mode"].(map[string]any)
	for _, ch := range channels {
		role := findRole(roles, ch.Name)
		if role == nil {
			return fmt.Errorf("targets.json: missing delegation %q", ch.Name)
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
		if ec, ok := editorChannels[ch.Name]; ok {
			entry, _ := editorMode[ch.Name].(map[string]any)
			if entry == nil {
				return fmt.Errorf("targets.json: editor_mode.%s missing", ch.Name)
			}
			keyids := asStringSlice(entry["keyids"])
			if len(keyids) != len(ec.KeyNames) {
				return fmt.Errorf("editor_mode.%s: %d keyids, want %d", ch.Name, len(keyids), len(ec.KeyNames))
			}
			if thr := asInt(entry["threshold"]); thr != ec.Threshold {
				return fmt.Errorf("editor_mode.%s: threshold %v, want %d", ch.Name, entry["threshold"], ec.Threshold)
			}
			// key separation (§9): editor keyids MUST NOT be the channel role keyid
			for _, kid := range keyids {
				if kid == role.KeyIDs[0] {
					return fmt.Errorf("editor_mode.%s: key %s is also the channel role key", ch.Name, kid)
				}
			}
			// key publication (§4): every keyid has its key object in the entry
			entryKeys, _ := entry["keys"].(map[string]any)
			for _, kid := range keyids {
				if entryKeys[kid] == nil {
					return fmt.Errorf("editor_mode.%s: key object for %s missing", ch.Name, kid)
				}
			}
		}
	}

	// per-channel role metadata: signed by the channel key, pinned by
	// snapshot, pins the channel's feed target.
	type roleCheck struct {
		meta *metadata.Metadata[metadata.TargetsType]
		path string
		info *metadata.TargetFiles
	}
	checks := map[string]roleCheck{}
	for _, ch := range channels {
		role, err := metadata.Targets().FromFile(filepath.Join(dir, ch.Name+".json"))
		if err != nil {
			return fmt.Errorf("loading %s.json: %w", ch.Name, err)
		}
		if err := targets.VerifyDelegate(ch.Name, role); err != nil {
			return fmt.Errorf("%s signature (by delegated role): %w", ch.Name, err)
		}
		info := snapshot.Signed.Meta[ch.Name+".json"]
		if info == nil {
			return fmt.Errorf("snapshot: meta.%s.json missing", ch.Name)
		}
		if info.Version != role.Signed.Version {
			return fmt.Errorf("snapshot references %s v%d, have v%d", ch.Name, info.Version, role.Signed.Version)
		}
		path := channelFeedPath(ch.Name)
		tf := role.Signed.Targets[path]
		if tf == nil {
			return fmt.Errorf("%s.json: missing target %q", ch.Name, path)
		}
		checks[ch.Name] = roleCheck{meta: role, path: path, info: tf}
	}

	// feed target bytes: length + sha256, consistent-snapshot copy identical
	for _, ch := range channels {
		rc := checks[ch.Name]
		feedBytes, err := os.ReadFile(filepath.Join(dir, rc.path))
		if err != nil {
			return err
		}
		if rc.info.Length != int64(len(feedBytes)) {
			return fmt.Errorf("%s: feed length %d != metadata %d", ch.Name, len(feedBytes), rc.info.Length)
		}
		sum := sha256.Sum256(feedBytes)
		if !bytes.Equal(sum[:], rc.info.Hashes["sha256"]) {
			return fmt.Errorf("%s: feed sha256 mismatch", ch.Name)
		}
		hashCopy, err := os.ReadFile(filepath.Join(dir, rc.path, "..", hex.EncodeToString(sum[:])+".feed.json"))
		if err != nil {
			return fmt.Errorf("%s: consistent-snapshot feed copy: %w", ch.Name, err)
		}
		if !bytes.Equal(hashCopy, feedBytes) {
			return fmt.Errorf("%s: consistent-snapshot feed copy differs from canonical feed.json", ch.Name)
		}
		fmt.Printf("  %s: %d bytes, sha256 %s\n", rc.path, len(feedBytes), hex.EncodeToString(sum[:]))

		// item signatures (editor mode strict, default attribution)
		var editorCfg *editorConfig
		if ec, ok := editorChannels[ch.Name]; ok {
			editorCfg = &ec
		}
		if err := verifyFeedFile(filepath.Join(dir, rc.path), keys, keys[ch.Name], editorCfg, nil); err != nil {
			return err
		}
	}

	// private capability feed: whole-document verification (§10)
	privFeeds, err := filepath.Glob(filepath.Join(siteDir, "channels", "tracking", "*", "feed.json"))
	if err != nil {
		return err
	}
	for _, pf := range privFeeds {
		url := metadataOrigin + strings.TrimPrefix(pf, siteDir)
		check := &privateFeedCheck{Channel: trackingPatternEntry.Channel, Key: keys["tracking"], FetchedURL: url}
		if err := verifyFeedFile(pf, keys, keys["tracking"], nil, check); err != nil {
			return err
		}
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

// asStringSlice normalizes a JSON-decoded value into a []string.
func asStringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// asInt normalizes a JSON-decoded number (float64) into an int.
func asInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case float64:
		return int(t)
	default:
		return 0
	}
}
