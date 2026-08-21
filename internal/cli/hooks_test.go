package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelsos/rwt/internal/checks"
	"github.com/kelsos/rwt/internal/hooks"
	"github.com/kelsos/rwt/internal/install"
)

// stubChecks replaces the runner so the tests exercise planning and hook
// plumbing without shelling out to eslint or cargo. Same pattern as installRun.
func stubChecks(t *testing.T) *[]checks.Check {
	t.Helper()
	var seen []checks.Check
	orig := checksRun
	checksRun = func(_ context.Context, _ string, planned []checks.Check, _ io.Writer) ([]checks.Result, error) {
		seen = append(seen, planned...)
		results := make([]checks.Result, len(planned))
		for i, c := range planned {
			results[i] = checks.Result{Check: c}
		}
		return results, nil
	}
	t.Cleanup(func() { checksRun = orig })
	return &seen
}

// hooksUmbrella is the shared setup: a throwaway umbrella with the heavy env
// warmer stubbed out.
func hooksUmbrella(t *testing.T) string {
	t.Helper()
	clearGitEnv(t)
	umbrella := setupUmbrella(t)
	t.Setenv("RWT_UMBRELLA", umbrella)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	orig := installRun
	installRun = func(context.Context, string, install.Opts) error { return nil }
	t.Cleanup(func() { installRun = orig })
	return umbrella
}

// TestHooksInstallTakesOverHooksPath covers the install/uninstall round trip,
// including the part that matters most: whatever core.hooksPath held before is
// recorded, not lost.
func TestHooksInstallTakesOverHooksPath(t *testing.T) {
	umbrella := hooksUmbrella(t)
	develop := filepath.Join(umbrella, "develop")
	ctx := context.Background()

	// Stand in for husky: a hooksPath that was set but points nowhere, which is
	// how git ends up silently running no hooks at all.
	dead := filepath.Join(umbrella, "gone", ".husky")
	gitRun(t, develop, "config", "--local", "core.hooksPath", dead)

	state, err := hooks.Install(ctx, develop, false)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !state.Installed {
		t.Fatal("install reported not installed")
	}
	if got := gitOut(t, develop, "config", "--local", "--get", "core.hooksPath"); got != state.Dir {
		t.Errorf("core.hooksPath = %q, want %q", got, state.Dir)
	}
	if state.Previous != dead {
		t.Errorf("previous = %q, want the displaced %q", state.Previous, dead)
	}
	if !state.PreviousBroken {
		t.Error("a hooksPath pointing at a missing dir should be reported as broken")
	}
	for _, stage := range hooks.Stages {
		body, err := os.ReadFile(filepath.Join(state.Dir, stage))
		if err != nil {
			t.Fatalf("read %s shim: %v", stage, err)
		}
		if !strings.Contains(string(body), "rwt hooks run "+stage) {
			t.Errorf("%s shim does not exec rwt: %s", stage, body)
		}
		if !strings.Contains(string(body), "RWT_HOOKS") {
			t.Errorf("%s shim has no bypass: %s", stage, body)
		}
	}

	if err := hooks.Uninstall(ctx, develop); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if got := gitOut(t, develop, "config", "--local", "--get", "core.hooksPath"); got != dead {
		t.Errorf("uninstall left core.hooksPath = %q, want the original %q", got, dead)
	}
}

// TestHooksInstallRefusesALiveHooksPath: an existing hook mechanism is somebody
// else's, and taking it over silently is how you break it.
func TestHooksInstallRefusesALiveHooksPath(t *testing.T) {
	umbrella := hooksUmbrella(t)
	develop := filepath.Join(umbrella, "develop")

	live := filepath.Join(umbrella, "live-hooks")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, develop, "config", "--local", "core.hooksPath", live)

	if _, err := hooks.Install(context.Background(), develop, false); err == nil {
		t.Fatal("expected install to refuse a hooksPath that exists")
	}
	if _, err := hooks.Install(context.Background(), develop, true); err != nil {
		t.Fatalf("--force install: %v", err)
	}
}

