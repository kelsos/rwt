package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/kelsos/rwt/internal/checks"
	"github.com/kelsos/rwt/internal/config"
	"github.com/kelsos/rwt/internal/git"
	"github.com/kelsos/rwt/internal/rotki"
	"github.com/spf13/cobra"
)

// checksRun is the check runner, a package var so tests can stub the real
// shell-outs. Same pattern as installRun.
var checksRun = checks.Run

// Stage names, which double as the hook names.
const (
	stagePreCommit = "pre-commit"
	stagePrePush   = "pre-push"
)

func checkCmd() *cobra.Command {
	var (
		stage   string
		full    bool
		dryRun  bool
		verbose bool
	)
	cmd := &cobra.Command{
		Use:   "check [name|.]",
		Short: "Run the CI gates that apply to your changes",
		Long: "Runs the local mirror of rotki's CI gates, narrowed to the parts of the\n" +
			"repo your change actually touches, using the same change groups CI uses\n" +
			"to decide which jobs to run.\n\n" +
			"--stage pre-commit scopes to the staged files and runs the fast tier\n" +
			"only. --stage pre-push scopes to the PR diff (merge-base against the\n" +
			"upstream base) and adds the slower gates. These are exactly what the\n" +
			"installed hooks run; see `rwt hooks install`.\n\n" +
			"A check is only planned when this worktree can run it, so a base that\n" +
			"lacks a script gets its checks skipped with a reason rather than a\n" +
			"command-not-found.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeWorktreeNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := "."
			if len(args) == 1 {
				arg = args[0]
			}
			wt, err := resolveWorktree(cmd.Context(), arg)
			if err != nil {
				return err
			}
			if stage != stagePreCommit && stage != stagePrePush {
				return fmt.Errorf("--stage must be %s or %s, got %q",
					stagePreCommit, stagePrePush, stage)
			}
			return runCheck(cmd.Context(), wt, stage, checkOpts{
				full: full, dryRun: dryRun, verbose: verbose,
			})
		},
	}
	cmd.Flags().StringVar(&stage, "stage", stagePrePush,
		"which gate to run: "+stagePreCommit+" (staged, fast) or "+stagePrePush+" (PR diff)")
	cmd.Flags().BoolVar(&full, "full", false, "include the heavy tier (targeted unit tests)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan and the detected groups, run nothing")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "list the changed files the plan was built from")
	_ = cmd.RegisterFlagCompletionFunc("stage",
		func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return []string{stagePreCommit, stagePrePush}, cobra.ShellCompDirectiveNoFileComp
		})
	return cmd
}

type checkOpts struct {
	full    bool
	dryRun  bool
	verbose bool
	quiet   bool // hook mode: say nothing when there is nothing to do
}

// runCheck is the whole gate: derive the changed files for the stage, plan the
// checks, then run them. Shared by `rwt check` and `rwt hooks run`.
func runCheck(ctx context.Context, wt, stage string, opts checkOpts) error {
	changed, scope, err := changedFor(ctx, wt, stage)
	if err != nil {
		return err
	}
	if len(changed) == 0 {
		if !opts.quiet {
			fmt.Printf("no changes in %s, nothing to check\n", scope)
		}
		return nil
	}

	planned, skipped := checks.Plan(wt, changed, tiersFor(stage, opts.full))
	groups := rotki.GroupsFor(changed)

	fmt.Printf("%s: %d changed file(s) in %s [%s]\n",
		stage, len(changed), scope, strings.Join(groups, ", "))
	if opts.verbose || opts.dryRun {
		for _, p := range changed {
			fmt.Printf("  %s\n", p)
		}
	}
	for _, s := range skipped {
		fmt.Printf("  skip %s: %s\n", s.Name, s.Reason)
	}
	if len(planned) == 0 {
		fmt.Println("nothing to run for these changes")
		return nil
	}
	if opts.dryRun {
		fmt.Println("\nwould run:")
		for _, c := range planned {
			fmt.Printf("  %-10s %-20s %s\n", c.Tier, c.Name, checks.Reproduce(c))
		}
		return nil
	}

	results, runErr := checksRun(ctx, wt, planned, os.Stdout)
	if checks.Report(os.Stdout, results, stage) {
		return errFailedChecks
	}
	if runErr != nil {
		return runErr
	}
	fmt.Printf("\nall %d check(s) passed\n", len(results))
	return nil
}

// errFailedChecks is returned when checks failed and Report has already printed
// everything the user needs. The caller prints nothing more; it only sets the
// exit status, which is what blocks the commit or push.
var errFailedChecks = &silentError{}

type silentError struct{}

func (*silentError) Error() string { return "" }

// changedFor derives the file list a stage gates on, and a human name for it.
//
// pre-commit gates the index, because that is exactly what the commit will
// contain. pre-push gates the PR diff, so a push re-checks everything the PR
// will show rather than only the commits added since the last push, so a fixup
// that reintroduces a problem in an earlier commit still gets caught.
func changedFor(ctx context.Context, wt, stage string) (paths []string, scope string, err error) {
	if stage == stagePreCommit {
		paths, err = git.StagedFiles(ctx, wt)
		return paths, "the index", err
	}
	base, ok := upstreamBase(ctx, wt)
	if !ok {
		return nil, "", fmt.Errorf(
			"could not tell which base %s came off; fetch %s and retry", wt, rotki.Upstream)
	}
	ref := rotki.Upstream + "/" + base
	mergeBase, err := git.MergeBase(ctx, wt, ref)
	if err != nil {
		return nil, "", fmt.Errorf("no merge base with %s: %w", ref, err)
	}
	paths, err = git.DiffFiles(ctx, wt, mergeBase)
	return paths, "the diff against " + ref, err
}

// upstreamBase resolves the long-lived base a worktree's PR would target. A
// checked-out base answers itself rather than being scored against the others,
// the same rule autoBase uses, but over every branchable base rather than only
// the ones with a demo mode.
func upstreamBase(ctx context.Context, wt string) (string, bool) {
	if b := git.CurrentBranch(ctx, wt); slices.Contains(rotki.LongLived, b) {
		return b, true
	}
	return git.NearestBase(ctx, wt, rotki.Upstream, rotki.Bases)
}

// tiersFor maps a stage to the tiers it runs. pre-commit is the fast tier and
// nothing else: anything slower belongs to a gate you hit less often.
func tiersFor(stage string, full bool) []checks.Tier {
	if stage == stagePreCommit {
		return []checks.Tier{checks.TierFast}
	}
	tiers := []checks.Tier{checks.TierFast, checks.TierStandard}
	if full {
		tiers = append(tiers, checks.TierHeavy)
	}
	return tiers
}

// hookDepth resolves how far a pre-push should go, from the persisted setting.
func hookDepth() bool {
	cfg, err := config.Load()
	if err != nil {
		return false
	}
	return cfg.Hooks == config.HooksFull
}
