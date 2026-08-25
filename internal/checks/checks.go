// Package checks is the local mirror of rotki's CI gates: a catalog of commands
// tagged by which part of the repo they cover and how much they cost, plus the
// planner that picks the ones a given set of changed files actually needs.
//
// Two rules shape everything here. A check is only planned when the worktree can
// actually run it (the script set differs by base, so `knip` exists on develop
// and not on bugfixes), and a check never mutates the tree, because a hook that
// rewrites what you staged is a hook you stop trusting.
package checks

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kelsos/rwt/internal/cargocache"
	"github.com/kelsos/rwt/internal/rotki"
)

// Tier is how much a check costs, which is what decides the stage it runs in.
// Tiers are ordered, and a run stops after the first tier that fails, so the
// cheap failure is the one you see.
type Tier int

const (
	// TierFast is seconds: formatters and file-scoped linters. This is the whole
	// of pre-commit.
	TierFast Tier = iota
	// TierStandard is tens of seconds to a couple of minutes: whole-project
	// typecheck, dead-export analysis, clippy.
	TierStandard
	// TierHeavy is minutes, and only ever runs tests narrowed to what changed.
	TierHeavy
)

func (t Tier) String() string {
	switch t {
	case TierFast:
		return "fast"
	case TierStandard:
		return "standard"
	case TierHeavy:
		return "heavy"
	}
	return "unknown"
}

// FileMode says whether the changed paths are appended to Argv.
type FileMode int

const (
	// FilesNone runs the command as written; it finds its own scope.
	FilesNone FileMode = iota
	// FilesAppend appends the matching changed paths, so the command only looks
	// at what you touched.
	FilesAppend
)

// Check is one command mirroring one CI gate.
type Check struct {
	Name  string // display name and log prefix, e.g. "ruff"
	Group string // one of the rotki.Group* constants
	Tier  Tier
	Dir   string   // working dir relative to the worktree root
	Argv  []string // command + args
	Env   []string // extra environment, "KEY=value"
	Files FileMode
	// Exts narrows FilesAppend to paths with these extensions (e.g. ".py").
	// Empty means every changed path in the group.
	Exts []string
	// Script, when set, is a pnpm script name that must exist in the worktree's
	// frontend/package.json. This is what keeps a bugfixes-based worktree from
	// planning checks that only landed on develop.
	Script string
	// Needs, when set, are paths relative to the worktree root that must all
	// exist for the check to be planned.
	//
	// The Python checks need their tool inside the worktree's .venv, not on
	// PATH: `uv run` would otherwise resolve the group on the fly and turn a
	// commit into a package install, or fail outright and block the commit over
	// a missing dependency rather than over the code.
	Needs []string
	// Fix, when set, is the command that makes an unmet Needs met. It is quoted
	// in the skip reason, so a skipped check tells you how to stop skipping it.
	Fix string
	// CIJob names the CI job this mirrors, so a local failure says what will
	// fail upstream.
	CIJob string
}

// Skip is a check that was not planned, and why. Skips are reported rather than
// swallowed: a gate that quietly does nothing is worse than no gate.
type Skip struct {
	Name   string
	Reason string
}

// VenvBin is where uv puts the worktree's Python tools. Checks gate on the tool
// being there rather than letting `uv run` resolve it, so a commit never turns
// into a package install and never fails over a missing dependency.
const VenvBin = ".venv/bin/"

// LintGroupFix is the command that installs the Python lint tools. rwt now syncs
// every dependency group, so this only comes up for a worktree warmed before
// that: re-running the uv step is all it takes.
const LintGroupFix = "rwt setup <worktree> --only uv"

