package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestResolveWorktreeDot covers the "." case: it must refuse a cwd that is not
// inside a git repo, and otherwise resolve to the repo root even from a
// subdirectory.
func TestResolveWorktreeDot(t *testing.T) {
	clearGitEnv(t)

	// Not a repo -> error.
	nonRepo := t.TempDir()
	t.Chdir(nonRepo)
	if _, err := resolveWorktree(context.Background(), "."); err == nil {
		t.Error("resolveWorktree(\".\") outside a git repo should error")
	}

	// A repo, entered from a subdirectory -> resolves to the repo root.
	repo := t.TempDir()
	gitRun(t, repo, "init", "-q")
	sub := filepath.Join(repo, "frontend", "app")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)

	got, err := resolveWorktree(context.Background(), ".")
	if err != nil {
		t.Fatalf("resolveWorktree(\".\"): %v", err)
	}
	// macOS /var -> /private/var symlinks, so compare resolved paths.
	wantResolved, _ := filepath.EvalSymlinks(repo)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("resolveWorktree(\".\") = %q, want repo root %q", gotResolved, wantResolved)
	}
}
