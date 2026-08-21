package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestDetectShell(t *testing.T) {
	cases := map[string]string{
		"/usr/bin/zsh":  "zsh",
		"/bin/bash":     "bash",
		"/usr/bin/fish": "fish",
		"/bin/sh":       "",
		"":              "",
	}
	for shellPath, want := range cases {
		t.Setenv("SHELL", shellPath)
		if got := detectShell(); got != want {
			t.Errorf("detectShell(SHELL=%q) = %q, want %q", shellPath, got, want)
		}
	}
}

func TestCompletionConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/cfg")
	if got := completionConfigHome(); got != "/custom/cfg" {
		t.Errorf("completionConfigHome with XDG set = %q", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := completionConfigHome(); got != filepath.Join(home, ".config") {
		t.Errorf("completionConfigHome default = %q", got)
	}
}

func TestCompleteConfigArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		want      []string
		wantDirs  bool
		wantEmpty bool
	}{
		{name: "settings", args: nil, want: []string{"path", "demo", "hooks", "dev-tools", "logs", "persist"}},
		{name: "demo modes", args: []string{"demo"}, want: []string{"off", "auto", "minor", "patch"}},
		{name: "hooks modes", args: []string{"hooks"}, want: []string{"full", "standard"}},
		{name: "flag states", args: []string{"logs"}, want: []string{"on", "off"}},
		{name: "path takes a dir", args: []string{"path"}, wantDirs: true},
		{name: "unknown setting", args: []string{"nope"}, wantEmpty: true},
		{name: "past the last arg", args: []string{"demo", "auto"}, wantEmpty: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, directive := completeConfigArgs(nil, tc.args, "")
			if tc.wantDirs {
				if directive != cobra.ShellCompDirectiveFilterDirs {
					t.Errorf("directive = %v, want FilterDirs", directive)
				}
				return
			}
			if tc.wantEmpty {
				if len(got) != 0 {
					t.Errorf("got %v, want no completions", got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i, want := range tc.want {
				if got[i] != want {
					t.Errorf("got[%d] = %q, want %q (full: %v)", i, got[i], want, got)
				}
			}
		})
	}
}

// fakeRoot is a minimal command that can generate completion scripts.
func fakeRoot() *cobra.Command {
	c := &cobra.Command{Use: "rwt"}
	c.AddCommand(&cobra.Command{Use: "new", Run: func(*cobra.Command, []string) {}})
	return c
}

func TestInstallFishCompletion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	if err := installFishCompletion(fakeRoot()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".config", "fish", "completions", "rwt.fish")
	assertNonEmptyFile(t, path)
}

func TestInstallBashCompletion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := installBashCompletion(fakeRoot()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".local", "share", "bash-completion", "completions", "rwt")
	assertNonEmptyFile(t, path)
}

// Under a temp HOME the chosen zsh dir can't be on any real $fpath, so install
// must fall back to ~/.zsh/completions and still write the file.
func TestInstallZshCompletionFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := installZshCompletion(fakeRoot()); err != nil {
		t.Fatal(err)
	}
	assertNonEmptyFile(t, filepath.Join(home, ".zsh", "completions", "_rwt"))
}

// Re-running install overwrites in place — install and update are the same op.
func TestInstallIsIdempotentUpdate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".zsh", "completions", "_rwt")
	if err := installZshCompletion(fakeRoot()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installZshCompletion(fakeRoot()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == "stale" {
		t.Error("re-install should regenerate (update) the file, not leave it stale")
	}
}

func TestIsWritableDir(t *testing.T) {
	dir := t.TempDir()
	if !isWritableDir(dir) {
		t.Error("temp dir should be writable")
	}
	if isWritableDir(filepath.Join(dir, "does-not-exist")) {
		t.Error("missing dir should not be writable")
	}
	f := filepath.Join(dir, "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isWritableDir(f) {
		t.Error("a file is not a writable dir")
	}
}

func assertNonEmptyFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected file at %s: %v", path, err)
	}
	if info.Size() == 0 {
		t.Errorf("completion file %s is empty", path)
	}
}
