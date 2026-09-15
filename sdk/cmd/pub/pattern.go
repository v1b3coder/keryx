package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/v1b3coder/keryx/sdk/publisher"
)

func (a *app) patternCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "pattern", Short: "Private-feed patterns (master ceremony)"}
	cmd.AddCommand(a.patternAddCmd(), a.patternRemoveCmd())
	return cmd
}

func (a *app) patternAddCmd() *cobra.Command {
	var spec publisher.PatternSpec
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Authorize a private-feed pattern",
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := runOrStage(cmd,
				func(dir, pass string) (publisher.Result, error) {
					return a.publisher().StagePatternAdd(a.ctx(), spec, dir, pass)
				},
				func() (publisher.Result, error) { return a.publisher().PatternAdd(a.ctx(), spec) })
			if err != nil {
				return err
			}
			a.render(res, fmt.Sprintf("pattern added for %s (targets v%d)", spec.Channel, res.Version))
			return nil
		},
	}
	addStageFlags(cmd)
	f := cmd.Flags()
	f.StringVar(&spec.Channel, "channel", "", "channel label")
	f.StringVar(&spec.Pattern, "pattern", "", "URL pattern (origin-exact, segment wildcard)")
	f.StringVar(&spec.KeyID, "keyid", "", "engine keyid")
	f.IntVar(&spec.Threshold, "threshold", 1, "signature threshold")
	f.StringVar(&spec.DisplayName, "display-name", "", "display name")
	f.StringVar(&spec.Purpose, "purpose", "", "purpose")
	_ = cmd.MarkFlagRequired("channel")
	_ = cmd.MarkFlagRequired("pattern")
	_ = cmd.MarkFlagRequired("keyid")
	return cmd
}

func (a *app) patternRemoveCmd() *cobra.Command {
	var channel string
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove a private-feed pattern",
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := runOrStage(cmd,
				func(dir, pass string) (publisher.Result, error) {
					return a.publisher().StagePatternRemove(a.ctx(), channel, dir, pass)
				},
				func() (publisher.Result, error) { return a.publisher().PatternRemove(a.ctx(), channel) })
			if err != nil {
				return err
			}
			a.render(res, fmt.Sprintf("pattern removed for %s (targets v%d)", channel, res.Version))
			return nil
		},
	}
	addStageFlags(cmd)
	cmd.Flags().StringVar(&channel, "channel", "", "channel label")
	_ = cmd.MarkFlagRequired("channel")
	return cmd
}

func (a *app) companyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "company", Short: "Company identity (master ceremony)"}
	cmd.AddCommand(a.companySetCmd())
	return cmd
}

func (a *app) companySetCmd() *cobra.Command {
	var name, logo, logoSHA string
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set master-signed company name and/or logo",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// a linked logo URL is fetched once and its sha256 recorded; a local
			// file is embedded as an inline data URL (spec/clients.md §2)
			if logo != "" && logoSHA == "" {
				resolved, sha, err := resolveLogo(logo)
				if err != nil {
					return err
				}
				logo, logoSHA = resolved, sha
			}
			res, err := runOrStage(cmd,
				func(dir, pass string) (publisher.Result, error) {
					return a.publisher().StageCompanySet(a.ctx(), name, logo, logoSHA, dir, pass)
				},
				func() (publisher.Result, error) { return a.publisher().CompanySet(a.ctx(), name, logo, logoSHA) })
			if err != nil {
				return err
			}
			a.render(res, fmt.Sprintf("company updated (targets v%d)", res.Version))
			return nil
		},
	}
	addStageFlags(cmd)
	cmd.Flags().StringVar(&name, "name", "", "company name")
	cmd.Flags().StringVar(&logo, "logo", "", "logo data URL or HTTPS URL")
	cmd.Flags().StringVar(&logoSHA, "logo-sha256", "", "logo_sha256 for a linked logo")
	return cmd
}
