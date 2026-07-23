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
