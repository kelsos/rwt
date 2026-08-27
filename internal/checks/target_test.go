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
	want := filepath.Join("src", "a.spec.ts")
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %v, want just %q", got, want)
	}

	// No sibling: fall back to the specs beside it.
	write(t, filepath.Join(wt, "frontend", "app", "src", "c.ts"), "")
	planned, _ = Plan(wt, []string{"frontend/app/src/c.ts"}, []Tier{TierHeavy})
	got = appended(t, "vitest", find(t, planned, "vitest").Argv)
	if len(got) != 2 {
		t.Errorf("directory fallback should pick up both specs, got %v", got)
	}
}

// TestFrontendSpecIsRelativeToVitestRoot pins the path base. The check runs from
// frontend/, but test:unit is a workspace filter, so vitest starts in
// frontend/app. A path carrying the app/ prefix matches no test file, and vitest
// exits 1 on an empty filter, so getting this wrong red-gates every frontend
// push rather than failing quietly.
func TestFrontendSpecIsRelativeToVitestRoot(t *testing.T) {
	wt := develop(t)
	write(t, filepath.Join(wt, "frontend", "app", "src", "x.spec.ts"), "")
	planned, _ := Plan(wt, []string{"frontend/app/src/x.spec.ts"}, []Tier{TierHeavy})
	got := appended(t, "vitest", find(t, planned, "vitest").Argv)
	if len(got) != 1 || got[0] != "src/x.spec.ts" {
		t.Errorf("got %v, want a path relative to frontend/app", got)
	}
}

// TestFrontendSpecsIgnoreSiblingPackages: test:unit only reaches the app
// package. common has no specs, and dev-proxy's belong to test:proxy, so a
// change confined to either must drop the check instead of handing vitest a
// filter that matches nothing.
func TestFrontendSpecsIgnoreSiblingPackages(t *testing.T) {
	wt := develop(t)
	write(t, filepath.Join(wt, "frontend", "common", "src", "y.ts"), "")
	write(t, filepath.Join(wt, "frontend", "dev-proxy", "src", "z.spec.ts"), "")

	planned, _ := Plan(wt, []string{
		"frontend/common/src/y.ts",
		"frontend/dev-proxy/src/z.spec.ts",
	}, []Tier{TierHeavy})
	for _, c := range planned {
		if c.Name == "vitest" {
			t.Errorf("vitest should not be planned, got argv %v", c.Argv)
		}
	}
}
