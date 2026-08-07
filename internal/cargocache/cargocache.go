// Package cargocache points every rotki worktree's cargo builds at one shared
// target directory per cargo workspace, so a fresh or switched-to worktree
// reuses already-compiled dependencies instead of recompiling the tree.
//
// Registry dependencies are fingerprinted by version, features and rustflags,
// not by workspace path, so every worktree reuses them from the shared dir.
// rotki's own crates carry a metadata hash derived from their manifest path, so
// artifacts from different worktrees coexist rather than clobbering each other:
// switching back to a worktree you already built is a no-op, not a rebuild.
//
// The wiring is a generated .cargo/config.toml at each workspace root rather
// than an environment variable, because the dev launch itself shells out to
// cargo (frontend/scripts/dev/services.ts runs `cargo build --locked` from the
// worktree root). A config file is picked up by both rwt's warm step and the
// app's own cargo invocations; an env var exported from a shell rc would miss
// anything launched from an IDE or an agent.
//
// Placement matters and is easy to get wrong: cargo discovers config files by
// walking up from the *current working directory*, never from --manifest-path.
// A config in colibri/ is therefore invisible to `cargo build --manifest-path
// colibri/Cargo.toml` run from the worktree root, which is how both rwt and the
// app build. The config always goes at the workspace root, whose ancestors
// cover every cwd a build is launched from.
//
// The one cost is contention: cargo takes an exclusive lock on a target dir, so
// two worktrees building the same workspace at the same time serialise. That is
// a wait during compilation, not a failure, and only bites while something is
// actually being built.
package cargocache

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kelsos/rwt/internal/git"
)

// Workspace is one cargo workspace inside a rotki worktree. Separate workspaces
// have separate lockfiles, so each gets its own shared target dir and they never
// contend with one another.
type Workspace struct {
	Name     string   // shared-cache dir name and log label
	Dir      string   // workspace root, relative to the worktree root
	Manifest string   // workspace manifest, relative to the worktree root
	Build    []string // the warm-build command, run with the workspace root as cwd
	Bins     []string // the service binaries that build produces, for LinkBins
}

// rootWorkspace is the current rotki layout: one workspace at the repo root with
// colibri and crates/* as members, sharing a single lockfile and target dir.
var rootWorkspace = Workspace{
	Name: "rotki", Dir: ".", Manifest: "Cargo.toml",
	Build: []string{"cargo", "build", "--locked", "-p", "colibri", "-p", "starling"},
	Bins:  []string{"colibri", "starling"},
}

// splitWorkspaces is the older layout, still checked out on long-lived bases
// that predate the root workspace: colibri and crates as two independent
// workspaces. A base older still (bugfixes, master) has colibri only.
//
// Transitional. The root workspace becomes the baseline on every live base after
// the next rotki release, at which point this fallback, the layout detection in
// Workspaces, and the extra entries in excludePatterns and localTargetDirs can
// all go, leaving one workspace at the worktree root.
var splitWorkspaces = []Workspace{
	{Name: "colibri", Dir: "colibri", Manifest: "colibri/Cargo.toml",
		Build: []string{"cargo", "build", "--locked"}, Bins: []string{"colibri"}},
	{Name: "crates", Dir: "crates", Manifest: "crates/Cargo.toml",
		Build: []string{"cargo", "build", "--locked", "-p", "starling"}, Bins: []string{"starling"}},
}

// Workspaces returns the cargo workspaces actually present in a worktree.
//
// The layout is detected rather than assumed, because it differs by base: the
// root workspace landed on develop, while older bases still carry the split
// colibri/crates layout. Detecting it per worktree is what keeps a single rwt
// binary correct across every base checked out at once.
func Workspaces(worktree string) []Workspace {
	if isWorkspaceManifest(filepath.Join(worktree, rootWorkspace.Manifest)) {
		return []Workspace{rootWorkspace}
	}
	var out []Workspace
	for _, ws := range splitWorkspaces {
		if _, err := os.Stat(filepath.Join(worktree, ws.Manifest)); err == nil {
			out = append(out, ws)
		}
	}
	return out
}

// isWorkspaceManifest reports whether a Cargo.toml declares a [workspace]. The
// root manifest is only a workspace root when it says so; requiring the table
// avoids mistaking some future root-level package manifest for the workspace.
func isWorkspaceManifest(path string) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "[workspace]") {
			return true
		}
	}
	return false
}

