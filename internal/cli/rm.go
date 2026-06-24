package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kelsos/rwt/internal/detect"
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
	)
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Tear down a worktree (and its dev:web instance, branch)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRm(cmd.Context(), args[0], keepBranch, force, purgeMemory)
		},
	}
	cmd.Flags().BoolVar(&keepBranch, "keep-branch", false, "do not delete the local branch")
	cmd.Flags().BoolVar(&force, "force", false, "remove despite uncommitted/unpushed work")
	cmd.Flags().BoolVar(&purgeMemory, "purge-memory", false, "also delete this worktree's path-keyed Claude memory dir")
	return cmd
}

func runRm(ctx context.Context, name string, keepBranch, force, purgeMemory bool) error {
	wt, err := resolveWorktree(name)
	if err != nil {
		return err
	}
	host := rotki.HostWorktreePath()

	// Determine the branch for this worktree from the worktree list.
	branch := ""
	if wts, err := git.List(ctx, host); err == nil {
		for _, w := range wts {
			if w.Path == wt {
				branch = w.Branch
				break
			}
		}
	}

	// Step 2 — never remove a long-lived base.
	if slices.Contains(rotki.LongLived, filepath.Base(wt)) || slices.Contains(rotki.LongLived, branch) {
		return fmt.Errorf("refusing to remove long-lived worktree %q", filepath.Base(wt))
	}

	// Step 1 — safety checks unless --force.
	if !force {
		if dirty, _ := git.IsDirty(ctx, wt); dirty {
			return fmt.Errorf("%s has uncommitted changes (use --force)", filepath.Base(wt))
		}
		if branch != "" && git.HasUnpushed(ctx, wt, branch) {
			return fmt.Errorf("%s has unpushed commits (use --force)", filepath.Base(wt))
		}
	}

	// Step 3 — free the dev:web instance, but ONLY if instance-capable. A
	// bugfixes worktree never got an INSTANCE_NAME, so there is nothing to
	// clean — shelling out to --clean would be pointless.
	if detect.Capability(wt).Capable {
		fmt.Printf("cleaning dev:web instance %q...\n", name)
		if err := devWebClean(ctx, filepath.Join(wt, "frontend"), name); err != nil {
			fmt.Fprintf(os.Stderr, "warning: --clean failed (continuing): %v\n", err)
		}
	}

	// Step 4 — remove the worktree.
	if err := git.WorktreeRemove(ctx, host, wt, force); err != nil {
		return err
	}
	fmt.Printf("removed worktree %s\n", filepath.Base(wt))

	// Step 5 — delete the local branch unless asked to keep it. Remote on
	// origin is left untouched (PR history).
	if !keepBranch && branch != "" {
		if err := git.DeleteBranch(ctx, host, branch, force); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not delete branch %s: %v\n", branch, err)
		} else {
			fmt.Printf("deleted branch %s\n", branch)
		}
	}

	// Step 6 — purge the path-keyed Claude memory dir only when asked. Left in
	// place by default so PR notes survive a worktree teardown.
	if purgeMemory {
		purgeClaudeMemory(wt)
	}
	return nil
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
