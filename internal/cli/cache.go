package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/kelsos/rwt/internal/cargocache"
)

// wireCargoCache points a worktree's cargo workspaces at the shared target dir
// before anything builds. It runs ahead of the install steps in new / setup /
// refresh so the warm build lands in the shared cache instead of seeding yet
// another per-worktree target dir.
//
// Fail-soft, like the install steps around it: a worktree that cannot be wired
// still builds correctly, just without the cross-worktree reuse.
func wireCargoCache(ctx context.Context, worktree string) {
	res, err := cargocache.Wire(worktree)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not wire the shared cargo cache: %v\n", err)
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
	root, _ := cargocache.Root()
	note := ""
	if cargocache.Wrapper() == "" {
		note = " (no sccache on PATH — shared target dir only)"
	}
	fmt.Printf("cargo cache: %s -> %s%s\n", strings.Join(wired, ", "), root, note)
}
