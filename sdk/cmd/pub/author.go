package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/v1b3coder/keryx/sdk/publisher"
)

func (a *app) authorCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "author", Short: "Authors role (authored channels)"}
	cmd.AddCommand(a.authorAddCmd(), a.authorRevokeCmd(), a.authorListCmd())
	return cmd
}

func (a *app) authorAddCmd() *cobra.Command {
	var channel, keyid string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add an author keyid (master ceremony)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.requireRole("operator"); err != nil {
				return err
			}
			res, err := runOrStage(cmd,
				func(dir, pass string) (publisher.Result, error) {
					return a.publisher().StageAuthorAdd(a.ctx(), channel, keyid, dir, pass)
				},
				func() (publisher.Result, error) { return a.publisher().AuthorAdd(a.ctx(), channel, keyid) })
			if err != nil {
				return err
			}
			a.render(res, fmt.Sprintf("author %s added to %s (targets v%d)", keyid, channel, res.Version))
			return nil
		},
	}
	addStageFlags(cmd)
	cmd.Flags().StringVar(&channel, "channel", "", "channel name")
	cmd.Flags().StringVar(&keyid, "keyid", "", "author keyid")
	_ = cmd.MarkFlagRequired("channel")
	_ = cmd.MarkFlagRequired("keyid")
	return cmd
}

func (a *app) authorRevokeCmd() *cobra.Command {
	var channel, keyid string
	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke an author keyid (refuses the last author)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.requireRole("operator"); err != nil {
				return err
			}
			res, err := runOrStage(cmd,
				func(dir, pass string) (publisher.Result, error) {
					return a.publisher().StageAuthorRevoke(a.ctx(), channel, keyid, dir, pass)
				},
				func() (publisher.Result, error) { return a.publisher().AuthorRevoke(a.ctx(), channel, keyid) })
			if err != nil {
				return err
			}
			a.render(res, fmt.Sprintf("author %s revoked from %s (targets v%d)", keyid, channel, res.Version))
			return nil
		},
	}
	addStageFlags(cmd)
	cmd.Flags().StringVar(&channel, "channel", "", "channel name")
	cmd.Flags().StringVar(&keyid, "keyid", "", "author keyid")
	_ = cmd.MarkFlagRequired("channel")
	_ = cmd.MarkFlagRequired("keyid")
	return cmd
}

func (a *app) authorListCmd() *cobra.Command {
	var channel string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List a channel's author keyids",
		RunE: func(_ *cobra.Command, _ []string) error {
			keyids, err := a.publisher().AuthorList(a.ctx(), channel)
			if err != nil {
				return err
			}
			if a.jsonOut {
				a.render(map[string]any{"channel": channel, "keyids": keyids}, "")
				return nil
			}
			for _, kid := range keyids {
				fmt.Println(kid)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&channel, "channel", "", "channel name")
	_ = cmd.MarkFlagRequired("channel")
	return cmd
}
