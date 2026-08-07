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

// TestWirePointsAtSharedTarget is the core contract: the generated config sends
// cargo at the shared per-workspace target dir, and separate workspaces get
// distinct dirs so they never contend on one lock.
func TestWirePointsAtSharedTarget(t *testing.T) {
	wt := worktree(t)
	splitLayout(t, wt, "colibri", "crates")

	if _, err := Wire(wt); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	seen := map[string]bool{}
	for _, ws := range Workspaces(wt) {
		body, err := os.ReadFile(ConfigPath(wt, ws))
		if err != nil {
			t.Fatalf("reading %s config: %v", ws.Name, err)
		}
		target, err := TargetDir(ws)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `target-dir = "`+target+`"`) {
			t.Errorf("%s config does not point at %s:\n%s", ws.Name, target, body)
		}
		if seen[target] {
			t.Errorf("workspaces share the target dir %s; they must be separate", target)
		}
		seen[target] = true
		if _, err := os.Stat(target); err != nil {
			t.Errorf("shared target dir %s was not created: %v", target, err)
		}
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
// wired after, with a leftover per-worktree target flagged stale.
func TestInspectReportsWiring(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)
	cargoTarget(t, filepath.Join(wt, "target"))

	before := Inspect(wt)[0]
	if before.Wired {
		t.Errorf("expected unwired before Wire, got %+v", before)
	}
	if !before.Stale {
		t.Errorf("expected the leftover target dir to be flagged stale")
	}

	if _, err := Wire(wt); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	if after := Inspect(wt)[0]; !after.Wired {
		t.Errorf("expected wired after Wire, got %+v", after)
	}
}

