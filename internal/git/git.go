// Package git wraps the few git operations rwt needs by shelling out to the
// git binary. It never re-implements porcelain; it just runs commands from the
// host worktree.
package git

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// run executes git in dir and returns trimmed stdout, or an error that
// includes stderr.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// CurrentBranch returns the checked-out branch name in worktree, or "" when
// HEAD is detached or the directory is not a repository.
func CurrentBranch(ctx context.Context, worktree string) string {
	branch, err := run(ctx, worktree, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || branch == "HEAD" {
		return ""
	}
	return branch
}

// NearestBase reports which of bases the worktree's HEAD most likely branched
// from, resolving each as a `<remote>/<base>` ref. A checked-out base worktree
// answers itself; otherwise it scores every base and takes the lowest. ok is
// false when no base ref resolves.
//
// The score is how many commits HEAD would add to that base (`base..HEAD`),
// i.e. how much of this history the base does not already have. On real rotki
// worktrees the two bases are orders of magnitude apart on this measure: a
// develop-cut branch is ~3 commits ahead of develop and ~1200 ahead of
// bugfixes, because bugfixes lacks everything develop merged since they last
// met.
//
// The tie-break is how far the fork point sits behind the base tip. It only
// matters when the histories nest exactly (bugfixes fully merged into develop,
// so a branch cut from the bugfixes tip adds the same commits to either) and
// there the base that has nothing newer than the fork point is the real one.
// A full tie keeps the earlier candidate, so callers order bases from most to
// least specific.
//
// The branch prefix is deliberately not consulted: rotki carries plenty of
// `fix/*` branches cut from develop, so the prefix says what kind of change it
// is, not what it was branched off.
func NearestBase(ctx context.Context, worktree, remote string, bases []string) (base string, ok bool) {
	if b := CurrentBranch(ctx, worktree); b != "" {
		for _, candidate := range bases {
			if b == candidate {
				return candidate, true
			}
		}
	}
	bestAhead, bestBehind := -1, -1
	for _, candidate := range bases {
		ref := remote + "/" + candidate
		mergeBase, err := run(ctx, worktree, "merge-base", "HEAD", ref)
		if err != nil {
			continue // base not fetched, or unrelated history
		}
		ahead, err := countCommits(ctx, worktree, mergeBase, "HEAD")
		if err != nil {
			continue
		}
		behind, err := countCommits(ctx, worktree, mergeBase, ref)
		if err != nil {
			continue
		}
		if bestAhead < 0 || ahead < bestAhead || (ahead == bestAhead && behind < bestBehind) {
			bestAhead, bestBehind, base, ok = ahead, behind, candidate, true
		}
	}
	return base, ok
}

// countCommits returns the number of commits in from..to.
func countCommits(ctx context.Context, dir, from, to string) (int, error) {
	out, err := run(ctx, dir, "rev-list", "--count", from+".."+to)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}

// RepoRoot returns the top-level directory of the git worktree containing dir.
// It errors when dir is not inside a git repository, so callers resolving "."
// can refuse rather than operate on an arbitrary cwd.
func RepoRoot(ctx context.Context, dir string) (string, error) {
	return run(ctx, dir, "rev-parse", "--show-toplevel")
}

// CommonDir returns the repository's common git dir as an absolute path. Every
// linked worktree shares it, so anything written there (info/exclude) applies
// to all of them at once.
func CommonDir(ctx context.Context, worktree string) (string, error) {
	return run(ctx, worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
}

// Dir returns the worktree's OWN git dir as an absolute path: .git for the main
// worktree, .git/worktrees/<name> for a linked one. Unlike CommonDir this is
// per-worktree, so anything written there applies to that worktree alone and is
// removed with it by `git worktree remove`.
func Dir(ctx context.Context, worktree string) (string, error) {
	return run(ctx, worktree, "rev-parse", "--path-format=absolute", "--git-dir")
}

// MergeBase returns the commit where HEAD and ref diverged.
func MergeBase(ctx context.Context, worktree, ref string) (string, error) {
	return run(ctx, worktree, "merge-base", "HEAD", ref)
}

// ConfigGet reads a repository-local config value. ok is false when the key is
// unset, which git reports as exit status 1 rather than as output.
func ConfigGet(ctx context.Context, worktree, key string) (value string, ok bool) {
	v, err := run(ctx, worktree, "config", "--local", "--get", key)
	if err != nil {
		return "", false
	}
	return v, true
}

// ConfigSet writes a repository-local config value. Linked worktrees share one
// config file, so this applies to every worktree in the umbrella at once.
func ConfigSet(ctx context.Context, worktree, key, value string) error {
	_, err := run(ctx, worktree, "config", "--local", key, value)
	return err
}

// ConfigUnset removes a repository-local config key. Unsetting a key that is
// already absent is not an error.
func ConfigUnset(ctx context.Context, worktree, key string) error {
	if _, ok := ConfigGet(ctx, worktree, key); !ok {
		return nil
	}
	_, err := run(ctx, worktree, "config", "--local", "--unset", key)
	return err
}

// StagedFiles lists the paths staged for commit, relative to the repo root.
// Deletions are excluded: a check cannot lint a file that is no longer there.
func StagedFiles(ctx context.Context, worktree string) ([]string, error) {
	return nameOnly(ctx, worktree, "diff", "--cached", "--name-only", "--diff-filter=ACMR")
}

// DiffFiles lists the paths that differ between a base commit and HEAD, using
// the `base...HEAD` form so commits the base gained since the fork point are not
// counted as this branch's changes. This is the PR diff.
func DiffFiles(ctx context.Context, worktree, base string) ([]string, error) {
	return nameOnly(ctx, worktree, "diff", "--name-only", "--diff-filter=ACMR", base+"...HEAD")
}

// nameOnly runs a --name-only diff and splits it into paths.
func nameOnly(ctx context.Context, worktree string, args ...string) ([]string, error) {
	out, err := run(ctx, worktree, args...)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// Fetch updates a remote from the host worktree.
func Fetch(ctx context.Context, hostWorktree, remote string) error {
	_, err := run(ctx, hostWorktree, "fetch", remote)
	return err
}

// BranchExists reports whether a local branch already exists.
func BranchExists(ctx context.Context, hostWorktree, branch string) bool {
	_, err := run(ctx, hostWorktree, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// WorktreeAdd creates a new worktree at path on a new branch based on a
// remote/base ref (e.g. upstream/develop).
func WorktreeAdd(ctx context.Context, hostWorktree, path, branch, baseRef string) error {
	_, err := run(ctx, hostWorktree, "worktree", "add", path, "-b", branch, baseRef)
	return err
}

// WorktreeRemove removes a worktree. force drops the uncommitted/locked guard.
func WorktreeRemove(ctx context.Context, hostWorktree, path string, force bool) error {
	args := []string{"worktree", "remove", path}
	if force {
		args = append(args, "--force")
	}
	_, err := run(ctx, hostWorktree, args...)
	return err
}

// DeleteBranch deletes a local branch (force = -D).
func DeleteBranch(ctx context.Context, hostWorktree, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := run(ctx, hostWorktree, "branch", flag, branch)
	return err
}

// MergeFFOnly fast-forwards the worktree's branch to ref, refusing a merge
// commit. Returns an error if not fast-forwardable.
func MergeFFOnly(ctx context.Context, worktree, ref string) error {
	_, err := run(ctx, worktree, "merge", "--ff-only", ref)
	return err
}

// IsAncestor reports whether ancestor is an ancestor of descendant (i.e.
// ancestor is fully contained in descendant's history — the merged check).
// Best-effort: `merge-base --is-ancestor` exits 0 for yes, non-zero for no or
// on any error, all of which collapse to false here.
func IsAncestor(ctx context.Context, dir, ancestor, descendant string) bool {
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = dir
	return cmd.Run() == nil
}

// HasEquivalentUpstream reports whether every commit on branch already has a
// patch-equivalent commit in upstreamRef. This is how a rebase-merged PR looks:
// the same patches under new SHAs, which IsAncestor cannot see.
//
// Deliberately conservative, since a false positive deletes work: an empty
// `git cherry` (branch adds nothing to upstream) yields false and is left to
// the ancestry check, and a multi-commit branch squashed into one upstream
// commit also yields false, because no individual patch-id matches the squash.
func HasEquivalentUpstream(ctx context.Context, dir, branch, upstreamRef string) bool {
	out, err := run(ctx, dir, "cherry", upstreamRef, branch)
	if err != nil || out == "" {
		return false
	}
	// One line per commit: "- <sha>" when an equivalent exists upstream,
	// "+ <sha>" when it does not.
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "-") {
			return false
		}
	}
	return true
}

// Worktree is one entry from `git worktree list --porcelain`.
type Worktree struct {
	Path   string
	Branch string // short name, e.g. "feat/foo"; empty if detached
	Head   string
	Dirty  bool
}

// List enumerates the umbrella's worktrees.
func List(ctx context.Context, hostWorktree string) ([]Worktree, error) {
	out, err := run(ctx, hostWorktree, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var wts []Worktree
	var cur *Worktree
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "worktree "):
			if cur != nil {
				wts = append(wts, *cur)
			}
			cur = &Worktree{Path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "HEAD "):
			if cur != nil {
				cur.Head = strings.TrimPrefix(line, "HEAD ")
			}
		case strings.HasPrefix(line, "branch "):
			if cur != nil {
				cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
			}
		}
	}
	if cur != nil {
		wts = append(wts, *cur)
	}
	// Best-effort dirty check per worktree.
	for i := range wts {
		if s, err := run(ctx, wts[i].Path, "status", "--porcelain"); err == nil {
			wts[i].Dirty = s != ""
		}
	}
	return wts, nil
}

// IsDirty reports uncommitted changes in a worktree.
func IsDirty(ctx context.Context, worktree string) (bool, error) {
	s, err := run(ctx, worktree, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return s != "", nil
}

// HasUnpushed reports whether the worktree's branch has commits not present on
// any remote tracking ref. Best-effort: returns false if it cannot tell.
func HasUnpushed(ctx context.Context, worktree, branch string) bool {
	if branch == "" {
		return false
	}
	// Scoped to this branch: worktrees share one repo, so --branches would also
	// count commits belonging to every other worktree's branch.
	out, err := run(ctx, worktree, "log", branch, "--not", "--remotes", "--oneline", "-1")
	if err != nil {
		return false
	}
	return out != ""
}
