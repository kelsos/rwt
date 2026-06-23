package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kelsos/rwt/internal/git"
	"github.com/kelsos/rwt/internal/install"
	"github.com/kelsos/rwt/internal/rotki"
	"github.com/spf13/cobra"
)

func refreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh",
		Short: "Fetch upstream and ff-only every long-lived base, warming cold ones",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			host := rotki.HostWorktreePath()

			fmt.Printf("fetching %s...\n", rotki.Upstream)
			if err := git.Fetch(ctx, host, rotki.Upstream); err != nil {
				return err
			}

			for _, base := range rotki.LongLived {
				wt := filepath.Join(rotki.UmbrellaRoot(), base)
				if _, err := os.Stat(wt); err != nil {
					fmt.Printf("skip %s: worktree not present\n", base)
					continue
				}

				// Re-assert dev flags on every present base, independent of the
				// ff/warm outcome below: this is what keeps VITE_PERSIST_STORE in
				// place so a post-refresh restart doesn't log you out.
				applyDevFlags(wt)

				dirty, _ := git.IsDirty(ctx, wt)
				if dirty {
					fmt.Printf("skip %s: dirty worktree\n", base)
					continue
				}
				ref := rotki.Upstream + "/" + base
				if err := git.MergeFFOnly(ctx, wt, ref); err != nil {
					fmt.Printf("skip %s: not fast-forwardable (%v)\n", base, err)
					continue
				}
				fmt.Printf("%s: fast-forwarded to %s\n", base, ref)

				// Seed cold bases so bugfixes/master come up warm.
				if install.NeedsWarm(wt) {
					fmt.Printf("%s: cold env, warming...\n", base)
					if err := install.Run(ctx, wt, install.Opts{}); err != nil {
						fmt.Fprintf(os.Stderr, "warning: %v\n", err)
					}
				}
			}
			return nil
		},
	}
}
