package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kelsos/rwt/internal/config"
	"github.com/kelsos/rwt/internal/git"
	"github.com/kelsos/rwt/internal/install"
	"github.com/kelsos/rwt/internal/rotki"
	"github.com/spf13/cobra"
)

func setupCmd() *cobra.Command {
	var (
		only []string
		demo string
		lint bool
	)
	cmd := &cobra.Command{
		Use:   "setup <name|.>",
		Short: "(Re)warm uv/cargo/pnpm in an existing worktree",
		Long: "Runs the env installer against an existing worktree without creating\n" +
			"one or writing any env. Use '.' for the current directory.\n\n" +
			"--only narrows the run to one ecosystem, which is how you rebuild the\n" +
			"Rust services after touching them: `rwt setup . --only cargo` runs the\n" +
			"warm build with the same uplift-slot and symlink bookkeeping a full\n" +
			"setup does, so the dev launcher keeps finding target/debug/<name>\n" +
			"instead of falling back to `cargo run`. A narrowed run skips the dev\n" +
			"flags, which a full setup still writes — except an explicit --demo,\n" +
			"which is always honored.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorktreeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if demo != "" {
				mode, err := config.ParseDemo(demo)
				if err != nil {
					return err
				}
				demo = mode
			}
			wt, err := resolveWorktree(ctx, args[0])
			if err != nil {
				return err
			}
			steps := install.DefaultSteps(wt)
			if lint {
				steps = install.StepsWithLint(wt)
			}
			opts := install.Opts{}
			if len(only) > 0 {
				narrowed, err := install.Only(steps, only)
				if err != nil {
					return err
				}
				opts.Steps = narrowed
			} else if lint {
				opts.Steps = steps
			}
			wireCargoCache(ctx, wt)
			fmt.Printf("warming envs in %s...\n", wt)
			err = install.Run(ctx, wt, opts)
			switch {
			case len(only) == 0:
				applyDevFlags(ctx, wt, demo)
			case demo != "":
				applyDemoOnly(ctx, wt, demo)
			}
			return err
		},
	}
	cmd.Flags().StringSliceVar(&only, "only", nil,
		"limit the run to these ecosystems: "+strings.Join(install.EcoSelectors(), ", "))
	cmd.Flags().BoolVar(&lint, "lint", false,
		"also install the Python lint group (ruff/mypy/pylint), which `rwt check` needs")
	_ = cmd.RegisterFlagCompletionFunc("only",
		func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return install.EcoSelectors(), cobra.ShellCompDirectiveNoFileComp
		})
	registerDemoFlag(cmd, &demo)
	return cmd
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
