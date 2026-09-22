package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Versions are the metadata versions that must be live before devices are
// woken: the freshness root and the chain the app verifies before it can show
// a new item.
type Versions struct {
	Timestamp int64 `json:"timestamp"`
	Snapshot  int64 `json:"snapshot"`
	Targets   int64 `json:"targets"`
	// Role is the followed channel's delegated role (channels.<channel>.json),
	// the index holding the new item.
	Role int64 `json:"role"`
}

// AtLeast reports whether every version is at least want's.
func (v Versions) AtLeast(want Versions) bool {
	return v.Timestamp >= want.Timestamp && v.Snapshot >= want.Snapshot &&
		v.Targets >= want.Targets && v.Role >= want.Role
}

func (v Versions) String() string {
	return fmt.Sprintf("timestamp %d snapshot %d targets %d channel %d", v.Timestamp, v.Snapshot, v.Targets, v.Role)
}

// LocalVersions reads the metadata versions of a local repo — the versions
// `pub publish` just wrote.
func LocalVersions(repoDir, channel string) (Versions, error) {
	var v Versions
	for _, f := range []struct {
		file string
		out  *int64
	}{
		{"timestamp.json", &v.Timestamp},
		{"snapshot.json", &v.Snapshot},
		{"targets.json", &v.Targets},
		{"channels." + channel + ".json", &v.Role},
	} {
		n, err := metadataVersionFile(filepath.Join(repoDir, f.file))
		if err != nil {
			return Versions{}, err
		}
		*f.out = n
	}
	return v, nil
}

// FetchVersions reads the deployed repo's metadata versions. The timestamp URL
// carries a per-fetch nonce (it is the freshness root, so it has no version to
// pin); the rest carry that nonce too, so a static host's CDN edge cannot
// answer any of them from a stale cache.
func FetchVersions(ctx context.Context, client *http.Client, base, channel string) (Versions, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	base = strings.TrimSuffix(base, "/") + "/"
	nonce := strconv.FormatInt(time.Now().UnixNano(), 10)
	var v Versions
	for _, f := range []struct {
		file string
		out  *int64
	}{
		{"timestamp.json", &v.Timestamp},
		{"snapshot.json", &v.Snapshot},
		{"targets.json", &v.Targets},
		{"channels." + channel + ".json", &v.Role},
	} {
		n, err := fetchVersion(ctx, client, base+f.file+"?t="+nonce)
		if err != nil {
			return Versions{}, err
		}
		*f.out = n
	}
	return v, nil
}

// pollInterval is the delay between deployed-repo polls (a test shortens it).
var pollInterval = 2 * time.Second

// WaitForDeploy polls the deployed repo until every metadata version is at
// least want's, then returns. A static host's CDN edge can serve the previous
// files for a while after a deploy, and how long varies by platform, so the
// publisher tool waits on the repository itself instead of sleeping for a guessed
// interval. Devices must not be woken before this returns: their first sync
// would otherwise read the previous metadata and silently show one publish behind.
func WaitForDeploy(ctx context.Context, client *http.Client, base, channel string, want Versions, timeout time.Duration) (Versions, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		got, err := FetchVersions(ctx, client, base, channel)
		if err == nil {
			if got.AtLeast(want) {
				return got, nil
			}
			lastErr = fmt.Errorf("deployed repo is at %s, want at least %s", got, want)
		} else {
			lastErr = err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return Versions{}, fmt.Errorf("deploy did not propagate within %s: %w", timeout, lastErr)
		}
		wait := pollInterval
		if remaining < wait {
			wait = remaining
		}
		select {
		case <-ctx.Done():
			return Versions{}, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// metadataVersionFile reads `signed.version` from a local metadata file.
func metadataVersionFile(path string) (int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return metadataVersion(raw, path)
}

// fetchVersion reads `signed.version` from a deployed metadata file.
func fetchVersion(ctx context.Context, client *http.Client, url string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, err
	}
	return metadataVersion(raw, url)
}

func metadataVersion(raw []byte, name string) (int64, error) {
	var doc struct {
		Signed struct {
			Version int64 `json:"version"`
		} `json:"signed"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if doc.Signed.Version <= 0 {
		return 0, fmt.Errorf("%s: no version", name)
	}
	return doc.Signed.Version, nil
}
