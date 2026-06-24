package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelsos/rwt/internal/install"
)

// TestLifecycle drives new -> ls -> rm against a throwaway temp-git umbrella,
// with the heavy installer and dev:web --clean shell-outs stubbed. It asserts
// worktree state, the appended env line + dev flags, and idempotent re-runs.
func TestLifecycle(t *testing.T) {
	clearGitEnv(t) // hermetic even when run inside a git hook
	umbrella := setupUmbrella(t)
	t.Setenv("RWT_UMBRELLA", umbrella)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config file -> default flags on

	// Stub the env warmer and instance teardown so the test stays hermetic.
	origInstall, origClean := installRun, devWebClean
	installRun = func(context.Context, string, install.Opts) error { return nil }
	devWebClean = func(context.Context, string, string) error { return nil }
	t.Cleanup(func() { installRun, devWebClean = origInstall, origClean })

	// --- new ---
	if err := runCLI(t, "new", "demo", "--from", "develop"); err != nil {
		t.Fatalf("new: %v", err)
	}
	wt := filepath.Join(umbrella, "feat-demo")
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("worktree dir not created: %v", err)
	}

	envPath := filepath.Join(wt, "frontend", "app", ".env.development.local")
	env := readFile(t, envPath)
	for _, want := range []string{
		"INSTANCE_NAME=demo",    // capability detected -> appended
		"ENABLE_DEV_TOOLS=true", // dev flags default on
		"VITE_DEV_LOGS=true",
		"VITE_PERSIST_STORE=true",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %q in:\n%s", want, env)
		}
	}

	// --- new again: idempotent resume, INSTANCE_NAME not doubled ---
	if err := runCLI(t, "new", "demo", "--from", "develop"); err != nil {
		t.Fatalf("idempotent new: %v", err)
	}
	if n := strings.Count(readFile(t, envPath), "INSTANCE_NAME=demo"); n != 1 {
		t.Errorf("INSTANCE_NAME line count = %d, want 1", n)
	}

	// --- ls: the new worktree is listed ---
	if err := runCLI(t, "ls"); err != nil {
		t.Fatalf("ls: %v", err)
	}

	// --- rm: worktree dir gone ---
	if err := runCLI(t, "rm", "demo"); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree dir still present after rm: %v", err)
	}
}

// TestGuardRefusesWithoutUmbrella confirms umbrella-touching commands refuse
// when no location is configured, while config/doctor stay usable.
func TestGuardRefusesWithoutUmbrella(t *testing.T) {
	t.Setenv("RWT_UMBRELLA", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := runCLI(t, "ls"); err == nil {
		t.Error("expected ls to refuse without a configured umbrella")
	}
	if err := runCLI(t, "config"); err != nil {
		t.Errorf("config should work without an umbrella: %v", err)
	}
}

func TestClaudeMemoryDir(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	got := claudeMemoryDir("/home/u/dev/rotki/rotki/feat-demo")
	want := "/home/u/.claude/projects/-home-u-dev-rotki-rotki-feat-demo/memory"
	if got != want {
		t.Errorf("claudeMemoryDir = %q, want %q", got, want)
	}
	// Dots in the path are also slugged to '-'.
	got = claudeMemoryDir("/home/u/x/.claude-worktrees/foo")
	want = "/home/u/.claude/projects/-home-u-x--claude-worktrees-foo/memory"
	if got != want {
		t.Errorf("claudeMemoryDir (dotted) = %q, want %q", got, want)
	}
}

func TestPurgeClaudeMemory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wt := filepath.Join(home, "dev", "rotki", "rotki", "feat-gone")
	dir := claudeMemoryDir(wt)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	purgeClaudeMemory(wt)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("memory dir not purged: %v", err)
	}
}

// runCLI builds a fresh root command (cobra commands hold per-run arg state)
// and executes it with the given args.
func runCLI(t *testing.T, args ...string) error {
	t.Helper()
	root := newRootCmd()
	root.SetArgs(args)
	return root.Execute()
}

// setupUmbrella builds a throwaway umbrella: a bare "upstream" repo with a
// develop branch (carrying the dev-instance capability files + a .gitignore for
// the env file) and an umbrella/develop host worktree wired to it.
func setupUmbrella(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	upstream := filepath.Join(root, "upstream.git")
	umbrella := filepath.Join(root, "umbrella")

	gitRun(t, "", "init", "--bare", "-b", "develop", upstream)

	// Seed the develop branch through a temp clone.
	seed := filepath.Join(root, "seed")
	gitRun(t, "", "clone", "-q", upstream, seed)
	gitIdentity(t, seed)
	writeFile(t, filepath.Join(seed, "frontend/scripts/dev-instance/index.ts"), "// dev-instance\n")
	writeFile(t, filepath.Join(seed, "frontend/scripts/start-dev.ts"), "import './dev-instance';\n")
	writeFile(t, filepath.Join(seed, ".gitignore"), "frontend/app/.env.development.local\nnode_modules\n.venv\n")
	gitRun(t, seed, "add", "-A")
	gitRun(t, seed, "commit", "-q", "-m", "seed")
	gitRun(t, seed, "push", "-q", "origin", "develop")

	// Host worktree: clone, rename origin->upstream, fetch.
	develop := filepath.Join(umbrella, "develop")
	gitRun(t, "", "clone", "-q", upstream, develop)
	gitIdentity(t, develop)
	gitRun(t, develop, "remote", "rename", "origin", "upstream")
	gitRun(t, develop, "fetch", "-q", "upstream")
	return umbrella
}

// clearGitEnv unsets the git environment a parent process (notably a pre-commit
// hook) injects, so subprocess git calls operate on the temp repos rather than
// the caller's index/worktree. Restored after the test.
func clearGitEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_PREFIX",
		"GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
	} {
		if v, ok := os.LookupEnv(k); ok {
			os.Unsetenv(k)
			k, v := k, v
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Keep git hermetic regardless of the host's global config.
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitIdentity(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "rwt test")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
