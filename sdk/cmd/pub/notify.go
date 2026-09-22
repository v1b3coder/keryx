package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/v1b3coder/keryx/sdk/keys"
	"github.com/v1b3coder/keryx/sdk/notify"
)

// notifyCmd is the publisher-side wake-up step: after `pub publish` put the
// content on the static repo, tell the relay to wake the devices
// (relay/SPECIFICATION.md §5.1). The channel key signs; the relay never
// holds publisher keys.
//
// Before waking anyone it waits for the deployed repo to actually serve the
// metadata `pub publish` just wrote: a static host's CDN edge can serve the
// previous files for a while after a deploy, and a device woken during that
// window syncs one publish behind. The wait polls the repository itself, so it
// does not depend on a guessed platform-specific delay; `--no-wait` skips it
// (e.g. when the deploy already settled or the repo is local).
func (a *app) notifyCmd() *cobra.Command {
	var channel, company, relay, repoBase string
	var seq int64
	var noWait bool
	var waitTimeout time.Duration
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Sign a wake-up and publish it to the relay",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := a.requireRole("operator", "ci"); err != nil {
				return err
			}
			if company == "" {
				company = a.cfg().Origin
			}
			if company == "" {
				return fmt.Errorf("--company is required (the join origin)")
			}
			if relay == "" {
				return fmt.Errorf("--relay is required (the relay base URL)")
			}
			id, err := canonicalCompany(company)
			if err != nil {
				return err
			}
			client := &http.Client{Timeout: 30 * time.Second}
			if !noWait {
				base := repoBase
				if base == "" {
					base = a.cfg().RepoBase
				}
				if base == "" {
					base = "https://" + id + "/keryx/"
				}
				want, err := notify.LocalVersions(a.repoPath(), channel)
				if err != nil {
					return fmt.Errorf("local repo (publish first, or pass --no-wait): %w", err)
				}
				got, err := notify.WaitForDeploy(a.ctx(), client, base, channel, want, waitTimeout)
				if err != nil {
					return err
				}
				fmt.Printf("deployed repo is at %s (publish is live)\n", got)
			}
			if seq <= 0 {
				seq = time.Now().Unix()
			}
			store := keys.NewDirStore(a.keysPath(), a.passphrase)
			key, err := keys.Resolve(a.ctx(), store, keys.RoleChannel, channel, "")
			if err != nil {
				return err
			}
			res, err := notify.Publish(a.ctx(), relay, id, channel, key, seq, client)
			if err != nil {
				return err
			}
			a.render(res, fmt.Sprintf("notified %s (channel %s, seq %d)\n  topic:     %s\n  providers: fcm=%s webpush sent=%d failed=%d dead=%d",
				id, channel, seq, res.Topic, res.Providers.FCM,
				res.Providers.WebPush.Sent, res.Providers.WebPush.Failed, res.Providers.WebPush.Dead))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&channel, "channel", "security", "channel name")
	f.StringVar(&company, "company", "", "company_id (the join origin; default config origin)")
	f.StringVar(&relay, "relay", "", "relay base URL (e.g. https://relay.example)")
	f.StringVar(&repoBase, "repo-base", "", "deployed repo base URL (default config repo_base, else https://<company>/keryx/)")
	f.BoolVar(&noWait, "no-wait", false, "publish the wake-up without waiting for the deploy to propagate")
	f.DurationVar(&waitTimeout, "wait-timeout", 5*time.Minute, "how long to wait for the deploy to propagate")
	f.Int64Var(&seq, "seq", 0, "wake-up seq (default: now)")
	return cmd
}

// canonicalCompany returns the canonical company_id for a join origin
// (relay/SPECIFICATION.md §2): the lowercase host, no scheme, port or path.
// A bare company_id (a host) is accepted as-is.
func canonicalCompany(origin string) (string, error) {
	if !strings.Contains(origin, "://") {
		if origin == "" || strings.ContainsAny(origin, "/: ") {
			return "", fmt.Errorf("company: %q is not a company_id or URL", origin)
		}
		return strings.ToLower(origin), nil
	}
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("company: %q is not a URL", origin)
	}
	return u.Hostname(), nil
}
