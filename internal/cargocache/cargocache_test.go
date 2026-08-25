package cargocache

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// worktree builds a fake worktree with the shared cache root redirected into the
// test's temp dir. Layout is added by the rootLayout / splitLayout helpers.
func worktree(t *testing.T) string {
	t.Helper()
	t.Setenv("RWT_CARGO_CACHE", filepath.Join(t.TempDir(), "cache"))
	return t.TempDir()
}

// rootLayout writes the current rotki layout: one [workspace] manifest at the
// worktree root with colibri and crates/* as members.
func rootLayout(t *testing.T, wt string) {
	t.Helper()
	write(t, filepath.Join(wt, "Cargo.toml"), "[workspace]\nresolver = \"2\"\nmembers = [\"colibri\", \"crates/starling\"]\n")
	write(t, filepath.Join(wt, "colibri", "Cargo.toml"), "[package]\nname = \"colibri\"\n")
}

// splitLayout writes the older layout: the named directories each hold their own
// independent workspace manifest.
func splitLayout(t *testing.T, wt string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		write(t, filepath.Join(wt, d, "Cargo.toml"), "[package]\nname = \""+d+"\"\n")
	}
}

// cargoTarget fakes a cargo target dir, markers included, so LocalTargets and
// Inspect see it the way they would see a real one.
func cargoTarget(t *testing.T, dir string) string {
	t.Helper()
	write(t, filepath.Join(dir, "CACHEDIR.TAG"), "Signature: 8a477f597d28d172789f06886806bc55\n")
	return dir
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWorkspacesDetectsRootLayout is the regression that motivated the layout
// detection: on a base with a root workspace there is exactly one workspace, and
// its config belongs at the worktree root. A config under colibri/ would be
// invisible to cargo, which discovers config by walking up from the cwd — and
// both rwt and the app build from the worktree root.
func TestWorkspacesDetectsRootLayout(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)

	ws := Workspaces(wt)
	if len(ws) != 1 || ws[0].Name != "rotki" {
		t.Fatalf("expected the single root workspace, got %+v", ws)
	}
	if got, want := ConfigPath(wt, ws[0]), filepath.Join(wt, ".cargo", "config.toml"); got != want {
		t.Errorf("config path = %s, want %s", got, want)
	}
}

// TestWorkspacesDetectsSplitLayout covers the older bases: colibri and crates as
// two independent workspaces, each wired at its own root.
func TestWorkspacesDetectsSplitLayout(t *testing.T) {
	wt := worktree(t)
	splitLayout(t, wt, "colibri", "crates")

	ws := Workspaces(wt)
	if len(ws) != 2 || ws[0].Name != "colibri" || ws[1].Name != "crates" {
		t.Fatalf("expected colibri and crates, got %+v", ws)
	}
	if got, want := ConfigPath(wt, ws[0]), filepath.Join(wt, "colibri", ".cargo", "config.toml"); got != want {
		t.Errorf("config path = %s, want %s", got, want)
	}
}

// TestWorkspacesSkipsAbsent covers a base that predates a workspace (crates not
// yet on bugfixes) and one with no cargo at all.
func TestWorkspacesSkipsAbsent(t *testing.T) {
	wt := worktree(t)
	splitLayout(t, wt, "colibri")
	if ws := Workspaces(wt); len(ws) != 1 || ws[0].Name != "colibri" {
		t.Errorf("expected colibri only, got %+v", ws)
	}

	if ws := Workspaces(worktree(t)); len(ws) != 0 {
		t.Errorf("expected no workspaces in an empty worktree, got %+v", ws)
	}
}

// TestRootPackageManifestIsNotAWorkspace guards the detection: a root Cargo.toml
// without a [workspace] table is a package, not the workspace root, and must not
// swallow the split layout.
func TestRootPackageManifestIsNotAWorkspace(t *testing.T) {
	wt := worktree(t)
	splitLayout(t, wt, "colibri")
	write(t, filepath.Join(wt, "Cargo.toml"), "[package]\nname = \"whatever\"\n")

	if ws := Workspaces(wt); len(ws) != 1 || ws[0].Name != "colibri" {
		t.Errorf("expected the split layout to stand, got %+v", ws)
	}
}

