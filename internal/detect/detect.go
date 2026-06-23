// Package detect decides whether a worktree's checkout understands the dev:web
// multi-instance feature (INSTANCE_NAME / managed-env block). Capability is a
// property of the checkout/commit, not the branch name, so it is determined by
// inspecting files — never by matching "bugfixes".
package detect

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/kelsos/rwt/internal/rotki"
)

// Result is the outcome of a capability probe.
type Result struct {
	Capable bool
	Reason  string // human-readable explanation (why not, when not capable)
}

// Capability checks the target worktree cheaply, without booting Node.
//
//  1. Primary signal: frontend/scripts/dev-instance/index.ts exists.
//  2. Secondary confirmation: start-dev.ts imports from ./dev-instance.
func Capability(worktree string) Result {
	indexPath := filepath.Join(worktree, rotki.DevInstanceIndexRel)
	if _, err := os.Stat(indexPath); err != nil {
		return Result{
			Capable: false,
			Reason:  rotki.DevInstanceIndexRel + " not present",
		}
	}

	// Secondary confirmation — best-effort. If start-dev.ts is unreadable we
	// still trust the primary signal.
	startDev := filepath.Join(worktree, rotki.StartDevRel)
	if data, err := os.ReadFile(startDev); err == nil {
		if !strings.Contains(string(data), "dev-instance") {
			return Result{
				Capable: false,
				Reason:  "dev-instance/ present but start-dev.ts does not import it",
			}
		}
	}

	return Result{Capable: true, Reason: "dev-instance module present"}
}
