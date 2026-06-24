package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// completionInstallCmd installs (or updates) the shell completion script into a
// per-user directory, so users don't have to redirect `completion <shell>`
// themselves. Re-running it regenerates the file, which is also how you update
// completions after upgrading rwt.
func completionInstallCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "install [bash|zsh|fish]",
		Short: "Install or update the completion script for the current user",
		Long: "Install the shell completion script into a per-user directory (no root).\n" +
			"With no argument the shell is detected from $SHELL. Re-run after upgrading\n" +
			"rwt to refresh the completions.",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish"},
		// Override the root umbrella guard: installing completions needs no
		// rotki umbrella. Cobra runs only the deepest PersistentPreRunE, so this
		// no-op replaces the root one for this command.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			shell := detectShell()
			if len(args) == 1 {
				shell = args[0]
			}
			switch shell {
			case "zsh":
				return installZshCompletion(root)
			case "bash":
				return installBashCompletion(root)
			case "fish":
				return installFishCompletion(root)
			case "":
				return fmt.Errorf("could not detect shell from $SHELL; pass one of bash|zsh|fish")
			default:
				return fmt.Errorf("unsupported shell %q (want bash|zsh|fish)", shell)
			}
		},
	}
}

// detectShell returns the shell base name from $SHELL ("zsh", "bash", "fish"),
// or "" when it can't be determined.
func detectShell() string {
	base := filepath.Base(os.Getenv("SHELL"))
	switch base {
	case "zsh", "bash", "fish":
		return base
	}
	return ""
}

// writeCompletion regenerates the script via gen and writes it to dir/name,
// creating dir if needed. Overwriting is intentional: install == update.
func writeCompletion(dir, name string, gen func(w io.Writer) error) (string, error) {
	var buf bytes.Buffer
	if err := gen(&buf); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func installZshCompletion(root *cobra.Command) error {
	dir, onFpath := zshCompletionDir()
	path, err := writeCompletion(dir, "_rwt", root.GenZshCompletion)
	if err != nil {
		return err
	}
	fmt.Printf("✓ installed zsh completion: %s\n", path)
	if !onFpath {
		fmt.Printf("  add it to your fpath, e.g. in ~/.zshrc before compinit:\n    fpath=(%s $fpath)\n", dir)
	}
	fmt.Println("  reload: rm -f ~/.zcompdump* && exec zsh")
	return nil
}

func installBashCompletion(root *cobra.Command) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".local", "share", "bash-completion", "completions")
	path, err := writeCompletion(dir, "rwt", func(w io.Writer) error {
		return root.GenBashCompletionV2(w, true)
	})
	if err != nil {
		return err
	}
	fmt.Printf("✓ installed bash completion: %s\n", path)
	fmt.Println("  needs the bash-completion package; reload your shell to pick it up")
	return nil
}

func installFishCompletion(root *cobra.Command) error {
	dir := filepath.Join(completionConfigHome(), "fish", "completions")
	path, err := writeCompletion(dir, "rwt.fish", func(w io.Writer) error {
		return root.GenFishCompletion(w, true)
	})
	if err != nil {
		return err
	}
	fmt.Printf("✓ installed fish completion: %s\n", path)
	fmt.Println("  fish loads it automatically in new shells")
	return nil
}

// completionConfigHome resolves $XDG_CONFIG_HOME, defaulting to ~/.config.
func completionConfigHome() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

// zshCompletionDir picks a per-user directory for the _rwt completion. It
// prefers the first writable directory under $HOME already on the live zsh
// $fpath (so completions load with no extra config); onFpath reports whether
// the chosen directory is one of those. Falls back to ~/.zsh/completions, which
// the caller is told to add to fpath.
func zshCompletionDir() (dir string, onFpath bool) {
	home, _ := os.UserHomeDir()
	// Interactive zsh (-i) so ~/.zshrc — and frameworks like oh-my-zsh that add
	// their own completions dir to $fpath — are sourced; a non-interactive shell
	// would only show the system fpath.
	// Bound the probe: an interactive zsh sources ~/.zshrc, which could block.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "zsh", "-ic", "print -rl -- $fpath").Output(); err == nil && home != "" {
		var candidates []string
		for _, d := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			d = strings.TrimSpace(d)
			// Per-user, writable, and not a plugin's own or a generated cache dir
			// (those are managed by the framework, not a place for user scripts).
			if d == "" || !strings.HasPrefix(d, home) ||
				strings.Contains(d, "/plugins/") || strings.Contains(d, "/cache/") {
				continue
			}
			if isWritableDir(d) {
				candidates = append(candidates, d)
			}
		}
		// Prefer a dedicated completions/site-functions directory.
		for _, d := range candidates {
			if strings.HasSuffix(d, "completions") || strings.HasSuffix(d, "site-functions") {
				return d, true
			}
		}
		if len(candidates) > 0 {
			return candidates[0], true
		}
	}
	return filepath.Join(home, ".zsh", "completions"), false
}

// isWritableDir reports whether dir exists and the current user can create files
// in it (probed by writing a temp file, the only portable check).
func isWritableDir(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	f, err := os.CreateTemp(dir, ".rwt-wtest-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}
