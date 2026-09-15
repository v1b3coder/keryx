package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/v1b3coder/keryx/sdk/publisher"
)

func (a *app) channelCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "channel", Short: "Channel lifecycle (master ceremony)"}
	cmd.AddCommand(
		a.channelAddCmd(),
		a.channelRemoveCmd(),
		a.channelModeCmd(),
		a.channelSetCmd(),
		a.channelListCmd(),
		a.channelKeyCmd(),
	)
	return cmd
}

func (a *app) channelAddCmd() *cobra.Command {
	var spec publisher.ChannelSpec
	var simple bool
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a channel (authored by default; --simple opts out)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec.Name = args[0]
			spec.Simple = simple
			res, err := runOrStage(cmd,
				func(dir, pass string) (publisher.Result, error) { return a.publisher().StageChannelAdd(a.ctx(), spec, dir, pass) },
				func() (publisher.Result, error) { return a.publisher().ChannelAdd(a.ctx(), spec) })
			if err != nil {
				return err
			}
			a.render(res, fmt.Sprintf("channel %s added (targets v%d)", spec.Name, res.Version))
			return nil
		},
	}
	addStageFlags(cmd)
	f := cmd.Flags()
	f.StringVar(&spec.DisplayName, "display-name", "", "display name")
	f.StringVar(&spec.Description, "description", "", "description")
	f.StringVar(&spec.KeyID, "keyid", "", "existing channel key")
	f.StringSliceVar(&spec.Authors, "author", nil, "existing author keyids (authored mode)")
	f.IntVar(&spec.Threshold, "threshold", 1, "authors-role threshold")
	f.BoolVar(&simple, "simple", false, "simple mode: no authors role")
	return cmd
}

func (a *app) channelRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := runOrStage(cmd,
				func(dir, pass string) (publisher.Result, error) {
					return a.publisher().StageChannelRemove(a.ctx(), args[0], dir, pass)
				},
				func() (publisher.Result, error) { return a.publisher().ChannelRemove(a.ctx(), args[0]) })
			if err != nil {
				return err
			}
			a.render(res, fmt.Sprintf("channel %s removed (targets v%d)", args[0], res.Version))
			return nil
		},
	}
	addStageFlags(cmd)
	return cmd
}

func (a *app) channelModeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mode <name> simple|authored",
		Short: "Master-signed channel mode change (re-signs items)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := runOrStage(cmd,
				func(dir, pass string) (publisher.Result, error) {
					return a.publisher().StageChannelMode(a.ctx(), args[0], args[1], dir, pass)
				},
				func() (publisher.Result, error) { return a.publisher().ChannelMode(a.ctx(), args[0], args[1]) })
			if err != nil {
				return err
			}
			a.render(res, fmt.Sprintf("channel %s now %s (targets v%d)", args[0], args[1], res.Version))
			return nil
		},
	}
	addStageFlags(cmd)
	return cmd
}

func (a *app) channelSetCmd() *cobra.Command {
	var channel, displayName, description string
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Update master-signed channel display metadata",
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := runOrStage(cmd,
				func(dir, pass string) (publisher.Result, error) {
					return a.publisher().StageChannelSet(a.ctx(), channel, displayName, description, dir, pass)
				},
				func() (publisher.Result, error) {
					return a.publisher().ChannelSet(a.ctx(), channel, displayName, description)
				})
			if err != nil {
				return err
			}
			a.render(res, fmt.Sprintf("channel %s updated (targets v%d)", channel, res.Version))
			return nil
		},
	}
	addStageFlags(cmd)
	cmd.Flags().StringVar(&channel, "channel", "", "channel name")
	cmd.Flags().StringVar(&displayName, "display-name", "", "display name")
	cmd.Flags().StringVar(&description, "description", "", "description")
	_ = cmd.MarkFlagRequired("channel")
	return cmd
}

func (a *app) channelListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List channels",
		RunE: func(_ *cobra.Command, _ []string) error {
			channels, err := a.publisher().Channels(a.ctx())
			if err != nil {
				return err
			}
			if a.jsonOut {
				a.render(channels, "")
				return nil
			}
			if len(channels) == 0 {
				fmt.Println("no channels")
				return nil
			}
			for _, c := range channels {
				fmt.Printf("%-16s %-8s %-9s %3d items  %s\n", c.Name, c.Mode, c.DisplayName, c.Items, c.Description)
			}
			return nil
		},
	}
}

func (a *app) channelKeyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "key", Short: "Channel key rotation / revocation"}
	cmd.AddCommand(
		func() *cobra.Command {
			c := &cobra.Command{
				Use:   "rotate <channel>",
				Short: "Add a new channel key alongside the old (overlap)",
				Args:  cobra.ExactArgs(1),
				RunE: func(cmd *cobra.Command, args []string) error {
					res, err := runOrStage(cmd,
						func(dir, pass string) (publisher.Result, error) {
							return a.publisher().StageChannelKeyRotate(a.ctx(), args[0], dir, pass)
						},
						func() (publisher.Result, error) { return a.publisher().RotateChannelKey(a.ctx(), args[0]) })
					if err != nil {
						return err
					}
					a.render(res, fmt.Sprintf("channel %s key rotated (targets v%d)", args[0], res.Version))
					return nil
				},
			}
			addStageFlags(c)
			return c
		}(),
		func() *cobra.Command {
			var keyid string
			c := &cobra.Command{
				Use:   "revoke <channel>",
				Short: "Drop a channel keyid",
				Args:  cobra.ExactArgs(1),
				RunE: func(cmd *cobra.Command, args []string) error {
					res, err := runOrStage(cmd,
						func(dir, pass string) (publisher.Result, error) {
							return a.publisher().StageChannelKeyRevoke(a.ctx(), args[0], keyid, dir, pass)
						},
						func() (publisher.Result, error) { return a.publisher().RevokeChannelKey(a.ctx(), args[0], keyid) })
					if err != nil {
						return err
					}
					a.render(res, fmt.Sprintf("channel %s key %s revoked (targets v%d)", args[0], keyid, res.Version))
					return nil
				},
			}
			addStageFlags(c)
			c.Flags().StringVar(&keyid, "keyid", "", "keyid to revoke")
			_ = c.MarkFlagRequired("keyid")
			return c
		}(),
	)
	return cmd
}
