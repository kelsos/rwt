package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelsos/rwt/internal/install"
	"github.com/kelsos/rwt/internal/rotki"
)

// TestDemoMode drives `new --demo` end to end against a throwaway umbrella
// whose bugfixes base has genuinely diverged from develop, which is what makes
// the auto derivation a real test rather than a coin flip.
func TestDemoMode(t *testing.T) {
	clearGitEnv(t)
	umbrella := setupUmbrella(t)
	t.Setenv("RWT_UMBRELLA", umbrella)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	origInstall := installRun
	installRun = func(context.Context, string, install.Opts) error { return nil }
	t.Cleanup(func() { installRun = origInstall })

	seedBugfixes(t, umbrella)

	tests := []struct {
		name string
		from string
		demo string
		want string // "" means the key must be absent
	}{
		{name: "auto-develop", from: "develop", demo: "auto", want: "minor"},
		{name: "auto-bugfixes", from: "bugfixes", demo: "auto", want: "patch"},
		{name: "pinned", from: "develop", demo: "patch", want: "patch"},
		{name: "explicit-off", from: "develop", demo: "off", want: ""},
		{name: "unset", from: "develop", demo: "", want: ""}, // config default is off
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"new", tc.name, "--from", tc.from}
			if tc.demo != "" {
				args = append(args, "--demo", tc.demo)
			}
			if err := runCLI(t, args...); err != nil {
				t.Fatalf("new: %v", err)
			}
			assertDemo(t, worktreeFor(umbrella, tc.from, tc.name), tc.want)
		})
	}

	// An invalid mode is rejected before anything is created.
	if err := runCLI(t, "new", "bogus", "--demo", "major"); err == nil {
		t.Error("expected --demo major to be rejected")
	}
}

// TestDemoModeFromConfig covers the persisted default: set it once, and every
// later worktree picks it up with no flag.
func TestDemoModeFromConfig(t *testing.T) {
	clearGitEnv(t)
	umbrella := setupUmbrella(t)
	t.Setenv("RWT_UMBRELLA", umbrella)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	origInstall := installRun
	installRun = func(context.Context, string, install.Opts) error { return nil }
	t.Cleanup(func() { installRun = origInstall })

	seedBugfixes(t, umbrella)

	if err := runCLI(t, "config", "demo", "auto"); err != nil {
		t.Fatalf("config demo: %v", err)
	}
	if err := runCLI(t, "new", "from-config", "--from", "bugfixes"); err != nil {
		t.Fatalf("new: %v", err)
	}
	assertDemo(t, worktreeFor(umbrella, "bugfixes", "from-config"), "patch")

	// The flag still wins over the persisted mode for a single run.
	if err := runCLI(t, "new", "overridden", "--from", "bugfixes", "--demo", "off"); err != nil {
		t.Fatalf("new: %v", err)
	}
	assertDemo(t, worktreeFor(umbrella, "bugfixes", "overridden"), "")
}

// seedBugfixes gives the fixture umbrella a bugfixes base that has diverged
// from develop in both directions, mirroring rotki: each carries commits the
// other does not.
func seedBugfixes(t *testing.T, umbrella string) {
	t.Helper()
	develop := filepath.Join(umbrella, "develop")

	gitRun(t, develop, "checkout", "-q", "-b", "bugfixes")
	writeFile(t, filepath.Join(develop, "bugfix.txt"), "b\n")
	gitRun(t, develop, "add", "-A")
	gitRun(t, develop, "commit", "-q", "-m", "bugfix")
	gitRun(t, develop, "push", "-q", "upstream", "bugfixes")

	gitRun(t, develop, "checkout", "-q", "develop")
	writeFile(t, filepath.Join(develop, "feature.txt"), "d\n")
	gitRun(t, develop, "add", "-A")
	gitRun(t, develop, "commit", "-q", "-m", "feature")
	gitRun(t, develop, "push", "-q", "upstream", "develop")

	gitRun(t, develop, "branch", "-q", "-D", "bugfixes")
	gitRun(t, develop, "fetch", "-q", "upstream")
}

func worktreeFor(umbrella, from, name string) string {
	return filepath.Join(umbrella, rotki.BranchPrefix[from]+"-"+name)
}

func assertDemo(t *testing.T, wt, want string) {
	t.Helper()
	envPath := filepath.Join(wt, rotki.EnvFileRel)
	data, err := os.ReadFile(envPath)
	if err != nil {
		if want == "" && os.IsNotExist(err) {
			return
		}
		t.Fatalf("read env: %v", err)
	}
	env := string(data)
	if want == "" {
		if strings.Contains(env, rotki.DemoKey) {
			t.Errorf("expected %s absent, got:\n%s", rotki.DemoKey, env)
		}
		return
	}
	if line := rotki.DemoKey + "=" + want; !strings.Contains(env, line) {
		t.Errorf("expected %q in:\n%s", line, env)
	}
}
