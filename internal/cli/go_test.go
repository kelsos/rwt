package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRunGo(t *testing.T) {
	umbrella := t.TempDir()
	t.Setenv("RWT_UMBRELLA", umbrella)
	if err := os.Mkdir(filepath.Join(umbrella, "feat-dark-mode"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Bare name resolves through the prefix variants.
	var buf bytes.Buffer
	if err := runGo(&buf, "dark-mode"); err != nil {
		t.Fatalf("runGo: %v", err)
	}
	want := "cd " + filepath.Join(umbrella, "feat-dark-mode") + "\n"
	if buf.String() != want {
		t.Errorf("runGo output = %q, want %q", buf.String(), want)
	}

	// Unknown worktree errors instead of printing a bogus cd.
	if err := runGo(&bytes.Buffer{}, "nope"); err == nil {
		t.Error("runGo on a missing worktree should error")
	}
}
