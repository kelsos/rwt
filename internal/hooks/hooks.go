// Package hooks installs rwt as the umbrella's git hook runner.
//
// Installation is umbrella-wide by construction: core.hooksPath lives in the
// repository-local config, and every linked worktree shares one config file, so
// there is no per-worktree hooksPath to set. Opting a single worktree out is
// therefore a separate mechanism, a marker file in that worktree's own git dir.
//
// The installed hooks are shims that exec `rwt hooks run`, so the logic lives in
// the binary and upgrading rwt upgrades the hooks. Nothing about a check is
// baked into the scripts.
package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kelsos/rwt/internal/git"
)

// hooksPathKey is the git config key an install takes over.
const hooksPathKey = "core.hooksPath"

// Stages are the hooks rwt installs.
var Stages = []string{"pre-commit", "pre-push"}

// optOutMarker is the file that makes the hooks inert in one worktree. It lives
// in the worktree's own git dir, so it is never a tracked or untracked file in
// the tree, needs no gitignore entry, and is removed along with the worktree by
// `git worktree remove`.
const optOutMarker = "rwt-hooks-off"

// Dir is where rwt keeps its hook scripts: inside the shared git dir, next to
// the hooks/ directory it stands in for.
func Dir(ctx context.Context, worktree string) (string, error) {
	common, err := git.CommonDir(ctx, worktree)
	if err != nil {
		return "", err
	}
	return filepath.Join(common, "rwt-hooks"), nil
}

// State is what `rwt hooks status` reports.
type State struct {
	Dir       string // where rwt's hooks live
	HooksPath string // the current core.hooksPath, "" when unset
	Installed bool   // hooksPath points at Dir and the scripts are there
	Previous  string // the hooksPath an install displaced, "" when none
	// PreviousBroken is set when Previous names a directory that does not
	// exist, which is how git silently runs no hooks at all.
	PreviousBroken bool
}

// Status inspects the umbrella without changing anything.
func Status(ctx context.Context, worktree string) (State, error) {
	dir, err := Dir(ctx, worktree)
	if err != nil {
		return State{}, err
	}
	s := State{Dir: dir}
	s.HooksPath, _ = git.ConfigGet(ctx, worktree, hooksPathKey)
	s.Installed = s.HooksPath == dir && scriptsPresent(dir)
	s.Previous = readPrevious(dir)
	if s.Installed {
		s.PreviousBroken = s.Previous != "" && !isDir(s.Previous)
	} else if s.HooksPath != "" {
		s.PreviousBroken = !isDir(s.HooksPath)
	}
	return s, nil
}

// Install writes the shims and points core.hooksPath at them, recording whatever
// it displaced so Uninstall can put it back and so the shim can chain to it.
//
// It refuses to displace a hooksPath that is a real directory unless force is
// set, because that directory is somebody else's hook mechanism.
func Install(ctx context.Context, worktree string, force bool) (State, error) {
	before, err := Status(ctx, worktree)
	if err != nil {
		return State{}, err
	}
	if before.HooksPath != "" && before.HooksPath != before.Dir && isDir(before.HooksPath) && !force {
		return before, fmt.Errorf(
			"%s already points at %s, which exists; re-run with --force to take it over "+
				"(rwt will chain to it after its own checks pass)",
			hooksPathKey, before.HooksPath)
	}
	if err := os.MkdirAll(before.Dir, 0o755); err != nil {
		return before, err
	}
	for _, stage := range Stages {
		path := filepath.Join(before.Dir, stage)
		if err := os.WriteFile(path, []byte(shim(stage)), 0o755); err != nil {
			return before, err
		}
	}
	// Only record a displaced path on the first install; re-installing must not
	// overwrite the original with rwt's own directory.
	if before.HooksPath != "" && before.HooksPath != before.Dir {
		if err := writePrevious(before.Dir, before.HooksPath); err != nil {
			return before, err
		}
	}
	if err := git.ConfigSet(ctx, worktree, hooksPathKey, before.Dir); err != nil {
		return before, err
	}
	return Status(ctx, worktree)
}

// Uninstall restores the displaced core.hooksPath, or unsets the key when rwt's
// install was the first one. The scripts are left in place; they are inert once
// nothing points at them, and keeping them makes a re-install a one-liner.
func Uninstall(ctx context.Context, worktree string) error {
	dir, err := Dir(ctx, worktree)
	if err != nil {
		return err
	}
	current, _ := git.ConfigGet(ctx, worktree, hooksPathKey)
	if current != dir {
		return fmt.Errorf("%s is %q, not rwt's %q, so there is nothing to uninstall",
			hooksPathKey, current, dir)
	}
	previous := readPrevious(dir)
	if previous == "" {
		return git.ConfigUnset(ctx, worktree, hooksPathKey)
	}
	if err := git.ConfigSet(ctx, worktree, hooksPathKey, previous); err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir, "previous"))
}

// Chained returns the displaced hook for a stage, if there is one and it is
// executable. `rwt hooks run` execs it after its own checks pass so a repo's own
// hook mechanism keeps working underneath rwt's.
func Chained(ctx context.Context, worktree, stage string) string {
	dir, err := Dir(ctx, worktree)
	if err != nil {
		return ""
	}
	previous := readPrevious(dir)
	if previous == "" {
		return ""
	}
	path := filepath.Join(previous, stage)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return ""
	}
	return path
}

// OptedOut reports whether the hooks are disabled for this worktree alone.
func OptedOut(ctx context.Context, worktree string) bool {
	path, err := markerPath(ctx, worktree)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// SetOptOut turns this worktree's hooks off (off=true) or back on.
func SetOptOut(ctx context.Context, worktree string, off bool) error {
	path, err := markerPath(ctx, worktree)
	if err != nil {
		return err
	}
	if !off {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(path, []byte(
		"rwt hooks are disabled for this worktree.\nRe-enable with: rwt hooks on\n"), 0o644)
}

func markerPath(ctx context.Context, worktree string) (string, error) {
	dir, err := git.Dir(ctx, worktree)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, optOutMarker), nil
}

// shim is the hook script. It stays this small on purpose: everything it could
// contain would be frozen at install time, while `rwt hooks run` is whatever
// version of rwt is on PATH today.
func shim(stage string) string {
	return `#!/bin/sh
# Generated by rwt. Do not edit; reinstall with: rwt hooks install
if [ "${RWT_HOOKS:-1}" = "0" ]; then exit 0; fi
if ! command -v rwt >/dev/null 2>&1; then
  echo "rwt not on PATH; skipping ` + stage + ` checks" >&2
  exit 0
fi
exec rwt hooks run ` + stage + ` "$@"
`
}

func scriptsPresent(dir string) bool {
	for _, stage := range Stages {
		if _, err := os.Stat(filepath.Join(dir, stage)); err != nil {
			return false
		}
	}
	return true
}

func readPrevious(dir string) string {
	body, err := os.ReadFile(filepath.Join(dir, "previous"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

func writePrevious(dir, path string) error {
	return os.WriteFile(filepath.Join(dir, "previous"), []byte(path+"\n"), 0o644)
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
