package cargocache

import (
	"os"
	"path/filepath"
	"testing"
)

// wireWorktree builds a worktree that Collisions will consider: a root cargo
// workspace, a .cargo/config.toml pointing at the shared target dir, and a
// target/debug/<bin> symlink to the artifact it supposedly built.
func wireWorktree(t *testing.T, umbrella, name string, links map[string]string) string {
	t.Helper()
	wt := filepath.Join(umbrella, name)
	target, err := TargetDir(rootWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, "Cargo.toml"), "[workspace]\nmembers = []\n")
	writeFile(t, ConfigPath(wt, rootWorkspace),
		"[build]\ntarget-dir = \""+target+"\"\n")
	for bin, artifact := range links {
		link := filepath.Join(LauncherBinDir(wt), bin)
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(artifact, link); err != nil {
			t.Fatal(err)
		}
	}
	return wt
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCollisionsFindsSharedArtifact is the case observed on a real umbrella:
// two worktrees with different sources whose links resolve to one artifact,
// because cargo's metadata hash does not distinguish worktrees.
func TestCollisionsFindsSharedArtifact(t *testing.T) {
	t.Setenv("RWT_CARGO_CACHE", t.TempDir())
	umbrella := t.TempDir()
	shared := "/cache/rotki/debug/deps/colibri-029385afae5985ed"

	a := wireWorktree(t, umbrella, "develop", map[string]string{"colibri": shared})
	b := wireWorktree(t, umbrella, "master", map[string]string{"colibri": shared})

	found := Collisions([]string{a, b})
	if len(found) != 1 {
		t.Fatalf("got %d collisions, want 1: %+v", len(found), found)
	}
	if found[0].Bin != "colibri" || found[0].Artifact != shared {
		t.Errorf("got %+v", found[0])
	}
	if len(found[0].Worktrees) != 2 {
		t.Errorf("want both worktrees named, got %v", found[0].Worktrees)
	}
}

// TestCollisionsIgnoresDistinctArtifacts: worktrees with their own artifacts are
// exactly what the design intends, and must not be reported.
func TestCollisionsIgnoresDistinctArtifacts(t *testing.T) {
	t.Setenv("RWT_CARGO_CACHE", t.TempDir())
	umbrella := t.TempDir()

	a := wireWorktree(t, umbrella, "develop",
		map[string]string{"colibri": "/cache/rotki/debug/deps/colibri-aaaa"})
	b := wireWorktree(t, umbrella, "master",
		map[string]string{"colibri": "/cache/rotki/debug/deps/colibri-bbbb"})

	if found := Collisions([]string{a, b}); len(found) != 0 {
		t.Errorf("distinct artifacts must not collide, got %+v", found)
	}
}

// TestCollisionsIgnoresUnwired: a worktree carrying its own .cargo/config.toml
// is deliberately out of the shared cache, so it cannot be in a collision.
func TestCollisionsIgnoresUnwired(t *testing.T) {
	t.Setenv("RWT_CARGO_CACHE", t.TempDir())
	umbrella := t.TempDir()
	shared := "/cache/rotki/debug/deps/colibri-shared"

	a := wireWorktree(t, umbrella, "develop", map[string]string{"colibri": shared})
	b := wireWorktree(t, umbrella, "master", map[string]string{"colibri": shared})
	writeFile(t, ConfigPath(b, rootWorkspace), "[build]\ntarget-dir = \"/somewhere/else\"\n")

	if found := Collisions([]string{a, b}); len(found) != 0 {
		t.Errorf("an unwired worktree must not collide, got %+v", found)
	}
}

// TestCollisionsIgnoresRealBinaries: a real file rather than a symlink is a
// build that predates wiring, and belongs to that worktree alone.
func TestCollisionsIgnoresRealBinaries(t *testing.T) {
	t.Setenv("RWT_CARGO_CACHE", t.TempDir())
	umbrella := t.TempDir()
	shared := "/cache/rotki/debug/deps/colibri-shared"

	a := wireWorktree(t, umbrella, "develop", map[string]string{"colibri": shared})
	b := wireWorktree(t, umbrella, "master", nil)
	writeFile(t, filepath.Join(LauncherBinDir(b), "colibri"), "an actual binary")

	if found := Collisions([]string{a, b}); len(found) != 0 {
		t.Errorf("a real binary is not a shared artifact, got %+v", found)
	}
}

func TestWorkspaceTargetOf(t *testing.T) {
	got := WorkspaceTargetOf("/home/x/.cache/rwt/target/rotki/debug/deps/colibri-abc123")
	if want := "/home/x/.cache/rwt/target/rotki"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Anything not shaped like an artifact path yields a placeholder rather
	// than a plausible-looking wrong directory.
	if got := WorkspaceTargetOf("/tmp/colibri"); got != "<cache-root>/<workspace>" {
		t.Errorf("got %q, want the placeholder", got)
	}
}
