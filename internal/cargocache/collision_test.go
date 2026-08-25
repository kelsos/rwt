package cargocache

import (
	"os"
	"path/filepath"
	"testing"
)

// legacyWorktree builds a worktree still on the shared-cache wiring, which is
// the state Collisions has to keep detecting after the migration: a root cargo
// workspace, a .cargo/config.toml pointing at a shared target dir, and a
// target/debug/<bin> symlink to the artifact it supposedly built.
func legacyWorktree(t *testing.T, umbrella, name string, links map[string]string) string {
	t.Helper()
	wt := filepath.Join(umbrella, name)
	writeFile(t, filepath.Join(wt, "Cargo.toml"), "[workspace]\nmembers = []\n")
	writeFile(t, ConfigPath(wt, rootWorkspace),
		"[build]\ntarget-dir = \"/cache/rotki\"\n")
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

	a := legacyWorktree(t, umbrella, "develop", map[string]string{"colibri": shared})
	b := legacyWorktree(t, umbrella, "master", map[string]string{"colibri": shared})

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

	a := legacyWorktree(t, umbrella, "develop",
		map[string]string{"colibri": "/cache/rotki/debug/deps/colibri-aaaa"})
	b := legacyWorktree(t, umbrella, "master",
		map[string]string{"colibri": "/cache/rotki/debug/deps/colibri-bbbb"})

	if found := Collisions([]string{a, b}); len(found) != 0 {
		t.Errorf("distinct artifacts must not collide, got %+v", found)
	}
}

// TestCollisionsReportsRegardlessOfConfig is why Collisions does not gate on
// wired(). After the migration, "wired" means pointing at <worktree>/target, so
// a worktree still on the old shared-cache config reads as unwired — and that is
// exactly the worktree whose collision still matters. The symlink is the
// evidence; the config is not consulted.
func TestCollisionsReportsRegardlessOfConfig(t *testing.T) {
	umbrella := t.TempDir()
	shared := "/cache/rotki/debug/deps/colibri-shared"

	a := legacyWorktree(t, umbrella, "develop", map[string]string{"colibri": shared})
	b := legacyWorktree(t, umbrella, "master", map[string]string{"colibri": shared})
	writeFile(t, ConfigPath(b, rootWorkspace), "[build]\ntarget-dir = \"/somewhere/else\"\n")

	if found := Collisions([]string{a, b}); len(found) != 1 {
		t.Errorf("a collision on the old wiring must still be reported, got %+v", found)
	}
}

// TestCollisionsIsSilentAfterMigration: once each worktree builds into its own
// target dir there are no symlinks left, so the guard reports nothing. This is
// the state the whole change exists to reach.
func TestCollisionsIsSilentAfterMigration(t *testing.T) {
	umbrella := t.TempDir()

	var wts []string
	for _, name := range []string{"develop", "master"} {
		wt := filepath.Join(umbrella, name)
		writeFile(t, filepath.Join(wt, "Cargo.toml"), "[workspace]\nmembers = []\n")
		if _, err := Wire(wt); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(LauncherBinDir(wt), "colibri"), "its own build")
		wts = append(wts, wt)
	}

	if found := Collisions(wts); len(found) != 0 {
		t.Errorf("migrated worktrees must not collide, got %+v", found)
	}
}

// TestCollisionsIgnoresRealBinaries: a real file rather than a symlink is a
// build that predates wiring, and belongs to that worktree alone.
func TestCollisionsIgnoresRealBinaries(t *testing.T) {
	t.Setenv("RWT_CARGO_CACHE", t.TempDir())
	umbrella := t.TempDir()
	shared := "/cache/rotki/debug/deps/colibri-shared"

	a := legacyWorktree(t, umbrella, "develop", map[string]string{"colibri": shared})
	b := legacyWorktree(t, umbrella, "master", nil)
	writeFile(t, filepath.Join(LauncherBinDir(b), "colibri"), "an actual binary")

	if found := Collisions([]string{a, b}); len(found) != 0 {
		t.Errorf("a real binary is not a shared artifact, got %+v", found)
	}
}
