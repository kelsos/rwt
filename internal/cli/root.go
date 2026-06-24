// Package cli wires up the rwt command-line interface.
package cli

import (
	"fmt"

	"github.com/kelsos/rwt/internal/rotki"
	"github.com/spf13/cobra"
)

// noUmbrellaNeeded are commands that work without a configured umbrella: config
// sets it, doctor diagnoses it, and the help/completion machinery never touches
// it. Everything else is gated by the guard below.
var noUmbrellaNeeded = map[string]bool{
	"rwt":        true,
	"config":     true,
	"doctor":     true,
	"help":       true,
	"completion": true,
	"__complete": true,
}

// Execute runs the root command.
func Execute() error {
	return newRootCmd().Execute()
}

// newRootCmd builds the root command with all subcommands wired. Split from
// Execute so tests can drive it via SetArgs.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "rwt",
		Short: "rotki git-worktree tool",
		Long: "rwt spawns and tears down git worktrees for parallel-agent / parallel-PR work\n" +
			"on the rotki app repo, and warms each worktree's uv / cargo / pnpm envs.\n" +
			"It is a thin shim: the app owns dev:web slot allocation and the port registry.",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Refuse umbrella-touching commands until the user configures a location
		// once — rwt assumes nothing about where rotki/rotki lives.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if noUmbrellaNeeded[cmd.Name()] {
				return nil
			}
			if _, _, ok := rotki.Umbrella(); !ok {
				return fmt.Errorf("no rotki umbrella configured — set it once with:\n" +
					"  rwt config path <dir>\n" +
					"(or export RWT_UMBRELLA=<dir>)")
			}
			return nil
		},
	}
	root.AddCommand(
		newCmd(),
		setupCmd(),
		lsCmd(),
		rmCmd(),
		refreshCmd(),
		goCmd(),
		configCmd(),
		doctorCmd(),
	)
	// Add an `install` subcommand under Cobra's auto-generated `completion`
	// command (which only prints), so users can install/update completions in
	// one step. InitDefaultCompletionCmd materializes that command now; Execute
	// sees it already present and won't re-add it.
	root.InitDefaultCompletionCmd()
	for _, sub := range root.Commands() {
		if sub.Name() == "completion" {
			sub.AddCommand(completionInstallCmd(root))
			break
		}
	}
	return root
}
