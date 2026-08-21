package checks

import (
	"path/filepath"
	"testing"
)

// TestHeavyTierNeverWidensToASuite is the guard that keeps a push cheap: a
// changed file with no matching test drops the check entirely rather than
// falling back to running everything.
func TestHeavyTierNeverWidensToASuite(t *testing.T) {
	wt := develop(t)
	write(t, filepath.Join(wt, "rotkehlchen", "chain", "evm.py"), "")

	planned, skipped := Plan(wt, []string{"rotkehlchen/chain/evm.py"}, []Tier{TierHeavy})
	if has(planned, "pytest") {
		t.Fatalf("planned pytest with no matching test file: %v",
			find(t, planned, "pytest").Argv)
	}
	var reason string
	for _, s := range skipped {
		if s.Name == "pytest" {
			reason = s.Reason
		}
	}
	if reason == "" {
		t.Error("dropping pytest should be reported so the gap is visible")
	}
}

// TestBackendTestsMapByName covers both backend mappings: a changed test file
// runs itself, and a changed module finds its same-named test.
func TestBackendTestsMapByName(t *testing.T) {
	wt := develop(t)
	write(t, filepath.Join(wt, "rotkehlchen", "db", "settings.py"), "")
	write(t, filepath.Join(wt, "rotkehlchen", "tests", "unit", "test_settings.py"), "")
	write(t, filepath.Join(wt, "rotkehlchen", "tests", "api", "test_assets.py"), "")

	planned, _ := Plan(wt, []string{"rotkehlchen/db/settings.py"}, []Tier{TierHeavy})
	pytest := find(t, planned, "pytest")
	if got := appended(t, "pytest", pytest.Argv); len(got) != 1 ||
		got[0] != filepath.Join("rotkehlchen", "tests", "unit", "test_settings.py") {
		t.Errorf("module should map to its same-named test, got %v", got)
	}

	planned, _ = Plan(wt, []string{"rotkehlchen/tests/api/test_assets.py"}, []Tier{TierHeavy})
	pytest = find(t, planned, "pytest")
	if got := appended(t, "pytest", pytest.Argv); len(got) != 1 ||
		got[0] != "rotkehlchen/tests/api/test_assets.py" {
		t.Errorf("a changed test should run itself, got %v", got)
	}
}

// TestFrontendSpecsPreferTheSibling covers the spec resolution order: the
// sibling spec wins, and only when there is none does the directory sweep run.
func TestFrontendSpecsPreferTheSibling(t *testing.T) {
	wt := develop(t)
	write(t, filepath.Join(wt, "frontend", "app", "src", "a.ts"), "")
	write(t, filepath.Join(wt, "frontend", "app", "src", "a.spec.ts"), "")
	write(t, filepath.Join(wt, "frontend", "app", "src", "b.spec.ts"), "")

	planned, _ := Plan(wt, []string{"frontend/app/src/a.ts"}, []Tier{TierHeavy})
	got := appended(t, "vitest", find(t, planned, "vitest").Argv)
	want := filepath.Join("app", "src", "a.spec.ts")
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %v, want just %q", got, want)
	}

	// No sibling: fall back to the specs beside it, paths relative to frontend/.
	write(t, filepath.Join(wt, "frontend", "app", "src", "c.ts"), "")
	planned, _ = Plan(wt, []string{"frontend/app/src/c.ts"}, []Tier{TierHeavy})
	got = appended(t, "vitest", find(t, planned, "vitest").Argv)
	if len(got) != 2 {
		t.Errorf("directory fallback should pick up both specs, got %v", got)
	}
}

// TestFrontendSpecIsRelativeToFrontend pins the path base. The test:unit script
// pins its cwd to frontend/, so a repo-root path would not resolve.
func TestFrontendSpecIsRelativeToFrontend(t *testing.T) {
	wt := develop(t)
	write(t, filepath.Join(wt, "frontend", "app", "src", "x.spec.ts"), "")
	planned, _ := Plan(wt, []string{"frontend/app/src/x.spec.ts"}, []Tier{TierHeavy})
	got := appended(t, "vitest", find(t, planned, "vitest").Argv)
	if len(got) != 1 || got[0] != "app/src/x.spec.ts" {
		t.Errorf("got %v, want a path relative to frontend/", got)
	}
}
