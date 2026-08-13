package install

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunSkipsAbsentSubproject covers a base that predates a subproject: the
// step's SkipIfAbsent manifest is missing, so the step is warned about and
// skipped rather than run or failed. Its tool is also left out of the PATH
// pre-flight, so a bogus command never executes.
func TestRunSkipsAbsentSubproject(t *testing.T) {
	wt := t.TempDir()
	var out bytes.Buffer

	step := Step{
		Name:         "starling",
		Dir:          ".",
		Argv:         []string{"definitely-not-a-real-binary-xyz"},
		SkipIfAbsent: "crates/Cargo.toml",
	}
	if err := Run(context.Background(), wt, Opts{Steps: []Step{step}, Stdout: &out}); err != nil {
		t.Fatalf("Run should skip the absent subproject, got: %v", err)
	}
	if !strings.Contains(out.String(), "[starling] skipped") {
		t.Errorf("expected a skip notice, got:\n%s", out.String())
	}
}

// TestRunKeepsPresentSubproject is the control: when the manifest exists the
// step is kept, so the PATH pre-flight flags its missing tool instead of
// silently skipping it.
func TestRunKeepsPresentSubproject(t *testing.T) {
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, "crates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "crates", "Cargo.toml"), []byte("[workspace]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer

	step := Step{
		Name:         "starling",
		Dir:          ".",
		Argv:         []string{"definitely-not-a-real-binary-xyz"},
		SkipIfAbsent: "crates/Cargo.toml",
	}
	err := Run(context.Background(), wt, Opts{Steps: []Step{step}, Stdout: &out})
	if err == nil {
		t.Fatal("Run should keep the present subproject and fail its PATH pre-flight")
	}
	if !strings.Contains(err.Error(), "not on PATH") {
		t.Errorf("expected a PATH pre-flight error, got: %v", err)
	}
}

// TestDefaultStepsRunCargoFromTheWorkspaceRoot is the regression behind the
// shared cargo cache: cargo discovers .cargo/config.toml by walking up from its
// cwd, so a step that stays at the worktree root and points --manifest-path at a
// workspace never sees that workspace's config. Each Rust step must run with the
// workspace root as its Dir.
func TestDefaultStepsRunCargoFromTheWorkspaceRoot(t *testing.T) {
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, "colibri"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "colibri", "Cargo.toml"), []byte("[package]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var cargo []Step
	for _, s := range DefaultSteps(wt) {
		if s.Argv[0] == "cargo" {
			cargo = append(cargo, s)
		}
	}
	if len(cargo) != 1 {
		t.Fatalf("expected one cargo step for the colibri-only layout, got %+v", cargo)
	}
	if cargo[0].Dir != "colibri" {
		t.Errorf("cargo step Dir = %q, want \"colibri\" so the config is discoverable", cargo[0].Dir)
	}
	for _, arg := range cargo[0].Argv {
		if arg == "--manifest-path" {
			t.Errorf("cargo step uses --manifest-path, which bypasses config discovery: %v", cargo[0].Argv)
		}
	}
}

// TestDefaultStepsFollowTheRootWorkspace covers the current layout: one cargo
// step at the worktree root building both members, matching what pnpm dev:web
// runs so the two agree on cargo's feature unification.
func TestDefaultStepsFollowTheRootWorkspace(t *testing.T) {
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "Cargo.toml"), []byte("[workspace]\nmembers = [\"colibri\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var cargo []Step
	for _, s := range DefaultSteps(wt) {
		if s.Argv[0] == "cargo" {
			cargo = append(cargo, s)
		}
	}
	if len(cargo) != 1 || cargo[0].Dir != "." {
		t.Fatalf("expected one cargo step at the worktree root, got %+v", cargo)
	}
	joined := strings.Join(cargo[0].Argv, " ")
	if !strings.Contains(joined, "-p colibri") || !strings.Contains(joined, "-p starling") {
		t.Errorf("root-workspace step should build both members, got %q", joined)
	}
}

// TestOnlySelectsByEcosystem is the point of the Eco tag: a Rust selector has to
// keep working across both layouts, where the step's Name is "rotki" on one and
// "colibri"/"crates" on the other.
func TestOnlySelectsByEcosystem(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Cargo.toml"), []byte("[workspace]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	split := t.TempDir()
	for _, dir := range []string{"colibri", "crates"} {
		if err := os.MkdirAll(filepath.Join(split, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(split, dir, "Cargo.toml"), []byte("[package]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		wt   string
		want int
	}{{root, 1}, {split, 2}} {
		for _, sel := range []string{"cargo", "rust", "colibri", "starling"} {
			got, err := Only(DefaultSteps(tc.wt), []string{sel})
			if err != nil {
				t.Fatalf("Only(%q): %v", sel, err)
			}
			if len(got) != tc.want {
				t.Errorf("Only(%q) returned %d steps, want %d", sel, len(got), tc.want)
			}
			for _, s := range got {
				if s.Argv[0] != "cargo" {
					t.Errorf("Only(%q) included a non-cargo step %q", sel, s.Name)
				}
			}
		}
	}

	got, err := Only(DefaultSteps(root), []string{"pnpm", "uv"})
	if err != nil {
		t.Fatalf("Only(pnpm, uv): %v", err)
	}
	if len(got) != 2 || got[0].Name != "pnpm" || got[1].Name != "uv" {
		t.Errorf("Only(pnpm, uv) = %+v, want the two non-cargo steps in order", got)
	}
}

// TestOnlyRejectsEmptySelections keeps a narrowed run from succeeding without
// building anything: both a typo and an ecosystem this worktree lacks must say
// so rather than exit 0.
func TestOnlyRejectsEmptySelections(t *testing.T) {
	wt := t.TempDir() // no Cargo.toml anywhere -> no cargo step

	if _, err := Only(DefaultSteps(wt), []string{"cargo"}); err == nil {
		t.Error("Only(cargo) on a worktree with no cargo workspace should error")
	}
	if _, err := Only(DefaultSteps(wt), []string{"carg"}); err == nil {
		t.Error("Only should reject an unknown selector")
	}
}