// Catalog is every check rwt knows how to run, in tier order.
//
// Command lines are copied from the CI jobs they mirror, and go through the
// project's own script/Makefile wrappers rather than invoking eslint, vitest or
// vue-tsc directly. The wrappers pin the working directory; invoking the tools
// straight from an arbitrary cwd silently breaks cwd-relative rule options and
// manufactures failures that are not real.
func Catalog() []Check {
	return []Check{
		// ---- fast: pre-commit ----
		{
			Name: "typos", Group: "", Tier: TierFast, Dir: ".",
			Argv: []string{"typos"}, Files: FilesAppend, CIJob: "check-typos",
		},
		{
			// lint-staged reads the git index itself, so it needs no file
			// arguments; it is also exactly what rotki's own husky hook ran.
			Name: "lint-staged", Group: rotki.GroupFrontend, Tier: TierFast, Dir: "frontend",
			Argv: []string{"pnpm", "run", "lint-staged"}, Script: "lint-staged",
			CIJob: "lint-frontend (ESLint + Stylelint)",
		},
		{
			Name: "ruff", Group: rotki.GroupBackend, Tier: TierFast, Dir: ".",
			Argv:  []string{"uv", "run", "--group", "lint", "ruff", "check"},
			Files: FilesAppend, Exts: []string{".py"},
			Needs: []string{VenvBin + "ruff"}, Fix: LintGroupFix, CIJob: "lint-backend",
		},
		{
			Name: "double-indent", Group: rotki.GroupBackend, Tier: TierFast, Dir: ".",
			Argv:  []string{"uv", "run", "--group", "lint", "double-indent", "--dry-run"},
			Files: FilesAppend, Exts: []string{".py"},
			Needs: []string{VenvBin + "double-indent"}, Fix: LintGroupFix, CIJob: "lint-backend",
		},
		{
			Name: "checksum-addresses", Group: rotki.GroupBackend, Tier: TierFast, Dir: ".",
			Argv:  []string{"uv", "run", "python", "tools/lint_checksum_addresses.py"},
			Needs: []string{"tools/lint_checksum_addresses.py", VenvBin + "python"},
			Fix:   "rwt setup <worktree>", CIJob: "lint-backend",
		},
		{
			Name: "cargo-fmt", Group: rotki.GroupStarling, Tier: TierFast, Dir: ".",
			Argv: []string{"cargo", "fmt", "--all", "--check"}, CIJob: "lint-starling",
		},

		// ---- standard: pre-push ----
		{
			Name: "typecheck", Group: rotki.GroupFrontend, Tier: TierStandard, Dir: "frontend",
			Argv: []string{"pnpm", "run", "typecheck"}, Script: "typecheck",
			CIJob: "lint-frontend (Typecheck)",
		},
		{
			// The gate with the worst local/CI gap: knip runs in CI and no local
			// script invokes it, so an export used only inside its own file
			// passes everything you can run by hand and fails the PR.
			Name: "knip", Group: rotki.GroupFrontend, Tier: TierStandard, Dir: "frontend",
			Argv: []string{"pnpm", "run", "knip"}, Script: "knip",
			CIJob: "lint-frontend (Knip)",
		},
		{
			Name: "stylelint", Group: rotki.GroupFrontend, Tier: TierStandard, Dir: "frontend",
			Argv: []string{"pnpm", "run", "lint:style"}, Script: "lint:style",
			CIJob: "lint-frontend (Stylelint)",
		},
		{
			Name: "linked-keys", Group: rotki.GroupFrontend, Tier: TierStandard, Dir: "frontend",
			Argv: []string{"pnpm", "run", "check:linked-keys"}, Script: "check:linked-keys",
			CIJob: "lint-frontend (Linked i18n keys)",
		},
		{
			Name: "dev-proxy", Group: rotki.GroupFrontend, Tier: TierStandard, Dir: "frontend",
			Argv: []string{"pnpm", "run", "test:proxy"}, Script: "test:proxy",
			CIJob: "lint-frontend (dev-proxy)",
		},
		{
			Name: "mypy", Group: rotki.GroupBackend, Tier: TierStandard, Dir: ".",
			Argv: []string{"uv", "run", "--group", "lint", "mypy",
				"rotkehlchen/", "rotkehlchen_mock/", "package.py", "docs/conf.py",
				"--install-types", "--non-interactive"},
			Needs: []string{VenvBin + "mypy"}, Fix: LintGroupFix, CIJob: "lint-backend",
		},
		{
			// Already diff-scoped in CI via LINT_DIFF_BASE, which is what makes
			// it a natural hook check rather than a whole-tree pass.
			Name: "logging-fstrings", Group: rotki.GroupBackend, Tier: TierStandard, Dir: ".",
			Argv:  []string{"uv", "run", "python", "tools/lint_new_logging_fstrings.py"},
			Needs: []string{"tools/lint_new_logging_fstrings.py", VenvBin + "python"},
			Fix:   "rwt setup <worktree>", CIJob: "lint-backend",
		},
		{
			Name: "clippy-colibri", Group: rotki.GroupColibri, Tier: TierStandard, Dir: ".",
			Argv: []string{"cargo", "clippy", "-p", "colibri", "--all-targets", "--locked"},
			Env:  []string{"RUSTFLAGS=-Dwarnings"}, CIJob: "lint-colibri",
		},
		{
			Name: "clippy-starling", Group: rotki.GroupStarling, Tier: TierStandard, Dir: ".",
			Argv: []string{"cargo", "clippy", "--workspace", "--exclude", "colibri",
				"--all-targets", "--locked"},
			Env: []string{"RUSTFLAGS=-Dwarnings"}, CIJob: "lint-starling",
		},

		// ---- heavy: pre-push, targeted ----
		// The two test checks carry no Argv paths of their own: the planner
		// appends the specs it mapped from the changed files, and drops the
		// check entirely when nothing maps. See targetedTests.
		{
			Name: "vitest", Group: rotki.GroupFrontend, Tier: TierHeavy, Dir: "frontend",
			Argv: []string{"pnpm", "run", "test:unit"}, Script: "test:unit",
			CIJob: "unittest-frontend",
		},
		{
			Name: "pytest", Group: rotki.GroupBackend, Tier: TierHeavy, Dir: ".",
			Argv: []string{"uv", "run", "pytest"}, CIJob: "test-backend",
		},
		{
			Name: "cargo-test-colibri", Group: rotki.GroupColibri, Tier: TierHeavy, Dir: ".",
			Argv: []string{"cargo", "test", "-p", "colibri", "--locked"},
			Env:  []string{"RUSTFLAGS=-Dwarnings"}, CIJob: "lint-colibri",
		},
		{
			Name: "cargo-test-starling", Group: rotki.GroupStarling, Tier: TierHeavy, Dir: ".",
			Argv: []string{"cargo", "test", "--workspace", "--exclude", "colibri", "--locked"},
			Env:  []string{"RUSTFLAGS=-Dwarnings"}, CIJob: "lint-starling",
		},
	}
}