// TestWirePointsAtThisWorktreesTarget is the core contract, and it is the
// inverse of the one this package shipped until 2026-08: every workspace in a
// worktree builds into that worktree's own target dir. Workspaces sharing a dir
// inside one worktree is fine (different packages, one layout at a time); it is
// worktrees sharing one that collapsed them onto a single binary.
func TestWirePointsAtThisWorktreesTarget(t *testing.T) {
	wt := worktree(t)
	splitLayout(t, wt, "colibri", "crates")

	if _, err := Wire(wt); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	target := TargetDir(wt)
	if want := filepath.Join(wt, "target"); target != want {
		t.Fatalf("TargetDir = %s, want %s", target, want)
	}
	for _, ws := range Workspaces(wt) {
		body, err := os.ReadFile(ConfigPath(wt, ws))
		if err != nil {
			t.Fatalf("reading %s config: %v", ws.Name, err)
		}
		if !strings.Contains(string(body), `target-dir = "`+target+`"`) {
			t.Errorf("%s config does not point at %s:\n%s", ws.Name, target, body)
		}
	}
}

// TestWireGivesTwoWorktreesDifferentTargets is the regression for the bug this
// design replaces: two worktrees must never resolve to one target dir, whatever
// their layout, because that is what gave cargo a single fingerprint namespace
// for both and let one serve the other's binary.
func TestWireGivesTwoWorktreesDifferentTargets(t *testing.T) {
	a, b := worktree(t), worktree(t)
	rootLayout(t, a)
	rootLayout(t, b)

	for _, wt := range []string{a, b} {
		if _, err := Wire(wt); err != nil {
			t.Fatalf("Wire(%s): %v", wt, err)
		}
	}
	if TargetDir(a) == TargetDir(b) {
		t.Fatalf("both worktrees build into %s", TargetDir(a))
	}
	bodyA, _ := os.ReadFile(ConfigPath(a, rootWorkspace))
	if strings.Contains(string(bodyA), TargetDir(b)) {
		t.Error("worktree a's config names worktree b's target dir")
	}
}

// TestWireRootLayoutWritesOneConfig checks the root layout end to end: a single
// config at the worktree root, and nothing left under colibri/.
func TestWireRootLayoutWritesOneConfig(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)

	res, err := Wire(wt)
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	if len(res.Wired) != 1 || res.Wired[0] != "rotki" {
		t.Fatalf("expected the root workspace wired, got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(wt, ".cargo", "config.toml")); err != nil {
		t.Errorf("root config not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "colibri", ".cargo")); !os.IsNotExist(err) {
		t.Errorf("colibri/.cargo should not exist on the root layout")
	}
}

// TestWirePrunesStaleLayoutConfig covers a worktree rebased from the split
// layout onto the root workspace: the config rwt wrote under colibri/ has to go,
// or a build started inside colibri/ resolves it and compiles into a different
// shared dir than a build from the worktree root.
func TestWirePrunesStaleLayoutConfig(t *testing.T) {
	wt := worktree(t)
	splitLayout(t, wt, "colibri")
	if _, err := Wire(wt); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	stale := ConfigPath(wt, splitWorkspaces[0])
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("expected the split-layout config first: %v", err)
	}

	rootLayout(t, wt) // the rebase
	if _, err := Wire(wt); err != nil {
		t.Fatalf("Wire after rebase: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale %s survived the layout change", stale)
	}
}

