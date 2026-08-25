package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kelsos/rwt/internal/cargocache"
	"github.com/kelsos/rwt/internal/git"
	"github.com/kelsos/rwt/internal/rotki"
	"github.com/spf13/cobra"
)

func cleanCmd() *cobra.Command {
	var (
		dryRun bool
		cache  bool
	)
	cmd := &cobra.Command{
		Use:   "clean [name|.]",
		Short: "Wire worktrees to their own cargo target dir and reclaim the leftovers",
		Long: "Points each worktree's cargo workspaces at <worktree>/target and removes\n" +
			"the target directories nothing builds into any more (colibri/target and\n" +
			"crates/target, left behind by the split layout).\n\n" +
			"This is also the migration off the shared cargo cache: wiring a worktree\n" +
			"drops the launcher symlinks that pointed into it, which is what stopped\n" +
			"several worktrees from running one another's binaries.\n\n" +
			"<worktree>/target itself is never reclaimed. It is the live build\n" +
			"directory now, and removing it would only buy back disk in exchange for\n" +
			"a cold rebuild. Use `cargo clean` in a worktree for that.\n\n" +
			"With no argument every worktree under the umbrella is cleaned; pass a\n" +
			"name (or '.') to limit it to one.\n\n" +
			"--cache drops the old shared cache root, which is where the reclaimable\n" +
			"disk actually is once every worktree has been migrated.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeWorktreeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClean(cmd.Context(), args, dryRun, cache)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be removed without removing it")
	cmd.Flags().BoolVar(&cache, "cache", false, "also drop the old shared cache root that nothing builds into now")
	return cmd
}

func runClean(ctx context.Context, args []string, dryRun, cache bool) error {
	worktrees, err := cleanTargets(ctx, args)
	if err != nil {
		return err
	}

	var reclaimed int64
	for _, wt := range worktrees {
		// Wire before removing: it is what redirects the worktree onto its own
		// target dir and drops the symlinks into the old shared cache.
		// Unconditional, since a worktree with nothing left to reclaim still
		// needs the wiring, which is the state every migrated worktree is in.
		if !dryRun {
			wireCargoCache(ctx, wt)
		}
		for _, dir := range cargocache.SupersededTargets(wt) {
			// Reclaim removes only what cargo put there, so target/backend (the
			// frozen python core the e2e run builds) survives.
			freed, err := cargocache.Reclaim(dir, dryRun)
			reclaimed += freed
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not fully reclaim %s: %v\n", dir, err)
			}
			verb := "reclaimed"
			if dryRun {
				verb = "would reclaim"
			}
			fmt.Printf("%s cargo output under %s (%s)\n",
				verb, filepath.Join(filepath.Base(wt), mustRel(wt, dir)), cargocache.HumanBytes(freed))
		}
	}

	if cache {
		root, err := cargocache.LegacyRoot()
		if err != nil {
			return err
		}
		size := cargocache.DirSize(root)
		if dryRun {
			fmt.Printf("would remove the old shared cache %s (%s)\n", root, cargocache.HumanBytes(size))
		} else if err := os.RemoveAll(root); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove %s: %v\n", root, err)
		} else {
			reclaimed += size
			fmt.Printf("removed the old shared cache %s (%s)\n", root, cargocache.HumanBytes(size))
		}
	}

	verb := "reclaimed"
	if dryRun {
		verb = "would reclaim"
	}
	fmt.Printf("\n%s %s\n", verb, cargocache.HumanBytes(reclaimed))
	return nil
}

// cleanTargets resolves the argument to the worktrees to clean: one named
// worktree, or every worktree git knows about under the umbrella.
func cleanTargets(ctx context.Context, args []string) ([]string, error) {
	if len(args) == 1 {
		wt, err := resolveWorktree(ctx, args[0])
		if err != nil {
			return nil, err
		}
		return []string{wt}, nil
	}
	wts, err := git.List(ctx, rotki.HostWorktreePath())
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(wts))
	for _, w := range wts {
		paths = append(paths, w.Path)
	}
	return paths, nil
}

// mustRel is filepath.Rel with the error collapsed to the absolute path, used
// only for display.
func mustRel(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return rel
}
