package cargocache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sccacheConf redirects the sccache config at a temp path and guarantees
// Wrapper() finds something, since SyncBasedirs is a no-op without sccache
// installed and would otherwise pass these tests by doing nothing.
func sccacheConf(t *testing.T) string {
	t.Helper()
	if Wrapper() == "" {
		t.Skip("sccache not installed")
	}
	path := filepath.Join(t.TempDir(), "sccache", "config")
	t.Setenv("SCCACHE_CONF", path)
	return path
}

// TestSyncBasedirsListsEveryWorktree is the contract: basedirs has to name each
// worktree root, because that is the only form that normalises paths across
// them. A common parent measured 0% Rust cache hits where per-worktree roots
// measured 92%.
func TestSyncBasedirsListsEveryWorktree(t *testing.T) {
	path := sccacheConf(t)
	wts := []string{"/umbrella/develop", "/umbrella/master"}

	changed, err := SyncBasedirs(wts)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("first write reported no change")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, wt := range wts {
		if !strings.Contains(string(body), `"`+wt+`"`) {
			t.Errorf("%s missing from basedirs:\n%s", wt, body)
		}
	}
	// The plural key, not the silently-ignored singular.
	if !strings.Contains(string(body), "basedirs = [") {
		t.Errorf("expected a basedirs list:\n%s", body)
	}
}

// TestSyncBasedirsIsIdempotent keeps rwt from restarting the sccache server on
// every command: the caller restarts it whenever this reports a change, and a
// server restart throws away the in-memory stats and costs a cold start.
func TestSyncBasedirsIsIdempotent(t *testing.T) {
	sccacheConf(t)
	wts := []string{"/umbrella/develop", "/umbrella/master"}

	if _, err := SyncBasedirs(wts); err != nil {
		t.Fatal(err)
	}
	changed, err := SyncBasedirs(wts)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("an unchanged worktree list rewrote the config")
	}
}

// Git's worktree order is not guaranteed, and reordering must not count as a
// change, or the server would restart for nothing.
func TestSyncBasedirsIgnoresOrdering(t *testing.T) {
	sccacheConf(t)

	if _, err := SyncBasedirs([]string{"/umbrella/develop", "/umbrella/master"}); err != nil {
		t.Fatal(err)
	}
	changed, err := SyncBasedirs([]string{"/umbrella/master", "/umbrella/develop"})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("reordering the same worktrees rewrote the config")
	}
}

// TestSyncBasedirsDropsRemovedWorktrees is why the file is regenerated rather
// than appended to. basedirs resolves by longest matching prefix, so a stale
// entry does not merely sit there: it can shadow the entry that should have
// matched.
func TestSyncBasedirsDropsRemovedWorktrees(t *testing.T) {
	path := sccacheConf(t)

	if _, err := SyncBasedirs([]string{"/umbrella/develop", "/umbrella/gone"}); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncBasedirs([]string{"/umbrella/develop"}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "/umbrella/gone") {
		t.Errorf("a removed worktree survived in basedirs:\n%s", body)
	}
}

// TestSyncBasedirsLeavesAHandWrittenConfigAlone: an sccache config the developer
// wrote may carry cache backends, size limits or auth that rwt knows nothing
// about. Losing those to a target-dir tool would be a bad trade for a 10% build
// speedup, so rwt reports and declines.
func TestSyncBasedirsLeavesAHandWrittenConfigAlone(t *testing.T) {
	path := sccacheConf(t)
	own := "[cache.redis]\nurl = \"redis://build-box\"\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := SyncBasedirs([]string{"/umbrella/develop"})
	if !IsSccacheConfigNotOurs(err) {
		t.Fatalf("err = %v, want the not-ours sentinel", err)
	}
	if changed {
		t.Error("reported a change while declining to write")
	}
	if body, _ := os.ReadFile(path); string(body) != own {
		t.Errorf("the developer's config was modified:\n%s", body)
	}
}