// TestWireLeavesHandWrittenConfigAlone is the other half of pruning: a config
// rwt did not write is the developer's, whatever the layout says.
func TestWireLeavesHandWrittenConfigAlone(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)
	mine := filepath.Join(wt, "colibri", ".cargo", "config.toml")
	write(t, mine, "[build]\nrustflags = [\"-C\", \"target-cpu=native\"]\n")

	if _, err := Wire(wt); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	body, err := os.ReadFile(mine)
	if err != nil {
		t.Fatalf("hand-written config was removed: %v", err)
	}
	if !strings.Contains(string(body), "target-cpu=native") {
		t.Errorf("hand-written config was rewritten:\n%s", body)
	}
}

// TestWireNeverClobbersAnActiveHandWrittenConfig is the counterpart at the
// active workspace root, where the config rwt wants to write and the one the
// developer already wrote are the same path. Theirs wins: a hand-tuned config
// (rustflags, a linker) disappearing behind a cache optimisation is a far worse
// outcome than not sharing that worktree's target dir.
func TestWireNeverClobbersAnActiveHandWrittenConfig(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)
	mine := filepath.Join(wt, ".cargo", "config.toml")
	write(t, mine, "[build]\nrustflags = [\"-C\", \"target-cpu=native\"]\n")

	res, err := Wire(wt)
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	if len(res.Wired) != 0 || len(res.Kept) != 1 || res.Kept[0] != "rotki" {
		t.Errorf("expected the workspace reported as kept, not wired, got %+v", res)
	}
	body, err := os.ReadFile(mine)
	if err != nil {
		t.Fatalf("hand-written config was removed: %v", err)
	}
	if !strings.Contains(string(body), "target-cpu=native") {
		t.Errorf("hand-written config was overwritten:\n%s", body)
	}
}

// TestWireIsIdempotent guards the mtime: cargo folds the config into its
// fingerprints, so rewriting identical content on every setup/refresh would
// invalidate the cache the file exists to preserve.
func TestWireIsIdempotent(t *testing.T) {
	wt := worktree(t)
	splitLayout(t, wt, "colibri")
	if _, err := Wire(wt); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	path := ConfigPath(wt, Workspaces(wt)[0])
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}

	if _, err := Wire(wt); err != nil {
		t.Fatalf("second Wire: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("re-wiring rewrote an unchanged config, invalidating cargo's fingerprints")
	}
}

// TestInspectReportsWiring covers what doctor renders: unwired before Wire,
// wired after.
func TestInspectReportsWiring(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)

	if before := Inspect(wt)[0]; before.Wired {
		t.Errorf("expected unwired before Wire, got %+v", before)
	}
	if _, err := Wire(wt); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	if after := Inspect(wt)[0]; !after.Wired {
		t.Errorf("expected wired after Wire, got %+v", after)
	}
}

// TestSupersededTargetsExcludesTheLiveDir is the footgun this rename guards.
// <worktree>/target is where every workspace builds now, so `rwt clean` must
// never offer it up: reclaiming it would delete the worktree's own output and
// buy back disk in exchange for a cold rebuild. Only the split layout's dirs,
// which nothing writes to any more, are reclaimable.
func TestSupersededTargetsExcludesTheLiveDir(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)
	cargoTarget(t, filepath.Join(wt, "target"))
	cargoTarget(t, filepath.Join(wt, "colibri", "target"))

	got := SupersededTargets(wt)
	want := []string{filepath.Join(wt, "colibri", "target")}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("SupersededTargets = %v, want %v", got, want)
	}
}

// TestDirSizeCountsAHardlinkOnce is what makes the reclaim figures honest.
// Cargo hardlinks heavily, so counting every link reported far more than the
// disk would actually give back — and the number exists precisely to answer
// "is reclaiming this worth it".
func TestDirSizeCountsAHardlinkOnce(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("x", 4096)
	write(t, filepath.Join(dir, "deps", "colibri-aaaa1111"), body)
	// The uplift slot: cargo populates it by hardlinking the deps artifact.
	if err := os.Link(filepath.Join(dir, "deps", "colibri-aaaa1111"), filepath.Join(dir, "colibri")); err != nil {
		t.Fatal(err)
	}

	if got := DirSize(dir); got != int64(len(body)) {
		t.Errorf("DirSize = %d, want %d (the hardlink counted twice)", got, len(body))
	}
}

