package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/kelsos/rwt/internal/cargocache"
	"github.com/kelsos/rwt/internal/git"
	"github.com/kelsos/rwt/internal/rotki"
)

// wireCargoCache points a worktree's cargo workspaces at its own target dir
// before anything builds. It runs ahead of the install steps in new / setup /
// refresh, and on an existing worktree it is also the migration off the old
// shared cache: Wire drops the launcher symlinks that used to point into it.
//
// Fail-soft, like the install steps around it: a worktree that cannot be wired
// still builds correctly, into whatever target dir cargo picks for it.
func wireCargoCache(ctx context.Context, worktree string) {
	res, err := cargocache.Wire(worktree)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not wire the cargo target dir: %v\n", err)
		return
	}
	for _, name := range res.Kept {
		fmt.Fprintf(os.Stderr, "note: %s has its own .cargo/config.toml — left alone, not wired\n", name)
	}
	wired := res.Wired
	if len(wired) == 0 {
		return
	}
	if err := cargocache.Exclude(ctx, worktree); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not update info/exclude: %v\n", err)
	}
	note := ""
	if cargocache.Wrapper() == "" {
		note = " (no sccache on PATH)"
	}
	fmt.Printf("cargo target dir: %s -> %s%s\n",
		strings.Join(wired, ", "), cargocache.TargetDir(worktree), note)
	syncSccacheBasedirs(ctx)
}

// syncSccacheBasedirs keeps sccache's basedirs matching the umbrella's current
// worktrees, which is what lets it reuse a compilation across them at all.
//
// Umbrella-wide rather than per-worktree, so it runs off the full list rather
// than the one worktree being wired: adding or removing a worktree changes what
// the file should say, and every path into here follows such a change.
//
// Fail-soft throughout. Without this sccache still works, it just stops hitting
// across worktrees, which is where the whole feature was before rwt managed it.
func syncSccacheBasedirs(ctx context.Context) {
	wts, err := git.List(ctx, rotki.HostWorktreePath())
	if err != nil {
		return
	}
	dirs := make([]string, 0, len(wts))
	for _, w := range wts {
		dirs = append(dirs, w.Path)
	}
	changed, err := cargocache.SyncBasedirs(dirs)
	switch {
	case cargocache.IsSccacheConfigNotOurs(err):
		path, _ := cargocache.SccacheConfigPath()
		fmt.Fprintf(os.Stderr,
			"note: %s is yours, not rwt's — left alone.\n"+
				"      add the worktree roots to its basedirs for cross-worktree cache hits.\n", path)
	case err != nil:
		fmt.Fprintf(os.Stderr, "warning: could not update the sccache basedirs: %v\n", err)
	case changed:
		// The server reads its config once, at startup.
		cargocache.RestartSccache()
		fmt.Printf("sccache basedirs: %d worktree(s); server restarted to pick them up\n", len(dirs))
	}
}
