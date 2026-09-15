// Command pub is the reference publisher CLI (spec/clients.md §2). All logic
// lives in the SDK; this binary owns flags, prompts, rendering and exit codes.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/v1b3coder/keryx/sdk/config"
	"github.com/v1b3coder/keryx/sdk/keys"
	"github.com/v1b3coder/keryx/sdk/publisher"
	"github.com/v1b3coder/keryx/sdk/repo"
)

type app struct {
	workspace  string
	repoDir    string
	anchorDir  string
	keystore   string
	passphrase string
	jsonOut    bool
}

func main() {
	a := &app{}
	root := &cobra.Command{
		Use:           "pub",
		Short:         "Keryx publisher tooling (SDK reference CLI)",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	pf := root.PersistentFlags()
	pf.StringVar(&a.workspace, "workspace", ".keryx", "workspace directory (repo/, anchor/, keys/)")
	pf.StringVar(&a.repoDir, "repo", "", "repo base directory (default <workspace>/repo)")
	pf.StringVar(&a.anchorDir, "anchor", "", "well-known anchor directory (default <workspace>/anchor)")
	pf.StringVar(&a.keystore, "keystore", "", "key store directory (default: $KERYX_KEYSTORE or ~/.local/share/keryx/keys)")
	pf.StringVar(&a.passphrase, "passphrase", os.Getenv("KERYX_PASSPHRASE"), "keystore passphrase (or KERYX_PASSPHRASE)")
	pf.BoolVar(&a.jsonOut, "json", false, "machine-readable JSON output")

	root.AddCommand(
		a.initCmd(),
		a.keysCmd(),
		a.channelCmd(),
		a.authorCmd(),
		a.patternCmd(),
		a.companyCmd(),
		a.itemCmd(),
		a.publishCmd(),
		a.refreshCmd(),
		a.rotateRootCmd(),
		a.validateCmd(),
		a.joinCmd(),
		a.qrCmd(),
		a.deployCmd(),
		a.pullCmd(),
		a.privateFeedCmd(),
		a.ceremonyCmd(),
		a.rotateCmd(),
		a.revokeCmd(),
	)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// cfg resolves the workspace layout (flags override the saved config).
func (a *app) cfg() config.Config {
	c, err := config.Load(a.workspace)
	if err != nil {
		return config.Default(a.workspace)
	}
	return c
}

func (a *app) repoPath() string {
	if a.repoDir != "" {
		return a.repoDir
	}
	return a.cfg().RepoDir()
}

func (a *app) anchorPath() string {
	if a.anchorDir != "" {
		return a.anchorDir
	}
	return a.cfg().AnchorDir()
}

func (a *app) keysPath() string {
	if a.keystore != "" {
		return a.keystore
	}
	return a.cfg().KeysDir()
}

// requireRole fails fast when the workspace role cannot run the command
// (design/tooling.md §2). The default role is operator.
func (a *app) requireRole(roles ...string) error {
	role := a.cfg().Role
	for _, r := range roles {
		if role == r {
			return nil
		}
	}
	return fmt.Errorf("workspace role %q cannot run this command (needs %s)", role, strings.Join(roles, " or "))
}

// checkKeystoreOutside refuses a key store inside the workspace, so private
// seeds can never be committed with the repo (design/tooling.md §3.2).
func (a *app) checkKeystoreOutside(dir string) error {
	ws, err := filepath.Abs(a.workspace)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if abs == ws || strings.HasPrefix(abs, ws+string(filepath.Separator)) {
		return fmt.Errorf("keystore %s is inside the workspace %s; keep keys outside the repo (use --keystore)", dir, a.workspace)
	}
	return nil
}

func (a *app) publisher() *publisher.Publisher {
	ks := keys.NewDirStore(a.keysPath(), a.passphrase)
	return publisher.New(repo.NewDirRepo(a.repoPath()), repo.NewDirRepo(a.anchorPath()), ks)
}

func (a *app) render(v any, human string) {
	if a.jsonOut {
		data, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(data))
		return
	}
	if human != "" {
		fmt.Println(human)
	}
}

func (a *app) ctx() context.Context { return context.Background() }

// addStageFlags adds the two-step ceremony flags to a master-ceremony command.
func addStageFlags(cmd *cobra.Command) {
	cmd.Flags().String("stage", "", "stage the ceremony to this directory instead of applying it")
	cmd.Flags().String("bundle-passphrase", os.Getenv("KERYX_BUNDLE_PASSPHRASE"), "encrypt the bundle keys (or KERYX_BUNDLE_PASSPHRASE)")
}

// staged returns the --stage directory (empty = single-step).
func staged(cmd *cobra.Command) (string, string) {
	dir, _ := cmd.Flags().GetString("stage")
	pass, _ := cmd.Flags().GetString("bundle-passphrase")
	return dir, pass
}

// runOrStage stages the ceremony when --stage is set, otherwise applies it
// locally (design/tooling.md §3.5).
func runOrStage(cmd *cobra.Command, stage func(dir, pass string) (publisher.Result, error), run func() (publisher.Result, error)) (publisher.Result, error) {
	dir, pass := staged(cmd)
	if dir != "" {
		return stage(dir, pass)
	}
	return run()
}

func (a *app) ceremonyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "ceremony", Short: "Finish a staged ceremony bundle"}
	var bundle, passphrase string
	apply := &cobra.Command{
		Use:   "apply",
		Short: "Verify a bundle and finish it on this machine",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := a.requireRole("ci", "operator"); err != nil {
				return err
			}
			if bundle == "" {
				return fmt.Errorf("--bundle is required")
			}
			res, err := a.publisher().Apply(a.ctx(), bundle, passphrase)
			if err != nil {
				return err
			}
			a.render(res, fmt.Sprintf("ceremony applied (targets v%d)", res.Version))
			return nil
		},
	}
	apply.Flags().StringVar(&bundle, "bundle", "", "bundle directory")
	apply.Flags().StringVar(&passphrase, "bundle-passphrase", os.Getenv("KERYX_BUNDLE_PASSPHRASE"), "bundle passphrase (or KERYX_BUNDLE_PASSPHRASE)")
	_ = apply.MarkFlagRequired("bundle")
	cmd.AddCommand(apply)
	return cmd
}