// Distinct files still add up; the dedup must key on the inode, not the size.
func TestDirSizeSumsDistinctFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a"), strings.Repeat("x", 100))
	write(t, filepath.Join(dir, "b"), strings.Repeat("y", 100))

	if got := DirSize(dir); got != 200 {
		t.Errorf("DirSize = %d, want 200", got)
	}
}

// TestRunningFromIgnoresAnIdleWorktree: the guard on `clean --deep` must not cry
// wolf, or it would refuse to reclaim anything.
func TestRunningFromIgnoresAnIdleWorktree(t *testing.T) {
	wt := worktree(t)
	write(t, filepath.Join(wt, "target", "debug", "colibri"), "not running")

	if got := RunningFrom(wt); len(got) != 0 {
		t.Errorf("RunningFrom = %+v, want none", got)
	}
}

// TestRunningFromFindsALiveProcess is the case that matters: rotki's starling
// supervises colibri and respawns it, so removing a target dir under a live dev
// session leaves the supervisor exec'ing a binary that is gone. Linux unlinks a
// running executable without complaint, so this has to be detected up front.
func TestRunningFromFindsALiveProcess(t *testing.T) {
	wt := worktree(t)
	// A real executable at the path a service binary would occupy.
	sh, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	src, err := os.ReadFile(sh)
	if err != nil {
		t.Skip("cannot read sleep binary")
	}
	bin := filepath.Join(wt, "target", "debug", "colibri")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, src, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	found := RunningFrom(wt)
	if len(found) != 1 {
		t.Fatalf("RunningFrom = %+v, want the live process", found)
	}
	if found[0].PID != cmd.Process.Pid || found[0].Name != "colibri" {
		t.Errorf("got %+v, want pid %d named colibri", found[0], cmd.Process.Pid)
	}
}

