package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// commit writes a file and records a commit on the current branch, with
// identity passed inline so the test needs no global git config.
func commit(t *testing.T, dir, file, msg string) {
	t.Helper()
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(msg), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(ctx, dir, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(ctx, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", msg); err != nil {
		t.Fatal(err)
	}
}

func TestIsAncestor(t *testing.T) {
	clearGitEnv(t) // hermetic even when run inside a git hook
	dir := t.TempDir()
	ctx := context.Background()
	if _, err := run(ctx, dir, "init", "-b", "master"); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "a.txt", "c0")

	// feat branches off, gets a commit, and is fast-forward-merged into master:
	// feat is now an ancestor of master (== merged).
	if _, err := run(ctx, dir, "checkout", "-b", "feat"); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "b.txt", "c1")
	if _, err := run(ctx, dir, "checkout", "master"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(ctx, dir, "merge", "--ff-only", "feat"); err != nil {
		t.Fatal(err)
	}

	// wip has its own commit not in master: not an ancestor (== not merged).
	if _, err := run(ctx, dir, "checkout", "-b", "wip"); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "c.txt", "c2")

	if !IsAncestor(ctx, dir, "feat", "master") {
		t.Error("feat should be an ancestor of master (merged)")
	}
	if IsAncestor(ctx, dir, "wip", "master") {
		t.Error("wip should NOT be an ancestor of master (unmerged)")
	}
	if IsAncestor(ctx, dir, "feat", "does-not-exist") {
		t.Error("missing descendant ref should yield false, not panic")
	}
}

// clearGitEnv unsets the git env vars a parent `git commit` (e.g. the pre-commit
// hook) exports, so a throwaway repo in this test isn't redirected at the real
// one. Restored on cleanup.
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
