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

func TestHasUnpushed(t *testing.T) {
	clearGitEnv(t)
	ctx := context.Background()

	origin := t.TempDir()
	if _, err := run(ctx, origin, "init", "--bare", "-b", "master"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := run(ctx, dir, "clone", origin, "."); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "a.txt", "c0")
	if _, err := run(ctx, dir, "push", "-u", "origin", "master"); err != nil {
		t.Fatal(err)
	}

	// pushed: fully on the remote. wip: a local-only commit. In a real setup
	// these live in sibling worktrees sharing this one repo.
	if _, err := run(ctx, dir, "checkout", "-b", "pushed"); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "b.txt", "c1")
	if _, err := run(ctx, dir, "push", "-u", "origin", "pushed"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(ctx, dir, "checkout", "-b", "wip", "master"); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "c.txt", "c2")

	// The regression: an unpushed `wip` elsewhere in the repo must not make a
	// fully-pushed branch look unpushed.
	if HasUnpushed(ctx, dir, "pushed") {
		t.Error("pushed branch should not report unpushed commits")
	}
	if !HasUnpushed(ctx, dir, "wip") {
		t.Error("wip branch should report unpushed commits")
	}
	if HasUnpushed(ctx, dir, "") {
		t.Error("empty branch should yield false, not scan the whole repo")
	}
	if HasUnpushed(ctx, dir, "does-not-exist") {
		t.Error("missing ref should yield false")
	}
}

func TestHasEquivalentUpstream(t *testing.T) {
	clearGitEnv(t)
	dir := t.TempDir()
	ctx := context.Background()
	if _, err := run(ctx, dir, "init", "-b", "master"); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "a.txt", "c0")

	// rebased: one commit, replayed onto a moved master. Upstream holds the same
	// patch under a different SHA, so ancestry fails but patch equivalence holds.
	if _, err := run(ctx, dir, "checkout", "-b", "rebased"); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "feature.txt", "the-patch")
	if _, err := run(ctx, dir, "checkout", "master"); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "other.txt", "moves master on")
	if _, err := run(ctx, dir, "cherry-pick", "rebased"); err != nil {
		t.Fatal(err)
	}

	if IsAncestor(ctx, dir, "rebased", "master") {
		t.Fatal("precondition: a rebase-merged branch is not an ancestor")
	}
	if !HasEquivalentUpstream(ctx, dir, "rebased", "master") {
		t.Error("rebase-merged branch should be detected as landed")
	}

	// wip: a genuinely unlanded commit must never be reported as merged.
	if _, err := run(ctx, dir, "checkout", "-b", "wip", "master"); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "wip.txt", "unlanded")
	if HasEquivalentUpstream(ctx, dir, "wip", "master") {
		t.Error("unlanded branch must not be reported as merged")
	}

	// partial: one landed patch plus one unlanded. All-or-nothing, so false.
	if _, err := run(ctx, dir, "checkout", "-b", "partial", "rebased"); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "extra.txt", "not upstream")
	if HasEquivalentUpstream(ctx, dir, "partial", "master") {
		t.Error("branch with one unlanded commit must not be reported as merged")
	}

	// Documented limit: two commits squashed into one upstream commit match no
	// individual patch-id, so the branch reads as unlanded and is left in place.
	if _, err := run(ctx, dir, "checkout", "-b", "squashed", "master"); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "s1.txt", "s1")
	commit(t, dir, "s2.txt", "s2")
	if _, err := run(ctx, dir, "checkout", "master"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(ctx, dir, "merge", "--squash", "squashed"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(ctx, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "squash"); err != nil {
		t.Fatal(err)
	}
	if HasEquivalentUpstream(ctx, dir, "squashed", "master") {
		t.Error("multi-commit squash is not detected; guard against silently relaxing this")
	}

	// A branch adding nothing (empty `git cherry`) is left to the ancestry check.
	if HasEquivalentUpstream(ctx, dir, "master", "master") {
		t.Error("branch with no commits of its own should yield false")
	}
	if HasEquivalentUpstream(ctx, dir, "rebased", "does-not-exist") {
		t.Error("missing upstream ref should yield false, not panic")
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
