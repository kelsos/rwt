package cli

import (
	"fmt"
	"io"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is the release string. It's overridable at build time via
//
//	go build -ldflags "-X github.com/kelsos/rwt/internal/cli.version=v1.2.3"
//
// When left empty (plain `go build`/`go install`), it falls back to the VCS
// revision Go stamps into the build, so a dev binary still reports something.
var version = ""

// resolveVersion returns the ldflags-injected version, or the VCS-derived one,
// or "dev" when neither is available.
func resolveVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
		var rev, dirty string
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				if s.Value == "true" {
					dirty = "-dirty"
				}
			}
		}
		if rev != "" {
			if len(rev) > 12 {
				rev = rev[:12]
			}
			return rev + dirty
		}
	}
	return "dev"
}

// versionCmd prints the rwt version.
func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the rwt version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVersion(cmd.OutOrStdout())
		},
	}
}

func runVersion(out io.Writer) error {
	_, err := fmt.Fprintf(out, "rwt %s\n", resolveVersion())
	return err
}
