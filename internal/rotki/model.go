// Package rotki holds the baked-in, rotki-specific model the rwt CLI is built
// around: remotes, branch conventions and the dev:web port layout.
//
// The one value that genuinely varies per machine — where the rotki/rotki
// umbrella lives — is NOT assumed. rwt resolves it from RWT_UMBRELLA or the
// user's config (see package config); until the user configures it once, every
// umbrella-touching command refuses rather than guessing.
package rotki

import (
	"os"
	"path/filepath"

	"github.com/kelsos/rwt/internal/config"
)

// Remotes. upstream is the source of truth (rotki/rotki); origin is the
// personal fork and may be stale. Always branch off upstream.
const (
	Upstream = "upstream"
	Origin   = "origin"
)

// HostWorktree is the long-lived worktree that hosts .git; git worktree
// commands run from here.
const HostWorktree = "develop"

// LongLived worktrees are never torn down by rwt rm and are the targets of
// rwt refresh.
var LongLived = []string{"develop", "bugfixes", "master"}

// BranchPrefix maps a --from base to the branch (and worktree-dir) prefix.
// Bases not listed here are rejected by rwt new.
var BranchPrefix = map[string]string{
	"develop":  "feat",
	"bugfixes": "fix",
}

// Port model — mirror of frontend/scripts/dev-instance/port-registry.ts.
// The app is the source of truth; these exist only so rwt ls / rm can display
// ports. rwt never allocates or writes them.
const (
	InstanceBasePort = 13000 // slot 1's dev port
	InstanceSlotStep = 10    // ports between neighbouring slots
)

// DefaultPorts is slot 0 only — reserved for a plain `pnpm dev` (non-instance).
var DefaultPorts = Ports{RestAPI: 4242, Proxy: 4243, Colibri: 4343, Dev: 8080}

// Ports is the four-port set allocated per dev:web instance slot (no ws_api —
// WS rides the proxy).
type Ports struct {
	RestAPI int
	Proxy   int
	Colibri int
	Dev     int
}

// PortsForSlot returns the ports for a given slot. Slot 0 is DefaultPorts;
// slots >=1 are packed contiguously from InstanceBasePort.
func PortsForSlot(slot int) Ports {
	if slot <= 0 {
		return DefaultPorts
	}
	base := InstanceBasePort + (slot-1)*InstanceSlotStep
	return Ports{Dev: base, RestAPI: base + 1, Proxy: base + 2, Colibri: base + 3}
}

// The single env key rwt writes (a plain appended line; the app absorbs it
// into its managed block on first dev:web run).
const (
	EnvFileRel = "frontend/app/.env.development.local"
	InputKey   = "INSTANCE_NAME"
)

// Capability-detection paths, relative to a worktree root. The dev:web
// multi-instance feature shipped as the dev-instance/ module split.
const (
	DevInstanceIndexRel = "frontend/scripts/dev-instance/index.ts"
	StartDevRel         = "frontend/scripts/start-dev.ts"
)

// Umbrella resolves the rotki/rotki umbrella directory that contains the
// develop/bugfixes/master worktrees. Resolution order: RWT_UMBRELLA env, then
// the user config. There is NO built-in default — ok is false when the user has
// not configured a location yet, and callers must refuse rather than guess.
func Umbrella() (path, source string, ok bool) {
	if v := os.Getenv("RWT_UMBRELLA"); v != "" {
		return v, "RWT_UMBRELLA env", true
	}
	if cfg, err := config.Load(); err == nil && cfg.Umbrella != "" {
		return cfg.Umbrella, "config", true
	}
	return "", "", false
}

// UmbrellaRoot is the resolved umbrella path, or "" when unconfigured. Commands
// gate on the root PersistentPreRunE guard, so a "" here is only reachable from
// code paths (config / doctor) that handle the unconfigured case explicitly.
func UmbrellaRoot() string {
	path, _, _ := Umbrella()
	return path
}

// HostWorktreePath is the develop worktree git worktree commands run from.
func HostWorktreePath() string {
	return filepath.Join(UmbrellaRoot(), HostWorktree)
}

// WorktreeDir is the on-disk directory name for a worktree, matching the
// existing manual convention (prefix-name, e.g. feat-accounting-overlay).
func WorktreeDir(prefix, name string) string {
	return prefix + "-" + name
}

// WorktreePath is the absolute path of a worktree directory under the umbrella.
func WorktreePath(prefix, name string) string {
	return filepath.Join(UmbrellaRoot(), WorktreeDir(prefix, name))
}

// Branch is the full branch name for a worktree (e.g. feat/accounting-overlay).
func Branch(prefix, name string) string {
	return prefix + "/" + name
}
