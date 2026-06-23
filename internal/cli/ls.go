package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/kelsos/rwt/internal/detect"
	"github.com/kelsos/rwt/internal/git"
	"github.com/kelsos/rwt/internal/rotki"
	"github.com/spf13/cobra"
)

func lsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List the umbrella's worktrees",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			wts, err := git.List(cmd.Context(), rotki.HostWorktreePath())
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "WORKTREE\tBRANCH\tINSTANCE-CAPABLE?\tDIRTY?")
			for _, w := range wts {
				cap := "no"
				if detect.Capability(w.Path).Capable {
					cap = "yes"
				}
				dirty := "clean"
				if w.Dirty {
					dirty = "dirty"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", filepath.Base(w.Path), branchOrDetached(w.Branch), cap, dirty)
			}
			return tw.Flush()
		},
	}
}

func branchOrDetached(b string) string {
	if b == "" {
		return "(detached)"
	}
	return b
}
