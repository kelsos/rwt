package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/kelsos/rwt/internal/git"
	"github.com/kelsos/rwt/internal/hooks"
	"github.com/kelsos/rwt/internal/rotki"
	"github.com/spf13/cobra"
)

func hooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Install and manage rwt's pre-commit / pre-push gates",
		Long: "Wires rwt in as the umbrella's git hook runner. The hooks run the same\n" +
			"change-scoped plan as `rwt check`: the fast tier on commit against the\n" +
			"index, the slower gates on push against the PR diff.\n\n" +
			"Install is umbrella-wide because it has to be: core.hooksPath lives in\n" +
			"the repository-local config, which every linked worktree shares. Use\n" +
			"`rwt hooks off` to make them inert in one worktree.",
		// The hooks run inside git, where refusing over an unconfigured umbrella
		// would block a commit for a reason that has nothing to do with the
		// commit. Every subcommand here works from the worktree it is run in.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
	}
	cmd.AddCommand(
		hooksInstallCmd(),
		hooksUninstallCmd(),
		hooksStatusCmd(),
		hooksToggleCmd("off", true),
		hooksToggleCmd("on", false),
		hooksRunCmd(),
	)
	return cmd
}

func hooksInstallCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Point core.hooksPath at rwt's hooks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			wt, err := hostWorktree(ctx)
			if err != nil {
				return err
			}
			before, _ := hooks.Status(ctx, wt)
			state, err := hooks.Install(ctx, wt, force)
			if err != nil {
				return err
			}
			// Reported only once the install has actually happened, so a refusal
			// is not preceded by a note describing what it declined to do.
			if before.HooksPath != "" && before.HooksPath != before.Dir {
				if before.PreviousBroken {
					fmt.Printf("note: core.hooksPath was %s, which does not exist,\n"+
						"      so git has been running no hooks at all in this umbrella.\n",
						before.HooksPath)
				} else {
					fmt.Printf("note: took over core.hooksPath from %s; rwt chains to it\n"+
						"      after its own checks pass.\n", before.HooksPath)
				}
			}
			fmt.Printf("installed %v in %s\n", hooks.Stages, state.Dir)
			fmt.Println("every worktree in the umbrella is now gated; opt one out with `rwt hooks off`")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"take over a core.hooksPath that points at a directory which exists")
	return cmd
}

func hooksUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Restore the core.hooksPath rwt displaced",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			wt, err := hostWorktree(cmd.Context())
			if err != nil {
				return err
			}
			if err := hooks.Uninstall(cmd.Context(), wt); err != nil {
				return err
			}
			fmt.Println("rwt hooks uninstalled")
			return nil
		},
	}
}

func hooksStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show what is installed and which worktrees are opted out",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			wt, err := hostWorktree(ctx)
			if err != nil {
				return err
			}
			state, err := hooks.Status(ctx, wt)
			if err != nil {
				return err
			}
			fmt.Printf("core.hooksPath: %s\n", orNone(state.HooksPath))
			fmt.Printf("rwt hooks dir:  %s\n", state.Dir)
			fmt.Printf("installed:      %v\n", state.Installed)
			if state.Previous != "" {
				fmt.Printf("displaced:      %s\n", state.Previous)
			}
			if state.PreviousBroken {
				if state.Installed {
					fmt.Println("note:           that path does not exist, so nothing is chained after rwt's checks")
				} else {
					fmt.Println("warning:        that path does not exist, so git is running no hooks at all")
				}
			}
			if !state.Installed {
				fmt.Println("\ninstall with: rwt hooks install")
				return nil
			}
			fmt.Println("\nper-worktree:")
			for _, w := range siblingWorktrees(ctx, wt) {
				mark := "on"
				if hooks.OptedOut(ctx, w) {
					mark = "off"
				}
				fmt.Printf("  %-4s %s\n", mark, filepath.Base(w))
			}
			return nil
		},
	}
}

func hooksToggleCmd(name string, off bool) *cobra.Command {
	short := "Re-enable the hooks in one worktree"
	if off {
		short = "Make the hooks inert in one worktree"
	}
	return &cobra.Command{
		Use:               name + " [name|.]",
		Short:             short,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeWorktreeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := "."
			if len(args) == 1 {
				arg = args[0]
			}
			wt, err := resolveWorktree(cmd.Context(), arg)
			if err != nil {
				return err
			}
			if err := hooks.SetOptOut(cmd.Context(), wt, off); err != nil {
				return err
			}
			fmt.Printf("hooks %s for %s\n", name, filepath.Base(wt))
			return nil
		},
	}
}

// hooksRunCmd is what the installed shim execs. Hidden: it is an entry point for
// git, not a command to type.
func hooksRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "run <" + stagePreCommit + "|" + stagePrePush + ">",
		Short:  "Run a stage's checks (called by the installed hook)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			stage := args[0]
			if stage != stagePreCommit && stage != stagePrePush {
				return fmt.Errorf("unknown stage %q", stage)
			}
			wt, err := resolveWorktree(ctx, ".")
			if err != nil {
				return err
			}
			if hooks.OptedOut(ctx, wt) {
				return chain(ctx, wt, stage)
			}
			opts := checkOpts{quiet: true, full: stage == stagePrePush && hookDepth()}
			if err := runCheck(ctx, wt, stage, opts); err != nil {
				return err
			}
			return chain(ctx, wt, stage)
		},
	}
}

// chain hands off to the hook rwt displaced, replacing this process so the
// other mechanism sees git's own stdin and exit status untouched.
func chain(ctx context.Context, wt, stage string) error {
	path := hooks.Chained(ctx, wt, stage)
	if path == "" {
		return nil
	}
	return syscall.Exec(path, []string{path}, os.Environ())
}

// hostWorktree is where the umbrella-wide git config lives. Prefer the worktree
// the user is standing in, so `rwt hooks install` works from anywhere inside the
// repo even with no umbrella configured; fall back to the configured host.
func hostWorktree(ctx context.Context) (string, error) {
	if wt, err := resolveWorktree(ctx, "."); err == nil {
		return wt, nil
	}
	if rotki.UmbrellaRoot() == "" {
		return "", fmt.Errorf("not inside a git repository, and no umbrella configured:\n" +
			"  run this from a rotki worktree, or set one with `rwt config path <dir>`")
	}
	return rotki.HostWorktreePath(), nil
}

// siblingWorktrees lists every worktree sharing this one's repository. Derived
// from the repo rather than the configured umbrella, so status describes the
// worktrees the install it just reported on actually covers. Best-effort: it
// degrades to omitting the table rather than failing.
func siblingWorktrees(ctx context.Context, worktree string) []string {
	wts, err := git.List(ctx, worktree)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(wts))
	for _, w := range wts {
		out = append(out, w.Path)
	}
	return out
}

func orNone(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}
