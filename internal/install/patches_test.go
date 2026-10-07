package install

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func writePatch(t *testing.T, worktree, name string) string {
	t.Helper()
	dir := filepath.Join(worktree, patchesDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("diff --git a/x b/x\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// TestBackdatePatchMtimesPutsPatchesBeforeNow is the whole point of the helper:
// pnpm treats a patch mtime equal to its last-validated stamp as a modification,
// and a fresh checkout lands on exactly that. Every patch must come out strictly
// older than the install that follows it.
func TestBackdatePatchMtimesPutsPatchesBeforeNow(t *testing.T) {
	wt := t.TempDir()
	first := writePatch(t, wt, "app-builder-lib.patch")
	second := writePatch(t, wt, "bignumber.js@11.1.5.patch")

	if err := backdatePatchMtimes(wt); err != nil {
		t.Fatalf("backdatePatchMtimes: %v", err)
	}

	// The install records its stamp after this returns, so "now" stands in for it.
	// The margin is a literal, deliberately not derived from patchBackdate: a
	// margin computed from the constant under test moves with it and the
	// assertion becomes vacuous, passing even at a zero backdate. The bug being
	// guarded is an equality collision, and merely "before now" cannot catch it
	// because two calls to time.Now() are always ordered.
	const minMargin = time.Second
	latest := time.Now().Add(-minMargin)
	for _, path := range []string{first, second} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if !info.ModTime().Before(latest) {
			t.Errorf("%s: mtime %v is not at least %v before the install stamp",
				filepath.Base(path), info.ModTime(), minMargin)
		}
	}
}

// TestBackdatePatchMtimesPutsPatchesBeforeAnExistingStamp covers `rwt setup` on
// an installed worktree: the install that follows is a no-op and keeps the old
// stamp, so the patches must land before that stamp, not merely before now.
func TestBackdatePatchMtimesPutsPatchesBeforeAnExistingStamp(t *testing.T) {
	wt := t.TempDir()
	path := writePatch(t, wt, "app-builder-lib.patch")
	validated := time.Now().Add(-3 * time.Hour).Truncate(time.Millisecond)
	state := filepath.Join(wt, workspaceStateFile)
	if err := os.MkdirAll(filepath.Dir(state), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := []byte(`{"lastValidatedTimestamp":` + strconv.FormatInt(validated.UnixMilli(), 10) + `}`)
	if err := os.WriteFile(state, body, 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	if err := backdatePatchMtimes(wt); err != nil {
		t.Fatalf("backdatePatchMtimes: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Literal margin, for the same reason as in the test above.
	if latest := validated.Add(-time.Second); !info.ModTime().Before(latest) {
		t.Errorf("mtime %v is not at least 1s before the existing stamp %v", info.ModTime(), validated)
	}
}

// TestBackdatePatchMtimesLeavesContentAlone guards the property that keeps this
// safe to run on every setup: it is a metadata change, so `git status` stays
// clean and no patch is ever rewritten.
func TestBackdatePatchMtimesLeavesContentAlone(t *testing.T) {
	wt := t.TempDir()
	path := writePatch(t, wt, "app-builder-lib.patch")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if err := backdatePatchMtimes(wt); err != nil {
		t.Fatalf("backdatePatchMtimes: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("patch content changed: %q -> %q", before, after)
	}
}

// TestBackdatePatchMtimesSkipsNonPatchFiles keeps the blast radius to the files
// pnpm actually stats. A README or a stray backup in patches/ is not ours to
// touch.
func TestBackdatePatchMtimesSkipsNonPatchFiles(t *testing.T) {
	wt := t.TempDir()
	writePatch(t, wt, "app-builder-lib.patch")
	other := filepath.Join(wt, patchesDir, "README.md")
	if err := os.WriteFile(other, []byte("why these exist\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	original, err := os.Stat(other)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if err := backdatePatchMtimes(wt); err != nil {
		t.Fatalf("backdatePatchMtimes: %v", err)
	}

	got, err := os.Stat(other)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !got.ModTime().Equal(original.ModTime()) {
		t.Errorf("README.md mtime changed from %v to %v", original.ModTime(), got.ModTime())
	}
}

// TestBackdatePatchMtimesIgnoresAbsentDirectory covers a base that has no
// patches at all: nothing to correct is success, not an error the run reports.
func TestBackdatePatchMtimesIgnoresAbsentDirectory(t *testing.T) {
	if err := backdatePatchMtimes(t.TempDir()); err != nil {
		t.Errorf("expected no error for a worktree without patches, got %v", err)
	}
}

// TestDefaultStepsBackdatePatchesBeforeInstalling pins the wiring, not just the
// helper: the correction is worthless unless it runs before pnpm install.
func TestDefaultStepsBackdatePatchesBeforeInstalling(t *testing.T) {
	wt := t.TempDir()
	path := writePatch(t, wt, "app-builder-lib.patch")

	var pnpm *Step
	for i, s := range DefaultSteps(wt) {
		if s.Eco == EcoPnpm {
			pnpm = &DefaultSteps(wt)[i]
			break
		}
	}
	if pnpm == nil {
		t.Fatal("no pnpm step in DefaultSteps")
	}
	if pnpm.Before == nil {
		t.Fatal("pnpm step has no Before hook, so patches are never backdated")
	}
	if err := pnpm.Before(); err != nil {
		t.Fatalf("Before: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.ModTime().Before(time.Now()) {
		t.Errorf("pnpm step's Before did not backdate the patch (mtime %v)", info.ModTime())
	}
}
