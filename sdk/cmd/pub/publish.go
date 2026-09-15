package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/v1b3coder/keryx/sdk/config"
	"github.com/v1b3coder/keryx/sdk/publisher"
)

func (a *app) initCmd() *cobra.Command {
	var domain, name, base, logo, mode string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize a full-mode repository (master + ops keys)",
		RunE: func(_ *cobra.Command, _ []string) error {
			if domain == "" {
				return fmt.Errorf("--domain is required")
			}
			if err := a.requireRole("operator"); err != nil {
				return err
			}
			if err := a.checkKeystoreOutside(a.keysPath()); err != nil {
				return err
			}
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if mode != "" && mode != "full" {
				return fmt.Errorf("--mode %q: lite mode is Phase 2 (only full is supported)", mode)
			}
			if base == "" {
				base = "https://" + domain + "/keryx"
			}
			logoValue, logoSHA, err := resolveLogo(logo)
			if err != nil {
				return err
			}
			cfg := config.Default(a.workspace)
			cfg.Role = "operator"
			cfg.RepoBase = base
			cfg.Origin = domain
			if !strings.HasPrefix(cfg.Origin, "http://") && !strings.HasPrefix(cfg.Origin, "https://") {
				cfg.Origin = "https://" + cfg.Origin
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			res, err := a.publisher().Init(a.ctx(), publisher.InitParams{
				RepoBase: base, CompanyName: name, Logo: logoValue, LogoSHA256: logoSHA,
			})
			if err != nil {
				return err
			}
			a.render(map[string]any{
				"company": name, "repo_base": base,
				"repo": a.repoPath(), "anchor": a.anchorPath(), "keys": a.keysPath(),
			}, fmt.Sprintf("initialized %s\n  repo base: %s\n  repo:      %s\n  anchor:    %s\n  keys:      %s\n\nBack up the keys in %s once — they are the trust anchor.",
				name, base, a.repoPath(), a.anchorPath(), a.keysPath(), a.keysPath()))
			_ = res
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&domain, "domain", "", "join origin (company.example)")
	f.StringVar(&name, "name", "", "company name")
	f.StringVar(&base, "base", "", "repo base URL (default https://<domain>/keryx)")
	f.StringVar(&logo, "logo", "", "logo URL or local file")
	f.StringVar(&mode, "mode", "full", "full (lite is Phase 2)")
	return cmd
}

// resolveLogo returns the custom.logo value and, for a linked URL, its sha256.
// A URL is stored linked and fetched once; a local file is embedded as a data URL.
func resolveLogo(logo string) (string, string, error) {
	if logo == "" {
		return "", "", nil
	}
	if strings.HasPrefix(logo, "https://") {
		client := &http.Client{}
		resp, err := client.Get(logo)
		if err != nil {
			return "", "", fmt.Errorf("fetch logo: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", "", fmt.Errorf("fetch logo: HTTP %d", resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return "", "", err
		}
		sum := sha256.Sum256(data)
		return logo, hex.EncodeToString(sum[:]), nil
	}
	if strings.HasPrefix(logo, "data:") {
		return logo, "", nil
	}
	data, err := os.ReadFile(logo)
	if err != nil {
		return "", "", fmt.Errorf("read logo: %w", err)
	}
	if len(data) > 64<<10 {
		return "", "", fmt.Errorf("logo file is %d bytes; inline logos should be ≤ 64 KB", len(data))
	}
	mimeType := mime.TypeByExtension(filepath.Ext(logo))
	if mimeType == "" {
		mimeType = "image/png"
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), "", nil
}

func (a *app) publishCmd() *cobra.Command {
	var channel, file string
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Publish a signed item to a channel",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := a.requireRole("ci", "operator"); err != nil {
				return err
			}
			if file == "" {
				return fmt.Errorf("--file is required")
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			item, err := decodeItem(data)
			if err != nil {
				return err
			}
			res, err := a.publisher().Publish(a.ctx(), publisher.PublishParams{Channel: channel, Item: item})
			if err != nil {
				return err
			}
			a.render(res, fmt.Sprintf("published to %s (channel v%d)", channel, res.Version))
			return nil
		},
	}
	cmd.Flags().StringVar(&channel, "channel", "", "channel name")
	cmd.Flags().StringVar(&file, "file", "", "signed item JSON")
	_ = cmd.MarkFlagRequired("channel")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func (a *app) refreshCmd() *cobra.Command {
	var expires string
	cmd := &cobra.Command{
		Use:   "refresh-timestamp",
		Short: "Cron line: re-sign timestamp with a fresh expiry",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := a.requireRole("ci", "operator"); err != nil {
				return err
			}
			var res publisher.Result
			var err error
			if expires != "" {
				d, derr := time.ParseDuration(expires)
				if derr != nil {
					return fmt.Errorf("invalid --expires %q (use a duration like 48h)", expires)
				}
				res, err = a.publisher().RefreshTimestampExpires(a.ctx(), d)
			} else {
				res, err = a.publisher().RefreshTimestamp(a.ctx())
			}
			if err != nil {
				return err
			}
			a.render(res, fmt.Sprintf("timestamp refreshed (v%d)", res.Version))
			return nil
		},
	}
	cmd.Flags().StringVar(&expires, "expires", "", "timestamp lifetime (e.g. 48h; default config value)")
	return cmd
}

func (a *app) rotateRootCmd() *cobra.Command {
	var announceNext bool
	cmd := &cobra.Command{
		Use:   "rotate-root",
		Short: "Root ceremony: root v+1 at the well-known anchor only",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := a.requireRole("operator"); err != nil {
				return err
			}
			res, err := a.publisher().RotateRoot(a.ctx(), announceNext)
			if err != nil {
				return err
			}
			a.render(res, fmt.Sprintf("root rotated to v%d (anchor only)", res.Version))
			return nil
		},
	}
	cmd.Flags().BoolVar(&announceNext, "announce-next-key", false, "publish next_key ahead of the rotation")
	return cmd
}

func (a *app) validateCmd() *cobra.Command {
	var strict bool
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Verify the full repository chain",
		RunE: func(_ *cobra.Command, _ []string) error {
			var res publisher.Result
			var err error
			if strict {
				res, err = a.publisher().ValidateStrict(a.ctx())
			} else {
				res, err = a.publisher().Validate(a.ctx())
			}
			if err != nil {
				return err
			}
			a.render(res, fmt.Sprintf("OK: %s — %d channel(s), targets v%d", res.Company, len(res.Channels), res.Version))
			return nil
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false, "also reject expired metadata")
	return cmd
}
