package main

import (
	"fmt"
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
// holds publisher keys. Call `pub refresh-timestamp` (or the relay's
// `/v1/companies/{id}/refresh`) first when metadata changed.
func (a *app) notifyCmd() *cobra.Command {
	var channel, company, relay string
	var seq int64
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
			if seq <= 0 {
				seq = time.Now().Unix()
			}
			store := keys.NewDirStore(a.keysPath(), a.passphrase)
			key, err := keys.Resolve(a.ctx(), store, keys.RoleChannel, channel, "")
			if err != nil {
				return err
			}
			res, err := notify.Publish(a.ctx(), relay, id, channel, key, seq, nil)
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