// excludePatterns are the generated config paths added to the repository's
// shared info/exclude so the wiring never shows up in `git status`. The rotki
// .gitignore covers the target dirs but says nothing about .cargo/.
//
// All layouts' paths are excluded regardless of which one a worktree is on:
// worktrees on different bases share one exclude file, and a worktree that
// switches base switches layout with it.
var excludePatterns = []string{"/.cargo/", "colibri/.cargo/", "crates/.cargo/"}

// Root is the shared cache root holding one target dir per workspace.
// RWT_CARGO_CACHE overrides it (used by the tests); otherwise it follows
// XDG_CACHE_HOME, falling back to ~/.cache.
func Root() (string, error) {
	if v := os.Getenv("RWT_CARGO_CACHE"); v != "" {
		return v, nil
	}
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, "rwt", "target"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot resolve the shared cargo cache root: %w", err)
	}
	return filepath.Join(home, ".cache", "rwt", "target"), nil
}

// TargetDir is the shared target directory for one workspace.
func TargetDir(ws Workspace) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ws.Name), nil
}

// ConfigPath is the generated config file for one workspace in a worktree.
func ConfigPath(worktree string, ws Workspace) string {
	return filepath.Join(worktree, ws.Dir, ".cargo", "config.toml")
}

// Wrapper returns the absolute path to sccache, or "" when it is not installed.
//
// sccache is a second cache layer beneath the shared target dir: it catches
// misses a target dir cannot (a rustc upgrade, changed rustflags, a fingerprint
// miss), and unlike a target dir it is not guarded by a build lock. Its absence
// is not an error — the shared target dir carries the bulk of the win on its own.
func Wrapper() string {
	p, err := exec.LookPath("sccache")
	if err != nil {
		return ""
	}
	return p
}

// header explains the file to whoever opens it in the rotki checkout, where it
// is otherwise an unexplained untracked-but-excluded artifact.
// generatedMarker opens every config rwt writes. It is what makes a config
// safe to delete when the layout moves on: a hand-written .cargo/config.toml is
// left alone.
const generatedMarker = "# Generated by rwt"

const header = generatedMarker + ` - do not edit; rwt new / setup / refresh rewrite this file.
#
# Every rotki worktree shares one target dir per cargo workspace, so dependencies
# compile once and are reused everywhere. A worktree that has not changed since
# its last build does not recompile at all; a fresh one compiles only rotki's own
# crates. Reclaim the superseded per-worktree target dirs with ` + "`rwt clean`" + `.
#
# Incremental compilation is deliberately left at its default (on). sccache
# cannot cache incrementally-compiled crates, so this trades sccache hits on the
# workspace members for a fast edit-rebuild loop; the registry dependencies are
# not built incrementally and are cached either way. Export CARGO_INCREMENTAL=0
# to flip that trade if cold rebuilds ever start hurting more than the inner loop.
`

// render builds the config file body. wrapper is omitted entirely when sccache
// is not installed, rather than written empty, so cargo does not see a wrapper
// key it then has to resolve.
func render(targetDir, wrapper string) string {
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n[build]\n")
	fmt.Fprintf(&b, "target-dir = %s\n", quote(targetDir))
	if wrapper != "" {
		fmt.Fprintf(&b, "rustc-wrapper = %s\n", quote(wrapper))
	}
	return b.String()
}

// quote renders a path as a TOML basic string.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// Result is the outcome of wiring one worktree.
type Result struct {
	Wired []string // workspaces now pointing at the shared cache
	Kept  []string // workspaces left alone because their config is not rwt's
}

