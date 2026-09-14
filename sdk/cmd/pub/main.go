// Command pub is the reference publisher CLI (spec/clients.md §2). All logic
// lives in the SDK; this binary owns flags, prompts, rendering and exit codes.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

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
	keysDir    string
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
	pf.StringVar(&a.keysDir, "keys", "", "key store directory (default <workspace>/keys)")
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
	if a.keysDir != "" {
		return a.keysDir
	}
	return a.cfg().KeysDir()
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
