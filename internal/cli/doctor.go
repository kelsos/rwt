package cli

import (
	"fmt"
	"os"
	"os/exec"

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

			// sccache wired as the cargo rustc wrapper — the biggest build win.
			wrapper := os.Getenv("CARGO_BUILD_RUSTC_WRAPPER")
			check("CARGO_BUILD_RUSTC_WRAPPER=sccache", wrapper != "" && fileBase(wrapper) == "sccache",
				"export CARGO_BUILD_RUSTC_WRAPPER=sccache for cross-worktree compile caching")

			// Umbrella configured + host worktree present.
			umbrella, source, configured := rotki.Umbrella()
			check("umbrella configured", configured,
				"set it once with: rwt config path <dir> (or export RWT_UMBRELLA)")
			if configured {
				_, errHost := os.Stat(rotki.HostWorktreePath())
				check("host worktree present ("+rotki.HostWorktree+")", errHost == nil,
					"expected umbrella at "+umbrella+" (source: "+source+")")
			}

			if !ok {
				return fmt.Errorf("doctor found issues")
			}
			fmt.Println("\nall good.")
			return nil
		},
	}
}

func fileBase(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}