// TestLocalTargetsSpansLayouts is what rwt clean reclaims. Every layout's target
// dir is found regardless of the worktree's current layout: rebasing onto the
// root workspace strands the old colibri/target, and that is the dir most worth
// reclaiming.
func TestLocalTargetsSpansLayouts(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)
	cargoTarget(t, filepath.Join(wt, "target"))
	cargoTarget(t, filepath.Join(wt, "colibri", "target"))

	got := LocalTargets(wt)
	want := []string{filepath.Join(wt, "target"), filepath.Join(wt, "colibri", "target")}
	if len(got) != len(want) {
		t.Fatalf("LocalTargets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("LocalTargets[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

// TestLocalTargetsIgnoresNonCargoDirs guards the removal: `target` is an ordinary
// enough directory name that rwt clean must confirm cargo owns it before
// deleting it.
func TestLocalTargetsIgnoresNonCargoDirs(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)
	if err := os.MkdirAll(filepath.Join(wt, "target", "something"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := LocalTargets(wt); len(got) != 0 {
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

// sharedDebug fakes the shared cache's debug dir for the root layout: one
// artifact per worktree under deps/, with the uplift slot hardlinked at the one
// belonging to `mine`, exactly as cargo leaves it after a build.
func sharedDebug(t *testing.T, bin, mine string, others ...string) string {
	t.Helper()
	target, err := TargetDir(rootWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	debug := filepath.Join(target, "debug")
	for _, hash := range append([]string{mine}, others...) {
		write(t, filepath.Join(debug, "deps", bin+"-"+hash), "binary "+hash)
		write(t, filepath.Join(debug, "deps", bin+"-"+hash+".d"), "depfile\n")
	}
	if err := os.Link(filepath.Join(debug, "deps", bin+"-"+mine), filepath.Join(debug, bin)); err != nil {
		t.Fatal(err)
	}
	return debug
}

// TestLinkBinsPointsAtThisWorktreesArtifact is the fix for the launcher falling
// back to `cargo run`: rotki's dev launcher runs <worktree>/target/debug/<name>,
// which redirecting the target dir leaves empty. The link has to resolve to the
// deps artifact the uplift slot is hardlinked at, not to the slot itself, since
// the slot belongs to whichever worktree built last.
func TestLinkBinsPointsAtThisWorktreesArtifact(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)
	if _, err := Wire(wt); err != nil {
		t.Fatal(err)
	}
	debug := sharedDebug(t, "colibri", "aaaa1111", "bbbb2222")

	linked, err := LinkBins(wt, rootWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(linked) != 1 || linked[0] != "colibri" {
		t.Fatalf("linked = %v, want [colibri]", linked)
	}
	dest, err := os.Readlink(filepath.Join(LauncherBinDir(wt), "colibri"))
	if err != nil {
		t.Fatalf("launcher path is not a symlink: %v", err)
	}
	if want := filepath.Join(debug, "deps", "colibri-aaaa1111"); dest != want {
		t.Errorf("link resolves to %s, want the deps artifact %s", dest, want)
	}
}

// TestLinkBinsReplacesAPreWiringBinary covers the worktree that was built before
// the shared cache existed: target/debug/colibri is a real binary there, and
// leaving it would have the launcher run a stale build forever.
func TestLinkBinsReplacesAPreWiringBinary(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)
	if _, err := Wire(wt); err != nil {
		t.Fatal(err)
	}
	sharedDebug(t, "colibri", "aaaa1111")
	write(t, filepath.Join(LauncherBinDir(wt), "colibri"), "stale pre-wiring binary")

	if _, err := LinkBins(wt, rootWorkspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(filepath.Join(LauncherBinDir(wt), "colibri")); err != nil {
		t.Errorf("stale binary was not replaced by a symlink: %v", err)
	}
}

// TestLinkBinsLeavesAnUnwiredWorkspaceAlone: a workspace kept on the
// developer's own .cargo/config.toml builds into its own target dir, so its
// binary is neither rwt's to remove nor the shared cache's to point at.
func TestLinkBinsLeavesAnUnwiredWorkspaceAlone(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)
	write(t, ConfigPath(wt, rootWorkspace), "[build]\nrustflags = []\n")
	sharedDebug(t, "colibri", "aaaa1111")
	own := filepath.Join(LauncherBinDir(wt), "colibri")
	write(t, own, "the developer's own build")

	linked, err := LinkBins(wt, rootWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(linked) != 0 {
		t.Errorf("linked %v in an unwired worktree", linked)
	}
	if body, err := os.ReadFile(own); err != nil || string(body) != "the developer's own build" {
		t.Errorf("the developer's own binary was touched: %q, %v", body, err)
	}
}

// TestLinkBinsSkipsAnUnresolvableSlot: with no uplift slot there is nothing to
// identify this worktree's artifact by, and guessing would mean linking another
// worktree's binary. Skipping leaves the launcher on its `cargo run` fallback,
// which is slower but right.
func TestLinkBinsSkipsAnUnresolvableSlot(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)
	if _, err := Wire(wt); err != nil {
		t.Fatal(err)
	}
	target, _ := TargetDir(rootWorkspace)
	write(t, filepath.Join(target, "debug", "deps", "colibri-aaaa1111"), "orphan")

	linked, err := LinkBins(wt, rootWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(linked) != 0 {
		t.Errorf("linked %v with no uplift slot to match against", linked)
	}
	if _, err := os.Lstat(filepath.Join(LauncherBinDir(wt), "colibri")); !os.IsNotExist(err) {
		t.Error("a link was planted despite the slot being unresolvable")
	}
}

// TestPrepareBuildClearsTheUpliftSlot: cargo only writes the slot when a build
// produces output, so a fresh build would leave another worktree's link in
// place. Removing it first is what makes cargo re-link and LinkBins reliable.
func TestPrepareBuildClearsTheUpliftSlot(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)
	if _, err := Wire(wt); err != nil {
		t.Fatal(err)
	}
	debug := sharedDebug(t, "colibri", "aaaa1111")

	if err := PrepareBuild(wt, rootWorkspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(debug, "colibri")); !os.IsNotExist(err) {
		t.Error("the uplift slot survived PrepareBuild")
	}
	// The compiled artifact must not go with it: the slot is only a hardlink,
	// and clearing it has to stay free of any rebuild cost.
	if _, err := os.Stat(filepath.Join(debug, "deps", "colibri-aaaa1111")); err != nil {
		t.Errorf("the deps artifact was removed along with the slot: %v", err)
	}
}

// TestPrepareBuildLeavesAnUnwiredWorkspaceAlone keeps rwt out of a target dir it
// does not own.
func TestPrepareBuildLeavesAnUnwiredWorkspaceAlone(t *testing.T) {
	wt := worktree(t)
	rootLayout(t, wt)
	debug := sharedDebug(t, "colibri", "aaaa1111")

	if err := PrepareBuild(wt, rootWorkspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(debug, "colibri")); err != nil {
		t.Errorf("cleared the slot for an unwired worktree: %v", err)
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

// TestReclaimKeepsLauncherLinks: the symlinks LinkBins plants live inside
// debug/, cost no disk, and still resolve after the reclaim. Dropping them would
// send the next dev launch back to `cargo run` for nothing gained.
func TestReclaimKeepsLauncherLinks(t *testing.T) {
	wt := worktree(t)
	target := cargoTarget(t, filepath.Join(wt, "target"))
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
	dest, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("the launcher link was removed: %v", err)
	}
	if dest != shared {
		t.Errorf("link now points at %s, want %s", dest, shared)
	}
	if _, err := os.Stat(filepath.Join(target, "debug", "deps")); !os.IsNotExist(err) {
		t.Error("cargo's deps output survived alongside the link")
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
