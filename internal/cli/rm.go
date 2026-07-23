package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kelsos/rwt/internal/detect"
	"github.com/kelsos/rwt/internal/envfile"
	"github.com/kelsos/rwt/internal/git"
	"github.com/kelsos/rwt/internal/rotki"
	"github.com/spf13/cobra"
)

// devWebClean shells out to the app's instance teardown. A package var so tests
// can stub it without a real pnpm project.
var devWebClean = func(ctx context.Context, frontendDir, name string) error {
	cmd := exec.CommandContext(ctx, "pnpm", "dev:web", "--clean", name)
	cmd.Dir = frontendDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func rmCmd() *cobra.Command {
	var (
		keepBranch  bool
		force       bool
		purgeMemory bool
		merged      bool
		yes         bool
	)
	cmd := &cobra.Command{
		Use:   "rm [name]",
		Short: "Tear down a worktree (and its dev:web instance, branch)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if merged {
				if len(args) > 0 {
					return fmt.Errorf("rm --merged sweeps all merged worktrees; don't also pass a name")
				}
				return runRmMerged(cmd.Context(), keepBranch, force, purgeMemory, yes)
			}
			if len(args) != 1 {
				return fmt.Errorf("rm needs exactly one worktree name (or use --merged)")
			}
			return runRm(cmd.Context(), args[0], keepBranch, force, purgeMemory)
		},
	}
	cmd.Flags().BoolVar(&keepBranch, "keep-branch", false, "do not delete the local branch")
	cmd.Flags().BoolVar(&force, "force", false, "remove despite uncommitted/unpushed work")
	cmd.Flags().BoolVar(&purgeMemory, "purge-memory", false, "also delete this worktree's path-keyed Claude memory dir")
	cmd.Flags().BoolVar(&merged, "merged", false, "remove every worktree whose branch is merged into an upstream base")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt (only meaningful with --merged)")
	return cmd
}

func runRm(ctx context.Context, name string, keepBranch, force, purgeMemory bool) error {
	wt, err := resolveWorktree(ctx, name)
	if err != nil {
		return err
	}
	host := rotki.HostWorktreePath()
	return tearDownWorktree(ctx, host, wt, branchForWorktree(ctx, host, wt), keepBranch, force, purgeMemory)
}