// Plan returns the checks to run for a set of changed paths, plus everything
// that was dropped and why.
//
// The filtering is, in order: the check's group must be touched by the change,
// the worktree must support the check (pnpm script present, required files
// present, cargo workspace present), and a file-scoped check must have at least
// one matching path left after its extension filter.
func Plan(worktree string, changed []string, tiers []Tier) (planned []Check, skipped []Skip) {
	groups := map[string]bool{}
	for _, g := range rotki.GroupsFor(changed) {
		groups[g] = true
	}
	scripts := pnpmScripts(worktree)
	crates := rustGroups(worktree)
	want := map[Tier]bool{}
	for _, t := range tiers {
		want[t] = true
	}

	for _, c := range Catalog() {
		if !want[c.Tier] {
			continue
		}
		// An empty Group is repo-wide: it runs whenever anything changed.
		if c.Group != "" && !groups[c.Group] {
			continue
		}
		if skip, ok := unsupported(worktree, c, scripts, crates); ok {
			skipped = append(skipped, skip)
			continue
		}
		if c.Tier == TierHeavy {
			targets := targetedTests(worktree, c, changed)
			if len(targets) == 0 {
				skipped = append(skipped, Skip{c.Name,
					"no local test maps to the changed files; CI covers it"})
				continue
			}
			c.Argv = append(append([]string{}, c.Argv...), targets...)
			planned = append(planned, c)
			continue
		}
		if c.Files == FilesAppend {
			files := matchingFiles(c, changed)
			if len(files) == 0 {
				continue
			}
			c.Argv = append(append([]string{}, c.Argv...), files...)
		}
		planned = append(planned, c)
	}
	return planned, skipped
}

// unsupported reports whether this worktree can run the check, and why not.
func unsupported(worktree string, c Check, scripts map[string]bool, crates map[string]bool) (Skip, bool) {
	if c.Script != "" && !scripts[c.Script] {
		return Skip{c.Name, fmt.Sprintf("frontend/package.json has no %q script on this base", c.Script)}, true
	}
	for _, need := range c.Needs {
		if _, err := os.Stat(filepath.Join(worktree, need)); err != nil {
			reason := need + " is not present in this worktree"
			if c.Fix != "" {
				reason += "; install it with: " + c.Fix
			}
			return Skip{c.Name, reason}, true
		}
	}
	switch c.Group {
	case rotki.GroupColibri, rotki.GroupStarling:
		if !crates[c.Group] {
			return Skip{c.Name, "no cargo workspace for " + c.Group + " in this worktree"}, true
		}
	}
	return Skip{}, false
}

// matchingFiles narrows the changed paths to the ones this check should be
// handed: the paths in its group (or all of them, for a repo-wide check), then
// its extension filter.
func matchingFiles(c Check, changed []string) []string {
	var out []string
	for _, p := range changed {
		if c.Group != "" && !inGroup(p, c.Group) {
			continue
		}
		if len(c.Exts) > 0 && !slices.Contains(c.Exts, filepath.Ext(p)) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// inGroup reports whether a path belongs to a change group.
func inGroup(path, group string) bool {
	for _, prefix := range rotki.ChangeGroups[group] {
		if path == prefix || (strings.HasSuffix(prefix, "/") && strings.HasPrefix(path, prefix)) {
			return true
		}
	}
	return false
}

// pnpmScripts reads the script names declared in the worktree's
// frontend/package.json. A missing or unparseable file yields an empty set,
// which skips every script-gated check rather than running a command that is
// not there.
func pnpmScripts(worktree string) map[string]bool {
	return readScripts(filepath.Join(worktree, "frontend", "package.json"))
}

// rustGroups reports which Rust change groups this worktree actually has,
// derived from the cargo layout rather than assumed: the root workspace builds
// both binaries, the older split layout may carry colibri alone, and a base with
// no cargo workspace at all gets no Rust checks. The binaries a workspace
// produces are the same names the change groups use, so they map straight over.
func rustGroups(worktree string) map[string]bool {
	out := map[string]bool{}
	for _, ws := range cargocache.Workspaces(worktree) {
		for _, bin := range ws.Bins {
			switch bin {
			case "colibri":
				out[rotki.GroupColibri] = true
			case "starling":
				out[rotki.GroupStarling] = true
			}
		}
	}
	return out
}
