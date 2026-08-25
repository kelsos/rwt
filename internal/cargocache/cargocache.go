// Package cargocache gives every rotki worktree its own cargo target directory,
// at <worktree>/target, and keeps the built service binaries where rotki's dev
// launcher looks for them.
//
// It used to do the opposite. Until 2026-08 every worktree was pointed at one
// shared target dir per workspace, on the theory that rotki's crates carry a
// metadata hash derived from their manifest path and so could not collide. That
// theory is false, and the cost of believing it was silent: cargo records
// depfile paths *relative* to the workspace root, never absolute, so the
// metadata hash and the fingerprint path are identical across worktrees. One
// shared target dir is one fingerprint namespace for every worktree using it,
// with freshness decided on mtimes inside it, and a worktree whose files are
// older than the cached fingerprint is declared fresh and handed the
// neighbour's compilation unit. Measured on a real umbrella, every worktree in
// it resolved to a single colibri and a single starling.
//
// A target dir per worktree removes the failure by construction rather than
// detecting it: separate dirs are separate namespaces, which is the arrangement
// cargo assumes by default. Collisions is kept as the regression guard.
//
// The reuse this gives up is smaller than it looks and was measured before it
// was traded away: a cold build of colibri and starling into an empty target dir
// takes ~50s and 1.4GB, and rebuilding an already-built worktree stays a ~0.1s
// no-op because each worktree keeps its own dir. Across the umbrella that is
// less disk in total than the shared cache had grown to, since a shared dir
// accumulates every worktree's artifacts without deduplicating them. Cargo's
// exclusive per-target-dir lock, which made two worktrees building at once
// serialise, goes away with it.
//
// sccache remains wired as a rustc-wrapper when installed, but it is a trim and
// not the mechanism: with paths normalised it reaches ~92% on rustc invocations
// across worktrees, and that converts to about 10% of wall clock, because a cold
// build is dominated by link steps that sccache cannot cache at all. Making it
// reach even that requires SCCACHE_BASEDIRS to name every worktree root, which
// is a global sccache setting rather than a cargo one and is not yet rwt's to
// manage.
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
package cargocache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

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

// LegacyRoot is the old shared cache root, which held one target dir per
// workspace shared by every worktree. Nothing builds into it any more; it
// survives only so `rwt clean --cache` can reclaim it and `rwt doctor` can say
// how much it is still holding. RWT_CARGO_CACHE overrides it (used by the
// tests); otherwise it follows XDG_CACHE_HOME, falling back to ~/.cache.
func LegacyRoot() (string, error) {
	if v := os.Getenv("RWT_CARGO_CACHE"); v != "" {
		return v, nil
	}
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, "rwt", "target"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot resolve the legacy cargo cache root: %w", err)
	}
	return filepath.Join(home, ".cache", "rwt", "target"), nil
}

