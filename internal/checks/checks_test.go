package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture builds a worktree skeleton: a frontend/package.json with the given
// scripts, a root cargo workspace, and the backend lint tools.
func fixture(t *testing.T, scripts ...string) string {
	t.Helper()
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "frontend"))
	mkdirAll(t, filepath.Join(dir, "tools"))
	var quoted []string
	for _, s := range scripts {
		quoted = append(quoted, `"`+s+`": "x"`)
	}
	write(t, filepath.Join(dir, "frontend", "package.json"),
		`{"scripts": {`+strings.Join(quoted, ",")+`}}`)
	write(t, filepath.Join(dir, "Cargo.toml"), "[workspace]\nmembers = []\n")
	write(t, filepath.Join(dir, "tools", "lint_checksum_addresses.py"), "")
	write(t, filepath.Join(dir, "tools", "lint_new_logging_fstrings.py"), "")
	// A .venv with the lint group synced, which `rwt new` does not produce.
	for _, tool := range []string{"python", "ruff", "mypy", "double-indent"} {
		write(t, filepath.Join(dir, VenvBin, tool), "")
	}
	return dir
}

// develop is the fixture with the full script set current bases carry.
func develop(t *testing.T) string {
	t.Helper()
	return fixture(t, "lint-staged", "typecheck", "knip", "lint:style",
		"check:linked-keys", "test:proxy", "test:unit")
}

func names(planned []Check) []string {
	out := make([]string, 0, len(planned))
	for _, c := range planned {
		out = append(out, c.Name)
	}
	return out
}

func has(planned []Check, name string) bool {
	for _, c := range planned {
		if c.Name == name {
			return true
		}
	}
	return false
}

