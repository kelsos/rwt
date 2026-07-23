package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/kelsos/rwt/internal/git"
	"github.com/kelsos/rwt/internal/rotki"
	"github.com/spf13/cobra"
)

// goCmd resolves a worktree by name and prints a `cd <path>` line. A child
// process cannot change its parent shell's cwd, so the line is meant to be
// eval'd (mirroring `rwt new --here`):
//
//	eval "$(rwt go login-crash)"
//
// or wrapped in a shell function so `rwt go x` cds directly.
func goCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "go <name>",
		Short: "Print a `cd` into a worktree (use: eval \"$(rwt go <name>)\")",
		Long: "Resolves a worktree by bare name (the prefix is optional) and prints\n" +
			"`cd <path>` on stdout. A binary can't change its parent shell's cwd, so\n" +
			"eval the output:\n\n" +
			"  eval \"$(rwt go login-crash)\"\n\n" +
			"or wrap it in a shell function so `rwt go x` cds for you.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorktreeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGo(cmd.Context(), cmd.OutOrStdout(), args[0])
		},
	}
}

func runGo(ctx context.Context, out io.Writer, name string) error {
	wt, err := resolveWorktree(ctx, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "cd %s\n", wt)
	return nil
}

// completeWorktreeNames offers the umbrella's worktree directory names for shell
// completion. Best-effort: returns nothing (rather than an error) when the
// umbrella is unset or git fails, so completion stays quiet instead of noisy.
func completeWorktreeNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if _, _, ok := rotki.Umbrella(); !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	wts, err := git.List(context.Background(), rotki.HostWorktreePath())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, w := range wts {
		names = append(names, filepath.Base(w.Path))
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