// Wire generates the shared-cache config for every cargo workspace present in
// the worktree. A worktree on a base with no cargo workspace at all wires
// nothing and reports no error.
//
// A .cargo/config.toml rwt did not write is the developer's, and is never
// overwritten: it is reported in Result.Kept so the caller can say so. rotki
// does not ship one today, but a hand-tuned local config (rustflags, a linker)
// is exactly the kind of thing that must not silently vanish.
//
// Writing is skipped when the content already matches, so re-running leaves
// mtimes alone. That matters: cargo folds the config into its fingerprints, and
// a rewritten rustc-wrapper or target-dir invalidates the cached artifacts.
func Wire(worktree string) (Result, error) {
	wrapper := Wrapper()
	var res Result
	for _, ws := range Workspaces(worktree) {
		target, err := TargetDir(ws)
		if err != nil {
			return res, err
		}
		path := ConfigPath(worktree, ws)
		want := render(target, wrapper)
		switch got, err := os.ReadFile(path); {
		case err == nil && string(got) == want:
			res.Wired = append(res.Wired, ws.Name)
			continue
		case err == nil && !strings.HasPrefix(string(got), generatedMarker):
			res.Kept = append(res.Kept, ws.Name)
			continue
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			return res, fmt.Errorf("cannot create shared target dir %s: %w", target, err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return res, err
		}
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			return res, err
		}
		res.Wired = append(res.Wired, ws.Name)
	}
	prune(worktree, append(res.Wired, res.Kept...))
	return res, nil
}

// prune removes configs rwt generated for a layout the worktree is no longer on.
// Rebasing a worktree from the split layout onto the root workspace otherwise
// leaves colibri/.cargo/config.toml in place, and since cargo resolves config
// from the cwd, a build started inside colibri/ would use the old shared dir
// while a build from the root used the new one: the same worktree compiling
// into two caches. Only files carrying rwt's marker are removed.
func prune(worktree string, active []string) {
	keep := map[string]bool{}
	for _, name := range active {
		keep[name] = true
	}
	for _, ws := range append([]Workspace{rootWorkspace}, splitWorkspaces...) {
		if keep[ws.Name] {
			continue
		}
		path := ConfigPath(worktree, ws)
		body, err := os.ReadFile(path)
		if err != nil || !strings.HasPrefix(string(body), generatedMarker) {
			continue
		}
		if err := os.Remove(path); err != nil {
			continue
		}
		// Best-effort: only succeeds when rwt's config was all it held.
		os.Remove(filepath.Dir(path))
	}
}

// Exclude appends the generated config paths to the repository's shared
// info/exclude, so the wiring stays invisible to `git status` in every worktree
// without touching the tracked .gitignore. Linked worktrees share the common git
// dir, so this is written once and applies everywhere; already-present patterns
// are left alone.
func Exclude(ctx context.Context, worktree string) error {
	common, err := git.CommonDir(ctx, worktree)
	if err != nil {
		return err
	}
	path := filepath.Join(common, "info", "exclude")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		have[strings.TrimSpace(line)] = true
	}
	var add []string
	for _, p := range excludePatterns {
		if !have[p] {
			add = append(add, p)
		}
	}
	if len(add) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n# rwt: shared cargo target-dir wiring\n")
	b.WriteString(strings.Join(add, "\n"))
	b.WriteString("\n")
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// LauncherBinDir is where rotki's dev launcher looks for prebuilt service
// binaries: <worktree>/target/debug/<name>, at the worktree root regardless of
// which workspace actually built them (devBuiltBinary in
// frontend/app/shared/starling/starling-args.ts, called with `path.join(root,
// 'target')`). Redirecting the target dir empties that path, which is why
// LinkBins has to put it back.
func LauncherBinDir(worktree string) string {
	return filepath.Join(worktree, "target", "debug")
}

// wired reports whether a workspace's config actually points at the current
// shared target dir. Everything that touches the shared cache's binaries is
// gated on it, so a workspace left alone because it carries the developer's own
// .cargo/config.toml is never involved.
func wired(worktree string, ws Workspace) bool {
	target, err := TargetDir(ws)
	if err != nil {
		return false
	}
	body, err := os.ReadFile(ConfigPath(worktree, ws))
	return err == nil && strings.Contains(string(body), quote(target))
}