func find(t *testing.T, planned []Check, name string) Check {
	t.Helper()
	for _, c := range planned {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q in plan %v", name, names(planned))
	return Check{}
}

// TestPlanScopesToChangedGroups is the core promise: a change to one part of the
// repo does not drag in another part's gates.
func TestPlanScopesToChangedGroups(t *testing.T) {
	wt := develop(t)
	all := []Tier{TierFast, TierStandard}

	planned, _ := Plan(wt, []string{"frontend/app/src/foo.ts"}, all)
	if !has(planned, "lint-staged") || !has(planned, "knip") {
		t.Errorf("frontend change should plan the frontend gates, got %v", names(planned))
	}
	for _, unwanted := range []string{"ruff", "mypy", "clippy-colibri", "clippy-starling"} {
		if has(planned, unwanted) {
			t.Errorf("frontend change planned %s, got %v", unwanted, names(planned))
		}
	}

	planned, _ = Plan(wt, []string{"rotkehlchen/db/foo.py"}, all)
	if !has(planned, "ruff") || !has(planned, "mypy") {
		t.Errorf("backend change should plan the backend gates, got %v", names(planned))
	}
	if has(planned, "typecheck") || has(planned, "knip") {
		t.Errorf("backend change planned frontend gates, got %v", names(planned))
	}
}

// TestPlanCargoLockMovesBothRustGroups pins the one path that deliberately sits
// in two groups.
func TestPlanCargoLockMovesBothRustGroups(t *testing.T) {
	wt := develop(t)
	planned, _ := Plan(wt, []string{"Cargo.lock"}, []Tier{TierStandard})
	if !has(planned, "clippy-colibri") || !has(planned, "clippy-starling") {
		t.Errorf("Cargo.lock should plan both crates, got %v", names(planned))
	}
}

// TestPlanGlobalDbReachesColibri covers the non-obvious grouping CI carries a
// comment about: colibri reads the packaged global database, so an asset-only
// change has to run its gates.
func TestPlanGlobalDbReachesColibri(t *testing.T) {
	wt := develop(t)
	planned, _ := Plan(wt, []string{"rotkehlchen/data/global.db"}, []Tier{TierStandard})
	if !has(planned, "clippy-colibri") {
		t.Errorf("global.db should reach colibri, got %v", names(planned))
	}
}

// TestPlanSkipsChecksTheBaseLacks is why the plan is built from the worktree
// rather than hardcoded: bugfixes has no knip or check:linked-keys script, and
// planning them would produce a command-not-found on every commit there.
func TestPlanSkipsChecksTheBaseLacks(t *testing.T) {
	wt := fixture(t, "lint-staged", "typecheck", "lint:style")
	planned, skipped := Plan(wt, []string{"frontend/app/src/foo.ts"},
		[]Tier{TierFast, TierStandard})
	if has(planned, "knip") {
		t.Errorf("planned knip on a base without the script: %v", names(planned))
	}
	if !has(planned, "typecheck") {
		t.Errorf("dropped typecheck, which this base does have: %v", names(planned))
	}
	var reason string
	for _, s := range skipped {
		if s.Name == "knip" {
			reason = s.Reason
		}
	}
	if !strings.Contains(reason, "knip") {
		t.Errorf("knip skip should say which script is missing, got %q", reason)
	}
}

// TestPlanSkipsPythonChecksWithoutTheLintGroup is the case a hook must not get
// wrong: the lint tools are absent from a worktree `rwt new` warmed, and letting
// `uv run` resolve them would either install packages mid-commit or block the
// commit over a missing dependency rather than over the code.
func TestPlanSkipsPythonChecksWithoutTheLintGroup(t *testing.T) {
	wt := develop(t)
	if err := os.Remove(filepath.Join(wt, VenvBin, "ruff")); err != nil {
		t.Fatal(err)
	}
	planned, skipped := Plan(wt, []string{"rotkehlchen/a.py"}, []Tier{TierFast})
	if has(planned, "ruff") {
		t.Error("planned ruff with no ruff in the venv")
	}
	if !has(planned, "double-indent") {
		t.Errorf("one missing tool must not drop the others: %v", names(planned))
	}
	var reason string
	for _, s := range skipped {
		if s.Name == "ruff" {
			reason = s.Reason
		}
	}
	if !strings.Contains(reason, "--lint") {
		t.Errorf("the skip should name the command that fixes it, got %q", reason)
	}
}

// TestPlanSkipsRustWithoutAWorkspace covers a checkout that predates the crates.
func TestPlanSkipsRustWithoutAWorkspace(t *testing.T) {
	wt := develop(t)
	if err := os.Remove(filepath.Join(wt, "Cargo.toml")); err != nil {
		t.Fatal(err)
	}
	planned, skipped := Plan(wt, []string{"Cargo.lock"}, []Tier{TierStandard})
	if len(planned) != 0 {
		t.Errorf("planned rust checks with no workspace: %v", names(planned))
	}
	if len(skipped) == 0 {
		t.Error("skipping every rust check should be reported, not silent")
	}
}

// TestPlanAppendsOnlyMatchingFiles pins the file scoping: ruff gets the Python
// paths and nothing else.
func TestPlanAppendsOnlyMatchingFiles(t *testing.T) {
	wt := develop(t)
	changed := []string{"rotkehlchen/a.py", "rotkehlchen/data/notes.txt", "frontend/app/b.ts"}
	planned, _ := Plan(wt, changed, []Tier{TierFast})

	ruff := find(t, planned, "ruff")
	if got := appended(t, "ruff", ruff.Argv); len(got) != 1 || got[0] != "rotkehlchen/a.py" {
		t.Errorf("ruff got paths %v, want only the .py one", got)
	}
	// typos is repo-wide, so it gets everything.
	typos := find(t, planned, "typos")
	if got := appended(t, "typos", typos.Argv); len(got) != len(changed) {
		t.Errorf("typos should get every changed path, got %v", got)
	}
}

// appended returns the paths the planner added to a check, by diffing the
// planned Argv against the catalog entry it came from.
func appended(t *testing.T, name string, argv []string) []string {
	t.Helper()
	for _, c := range Catalog() {
		if c.Name == name {
			return argv[len(c.Argv):]
		}
	}
	t.Fatalf("no catalog entry named %q", name)
	return nil
}

// TestPlanFastTierIsPreCommitSized guards the tier boundary: nothing slow may
// drift into the stage that runs on every commit.
func TestPlanFastTierIsPreCommitSized(t *testing.T) {
	slow := map[string]bool{"typecheck": true, "knip": true, "mypy": true,
		"vitest": true, "pytest": true}
	for _, c := range Catalog() {
		if c.Tier == TierFast && slow[c.Name] {
			t.Errorf("%s is in the fast tier, which runs on every commit", c.Name)
		}
	}
}

// TestReproduceIncludesCwdAndEnv checks the line a blocked commit prints is
// actually runnable.
func TestReproduceIncludesCwdAndEnv(t *testing.T) {
	got := Reproduce(Check{Dir: "frontend", Argv: []string{"pnpm", "run", "knip"}})
	if got != "cd frontend && pnpm run knip" {
		t.Errorf("got %q", got)
	}
	got = Reproduce(Check{Dir: ".", Env: []string{"RUSTFLAGS=-Dwarnings"},
		Argv: []string{"cargo", "clippy"}})
	if got != "RUSTFLAGS=-Dwarnings cargo clippy" {
		t.Errorf("got %q", got)
	}
}

func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	mkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
