package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kelsos/rwt/internal/git"
	"github.com/kelsos/rwt/internal/install"
	"github.com/kelsos/rwt/internal/rotki"
	"github.com/spf13/cobra"
)

func setupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup <name|.>",
		Short: "(Re)warm uv/cargo/pnpm in an existing worktree",
		Long: "Runs the env installer against an existing worktree without creating\n" +
			"one or writing any env. Use '.' for the current directory.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wt, err := resolveWorktree(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Printf("warming envs in %s...\n", wt)
			err = install.Run(cmd.Context(), wt, install.Opts{})
			applyDevFlags(wt)
			return err
		},
	}
}

// resolveWorktree turns a name or "." into an absolute worktree path. A bare
// name is looked up under the umbrella by matching the directory suffix, since
// the on-disk dir carries the branch prefix (feat-/fix-/chore-/...). "." must be
// inside a git repository and resolves to that repo's root, so running from a
// subdirectory still targets the worktree rather than the cwd.
func resolveWorktree(ctx context.Context, arg string) (string, error) {
	if arg == "." {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		root, err := git.RepoRoot(ctx, cwd)
		if err != nil {
			return "", fmt.Errorf("%q is not inside a git repository: %w", cwd, err)
		}
		return root, nil
	}
	if filepath.IsAbs(arg) {
		return arg, nil
	}
	// Try exact dir name first, then prefix-name variants across every known
	// prefix (not just the --from defaults) so --type worktrees resolve too.
	umbrella := rotki.UmbrellaRoot()
	candidates := []string{arg}
	for _, p := range rotki.Prefixes {
		candidates = append(candidates, rotki.WorktreeDir(p, arg))
	}
	for _, c := range candidates {
		path := filepath.Join(umbrella, c)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no worktree found for %q under %s", arg, umbrella)
}
