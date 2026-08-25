package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/kelsos/rwt/internal/cargocache"
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
}
