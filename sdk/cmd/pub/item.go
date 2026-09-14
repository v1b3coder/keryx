package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/v1b3coder/keryx/sdk/feed"
	"github.com/v1b3coder/keryx/sdk/keys"
)

func (a *app) itemCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "item", Short: "Author-side item signing and unpublish"}
	cmd.AddCommand(a.itemSignCmd(), a.itemUnpublishCmd())
	return cmd
}

// itemSignCmd is the author-side command: it needs only the author key and the
// draft, never the repo (design/tooling.md §3.2). Re-signing an already-signed
// file adds a signature (threshold accumulation).
func (a *app) itemSignCmd() *cobra.Command {
	var channel, file, out, keyid string
	cmd := &cobra.Command{
		Use:   "sign",
		Short: "Sign an item draft (OLPC) with an author key",
		RunE: func(_ *cobra.Command, _ []string) error {
			if file == "" {
				return fmt.Errorf("--file is required")
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			item, err := feed.Decode(data)
			if err != nil {
				return err
			}
			if err := feed.ValidateItem(item); err != nil {
				return err
			}
			store := keys.NewDirStore(a.keysPath(), a.passphrase)
			var key *keys.Key
			if keyid != "" {
				key, err = store.Get(a.ctx(), keyid)
			} else {
				key, err = store.Find(a.ctx(), keys.RoleAuthor, "")
			}
			if err != nil {
				return err
			}
			if err := feed.SignItem(item, key); err != nil {
				return err
			}
			signed, err := feed.Encode(item)
			if err != nil {
				return err
			}
			if out == "" {
				out = file
			}
			if err := os.WriteFile(out, signed, 0o644); err != nil {
				return err
			}
			a.render(map[string]any{"out": out, "id": feed.IDOf(item), "channel": channel, "keyid": key.KeyID()},
				fmt.Sprintf("signed %s with %s → %s", feed.IDOf(item), key.KeyID(), out))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&channel, "channel", "", "channel name (advisory)")
	f.StringVar(&file, "file", "", "item draft JSON")
	f.StringVar(&out, "out", "", "output path (default: overwrite --file)")
	f.StringVar(&keyid, "keyid", "", "author keyid (default: the author key in the store)")
	return cmd
}

func (a *app) itemUnpublishCmd() *cobra.Command {
	var channel, id string
	cmd := &cobra.Command{
		Use:   "unpublish",
		Short: "Remove an item's index entry (absence = unpublished)",
		RunE: func(_ *cobra.Command, _ []string) error {
			res, err := a.publisher().Unpublish(a.ctx(), channel, id)
			if err != nil {
				return err
			}
			a.render(res, fmt.Sprintf("unpublished %s from %s (channel v%d)", id, channel, res.Version))
			return nil
		},
	}
	cmd.Flags().StringVar(&channel, "channel", "", "channel name")
	cmd.Flags().StringVar(&id, "id", "", "item id")
	_ = cmd.MarkFlagRequired("channel")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}