// PrepareBuild clears the shared uplift slots for a workspace's binaries so the
// build that follows is guaranteed to re-link them for this worktree.
//
// <shared>/debug/<name> is one hardlink slot shared by every worktree, and cargo
// only writes it when a build actually produces output: a build that is entirely
// fresh leaves whichever worktree linked it last in place. Removing the slot
// first turns that into a guarantee, because cargo does re-link a *missing* slot
// even when nothing recompiles. That is what lets LinkBins tell, afterwards,
// which deps/<name>-<hash> artifact is this worktree's.
//
// Deleting the slot costs nothing: it is a hardlink to an artifact under deps/,
// so the compiled output survives and the relink is a filesystem operation, not
// a rebuild.
func PrepareBuild(worktree string, ws Workspace) error {
	if !wired(worktree, ws) {
		return nil
	}
	target, err := TargetDir(ws)
	if err != nil {
		return err
	}
	for _, bin := range ws.Bins {
		if err := os.Remove(filepath.Join(target, "debug", bin)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// LinkBins points <worktree>/target/debug/<name> at this worktree's freshly
// built artifact in the shared cache, and returns the binaries it linked.
//
// It must be called after a build that PrepareBuild ran ahead of; that pairing
// is what makes the uplift slot trustworthy. The link is made to the artifact
// under deps/ rather than to the slot itself: the slot belongs to whichever
// worktree built last, while the deps path carries a hash derived from this
// worktree's manifest path and so stays this worktree's artifact across every
// later rebuild. Cargo rewrites that path in place, so the symlink never needs
// refreshing and can never serve a stale binary.
//
// A link that cannot be resolved is skipped rather than guessed at: the launcher
// then falls back to `cargo run`, which is correct, just slower.
func LinkBins(worktree string, ws Workspace) ([]string, error) {
	if !wired(worktree, ws) {
		return nil, nil
	}
	target, err := TargetDir(ws)
	if err != nil {
		return nil, err
	}
	debug := filepath.Join(target, "debug")
	var linked []string
	for _, bin := range ws.Bins {
		artifact := artifactFor(debug, bin)
		if artifact == "" {
			continue
		}
		link := filepath.Join(LauncherBinDir(worktree), bin)
		if dest, err := os.Readlink(link); err == nil && dest == artifact {
			linked = append(linked, bin)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			return linked, err
		}
		// Removes a leftover real binary from before this worktree was wired as
		// readily as a stale symlink: either way it is what the launcher would
		// otherwise run, and the shared cache now holds the current one.
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			return linked, err
		}
		if err := os.Symlink(artifact, link); err != nil {
			return linked, err
		}
		linked = append(linked, bin)
	}
	return linked, nil
}

// artifactFor resolves the shared uplift slot for one binary to the underlying
// deps/<name>-<hash> artifact by matching inodes: cargo populates the slot by
// hardlinking, so the two are the same file. Returns "" when there is no match,
// which is the honest answer for a build that failed or a cargo that stopped
// uplifting.
func artifactFor(debug, bin string) string {
	slot, err := os.Stat(filepath.Join(debug, bin))
	if err != nil {
		return ""
	}
	deps := filepath.Join(debug, "deps")
	entries, err := os.ReadDir(deps)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), bin+"-") {
			continue
		}
		p := filepath.Join(deps, e.Name())
		if info, err := os.Stat(p); err == nil && os.SameFile(info, slot) {
			return p
		}
	}
	return ""
}

// Status is one present workspace's cache wiring in a worktree, for rwt doctor.
type Status struct {
	Name   string
	Wired  bool   // its config.toml points at the current shared target dir
	Target string // the shared target dir it should point at
	Stale  bool   // a per-worktree target dir it supersedes is still on disk
}

// Inspect reports the wiring state of every workspace present in a worktree.
func Inspect(worktree string) []Status {
	var out []Status
	for _, ws := range Workspaces(worktree) {
		s := Status{Name: ws.Name}
		if target, err := TargetDir(ws); err == nil {
			s.Target = target
			s.Wired = wired(worktree, ws)
		}
		s.Stale = isCargoTarget(filepath.Join(worktree, ws.Dir, "target"))
		out = append(out, s)
	}
	return out
}

// localTargetDirs are every place a cargo target dir lands under a worktree,
// across all three layouts. All of them are checked regardless of the worktree's
// current layout: rebasing a worktree onto the root workspace leaves the old
// colibri/target behind, and that leftover is exactly what needs reclaiming.
var localTargetDirs = []string{"target", "colibri/target", "crates/target"}

// LocalTargets returns the per-worktree target directories that exist in a
// worktree. These are what the shared cache supersedes and what rwt clean
// reclaims.
func LocalTargets(worktree string) []string {
	var out []string
	for _, rel := range localTargetDirs {
		p := filepath.Join(worktree, rel)
		if isCargoTarget(p) {
			out = append(out, p)
		}
	}
	return out
}

