package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/kelsos/rwt/internal/config"
	"github.com/kelsos/rwt/internal/envfile"
	"github.com/kelsos/rwt/internal/rotki"
	"github.com/spf13/cobra"
)

func configCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config [path <dir> | <flag> on|off]",
		Short: "Show or set the umbrella path and dev env flags",
		Long: "With no args, prints the configured rotki umbrella path and each dev flag.\n" +
			"`rwt config path <dir>` sets the umbrella location (rwt assumes none until\n" +
			"you do). `rwt config <flag> on|off` toggles a dev flag. State is persisted to\n" +
			"~/.config/rwt/config.json; enabled flags are asserted into a worktree's\n" +
			".env.development.local on the next rwt new / setup / refresh.",
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if len(args) == 0 {
				printConfig(cfg)
				return nil
			}

			if args[0] == "path" {
				return runConfigPath(cfg, args[1:])
			}

			flag, ok := config.Lookup(args[0])
			if !ok {
				return fmt.Errorf("unknown flag %q (known: %s)", args[0], config.AliasList())
			}
			if len(args) == 1 {
				return fmt.Errorf("usage: rwt config %s on|off", flag.Alias)
			}

			on, err := parseOnOff(args[1])
			if err != nil {
				return err
			}
			cfg.Set(flag.Alias, on)
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Printf("%s (%s) -> %s\n", flag.Alias, flag.EnvKey, stateWord(on))
			fmt.Println("applies on the next rwt new / setup / refresh")
			return nil
		},
	}
}

// runConfigPath shows or sets the umbrella path. `path` with no dir prints the
// resolved location and its source; `path <dir>` persists it; `path unset`
// clears the override.
func runConfigPath(cfg config.Config, args []string) error {
	if len(args) == 0 {
		printUmbrella()
		return nil
	}

	dir := args[0]
	switch dir {
	case "unset", "clear", "":
		cfg.Umbrella = ""
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Println("umbrella path cleared")
		return nil
	}

	abs, err := expandPath(dir)
	if err != nil {
		return err
	}
	cfg.Umbrella = abs
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("umbrella -> %s\n", abs)
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "warning: %s does not exist yet\n", abs)
	}
	return nil
}

func printConfig(cfg config.Config) {
	if path, err := config.Path(); err == nil {
		fmt.Printf("config: %s\n", path)
	}
	printUmbrella()
	fmt.Println()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, f := range config.Flags {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", f.Alias, stateWord(cfg.Flags[f.Alias]), f.EnvKey, f.Desc)
	}
	tw.Flush()
}

func printUmbrella() {
	path, source, ok := rotki.Umbrella()
	if !ok {
		fmt.Println("umbrella: (not configured — set with `rwt config path <dir>`)")
		return
	}
	suffix := ""
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		suffix = "  [missing!]"
	}
	fmt.Printf("umbrella: %s (%s)%s\n", path, source, suffix)
}

// expandPath resolves a leading ~ and returns an absolute path.
func expandPath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return filepath.Abs(p)
}

func parseOnOff(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "on", "true", "1", "yes":
		return true, nil
	case "off", "false", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("state must be on|off, got %q", s)
	}
}

func stateWord(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// applyDevFlags loads the user's dev-flag config and upserts the flags into the
// worktree's env. Fail-soft: it warns but never aborts the calling command, so
// a config glitch can't block a worktree create/refresh.
func applyDevFlags(wt string) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load dev-flag config: %v\n", err)
		return
	}
	if err := envfile.ApplyFlags(wt, cfg.EnvFlags()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not apply dev flags to %s: %v\n", wt, err)
	}
}
