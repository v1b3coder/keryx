package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/v1b3coder/keryx/sdk/deploy"
	"github.com/v1b3coder/keryx/sdk/feed"
	"github.com/v1b3coder/keryx/sdk/repo"
	"github.com/v1b3coder/keryx/sdk/tufrepo"
)

func (a *app) deployCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "deploy", Short: "Upload the two output directories"}
	var target, endpoint, bucket, prefix, accessKey, secretKey string
	var secure bool
	local := &cobra.Command{
		Use:   "local",
		Short: "Copy anchor/ + repo/ to a local directory",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := a.requireRole("ci", "operator"); err != nil {
				return err
			}
			if _, err := a.publisher().Validate(a.ctx()); err != nil {
				return fmt.Errorf("refusing to deploy an invalid repo: %w", err)
			}
			res, err := deploy.LocalDeployer{Target: target}.Deploy(a.ctx(), a.anchorPath(), a.repoPath())
			if err != nil {
				return err
			}
			a.render(res, fmt.Sprintf("deployed %d file(s) to %s", res.Files, res.Target))
			return nil
		},
	}
	local.Flags().StringVar(&target, "target", "", "local target directory")
	_ = local.MarkFlagRequired("target")

	s3 := &cobra.Command{
		Use:   "s3",
		Short: "Upload to an S3-compatible bucket",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := a.requireRole("ci", "operator"); err != nil {
				return err
			}
			if _, err := a.publisher().Validate(a.ctx()); err != nil {
				return fmt.Errorf("refusing to deploy an invalid repo: %w", err)
			}
			d := deploy.NewS3(endpoint, bucket, prefix, accessKey, secretKey, secure)
			res, err := d.Deploy(a.ctx(), a.anchorPath(), a.repoPath())
			if err != nil {
				return err
			}
			a.render(res, fmt.Sprintf("deployed %d file(s) to s3://%s/%s", res.Files, bucket, prefix))
			return nil
		},
	}
	f := s3.Flags()
	f.StringVar(&endpoint, "endpoint", "", "S3 endpoint")
	f.StringVar(&bucket, "bucket", "", "bucket")
	f.StringVar(&prefix, "prefix", "", "key prefix")
	f.StringVar(&accessKey, "access-key", os.Getenv("AWS_ACCESS_KEY_ID"), "access key")
	f.StringVar(&secretKey, "secret-key", os.Getenv("AWS_SECRET_ACCESS_KEY"), "secret key")
	f.BoolVar(&secure, "secure", true, "use TLS")
	_ = s3.MarkFlagRequired("bucket")
	cmd.AddCommand(local, s3)
	return cmd
}

// pull fetches and verifies a repo (and optionally the anchor) from a base URL
// into the workspace (design/tooling.md §3.2 — CI without git).
func (a *app) pullCmd() *cobra.Command {
	var base, anchorURL string
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Fetch + verify the repo from the deployed base",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := a.requireRole("ci", "operator"); err != nil {
				return err
			}
			if base == "" {
				return fmt.Errorf("--base is required")
			}
			base = strings.TrimSuffix(base, "/") + "/"
			client := &http.Client{Timeout: 30 * time.Second}
			// anchor defaults to the repo base origin's well-known space
			if anchorURL == "" {
				if u, err := url.Parse(base); err == nil && u.Host != "" {
					anchorURL = u.Scheme + "://" + u.Host + "/.well-known/keryx/"
				}
			}
			if anchorURL == "" {
				return fmt.Errorf("--anchor-url is required (could not derive it from --base)")
			}
			anchorURL = strings.TrimSuffix(anchorURL, "/") + "/"
			// the whole root chain: root.json plus every released N.root.json
			// (the client walks it; pull must fetch it too)
			if err := fetchTo(client, anchorURL+"root.json", filepath.Join(a.anchorPath(), "root.json")); err != nil {
				return err
			}
			rootBytes, err := os.ReadFile(filepath.Join(a.anchorPath(), "root.json"))
			if err != nil {
				return err
			}
			var rootDoc struct {
				Signed struct {
					Version int64 `json:"version"`
				} `json:"signed"`
			}
			if err := json.Unmarshal(rootBytes, &rootDoc); err != nil {
				return fmt.Errorf("root.json: %w", err)
			}
			for v := int64(1); v <= rootDoc.Signed.Version; v++ {
				name := fmt.Sprintf("%d.root.json", v)
				if err := fetchToOptional(client, anchorURL+name, filepath.Join(a.anchorPath(), name)); err != nil {
					return err
				}
			}
			// targets.json is the root of the non-root metadata tree
			if err := fetchTo(client, base+"targets.json", filepath.Join(a.repoPath(), "targets.json")); err != nil {
				return err
			}
			targetsBytes, err := os.ReadFile(filepath.Join(a.repoPath(), "targets.json"))
			if err != nil {
				return err
			}
			targets, err := metadata.Targets().FromBytes(targetsBytes)
			if err != nil {
				return err
			}
			for _, role := range targets.Signed.Delegations.Roles {
				name := role.Name
				if err := fetchTo(client, base+name+".json", filepath.Join(a.repoPath(), name+".json")); err != nil {
					return err
				}
				roleBytes, err := os.ReadFile(filepath.Join(a.repoPath(), name+".json"))
				if err != nil {
					return err
				}
				roleMeta, err := metadata.Targets().FromBytes(roleBytes)
				if err != nil {
					return err
				}
				for path := range roleMeta.Signed.Targets {
					if err := fetchTo(client, base+path, filepath.Join(a.repoPath(), path)); err != nil {
						return err
					}
				}
			}
			for _, name := range []string{"snapshot.json", "timestamp.json"} {
				if err := fetchTo(client, base+name, filepath.Join(a.repoPath(), name)); err != nil {
					return err
				}
			}
			st, err := tufrepo.Load(a.ctx(), repo.NewDirRepo(a.repoPath()), repo.NewDirRepo(a.anchorPath()))
			if err != nil {
				return err
			}
			if err := st.Verify(); err != nil {
				return err
			}
			a.render(map[string]any{"base": base, "channels": st.ChannelNames()},
				fmt.Sprintf("pulled and verified %s (%d channel(s))", base, len(st.Channels)))
			return nil
		},
	}
	cmd.Flags().StringVar(&base, "base", "", "repo base URL")
	cmd.Flags().StringVar(&anchorURL, "anchor-url", "", "anchor URL (defaults to the base origin's /.well-known/keryx/)")
	return cmd
}

