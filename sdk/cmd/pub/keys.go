package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/v1b3coder/keryx/sdk/keys"
)

func (a *app) keysCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "keys", Short: "Manage software keys"}
	cmd.AddCommand(
		a.keysListCmd(),
		a.keysGenerateCmd(),
		a.keysExportCmd(),
		a.keysImportCmd(),
	)
	return cmd
}

func (a *app) keysListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List keys in the role-scoped store",
		RunE: func(_ *cobra.Command, _ []string) error {
			infos, err := keys.NewDirStore(a.keysPath(), a.passphrase).List(a.ctx())
			if err != nil {
				return err
			}
			if a.jsonOut {
				a.render(infos, "")
				return nil
			}
			if len(infos) == 0 {
				fmt.Println("no keys")
				return nil
			}
			for _, info := range infos {
				fmt.Printf("%-24s %-8s %s\n", info.Name, info.Role, info.KeyID)
			}
			return nil
		},
	}
}

func (a *app) keysGenerateCmd() *cobra.Command {
	var role string
	cmd := &cobra.Command{
		Use:   "generate <name>",
		Short: "Generate a new Ed25519 key",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			r := keys.Role(role)
			switch r {
			case keys.RoleMaster, keys.RoleOps, keys.RoleChannel, keys.RoleAuthor, keys.RoleEngine:
			default:
				return fmt.Errorf("unknown role %q", role)
			}
			k, err := keys.Generate(r, args[0])
			if err != nil {
				return err
			}
			store := keys.NewDirStore(a.keysPath(), a.passphrase)
			if err := store.Add(a.ctx(), k); err != nil {
				return err
			}
			a.render(keys.Info{Name: k.Name, Role: k.Role, KeyID: k.KeyID()}, fmt.Sprintf("generated %s key %s (%s)", role, args[0], k.KeyID()))
			return nil
		},
	}
	cmd.Flags().StringVar(&role, "role", "channel", "master|ops|channel|author|engine")
	return cmd
}

func (a *app) keysExportCmd() *cobra.Command {
	var out string
	var public bool
	var keyids []string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export role-tagged keys as a bundle",
		RunE: func(_ *cobra.Command, _ []string) error {
			if out == "" {
				return fmt.Errorf("--out is required")
			}
			store := keys.NewDirStore(a.keysPath(), a.passphrase)
			var data []byte
			var err error
			if public {
				infos, lerr := store.List(a.ctx())
				if lerr != nil {
					return lerr
				}
				bundle := keys.ExportBundle{Version: 1}
				for _, info := range infos {
					if len(keyids) > 0 && !contains(keyids, info.KeyID) && !contains(keyids, info.Name) {
						continue
					}
					k, gerr := store.Get(a.ctx(), info.KeyID)
					if gerr != nil {
						return gerr
					}
					bundle.Keys = append(bundle.Keys, keys.KeyFile{
						Name: k.Name, Role: k.Role,
						Public: hex.EncodeToString(k.Public()),
						KeyID:  k.KeyID(),
					})
				}
				data, err = jsonMarshal(bundle)
			} else {
				data, err = keys.Export(a.ctx(), store, keyids, a.passphrase)
			}
			if err != nil {
				return err
			}
			if err := os.WriteFile(out, data, 0o600); err != nil {
				return err
			}
			a.render(map[string]any{"out": out, "bytes": len(data)}, fmt.Sprintf("wrote %s (%d bytes)", out, len(data)))
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "output bundle path")
	cmd.Flags().StringSliceVar(&keyids, "keyid", nil, "limit to these keyids/names")
	cmd.Flags().BoolVar(&public, "public", false, "export public keys only")
	return cmd
}

func (a *app) keysImportCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a role-tagged key bundle",
		RunE: func(_ *cobra.Command, _ []string) error {
			if file == "" {
				return fmt.Errorf("--file is required")
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			store := keys.NewDirStore(a.keysPath(), a.passphrase)
			infos, err := keys.Import(a.ctx(), store, data)
			if err != nil {
				return err
			}
			a.render(infos, fmt.Sprintf("imported %d key(s)", len(infos)))
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "bundle path")
	return cmd
}
