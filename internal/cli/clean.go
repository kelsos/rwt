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
		Short: "Reclaim per-worktree cargo target dirs superseded by the shared cache",
		Long: "Wires each worktree to the shared cargo target dir and removes the\n" +
			"per-worktree target directories it supersedes (target, colibri/target\n" +
			"and crates/target, whichever the worktree's layout left behind).\n" +
			"Wiring happens first by design: deleting a target dir from an unwired\n" +
			"worktree would just trade disk for a full cold rebuild.\n\n" +
			"With no argument every worktree under the umbrella is cleaned; pass a\n" +
			"name (or '.') to limit it to one.\n\n" +
			"--cache additionally drops the shared target dirs themselves, for when\n" +
			"the shared cache has grown past its worth or needs a hard reset. That\n" +
			"one costs a full rebuild for every worktree, so it is never the default.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClean(cmd.Context(), args, dryRun, cache)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be removed without removing it")
	cmd.Flags().BoolVar(&cache, "cache", false, "also drop the shared target dirs (forces a full rebuild everywhere)")
	return cmd
}

func runClean(ctx context.Context, args []string, dryRun, cache bool) error {
	worktrees, err := cleanTargets(ctx, args)
	if err != nil {
		return err
	}

	var reclaimed int64
	for _, wt := range worktrees {
		// Wire before removing so the next build repopulates the shared cache
		// instead of rebuilding into a fresh local target dir. Unconditional:
		// a worktree with nothing left to reclaim still needs the wiring, which
		// is the state every already-cleaned worktree is in.
		if !dryRun {
			wireCargoCache(ctx, wt)
		}
		for _, dir := range cargocache.LocalTargets(wt) {
			// Reclaim removes only what cargo put there, so target/backend (the
			// frozen python core the e2e run builds) and the launcher symlinks
			// into the shared cache both survive.
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
		root, err := cargocache.Root()
		if err != nil {
			return err
		}
		size := cargocache.DirSize(root)
		if dryRun {
			fmt.Printf("would remove the shared cache %s (%s)\n", root, cargocache.HumanBytes(size))
		} else if err := os.RemoveAll(root); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove %s: %v\n", root, err)
		} else {
			reclaimed += size
			fmt.Printf("removed the shared cache %s (%s)\n", root, cargocache.HumanBytes(size))
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