// TestHooksOptOutIsPerWorktree: install is umbrella-wide by construction, so the
// opt-out has to be the thing that is per-worktree.
func TestHooksOptOutIsPerWorktree(t *testing.T) {
	umbrella := hooksUmbrella(t)
	ctx := context.Background()
	develop := filepath.Join(umbrella, "develop")
	if err := runCLI(t, "new", "gated"); err != nil {
		t.Fatalf("new: %v", err)
	}
	feature := filepath.Join(umbrella, "feat-gated")

	if _, err := hooks.Install(ctx, develop, false); err != nil {
		t.Fatalf("install: %v", err)
	}
	if hooks.OptedOut(ctx, feature) || hooks.OptedOut(ctx, develop) {
		t.Fatal("nothing should be opted out right after install")
	}
	if err := hooks.SetOptOut(ctx, feature, true); err != nil {
		t.Fatalf("opt out: %v", err)
	}
	if !hooks.OptedOut(ctx, feature) {
		t.Error("feature worktree should be opted out")
	}
	if hooks.OptedOut(ctx, develop) {
		t.Error("opting one worktree out must not touch another")
	}
	// The marker lives in git's own per-worktree dir, so it never shows up as
	// an untracked file needing a gitignore entry.
	if status := gitOut(t, feature, "status", "--porcelain"); status != "" {
		t.Errorf("opt-out marker dirtied the worktree: %q", status)
	}
	if err := hooks.SetOptOut(ctx, feature, false); err != nil {
		t.Fatalf("opt back in: %v", err)
	}
	if hooks.OptedOut(ctx, feature) {
		t.Error("opting back in should remove the marker")
	}
}

// TestCheckPlansFromStagedFiles drives `rwt check` end to end at the pre-commit
// stage and asserts the plan follows what is actually staged.
func TestCheckPlansFromStagedFiles(t *testing.T) {
	umbrella := hooksUmbrella(t)
	if err := runCLI(t, "new", "staged"); err != nil {
		t.Fatalf("new: %v", err)
	}
	wt := filepath.Join(umbrella, "feat-staged")
	seedCheckable(t, wt)

	seen := stubChecks(t)
	writeFile(t, filepath.Join(wt, "rotkehlchen", "thing.py"), "x = 1\n")
	gitRun(t, wt, "add", "rotkehlchen/thing.py")

	if err := runCLI(t, "check", wt, "--stage", "pre-commit"); err != nil {
		t.Fatalf("check: %v", err)
	}
	if !planHas(*seen, "ruff") {
		t.Errorf("staged .py should plan ruff, got %v", planNames(*seen))
	}
	if planHas(*seen, "lint-staged") {
		t.Errorf("staged .py should not plan the frontend gates, got %v", planNames(*seen))
	}
	for _, c := range *seen {
		if c.Tier != checks.TierFast {
			t.Errorf("pre-commit planned a %s check (%s)", c.Tier, c.Name)
		}
	}
}

// TestCheckPreCommitIgnoresUnstagedWork pins the scope: pre-commit gates the
// index, because that is what the commit will contain.
func TestCheckPreCommitIgnoresUnstagedWork(t *testing.T) {
	umbrella := hooksUmbrella(t)
	if err := runCLI(t, "new", "unstaged"); err != nil {
		t.Fatalf("new: %v", err)
	}
	wt := filepath.Join(umbrella, "feat-unstaged")
	seedCheckable(t, wt)

	seen := stubChecks(t)
	writeFile(t, filepath.Join(wt, "rotkehlchen", "loose.py"), "x = 1\n")

	if err := runCLI(t, "check", wt, "--stage", "pre-commit"); err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(*seen) != 0 {
		t.Errorf("unstaged work should plan nothing, got %v", planNames(*seen))
	}
}

// seedCheckable gives a fixture worktree the files the planner detects against.
func seedCheckable(t *testing.T, wt string) {
	t.Helper()
	writeFile(t, filepath.Join(wt, "frontend", "package.json"),
		`{"scripts": {"lint-staged": "x", "typecheck": "x", "knip": "x"}}`)
	writeFile(t, filepath.Join(wt, "tools", "lint_checksum_addresses.py"), "")
	// The Python checks gate on their tool being in the worktree's venv.
	for _, tool := range []string{"python", "ruff", "double-indent"} {
		writeFile(t, filepath.Join(wt, checks.VenvBin, tool), "")
	}
	gitRun(t, wt, "add", "-A")
	gitRun(t, wt, "commit", "-q", "-m", "seed")
}

func planNames(planned []checks.Check) []string {
	out := make([]string, 0, len(planned))
	for _, c := range planned {
		out = append(out, c.Name)
	}
	return out
}

func planHas(planned []checks.Check, name string) bool {
	for _, c := range planned {
		if c.Name == name {
			return true
		}
	}
	return false
}
