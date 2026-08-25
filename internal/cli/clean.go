package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kelsos/rwt/internal/cargocache"
	"github.com/kelsos/rwt/internal/git"
	"github.com/kelsos/rwt/internal/rotki"
	"github.com/spf13/cobra"
)

func cleanCmd() *cobra.Command {
	var (
		dryRun bool
		cache  bool
		deep   bool
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
			"<worktree>/target is left alone by default. It is the live build\n" +
			"directory now, so reclaiming it buys back disk in exchange for a cold\n" +
			"rebuild (~50s per worktree). --deep does it anyway, for when the disk\n" +
			"matters more; worktrees with a running dev session are skipped.\n\n" +
			"With no argument every worktree under the umbrella is cleaned; pass a\n" +
			"name (or '.') to limit it to one.\n\n" +
			"--cache drops the old shared cache root, which is where the reclaimable\n" +
			"disk actually is once every worktree has been migrated.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeWorktreeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClean(cmd.Context(), args, dryRun, cache, deep)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be removed without removing it")
	cmd.Flags().BoolVar(&cache, "cache", false, "also drop the old shared cache root that nothing builds into now")
	cmd.Flags().BoolVar(&deep, "deep", false, "also reclaim each worktree's live target dir (costs a cold rebuild)")
	return cmd
}

func runClean(ctx context.Context, args []string, dryRun, cache, deep bool) error {
	worktrees, err := cleanTargets(ctx, args)
	if err != nil {
		return err
	}

	var reclaimed int64
	for _, wt := range worktrees {
		if deep {
			reclaimed += deepClean(wt, dryRun)
		}
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

// deepClean reclaims a worktree's live target dir, returning the bytes freed.
//
// Skipped outright when something is running out of it. rotki's starling
// supervises colibri and restarts it, so removing the directory under a live dev
// session leaves the supervisor respawning a binary that is no longer there —
// and Linux unlinks a running executable without any complaint that would make
// the cause obvious. Reporting the pid is more use than a generic refusal.
//
// Reclaim rather than RemoveAll, so target/backend (the frozen python core the
// e2e run builds, which cargo never wrote and which is slow to rebuild) survives
// here exactly as it does for the superseded dirs.
func deepClean(worktree string, dryRun bool) int64 {
	if running := cargocache.RunningFrom(worktree); len(running) > 0 {
		var who []string
		for _, r := range running {
			who = append(who, fmt.Sprintf("%s (pid %d)", r.Name, r.PID))
		}
		fmt.Printf("skipped %s: %s running from its target dir\n",
			filepath.Base(worktree), strings.Join(who, ", "))
		return 0
	}
	dir := cargocache.TargetDir(worktree)
	freed, err := cargocache.Reclaim(dir, dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not fully reclaim %s: %v\n", dir, err)
	}
	if freed > 0 {
		verb := "reclaimed"
		if dryRun {
			verb = "would reclaim"
		}
		fmt.Printf("%s the target dir of %s (%s)\n",
			verb, filepath.Base(worktree), cargocache.HumanBytes(freed))
	}
	return freed
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