// fetchToOptional fetches a file, tolerating 404 (older repos may not have
// every versioned root file).
func fetchToOptional(client *http.Client, url, path string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func fetchTo(client *http.Client, url, path string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (a *app) privateFeedCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "private-feed", Short: "Per-order capability feed engine"}
	var keyid, channel, feedURL, file, out, expires, itemsFile string
	newCmd := &cobra.Command{
		Use:   "new",
		Short: "Sign a new capability feed document",
		RunE: func(_ *cobra.Command, _ []string) error {
			items, err := readItems(file)
			if err != nil {
				return err
			}
			exp, err := parseExpires(expires)
			if err != nil {
				return err
			}
			doc, err := a.publisher().BuildPrivateDocument(a.ctx(), keyid, feedURL, channel, items, exp)
			if err != nil {
				return err
			}
			return a.writePrivate(doc, out)
		},
	}
	updateCmd := &cobra.Command{
		Use:   "update",
		Short: "Rewrite a capability feed (version+1)",
		RunE: func(_ *cobra.Command, _ []string) error {
			prev, err := readDoc(file)
			if err != nil {
				return err
			}
			items, err := readItems(itemsFile)
			if err != nil {
				return err
			}
			exp, err := parseExpires(expires)
			if err != nil {
				return err
			}
			doc, err := a.publisher().UpdatePrivateDocument(a.ctx(), keyid, prev, items, exp)
			if err != nil {
				return err
			}
			return a.writePrivate(doc, out)
		},
	}
	expireCmd := &cobra.Command{
		Use:   "expire",
		Short: "Close a capability feed (expired: true)",
		RunE: func(_ *cobra.Command, _ []string) error {
			prev, err := readDoc(file)
			if err != nil {
				return err
			}
			doc, err := a.publisher().ExpirePrivateDocument(a.ctx(), keyid, prev)
			if err != nil {
				return err
			}
			return a.writePrivate(doc, out)
		},
	}
	for _, c := range []*cobra.Command{newCmd, updateCmd, expireCmd} {
		f := c.Flags()
		f.StringVar(&keyid, "keyid", "", "engine keyid")
		f.StringVar(&channel, "channel", "tracking", "channel label")
		f.StringVar(&feedURL, "url", "", "canonical capability URL")
		f.StringVar(&file, "file", "", "document (update/expire) or item JSON array (new)")
		f.StringVar(&itemsFile, "items", "", "item JSON array (update)")
		f.StringVar(&out, "out", "feed.json", "output document")
		f.StringVar(&expires, "expires", "720h", "expiry duration or RFC3339")
		_ = c.MarkFlagRequired("keyid")
	}
	_ = newCmd.MarkFlagRequired("url")
	_ = newCmd.MarkFlagRequired("file")
	cmd.AddCommand(newCmd, updateCmd, expireCmd)
	return cmd
}

func (a *app) writePrivate(doc map[string]any, out string) error {
	data, err := feed.Encode(doc)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		return err
	}
	a.render(map[string]any{"out": out, "version": doc["version"]}, fmt.Sprintf("wrote %s", out))
	return nil
}

func readItems(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var items []map[string]any
	if err := json.Unmarshal(data, &items); err == nil {
		return items, nil
	}
	// also accept a single item object
	item, err := feed.Decode(data)
	if err != nil {
		return nil, err
	}
	return []map[string]any{item}, nil
}

func readDoc(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return feed.Decode(data)
}

func parseExpires(s string) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().UTC().Add(d).Truncate(time.Second), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid --expires %q", s)
	}
	return t.UTC(), nil
}

func decodeItem(data []byte) (map[string]any, error) { return feed.Decode(data) }

func jsonMarshal(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
