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

// RepoRoot returns the top-level directory of the git worktree containing dir.
// It errors when dir is not inside a git repository, so callers resolving "."
// can refuse rather than operate on an arbitrary cwd.
func RepoRoot(ctx context.Context, dir string) (string, error) {
	return run(ctx, dir, "rev-parse", "--show-toplevel")
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
	// Count commits on HEAD not reachable from any remote ref.
	out, err := run(ctx, worktree, "log", "--branches", "--not", "--remotes", "--oneline", "-1")
	if err != nil {
		return false
	}
	return out != ""
}
