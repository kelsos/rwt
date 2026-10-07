package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// patchBackdate is how far back the patch files' mtimes are set before pnpm
// installs. Anything strictly greater than zero works; seconds of slack keep it
// clear of filesystem timestamp granularity without making the files look
// suspiciously old to a human reading `ls -l`.
const patchBackdate = 10 * time.Second

// patchesDir is where rotki keeps the files pnpm's patchedDependencies point at.
const patchesDir = "frontend/patches"

// workspaceStateFile is pnpm's record of the last install it validated, holding
// lastValidatedTimestamp in milliseconds.
const workspaceStateFile = "frontend/node_modules/.pnpm-workspace-state-v1.json"

// lastValidated reads pnpm's lastValidatedTimestamp, reporting false when the
// worktree has never been installed or the file cannot be read.
func lastValidated(worktree string) (time.Time, bool) {
	data, err := os.ReadFile(filepath.Join(worktree, workspaceStateFile))
	if err != nil {
		return time.Time{}, false
	}
	var state struct {
		LastValidatedTimestamp int64 `json:"lastValidatedTimestamp"`
	}
	if json.Unmarshal(data, &state) != nil || state.LastValidatedTimestamp <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(state.LastValidatedTimestamp), true
}

// backdatePatchMtimes makes every pnpm patch file older than the install that is
// about to run.
//
// pnpm decides whether node_modules is in sync by comparing each patch file's
// mtime against lastValidatedTimestamp in
// node_modules/.pnpm-workspace-state-v1.json, and the comparison is not strict:
// equal timestamps read as "Patches were modified". A fresh worktree hits that
// exactly, because git writes patches/*.patch and the install records its stamp
// close enough together to land on the same millisecond.
//
// Once that happens it never clears on its own. The follow-up install is a no-op
// ("Lockfile is up to date, resolution step is skipped") and so does not advance
// the stamp, leaving it equal to the mtime forever. Every subsequent `pnpm run`
// then reverifies, reinstalls, and re-runs frontend/app's postinstall, which
// shells out to `electron-builder install-app-deps` and rebuilds native modules.
// Gates that run concurrently rebuild the same paths at once and clobber each
// other, which surfaces as node-gyp ENOENT failures that look like the change
// under test broke something.
//
// Backdating before the install restores the ordering a long-lived checkout has
// naturally, where the patch files are days older than the last install. It
// touches mtimes only, so the working tree stays clean.
//
// The target is the earlier of now and pnpm's existing stamp. On an installed
// worktree the follow-up install is the no-op that keeps the old stamp, so a
// patch backdated only relative to now would land hours after that stamp and
// start the reinstall loop this exists to prevent.
func backdatePatchMtimes(worktree string) error {
	dir := filepath.Join(worktree, patchesDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		// A base without the patches directory has nothing to correct.
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", patchesDir, err)
	}

	before := time.Now()
	if validated, ok := lastValidated(worktree); ok && validated.Before(before) {
		before = validated
	}
	stamp := before.Add(-patchBackdate)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".patch" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			return fmt.Errorf("backdate %s: %w", e.Name(), err)
		}
	}
	return nil
}
