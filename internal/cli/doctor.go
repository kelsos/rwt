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
		// Two different facts wear the same sentence, and conflating them reads
		// as "the hooks you just installed run nothing". When rwt is installed
		// the broken path is the one it displaced and chains to, so the hooks
		// work and only the chained-to mechanism is dead. When rwt is not
		// installed, the broken path IS core.hooksPath and nothing runs at all.
		if state.Installed {
			fmt.Printf("       note: the displaced %s does not exist, so nothing is chained after rwt's checks\n",
				state.Previous)
		} else {
			fmt.Printf("       warning: %s does not exist, so git is running no hooks at all\n",
				state.HooksPath)
		}
	}
	for _, base := range rotki.LongLived {
		wt := filepath.Join(rotki.UmbrellaRoot(), base)
		if _, err := os.Stat(wt); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(wt, ".venv", "bin", "ruff")); err != nil {
			fmt.Printf("       %s has no Python lint group; `rwt check` will skip ruff/mypy there\n", base)
			fmt.Printf("       fix with: rwt setup %s --only uv\n", base)
			break
		}
	}
}

// reportCargoCache summarises cargo target dirs: which workspaces are wired to
// their own, and how much disk the superseded dirs and the old shared cache are
// still holding. Informational rather than pass/fail — an unwired worktree
// builds fine, it just may not build where the dev launcher looks.
// It returns whether it found a cross-worktree binary collision, which is a
// real fault rather than a note.
func reportCargoCache(ctx context.Context) (collided bool) {
	wts, err := git.List(ctx, rotki.HostWorktreePath())
	if err != nil {
		return false
	}
	var unwired []string
	var supersededBytes, liveBytes int64
	for _, w := range wts {
		for _, s := range cargocache.Inspect(w.Path) {
			if !s.Wired {
				unwired = append(unwired, filepath.Base(w.Path)+"/"+s.Name)
			}
		}
		for _, dir := range cargocache.SupersededTargets(w.Path) {
			supersededBytes += cargocache.DirSize(dir)
		}
		liveBytes += cargocache.DirSize(cargocache.TargetDir(w.Path))
	}
	fmt.Printf("\ncargo target dirs: one per worktree (%s across %d)\n",
		cargocache.HumanBytes(liveBytes), len(wts))
	if len(unwired) > 0 {
		fmt.Printf("       %d workspace(s) not wired: %s\n", len(unwired), strings.Join(unwired, ", "))
		fmt.Printf("       wire them with: rwt clean\n")
	}
	if supersededBytes > 0 {
		fmt.Printf("       %s held by superseded target dirs (rwt clean)\n",
			cargocache.HumanBytes(supersededBytes))
	}
	if root, err := cargocache.LegacyRoot(); err == nil {
		if size := cargocache.DirSize(root); size > 0 {
			fmt.Printf("       %s still in the old shared cache %s\n", cargocache.HumanBytes(size), root)
			fmt.Printf("       nothing builds into it now; reclaim it with: rwt clean --cache\n")
		}
	}
	reportBasedirs(paths(wts))
	reportIncremental()
	return reportCollisions(paths(wts))
}

// reportBasedirs reports whether sccache is set up to reuse compilations across
// worktrees, which is invisible otherwise: without basedirs it caches happily,
// reports healthy stats, and simply never hits from another worktree. That state
// looked exactly like a working cache for as long as rwt shipped it.
func reportBasedirs(worktrees []string) {
	if cargocache.Wrapper() == "" {
		return
	}
	path, err := cargocache.SccacheConfigPath()
	if err != nil {
		return
	}
	body, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("\nsccache: no config at %s, so it cannot hit across worktrees\n", path)
		fmt.Println("       fix with: rwt clean")
		return
	}
	var missing []string
	for _, wt := range worktrees {
		if !strings.Contains(string(body), `"`+wt+`"`) {
			missing = append(missing, filepath.Base(wt))
		}
	}
	if len(missing) == 0 {
		fmt.Printf("\nsccache: basedirs cover all %d worktree(s)\n", len(worktrees))
		return
	}
	fmt.Printf("\nsccache: %d worktree(s) missing from basedirs: %s\n",
		len(missing), strings.Join(missing, ", "))
	fmt.Println("       they compile without reusing anything the others built")
	fmt.Println("       fix with: rwt clean")
}

// reportIncremental warns about CARGO_INCREMENTAL being exported, which is fatal
// in combination with the rustc-wrapper the generated config sets: sccache
// checks the environment variable rather than the rustc flag and refuses to run
// at all, failing the build with "incremental compilation is prohibited". The
// error names neither rwt nor the variable that caused it, and it fires for
// every cargo build in every worktree, so it is worth naming here.
func reportIncremental() {
	v, set := os.LookupEnv("CARGO_INCREMENTAL")
	if !set || v == "0" || cargocache.Wrapper() == "" {
		return
	}
	fmt.Printf("\n[warn] CARGO_INCREMENTAL=%s is exported and sccache is wired as a rustc-wrapper\n", v)
	fmt.Println("       sccache refuses to run at all in that combination, so every cargo")
	fmt.Println("       build fails with \"incremental compilation is prohibited\".")
	fmt.Println("       Unset it: cargo still builds workspace members incrementally.")
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
	fmt.Println("       They are still on the old shared target dir, which gave cargo one")
	fmt.Println("       fingerprint namespace for every worktree using it.")
	fmt.Println("       Migrate them onto their own target dirs:")
	fmt.Println("         rwt clean")
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