// runRmMerged removes every non-long-lived worktree whose branch is already
// merged into an upstream base (the worktree analogue of `git branch --merged`).
// It fetches upstream first so the merge check isn't stale, lists the
// candidates, and (unless --yes) asks before removing.
func runRmMerged(ctx context.Context, keepBranch, force, purgeMemory, yes bool) error {
	host := rotki.HostWorktreePath()

	fmt.Printf("fetching %s...\n", rotki.Upstream)
	if err := git.Fetch(ctx, host, rotki.Upstream); err != nil {
		fmt.Fprintf(os.Stderr, "warning: fetch failed (merge check may be stale): %v\n", err)
	}

	wts, err := git.List(ctx, host)
	if err != nil {
		return err
	}
	var merged []git.Worktree
	for _, w := range wts {
		if w.Branch == "" || isLongLived(w.Path, w.Branch) {
			continue
		}
		if isMergedIntoUpstream(ctx, w.Path, w.Branch) {
			merged = append(merged, w)
		}
	}
	if len(merged) == 0 {
		fmt.Println("no merged worktrees to remove")
		return nil
	}

	fmt.Println("merged worktrees to remove:")
	for _, w := range merged {
		fmt.Printf("  %s (%s)\n", filepath.Base(w.Path), w.Branch)
	}
	if !yes && !confirmYesNo(fmt.Sprintf("remove these %d worktrees?", len(merged))) {
		fmt.Println("aborted")
		return nil
	}

	var firstErr error
	for _, w := range merged {
		if err := tearDownWorktree(ctx, host, w.Path, w.Branch, keepBranch, force, purgeMemory); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", filepath.Base(w.Path), err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// isMergedIntoUpstream reports whether the branch is fully contained in any
// upstream long-lived base — i.e. the PR has landed and the worktree is stale.
func isMergedIntoUpstream(ctx context.Context, wt, branch string) bool {
	for _, base := range rotki.LongLived {
		if git.IsAncestor(ctx, wt, branch, rotki.Upstream+"/"+base) {
			return true
		}
	}
	return false
}

func isLongLived(wt, branch string) bool {
	return slices.Contains(rotki.LongLived, filepath.Base(wt)) || slices.Contains(rotki.LongLived, branch)
}

// branchForWorktree looks up the short branch name for a worktree path.
func branchForWorktree(ctx context.Context, host, wt string) string {
	if wts, err := git.List(ctx, host); err == nil {
		for _, w := range wts {
			if w.Path == wt {
				return w.Branch
			}
		}
	}
	return ""
}

// tearDownWorktree is the shared single-worktree removal used by both `rm
// <name>` and `rm --merged`: long-lived guard, dirty/unpushed safety (unless
// force), dev:web instance clean (if capable), worktree + branch removal, and
// optional Claude-memory purge.
func tearDownWorktree(ctx context.Context, host, wt, branch string, keepBranch, force, purgeMemory bool) error {
	base := filepath.Base(wt)

	// Never remove a long-lived base.
	if isLongLived(wt, branch) {
		return fmt.Errorf("refusing to remove long-lived worktree %q", base)
	}

	// Safety checks unless --force.
	if !force {
		if dirty, _ := git.IsDirty(ctx, wt); dirty {
			return fmt.Errorf("%s has uncommitted changes (use --force)", base)
		}
		if branch != "" && git.HasUnpushed(ctx, wt, branch) {
			return fmt.Errorf("%s has unpushed commits (use --force)", base)
		}
	}

	// Free the dev:web instance, but ONLY if instance-capable. A bugfixes
	// worktree never got an INSTANCE_NAME, so there is nothing to clean.
	if detect.Capability(wt).Capable {
		instance := instanceNameFor(wt)
		fmt.Printf("cleaning dev:web instance %q...\n", instance)
		if err := devWebClean(ctx, filepath.Join(wt, "frontend"), instance); err != nil {
			fmt.Fprintf(os.Stderr, "warning: --clean failed (continuing): %v\n", err)
		}
	}

	// Remove the worktree.
	if err := git.WorktreeRemove(ctx, host, wt, force); err != nil {
		return err
	}
	fmt.Printf("removed worktree %s\n", base)

	// Delete the local branch unless asked to keep it. Remote on origin is left
	// untouched (PR history).
	if !keepBranch && branch != "" {
		if err := git.DeleteBranch(ctx, host, branch, force); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not delete branch %s: %v\n", branch, err)
		} else {
			fmt.Printf("deleted branch %s\n", branch)
		}
	}

	// Purge the path-keyed Claude memory dir only when asked. Left in place by
	// default so PR notes survive a worktree teardown.
	if purgeMemory {
		purgeClaudeMemory(wt)
	}
	return nil
}

// instanceNameFor is the dev:web instance name for a worktree: its written
// INSTANCE_NAME, falling back to the dir slug (name minus a known prefix).
func instanceNameFor(wt string) string {
	if name, ok, _ := envfile.ReadInstanceName(wt); ok && name != "" {
		return name
	}
	base := filepath.Base(wt)
	for _, p := range rotki.Prefixes {
		if strings.HasPrefix(base, p+"-") {
			return strings.TrimPrefix(base, p+"-")
		}
	}
	return base
}

// confirmYesNo prompts on stdout and reads a yes/no answer from stdin,
// defaulting to no on empty input or read error.
func confirmYesNo(prompt string) bool {
	fmt.Printf("%s [y/N]: ", prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// purgeClaudeMemory deletes the worktree's path-keyed Claude memory directory.
// Fail-soft: a missing dir or removal error only logs.
func purgeClaudeMemory(wt string) {
	dir := claudeMemoryDir(wt)
	if dir == "" {
		return
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		fmt.Printf("no Claude memory at %s\n", dir)
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not purge Claude memory: %v\n", err)
		return
	}
	fmt.Printf("purged Claude memory %s\n", dir)
}

// claudeMemoryDir maps a worktree path to its Claude memory dir:
// ~/.claude/projects/<slug>/memory, where <slug> is the absolute worktree path
// with '/' and '.' each replaced by '-' (Claude Code's project-key convention).
func claudeMemoryDir(wt string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	abs, err := filepath.Abs(wt)
	if err != nil {
		return ""
	}
	slug := strings.Map(func(r rune) rune {
		if r == '/' || r == '.' {
			return '-'
		}
		return r
	}, abs)
	return filepath.Join(home, ".claude", "projects", slug, "memory")
}