// TestRunningFromFindsAProcessBehindASymlink is the case the first version of
// this guard missed, and the one that matters most: a worktree still on the old
// shared-cache wiring runs its binary through a symlink, so /proc resolves the
// exe to the shared cache and a prefix test on the worktree finds nothing. That
// pointed the guard away from exactly the worktrees most likely to have a live
// dev session.
func TestRunningFromFindsAProcessBehindASymlink(t *testing.T) {
	wt := worktree(t)
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	src, err := os.ReadFile(sleep)
	if err != nil {
		t.Skip("cannot read sleep binary")
	}
	// The artifact lives outside the worktree, as a shared cache's would.
	shared := filepath.Join(t.TempDir(), "colibri-aaaa1111")
	if err := os.WriteFile(shared, src, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(wt, "target", "debug", "colibri")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, link); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(link, "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	found := RunningFrom(wt)
	if len(found) != 1 || found[0].PID != cmd.Process.Pid {
		t.Fatalf("RunningFrom = %+v, want pid %d", found, cmd.Process.Pid)
	}
}

// TestSupersededTargetsIgnoresNonCargoDirs guards the removal: `target` is an
// ordinary enough directory name that rwt clean must confirm cargo owns it
// before deleting it.
func TestSupersededTargetsIgnoresNonCargoDirs(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)
	if err := os.MkdirAll(filepath.Join(wt, "colibri", "target", "something"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := SupersededTargets(wt); len(got) != 0 {
		t.Errorf("expected an unmarked target dir to be left alone, got %v", got)
	}
}

// TestExcludeIsIdempotent checks the info/exclude append: the patterns land in
// the repository's shared exclude file once, however often it runs, so repeated
// setups don't grow the file.
func TestExcludeIsIdempotent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	wt := t.TempDir()
	if out, err := exec.Command("git", "-C", wt, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	ctx := context.Background()
	for range 2 {
		if err := Exclude(ctx, wt); err != nil {
			t.Fatalf("Exclude: %v", err)
		}
	}
	body, err := os.ReadFile(filepath.Join(wt, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	// Counted as whole lines, not substrings: "/.cargo/" occurs inside
	// "colibri/.cargo/" too, and only the standalone entry is this one.
	lines := map[string]int{}
	for _, line := range strings.Split(string(body), "\n") {
		lines[strings.TrimSpace(line)]++
	}
	for _, p := range excludePatterns {
		if n := lines[p]; n != 1 {
			t.Errorf("pattern %q appears %d times in info/exclude, want 1", p, n)
		}
	}
}

// legacyLink plants the launcher symlink the shared-cache design used to leave
// at <worktree>/target/debug/<bin>: an absolute link into a cache directory
// outside the worktree. This is the state every worktree is in before it is
// migrated, and the thing that kept several of them running one binary.
func legacyLink(t *testing.T, wt, bin string) string {
	t.Helper()
	shared := filepath.Join(t.TempDir(), "shared", "debug", "deps")
	artifact := filepath.Join(shared, bin+"-aaaa1111")
	write(t, artifact, "a neighbour's binary")
	link := filepath.Join(wt, "target", "debug", bin)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(artifact, link); err != nil {
		t.Fatal(err)
	}
	return link
}

// TestWireDropsTheSharedCacheSymlink is the migration, and the whole point of
// the change: rewiring a worktree onto its own target dir has to remove the link
// into the old shared cache in the same breath. Leaving it would have the
// launcher keep running the neighbour's binary even though this worktree now
// builds its own, which is the original bug wearing a new hat.
func TestWireDropsTheSharedCacheSymlink(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)
	link := legacyLink(t, wt, "colibri")

	if _, err := Wire(wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("the symlink into the shared cache survived Wire: %v", err)
	}
}

// TestDropSharedLinksKeepsARealBinary: under the new scheme cargo's own output
// sits at exactly the path the old symlink occupied, so removing anything that
// is not a symlink out of the worktree would delete a freshly built binary.
func TestDropSharedLinksKeepsARealBinary(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)
	real := filepath.Join(wt, "target", "debug", "colibri")
	write(t, real, "this worktree's own build")

	if dropped := DropSharedLinks(wt); len(dropped) != 0 {
		t.Fatalf("dropped %v, want nothing", dropped)
	}
	if body, err := os.ReadFile(real); err != nil || string(body) != "this worktree's own build" {
		t.Errorf("a real binary was removed: %q, %v", body, err)
	}
}

// A link pointing inside the worktree is not the shared cache's, so it stays.
func TestDropSharedLinksKeepsAnInternalLink(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)
	artifact := filepath.Join(wt, "target", "debug", "deps", "colibri-aaaa1111")
	write(t, artifact, "mine")
	link := filepath.Join(wt, "target", "debug", "colibri")
	if err := os.Symlink(artifact, link); err != nil {
		t.Fatal(err)
	}

	if dropped := DropSharedLinks(wt); len(dropped) != 0 {
		t.Fatalf("dropped %v, want nothing", dropped)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("a link inside the worktree was removed: %v", err)
	}
}

// TestReclaimKeepsWhatCargoDidNotWrite is the guard on `rwt clean`: rotki's e2e
// run freezes the python core into target/backend, which shares the directory
// with cargo's output. Removing the target dir wholesale would take it too.
func TestReclaimKeepsWhatCargoDidNotWrite(t *testing.T) {
	wt := worktree(t)
	target := cargoTarget(t, filepath.Join(wt, "target"))
	write(t, filepath.Join(target, "backend", "rotki-core", "rotki-core-1.2.3"), "frozen python core")
	write(t, filepath.Join(target, "debug", "deps", "colibri-aaaa1111"), "compiled")

	freed, err := Reclaim(target, false)
	if err != nil {
		t.Fatal(err)
	}
	if freed == 0 {
		t.Error("reclaimed nothing")
	}
	if _, err := os.Stat(filepath.Join(target, "backend", "rotki-core", "rotki-core-1.2.3")); err != nil {
		t.Errorf("target/backend was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "debug")); !os.IsNotExist(err) {
		t.Error("cargo's debug output survived")
	}
	if _, err := os.Stat(filepath.Join(target, "CACHEDIR.TAG")); !os.IsNotExist(err) {
		t.Error("cargo's marker survived")
	}
}

// TestReclaimRemovesLauncherLinks is the inverse of what this used to assert.
// Reclaim once preserved the symlinks into the shared cache, on the grounds that
// they cost no disk and kept the dev launcher off its `cargo run` fallback. They
// now have to go: they resolve to another worktree's binary, and leaving one
// behind in a reclaimed dir would preserve the exact bug the migration removes.
func TestReclaimRemovesLauncherLinks(t *testing.T) {
	wt := worktree(t)
	target := cargoTarget(t, filepath.Join(wt, "colibri", "target"))
	write(t, filepath.Join(target, "debug", "deps", "colibri-aaaa1111"), "compiled")
	shared := filepath.Join(t.TempDir(), "colibri-aaaa1111")
	write(t, shared, "the shared cache artifact")
	link := filepath.Join(target, "debug", "colibri")
	if err := os.Symlink(shared, link); err != nil {
		t.Fatal(err)
	}

	if _, err := Reclaim(target, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("the link into the shared cache survived the reclaim: %v", err)
	}
}

// TestReclaimRemovesCrossCompileOutput: a worktree that cross-compiled has
// per-triple dirs at the target root, and those are cargo's too. They are
// matched by what cargo builds inside them rather than by name, so a new rust
// triple needs no change here.
func TestReclaimRemovesCrossCompileOutput(t *testing.T) {
	wt := worktree(t)
	target := cargoTarget(t, filepath.Join(wt, "target"))
	triple := filepath.Join(target, "x86_64-pc-windows-msvc")
	write(t, filepath.Join(triple, "debug", ".fingerprint", "colibri-aaaa1111", "bin-colibri"), "{}")

	if _, err := Reclaim(target, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(triple); !os.IsNotExist(err) {
		t.Error("the cross-compile output dir survived")
	}
}

// TestReclaimDryRunMeasuresWithoutRemoving: --dry-run and the real run share one
// walk, so the preview is the number the real run will report, not an estimate
// over a tree that includes content Reclaim would have kept.
func TestReclaimDryRunMeasuresWithoutRemoving(t *testing.T) {
	wt := worktree(t)
	target := cargoTarget(t, filepath.Join(wt, "target"))
	write(t, filepath.Join(target, "backend", "core"), strings.Repeat("x", 4096))
	write(t, filepath.Join(target, "debug", "deps", "colibri-aaaa1111"), strings.Repeat("y", 1024))

	preview, err := Reclaim(target, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "debug", "deps", "colibri-aaaa1111")); err != nil {
		t.Errorf("--dry-run removed cargo output: %v", err)
	}
	actual, err := Reclaim(target, false)
	if err != nil {
		t.Fatal(err)
	}
	if preview != actual {
		t.Errorf("--dry-run previewed %d bytes, the run freed %d", preview, actual)
	}
	if preview >= 4096 {
		t.Errorf("preview of %d bytes counts the preserved backend dir", preview)
	}
}

// TestReclaimIgnoresADirectoryCargoNeverWrote: `target` is a common enough name
// that acting on it without cargo's markers would eventually delete something
// that was never cargo's.
func TestReclaimIgnoresADirectoryCargoNeverWrote(t *testing.T) {
	wt := worktree(t)
	dir := filepath.Join(wt, "target")
	write(t, filepath.Join(dir, "debug", "notes.txt"), "someone else's directory")

	freed, err := Reclaim(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if freed != 0 {
		t.Errorf("freed %d bytes from a directory with no cargo markers", freed)
	}
	if _, err := os.Stat(filepath.Join(dir, "debug", "notes.txt")); err != nil {
		t.Errorf("removed content from a non-cargo directory: %v", err)
	}
}
