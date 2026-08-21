package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/kelsos/rwt/internal/config"
	"github.com/kelsos/rwt/internal/envfile"
	"github.com/kelsos/rwt/internal/git"
	"github.com/kelsos/rwt/internal/rotki"
	"github.com/spf13/cobra"
)

func configCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config [path <dir> | demo <mode> | <flag> on|off]",
		Short: "Show or set the umbrella path, dev env flags and demo mode",
		Long: "With no args, prints the configured rotki umbrella path and each dev flag.\n" +
			"`rwt config path <dir>` sets the umbrella location (rwt assumes none until\n" +
			"you do). `rwt config <flag> on|off` toggles a dev flag. `rwt config demo\n" +
			"off|auto|minor|patch` sets the default " + rotki.DemoKey + "; auto derives it\n" +
			"per worktree from the base it came off (develop/master->minor,\n" +
			"bugfixes->patch).\n" +
			"State is persisted to ~/.config/rwt/config.json and asserted into a\n" +
			"worktree's .env.development.local on the next rwt new / setup / refresh.",
		Args:              cobra.MaximumNArgs(2),
		ValidArgsFunction: completeConfigArgs,
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
			if args[0] == "demo" {
				return runConfigDemo(cfg, args[1:])
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

// completeConfigArgs completes both positions of `rwt config`: the setting
// name, then the value that setting takes. The settings do not share a value
// vocabulary — a flag takes on|off, demo takes a mode, path takes a directory —
// so the second position dispatches on the first.
func completeConfigArgs(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		names := []string{"path", "demo"}
		for _, f := range config.Flags {
			names = append(names, f.Alias)
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	case 1:
		switch args[0] {
		case "path":
			// A directory, so offer real ones; `path unset` is documented in
			// help rather than completed, since mixing it into the list would
			// suppress the directory filter.
			return nil, cobra.ShellCompDirectiveFilterDirs
		case "demo":
			return config.DemoModes, cobra.ShellCompDirectiveNoFileComp
		}
		if _, ok := config.Lookup(args[0]); ok {
			return []string{"on", "off"}, cobra.ShellCompDirectiveNoFileComp
		}
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// registerDemoFlag adds the --demo override shared by new / setup / refresh.
// An unset flag means "use the configured mode", which is why the zero value is
// "" rather than config.DemoOff — passing --demo off must be distinguishable
// from not passing it at all.
func registerDemoFlag(cmd *cobra.Command, target *string) {
	cmd.Flags().StringVar(target, "demo", "",
		"override "+rotki.DemoKey+" for this run: "+strings.Join(config.DemoModes, "|")+
			" (default: the configured mode)")
	_ = cmd.RegisterFlagCompletionFunc("demo",
		func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return config.DemoModes, cobra.ShellCompDirectiveNoFileComp
		})
}

// runConfigDemo shows or sets the persisted demo mode.
func runConfigDemo(cfg config.Config, args []string) error {
	if len(args) == 0 {
		fmt.Printf("demo: %s (%s)\n", cfg.Demo, rotki.DemoKey)
		return nil
	}
	mode, err := config.ParseDemo(args[0])
	if err != nil {
		return err
	}
	cfg.Demo = mode
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("demo (%s) -> %s\n", rotki.DemoKey, describeDemo(mode))
	fmt.Println("applies on the next rwt new / setup / refresh")
	return nil
}

// describeDemo spells out what a mode will write, since "auto" and "off" both
// name a policy rather than a value.
func describeDemo(mode string) string {
	switch mode {
	case config.DemoAuto:
		return "auto (develop/master->minor, bugfixes->patch)"
	case config.DemoOff:
		return "off (key removed)"
	}
	return mode
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
	fmt.Fprintf(tw, "demo\t%s\t%s\t%s\n", cfg.Demo, rotki.DemoKey,
		"fake a released version (auto: develop/master->minor, bugfixes->patch)")
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

// applyDevFlags loads the user's dev-flag config and upserts the flags — plus
// VITE_DEMO_MODE — into the worktree's env. demoOverride is the value of a
// --demo flag, or "" to use the configured mode. Fail-soft: it warns but never
// aborts the calling command, so a config glitch can't block a worktree
// create/refresh.
func applyDevFlags(ctx context.Context, wt, demoOverride string) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load dev-flag config: %v\n", err)
		return
	}
	values := make(map[string]string, len(config.Flags)+1)
	for key, on := range cfg.EnvFlags() {
		if on {
			values[key] = "true"
		} else {
			values[key] = "" // removed, not written false
		}
	}
	mode := cfg.Demo
	if demoOverride != "" {
		mode = demoOverride
	}
	values[rotki.DemoKey] = demoValue(ctx, wt, mode)

	if err := envfile.ApplyValues(wt, values); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not apply dev flags to %s: %v\n", wt, err)
	}
}

// applyDemoOnly writes just VITE_DEMO_MODE, leaving the boolean dev flags
// alone. Used by the narrowed `setup --only` path, where an explicit --demo
// should still land even though a narrowed run skips the flag pass.
func applyDemoOnly(ctx context.Context, wt, mode string) {
	if err := envfile.ApplyValues(wt, map[string]string{rotki.DemoKey: demoValue(ctx, wt, mode)}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not apply %s to %s: %v\n", rotki.DemoKey, wt, err)
	}
}

// demoValue resolves a demo mode to the literal env value to write, "" meaning
// remove the key. Only "auto" needs the worktree: it maps the base HEAD
// branched off to the release that base ships.
func demoValue(ctx context.Context, wt, mode string) string {
	switch mode {
	case config.DemoMinor, config.DemoPatch:
		return mode
	case config.DemoAuto:
		base, ok := autoBase(ctx, wt)
		if !ok {
			fmt.Fprintf(os.Stderr, "warning: demo auto: could not tell which base %s came off; leaving %s unset\n",
				filepath.Base(wt), rotki.DemoKey)
			return ""
		}
		value := rotki.DemoForBase(base)
		if value == "" {
			fmt.Fprintf(os.Stderr, "warning: demo auto: base %s has no release of its own; leaving %s unset\n",
				base, rotki.DemoKey)
		}
		return value
	}
	return "" // off, or an empty/unknown mode
}

// autoBase resolves the base whose next release a worktree's work would ship
// in. A checked-out long-lived base answers itself rather than being scored
// against the others, which during the release window (master holding an
// untagged develop merge) is the only thing that keeps the three apart.
func autoBase(ctx context.Context, wt string) (string, bool) {
	if b := git.CurrentBranch(ctx, wt); slices.Contains(rotki.LongLived, b) {
		return b, true
	}
	return git.NearestBase(ctx, wt, rotki.Upstream, rotki.DemoBases)
}
