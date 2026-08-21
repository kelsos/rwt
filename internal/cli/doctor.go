package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kelsos/rwt/internal/cargocache"
	"github.com/kelsos/rwt/internal/git"
	"github.com/kelsos/rwt/internal/hooks"
	"github.com/kelsos/rwt/internal/rotki"
	"github.com/spf13/cobra"
)

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Preflight the environment for silent foot-guns",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ok := true
			check := func(label string, pass bool, hint string) {
				mark := "ok"
				if !pass {
					mark = "FAIL"
					ok = false
				}
				fmt.Printf("[%s] %s\n", mark, label)
				if !pass && hint != "" {
					fmt.Printf("       %s\n", hint)
				}
			}

			for _, tool := range []string{"git", "pnpm", "uv", "cargo"} {
				_, err := exec.LookPath(tool)
				check(tool+" on PATH", err == nil, "install "+tool)
			}
			_, ideaErr := exec.LookPath("idea")
			check("idea on PATH (optional)", ideaErr == nil, "--idea launch will be unavailable")

			// sccache backs the shared target dir as a second cache layer. Its
			// absence costs cache misses, not correctness, so this is advisory.
			check("sccache on PATH (optional)", cargocache.Wrapper() != "",
				"install sccache to also cache across rustc upgrades and rustflag changes")

			// Umbrella configured + host worktree present.
			umbrella, source, configured := rotki.Umbrella()
			check("umbrella configured", configured,
				"set it once with: rwt config path <dir> (or export RWT_UMBRELLA)")
			if configured {
				_, errHost := os.Stat(rotki.HostWorktreePath())
				check("host worktree present ("+rotki.HostWorktree+")", errHost == nil,
					"expected umbrella at "+umbrella+" (source: "+source+")")
				if errHost == nil {
					reportHooks(cmd.Context())
					// A collision is a correctness bug, not an advisory: it
					// silently runs another worktree's code. Fail on it, so
					// doctor never prints "all good" over one.
					if reportCargoCache(cmd.Context()) {
						ok = false
					}
				}
			}

			if !ok {
				return fmt.Errorf("doctor found issues")
			}
			fmt.Println("\nall good.")
			return nil
		},
	}
}

// reportHooks summarises the local gates. Informational rather than pass/fail:
// not having them installed is a choice, not a fault. Two things here are worth
// saying out loud because they fail silently otherwise: a core.hooksPath
// pointing at a directory that does not exist runs no hooks and says nothing,
// and a worktree whose .venv lacks the lint group skips every Python check.
func reportHooks(ctx context.Context) {
	host := rotki.HostWorktreePath()
	state, err := hooks.Status(ctx, host)
	if err != nil {
		return
	}
	fmt.Println()
	switch {
	case state.Installed:
		fmt.Printf("hooks: installed (%s)\n", state.Dir)
	case state.HooksPath == "":
		fmt.Println("hooks: not installed (install with: rwt hooks install)")
	default:
		fmt.Printf("hooks: not rwt's; core.hooksPath is %s\n", state.HooksPath)
	}
	if state.PreviousBroken {
		path := state.HooksPath
		if state.Installed {
			path = state.Previous
		}
		fmt.Printf("       warning: %s does not exist, so it runs nothing\n", path)
	}
	for _, base := range rotki.LongLived {
		wt := filepath.Join(rotki.UmbrellaRoot(), base)
		if _, err := os.Stat(wt); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(wt, ".venv", "bin", "ruff")); err != nil {
			fmt.Printf("       %s has no Python lint group; `rwt check` will skip ruff/mypy there\n", base)
			fmt.Printf("       fix with: rwt setup %s --only uv --lint\n", base)
			break
		}
	}
}

// reportCargoCache summarises the shared cargo cache: how big it is, how many
// worktrees are wired to it, and how much disk the superseded per-worktree
// target dirs are still holding. Informational rather than pass/fail — an
// unwired worktree builds fine, it just pays for every compile itself.
// It returns whether it found a cross-worktree binary collision, which is a
// real fault rather than a note.
func reportCargoCache(ctx context.Context) (collided bool) {
	root, err := cargocache.Root()
	if err != nil {
		return false
	}
	fmt.Printf("\ncargo cache: %s (%s)\n", root, cargocache.HumanBytes(cargocache.DirSize(root)))

	wts, err := git.List(ctx, rotki.HostWorktreePath())
	if err != nil {
		return false
	}
	var unwired []string
	var localBytes int64
	for _, w := range wts {
		for _, s := range cargocache.Inspect(w.Path) {
			if !s.Wired {
				unwired = append(unwired, filepath.Base(w.Path)+"/"+s.Name)
			}
		}
		for _, dir := range cargocache.LocalTargets(w.Path) {
			localBytes += cargocache.DirSize(dir)
		}
	}
	if len(unwired) > 0 {
		fmt.Printf("       %d workspace(s) not wired: %s\n", len(unwired), strings.Join(unwired, ", "))
		fmt.Printf("       wire them with: rwt clean\n")
	}
	if localBytes > 0 {
		fmt.Printf("       %s still held by superseded per-worktree target dirs (rwt clean)\n",
			cargocache.HumanBytes(localBytes))
	}
	return reportCollisions(paths(wts))
}

// reportCollisions names the worktrees that resolve to the same built binary.
//
// The loudest thing doctor says, because it is the one failure here that lies to
// you: the build succeeds, the app starts, and it is running another worktree's
// code. Nothing in cargo's output mentions it, and the usual next step (rebuild,
// restart) does not help, because the neighbour owns the artifact.
func reportCollisions(worktrees []string) bool {
	found := cargocache.Collisions(worktrees)
	if len(found) == 0 {
		return false
	}
	fmt.Printf("\n[FAIL] %s shared across worktrees\n", countOf(len(found), "binary", "binaries"))
	for _, c := range found {
		var names []string
		for _, wt := range c.Worktrees {
			names = append(names, filepath.Base(wt))
		}
		fmt.Printf("       %s: %s\n", c.Bin, strings.Join(names, ", "))
	}
	fmt.Println("       These worktrees all run the SAME binary; only one of them built it.")
	fmt.Println("       A shared target dir gives cargo one fingerprint namespace for every")
	fmt.Println("       worktree, so it cannot tell them apart (see cargocache.LinkBins).")
	fmt.Println("       Isolate the worktree you are working in:")
	fmt.Printf("         CARGO_TARGET_DIR=%s-<slug> pnpm run dev:web\n",
		cargocache.WorkspaceTargetOf(found[0].Artifact))
	fmt.Println("       Verify what is actually running, never the build line:")
	fmt.Println("         readlink /proc/<pid-listening-on-its-port>/exe")
	return true
}

// countOf renders "1 binary" / "2 binaries".
func countOf(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// paths projects worktree records onto their paths.
func paths(wts []git.Worktree) []string {
	out := make([]string, 0, len(wts))
	for _, w := range wts {
		out = append(out, w.Path)
	}
	return out
}
