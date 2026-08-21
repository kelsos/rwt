package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/kelsos/rwt/internal/config"
	"github.com/kelsos/rwt/internal/detect"
	"github.com/kelsos/rwt/internal/envfile"
	"github.com/kelsos/rwt/internal/git"
	"github.com/kelsos/rwt/internal/install"
	"github.com/kelsos/rwt/internal/rotki"
	"github.com/spf13/cobra"
)

// installRun is the env warmer, a package var so tests can stub the heavy
// pnpm/uv/cargo shell-outs.
var installRun = install.Run

func newCmd() *cobra.Command {
	var (
		from            string
		typ             string
		idea            bool
		forceManagedEnv bool
		here            bool
		demo            string
	)
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Create a worktree, warm its envs, and enable instance mode",
		Long: "Creates ../<prefix>-<name> off upstream/<base>, warms uv/cargo/pnpm,\n" +
			"and (if the checkout supports it) appends INSTANCE_NAME for dev:web\n" +
			"instance mode. Idempotent: re-run to resume after a failed step.\n\n" +
			"<prefix> defaults to the --from base (develop->feat, bugfixes/master->fix);\n" +
			"override it with --type to use any Conventional Commit type.\n\n" +
			"--from master is for the release window only: the stretch between\n" +
			"develop being merged into master and the stable tag, where a release\n" +
			"blocker has to be fixed on master itself.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if demo != "" {
				mode, err := config.ParseDemo(demo)
				if err != nil {
					return err
				}
				demo = mode
			}
			return runNew(cmd.Context(), args[0], from, typ, demo, idea, forceManagedEnv, here)
		},
	}
	cmd.Flags().StringVar(&from, "from", "develop", "base worktree to branch off ("+strings.Join(rotki.Bases, "|")+")")
	_ = cmd.RegisterFlagCompletionFunc("from",
		func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return rotki.Bases, cobra.ShellCompDirectiveNoFileComp
		})
	cmd.Flags().StringVarP(&typ, "type", "t", "", "branch prefix / Conventional Commit type (default: derived from --from)")
	cmd.Flags().BoolVar(&idea, "idea", false, "open the worktree in IntelliJ IDEA")
	cmd.Flags().BoolVar(&forceManagedEnv, "force-managed-env", false, "write INSTANCE_NAME even if the checkout looks unsupported")
	cmd.Flags().BoolVar(&here, "here", false, "print a `cd <path>` snippet on stdout for eval")
	registerDemoFlag(cmd, &demo)
	return cmd
}

func runNew(ctx context.Context, name, from, typ, demo string, idea, forceManagedEnv, here bool) error {
	// --from picks the base to branch off (and the default prefix); --type, when
	// set, overrides the prefix with any known Conventional Commit type.
	prefix, ok := rotki.BranchPrefix[from]
	if !ok {
		return fmt.Errorf("--from must be one of %s, got %q", strings.Join(rotki.Bases, "|"), from)
	}
	if typ != "" {
		if !rotki.IsPrefix(typ) {
			return fmt.Errorf("--type must be one of %s, got %q", strings.Join(rotki.Prefixes, "|"), typ)
		}
		prefix = typ
	}
	host := rotki.HostWorktreePath()
	branch := rotki.Branch(prefix, name)
	wtPath := rotki.WorktreePath(prefix, name)

	// Step 1 — pre-flight (idempotent: an existing worktree means resume).
	_, dirExists := os.Stat(wtPath)
	resume := dirExists == nil
	if !resume {
		if git.BranchExists(ctx, host, branch) {
			return fmt.Errorf("branch %s already exists (but worktree %s does not) — resolve manually", branch, wtPath)
		}
	} else {
		fmt.Printf("worktree %s already exists — resuming install/env steps\n", wtPath)
	}

	// Step 2 — fetch upstream FIRST (correctness: never branch off a stale ref).
	if !resume {
		fmt.Printf("fetching %s...\n", rotki.Upstream)
		if err := git.Fetch(ctx, host, rotki.Upstream); err != nil {
			return err
		}
		// Step 3 — create the worktree off upstream/<base>.
		baseRef := rotki.Upstream + "/" + from
		fmt.Printf("creating worktree %s on %s off %s...\n", wtPath, branch, baseRef)
		if err := git.WorktreeAdd(ctx, host, wtPath, branch, baseRef); err != nil {
			return err
		}
	}

	// Step 4 — warm the language envs (capability-independent). The cargo cache
	// is wired first so the warm build populates the shared target dir rather
	// than seeding a per-worktree one.
	wireCargoCache(ctx, wtPath)
	fmt.Println("warming envs (pnpm / uv / cargo)...")
	if err := installRun(ctx, wtPath, install.Opts{}); err != nil {
		// Fail-soft: the worktree exists; tell the user how to resume.
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}

	// Step 5 — capability detection + one-line INSTANCE_NAME append.
	cap := detect.Capability(wtPath)
	switch {
	case cap.Capable:
		if err := envfile.AppendInstanceName(wtPath, name); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not write %s: %v\n", rotki.InputKey, err)
		} else {
			fmt.Printf("instance mode enabled (%s=%s)\n", rotki.InputKey, name)
		}
	case forceManagedEnv:
		fmt.Println("--force-managed-env: writing INSTANCE_NAME despite unsupported checkout")
		if err := envfile.AppendInstanceName(wtPath, name); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
	default:
		printNotCapableWarning(from, cap.Reason)
	}

	// Step 6 — assert the user's dev env flags (dev-tools / logs / persist)
	// and the demo mode.
	applyDevFlags(ctx, wtPath, demo)

	// Step 7 — optional IDEA launch.
	if idea {
		if err := exec.CommandContext(ctx, "idea", wtPath).Start(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not launch idea: %v\n", err)
		}
	}

	fmt.Printf("\nready: %s\n", wtPath)
	if here {
		fmt.Printf("cd %s\n", wtPath)
	}
	return nil
}

func printNotCapableWarning(base, reason string) {
	fmt.Fprintf(os.Stderr, `
WARNING: multi-instance dev:web is not available in this worktree.
  base: %s  ->  %s
This checkout's start-dev.ts ignores INSTANCE_NAME/INSTANCE_PORT_SLOT.
`+"`pnpm dev:web`"+` here will use the DEFAULT ports (4242/4243/4343/8080)
and the DEFAULT (shared) data dir — NO isolation.

Options:
  - Run dev:web here only when no other instance is live.
  - Base this work on develop instead (--from develop).
  - Backport the dev-instance feature to %s, then re-run rwt new.
  - --force-managed-env to write the env vars anyway (NOT recommended).
`, base, reason, base)
}
