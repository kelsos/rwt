package install

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeCIFiles(t *testing.T, worktree, env, workflow string) {
	t.Helper()
	for path, body := range map[string]string{ciEnvFile: env, ciWorkflowFile: workflow} {
		full := filepath.Join(worktree, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func uvArgv(t *testing.T, worktree string) []string {
	t.Helper()
	for _, s := range DefaultSteps(worktree) {
		if s.Name == "uv" {
			return s.Argv
		}
	}
	t.Fatal("no uv step in DefaultSteps")
	return nil
}

// TestUvFollowsAFreeThreadedCI covers develop: its CI runs free-threaded, so the
// worktree must too, or it tests against a build CI never runs.
func TestUvFollowsAFreeThreadedCI(t *testing.T) {
	wt := t.TempDir()
	writeCIFiles(t, wt, "PYTHON_VERSION=3.14.4\n", "        with:\n          freethreaded: true\n")

	argv := uvArgv(t, wt)
	if i := slices.Index(argv, "--python"); i < 0 || argv[i+1] != "3.14t" {
		t.Errorf("uv argv %v, want --python 3.14t", argv)
	}
}

// TestUvKeepsTheGILWhereCIDoes covers bugfixes: its CI uses the regular build,
// and it locks packages with no free-threaded wheel. A bare request lets uv pick
// its newer free-threaded build and the source build fails, so the request must
// exclude it.
func TestUvKeepsTheGILWhereCIDoes(t *testing.T) {
	wt := t.TempDir()
	writeCIFiles(t, wt, "PYTHON_VERSION=3.14.4\n", "        with:\n          python-version: x\n")

	argv := uvArgv(t, wt)
	if i := slices.Index(argv, "--python"); i < 0 || argv[i+1] != "3.14+gil" {
		t.Errorf("uv argv %v, want --python 3.14+gil", argv)
	}
}

// TestUvLeavesTheChoiceAloneWithoutCIFiles: with nothing to match, rwt must not
// guess an interpreter.
func TestUvLeavesTheChoiceAloneWithoutCIFiles(t *testing.T) {
	if argv := uvArgv(t, t.TempDir()); slices.Contains(argv, "--python") {
		t.Errorf("uv argv %v should carry no --python without CI files", argv)
	}
}