// cargoEntries are the entries cargo owns at the root of a target dir. Anything
// else found there was put there by something that is not cargo and is not
// Reclaim's to remove.
var cargoEntries = map[string]bool{
	"CACHEDIR.TAG":              true,
	".rustc_info.json":          true,
	".rustdoc_fingerprint.json": true,
	"debug":                     true,
	"release":                   true,
	"doc":                       true,
	"package":                   true,
	"tmp":                       true,
}

// Reclaim removes cargo's own output from a per-worktree target dir, returning
// the bytes it freed. With dryRun it removes nothing and returns what it would
// have freed; sharing one walk between the two modes is what keeps `rwt clean
// --dry-run` an honest preview rather than an estimate. The directory itself is
// removed only when nothing was left in it.
//
// Removing the tree wholesale would be simpler and wrong. rotki's e2e path
// freezes the python core into target/backend (`pyinstaller --distpath
// target/backend`), which cargo never wrote and which is slow to rebuild, so
// only the entries cargo owns are touched: its markers, its profile dirs, and
// the per-triple dirs a cross-compile leaves behind.
func Reclaim(dir string, dryRun bool) (int64, error) {
	if !isCargoTarget(dir) {
		return 0, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var freed int64
	kept := 0
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		switch {
		case e.Name() == "debug" || e.Name() == "release":
			n, left, err := reclaimProfile(p, dryRun)
			freed += n
			if err != nil {
				return freed, err
			}
			if left {
				kept++
			}
		case cargoEntries[e.Name()] || isTargetTriple(p):
			n, err := remove(p, dryRun)
			freed += n
			if err != nil {
				return freed, err
			}
		default:
			kept++
		}
	}
	if kept == 0 && !dryRun {
		os.Remove(dir)
	}
	return freed, nil
}

// reclaimProfile empties a profile dir of cargo's output while preserving the
// launcher symlinks LinkBins plants in it. Those point into the shared cache,
// occupy no disk, and still resolve afterwards; deleting them would push the
// next dev launch back onto the `cargo run` fallback for nothing gained. It
// reports the bytes freed and whether anything was left behind.
func reclaimProfile(dir string, dryRun bool) (int64, bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, false, err
	}
	var freed int64
	kept := 0
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if isLauncherLink(p) {
			kept++
			continue
		}
		n, err := remove(p, dryRun)
		freed += n
		if err != nil {
			return freed, kept > 0, err
		}
	}
	if kept == 0 && !dryRun {
		os.Remove(dir)
	}
	return freed, kept > 0, nil
}

// remove deletes a path and reports its size, or with dryRun just measures it.
func remove(path string, dryRun bool) (int64, error) {
	n := DirSize(path)
	if dryRun {
		return n, nil
	}
	if err := os.RemoveAll(path); err != nil {
		return 0, err
	}
	return n, nil
}

// isLauncherLink identifies a symlink LinkBins planted: one pointing at an
// absolute path outside the directory that holds it. Cargo's own output is
// never that, so the test needs no bookkeeping to stay accurate.
func isLauncherLink(p string) bool {
	dest, err := os.Readlink(p)
	if err != nil {
		return false
	}
	return filepath.IsAbs(dest) && !strings.HasPrefix(dest, filepath.Dir(p)+string(filepath.Separator))
}

// isTargetTriple reports whether a target-dir entry is a cross-compile output
// dir, by what cargo builds inside one. Matching on the name would mean carrying
// a list of rust triples and still missing new ones.
func isTargetTriple(dir string) bool {
	for _, profile := range []string{"debug", "release"} {
		if st, err := os.Stat(filepath.Join(dir, profile, ".fingerprint")); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

// isCargoTarget reports whether a directory is a cargo target dir, by the
// markers cargo writes at its root. The check guards `rwt clean`: `target` is a
// common enough directory name that removing one on the strength of its name
// alone would eventually delete something that was never cargo's.
func isCargoTarget(dir string) bool {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return false
	}
	for _, marker := range []string{"CACHEDIR.TAG", ".rustc_info.json"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

// DirSize sums the on-disk size of a directory tree, best-effort: unreadable
// entries are skipped rather than failing the walk, since this only ever feeds a
// human-facing "reclaimed" number.
func DirSize(root string) int64 {
	var total int64
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// HumanBytes formats a byte count for the CLI.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