// TargetDir is a worktree's own target directory, shared by every cargo
// workspace in it.
//
// One dir per worktree rather than one per workspace, for two reasons. rotki's
// dev launcher looks for prebuilt binaries at <worktree>/target/debug/<name>
// regardless of which workspace built them, so this is the only placement that
// keeps that shortcut working on the split colibri/crates layout as well as on
// the root workspace. And the path relative to the worktree root is then
// identical everywhere, which is what lets sccache normalise the --out-dir and
// -L dependency= arguments it hashes.
//
// Two workspaces sharing one dir inside a worktree is safe in a way that two
// worktrees sharing one never was: they hold different packages, and a worktree
// is only ever on one layout at a time.
func TargetDir(worktree string) string {
	return filepath.Join(worktree, "target")
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

// SccacheConfigPath is the config file sccache reads at server start, honouring
// SCCACHE_CONF and then XDG_CONFIG_HOME.
func SccacheConfigPath() (string, error) {
	if v := os.Getenv("SCCACHE_CONF"); v != "" {
		return v, nil
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "sccache", "config"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot resolve the sccache config path: %w", err)
	}
	return filepath.Join(home, ".config", "sccache", "config"), nil
}

const sccacheHeader = generatedMarker + ` - do not edit; rwt rewrites this file.
#
# basedirs is what lets sccache reuse a compilation across worktrees. It strips
# these prefixes from paths before hashing, so the same source at two checkouts
# hashes identically. sccache hashes --out-dir and -L dependency= as well as the
# sources, which is why this pairs with rwt's per-worktree target dirs: relative
# to the worktree root every build presents "target/debug/deps", identically.
#
# It must list each WORKTREE ROOT. A common parent does not work: stripping it
# leaves "develop/colibri/src/..." against "master/colibri/src/...", which still
# differ. Measured on two worktrees at the same commit, a common parent gave 0%
# Rust cache hits and per-worktree roots gave 92%.
#
# Note the plural. SCCACHE_BASEDIR (singular) is silently ignored, and the only
# symptom is "Base directories (none)" in ` + "`sccache --show-stats`" + `.
`

// SyncBasedirs points sccache's basedirs at every worktree in the umbrella, and
// reports whether the file changed.
//
// Regenerated wholesale from the current worktree list rather than appended to,
// because a stale entry for a removed worktree is not harmless clutter: basedirs
// resolves by longest matching prefix, so a leftover can shadow the entry that
// should have matched.
//
// A config sccache's user wrote themselves is never overwritten. It may hold
// cache backends, size limits or auth that rwt knows nothing about, and losing
// those to a target-dir tool would be a poor trade for a 10% build speedup.
//
// The running server does not reread this: it loads the config at startup, so a
// changed file takes effect on the next server start. Callers that care use
// RestartSccache.
func SyncBasedirs(worktrees []string) (changed bool, err error) {
	if Wrapper() == "" {
		return false, nil
	}
	path, err := SccacheConfigPath()
	if err != nil {
		return false, err
	}
	existing, err := os.ReadFile(path)
	switch {
	case err == nil && !strings.HasPrefix(string(existing), generatedMarker):
		return false, errSccacheConfigNotOurs
	case err != nil && !os.IsNotExist(err):
		return false, err
	}
	want := renderSccacheConfig(worktrees)
	if string(existing) == want {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// errSccacheConfigNotOurs marks the one failure callers report rather than treat
// as broken: the developer has their own sccache config.
var errSccacheConfigNotOurs = errors.New("sccache config was not written by rwt")

// IsSccacheConfigNotOurs reports whether SyncBasedirs declined because the
// config file belongs to the developer.
func IsSccacheConfigNotOurs(err error) bool { return errors.Is(err, errSccacheConfigNotOurs) }

// renderSccacheConfig builds the config body. Sorted so an unchanged umbrella
// renders byte-identically and the file is not rewritten (and the server not
// restarted) for a reordering of git's output.
func renderSccacheConfig(worktrees []string) string {
	dirs := append([]string(nil), worktrees...)
	sort.Strings(dirs)
	var b strings.Builder
	b.WriteString(sccacheHeader)
	b.WriteString("\nbasedirs = [\n")
	for _, d := range dirs {
		fmt.Fprintf(&b, "  %s,\n", quote(d))
	}
	b.WriteString("]\n")
	return b.String()
}

// RestartSccache stops the sccache server so the next compile starts one that
// has read the current config. Stopping is enough: any cargo build starts one on
// demand. Best-effort, and silent when sccache is absent — a failure here costs
// cache hits, not correctness.
func RestartSccache() {
	if wrapper := Wrapper(); wrapper != "" {
		exec.Command(wrapper, "--stop-server").Run()
	}
}

// header explains the file to whoever opens it in the rotki checkout, where it
// is otherwise an unexplained untracked-but-excluded artifact.
// generatedMarker opens every config rwt writes. It is what makes a config
// safe to delete when the layout moves on: a hand-written .cargo/config.toml is
// left alone.
const generatedMarker = "# Generated by rwt"

const header = generatedMarker + ` - do not edit; rwt new / setup / refresh rewrite this file.
#
# This worktree builds into its own target dir. Worktrees used to share one, which
# was silently wrong: cargo fingerprints its own crates on paths relative to the
# workspace root, so a shared target dir is one fingerprint namespace for every
# worktree using it, and worktrees with different sources end up running whichever
# binary was built last. A dir per worktree is what cargo assumes by default.
#
# The cost is that a fresh worktree compiles its dependencies once (~50s), instead
# of inheriting them. Rebuilds of an already-built worktree stay a no-op.
#
# Do NOT export CARGO_INCREMENTAL=1 with a rustc-wrapper set: sccache checks that
# variable rather than the rustc flag and aborts the build outright with
# "incremental compilation is prohibited". Left unset, as it is here, cargo still
# compiles workspace members incrementally and sccache simply declines to cache
# those, which is the trade we want.
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
	target := TargetDir(worktree)
	var res Result
	for _, ws := range Workspaces(worktree) {
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
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return res, err
		}
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			return res, err
		}
		res.Wired = append(res.Wired, ws.Name)
	}
	prune(worktree, append(res.Wired, res.Kept...))
	if len(res.Wired) > 0 {
		DropSharedLinks(worktree)
	}
	return res, nil
}

// DropSharedLinks removes the launcher symlinks the shared-cache design planted
// at <worktree>/target/debug/<name>, returning the binaries it unlinked.
//
// This is the migration, and it has to happen the moment a worktree is rewired:
// those links point into the old shared cache, so leaving one in place would
// keep the launcher running a neighbour's binary even though this worktree now
// builds its own. The next build puts a real file at the same path.
//
// Only symlinks leading outside the worktree are removed. A real file there is
// cargo's own output under the new scheme, and a relative link is not something
// this package ever wrote.
func DropSharedLinks(worktree string) []string {
	debug := filepath.Join(TargetDir(worktree), "debug")
	entries, err := os.ReadDir(debug)
	if err != nil {
		return nil
	}
	var dropped []string
	for _, e := range entries {
		p := filepath.Join(debug, e.Name())
		dest, err := os.Readlink(p)
		if err != nil || !filepath.IsAbs(dest) {
			continue
		}
		if strings.HasPrefix(dest, worktree+string(filepath.Separator)) {
			continue
		}
		if err := os.Remove(p); err == nil {
			dropped = append(dropped, e.Name())
		}
	}
	return dropped
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

// wired reports whether a workspace's config actually points at this worktree's
// own target dir, so that a workspace left alone because it carries the
// developer's own .cargo/config.toml is never counted as rwt's.
func wired(worktree string, ws Workspace) bool {
	body, err := os.ReadFile(ConfigPath(worktree, ws))
	return err == nil && strings.Contains(string(body), quote(TargetDir(worktree)))
}

// Status is one present workspace's wiring in a worktree, for rwt doctor.
// The target dir it should point at is not carried here: it is TargetDir(worktree)
// for every workspace in the worktree, so a per-workspace copy of it was only
// ever a way to disagree with that.
type Status struct {
	Name  string
	Wired bool // its config.toml points at this worktree's own target dir
}

// Inspect reports the wiring state of every workspace present in a worktree.
func Inspect(worktree string) []Status {
	var out []Status
	for _, ws := range Workspaces(worktree) {
		out = append(out, Status{Name: ws.Name, Wired: wired(worktree, ws)})
	}
	return out
}

// Collision is one built binary that several worktrees resolve to. Every
// worktree in Worktrees runs the same file, whatever its own sources say, and
// only one of them built it.
type Collision struct {
	Bin       string   // the binary, e.g. "colibri"
	Artifact  string   // the shared deps/<name>-<hash> every link resolves to
	Worktrees []string // worktree paths, in the order given
}

// Collisions reports binaries that more than one worktree resolves to.
//
// Per-worktree target dirs make this unreachable, which is the point: it is kept
// as the regression guard for the failure described in the package comment, and
// on a migrated umbrella it returns nothing. What it still catches is a worktree
// left on the old wiring, since the symlink into the shared cache is exactly
// what it looks for, and anyone who reintroduces a shared target dir by hand.
//
// Reported by symlink target rather than by comparing sources: resolving the
// link is exact and costs a readlink, whereas "are these trees the same" is both
// expensive and beside the point. Two worktrees on identical sources sharing a
// binary is harmless today and still a collision waiting to bite the moment one
// of them changes.
func Collisions(worktrees []string) []Collision {
	// artifact -> bin, and artifact -> the worktrees pointing at it.
	bins := map[string]string{}
	users := map[string][]string{}
	var order []string
	for _, wt := range worktrees {
		for _, ws := range Workspaces(wt) {
			// Deliberately not gated on wired(): a worktree still on the old
			// shared-cache config is precisely what this has to catch, and that
			// config points somewhere wired() now rejects. The symlink is the
			// evidence, so read it and let the config say whatever it says.
			for _, bin := range ws.Bins {
				dest, err := os.Readlink(filepath.Join(LauncherBinDir(wt), bin))
				if err != nil {
					continue // a real file, or nothing: not a shared artifact
				}
				if _, seen := users[dest]; !seen {
					order = append(order, dest)
					bins[dest] = bin
				}
				users[dest] = append(users[dest], wt)
			}
		}
	}
	var out []Collision
	for _, artifact := range order {
		if len(users[artifact]) < 2 {
			continue
		}
		out = append(out, Collision{
			Bin: bins[artifact], Artifact: artifact, Worktrees: users[artifact],
		})
	}
	return out
}

// supersededTargetDirs are the target dirs the split colibri/crates layout used
// to build into. Every workspace now shares <worktree>/target, so these are
// leftovers whichever layout the worktree is currently on: a worktree rebased
// onto the root workspace keeps its old colibri/target, and one still on the
// split layout stops writing to them the moment it is rewired.
//
// <worktree>/target is deliberately absent. It is the live build directory now,
// not a leftover, and reclaiming it would delete the worktree's own output.
var supersededTargetDirs = []string{"colibri/target", "crates/target"}

// Running is one live process executing a binary out of a worktree's target dir.
type Running struct {
	PID  int
	Exe  string // the binary it is running, as /proc reports it
	Name string // the binary's base name, e.g. "starling"
}

// RunningFrom reports processes currently executing a binary from this
// worktree's target dir.
//
// `rwt clean --deep` removes that directory, and doing it under a live dev
// session is a real hazard rather than a theoretical one: starling supervises
// colibri and restarts it, so a respawn after the removal finds nothing to
// exec. Linux lets you unlink a running binary without complaint, which is
// exactly why this has to be checked rather than relied on to fail.
//
// Read from /proc/<pid>/exe, which is the kernel's answer to "what is this
// process actually running" and immune to the argv rewriting that makes a
// pgrep-style match guessy. Processes owned by other users are skipped: their
// exe link is unreadable, and they cannot be running out of this worktree's
// target dir in any case.
//
// Two paths count as this worktree's, because /proc resolves symlinks. A
// migrated worktree runs a real file under its own target dir, which the prefix
// catches. One still on the old shared-cache wiring runs through a symlink, so
// its exe reports the shared cache instead, and only comparing against what the
// worktree's own links point at finds it. Missing that case would aim the guard
// away from the worktrees most likely to be mid-session.
func RunningFrom(worktree string) []Running {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	target := TargetDir(worktree)
	prefix := target + string(filepath.Separator)
	linked := linkTargets(filepath.Join(target, "debug"))
	var out []Running
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe"))
		if err != nil {
			continue
		}
		// A binary unlinked since it started still reports its old path, with a
		// " (deleted)" tail. Compared as a path, so it still matches: a process
		// running a deleted artifact is the strongest reason not to touch the
		// directory, since its supervisor cannot respawn it either.
		exe = strings.TrimSuffix(exe, " (deleted)")
		if !strings.HasPrefix(exe, prefix) && !linked[exe] {
			continue
		}
		out = append(out, Running{PID: pid, Exe: exe, Name: filepath.Base(exe)})
	}
	return out
}

// linkTargets maps the absolute destinations of the symlinks directly inside a
// directory, so a process running through one can be traced back to it.
func linkTargets(dir string) map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if dest, err := os.Readlink(filepath.Join(dir, e.Name())); err == nil && filepath.IsAbs(dest) {
			out[dest] = true
		}
	}
	return out
}

// SupersededTargets returns the target directories in a worktree that nothing
// builds into any more. These are what rwt clean reclaims.
func SupersededTargets(worktree string) []string {
	var out []string
	for _, rel := range supersededTargetDirs {
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
//
// A hardlinked file is counted once, however many links to it the tree holds.
// Cargo hardlinks heavily — the uplift slot at debug/<name> is a link to the
// artifact under debug/deps/ — so counting every link inflated these figures
// well past what the disk would actually give back: a target dir `du` put at
// 1.4 GB reported as several. Since the number exists to answer "is reclaiming
// this worth it", the honest one is what `du` would say.
func DirSize(root string) int64 {
	var total int64
	seen := map[[2]uint64]bool{}
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		// Only multiply-linked files need the bookkeeping, and only on systems
		// exposing the inode. Anywhere else this is the plain sum it always was.
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
			key := [2]uint64{uint64(st.Dev), uint64(st.Ino)}
			if seen[key] {
				return nil
			}
			seen[key] = true
		}
		total += info.Size()
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
