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
					reportCargoCache(cmd.Context())
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

// reportCargoCache summarises the shared cargo cache: how big it is, how many
// worktrees are wired to it, and how much disk the superseded per-worktree
// target dirs are still holding. Informational rather than pass/fail — an
// unwired worktree builds fine, it just pays for every compile itself.
func reportCargoCache(ctx context.Context) {
	root, err := cargocache.Root()
	if err != nil {
		return
	}
	fmt.Printf("\ncargo cache: %s (%s)\n", root, cargocache.HumanBytes(cargocache.DirSize(root)))

	wts, err := git.List(ctx, rotki.HostWorktreePath())
	if err != nil {
		return
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
}
