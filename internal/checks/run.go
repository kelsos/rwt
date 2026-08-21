package checks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Result is one check's outcome.
type Result struct {
	Check   Check
	Err     error
	Output  string // combined stdout+stderr, printed only on failure
	Elapsed time.Duration
}

// Run executes the planned checks tier by tier, concurrently within a tier, and
// stops after the first tier with a failure.
//
// Stopping early is the point of the tiers: a formatting error should not cost
// you a two-minute typecheck before you are told about it. Output is captured
// rather than streamed, because concurrent linters interleave into noise and a
// blocked commit needs one legible failure, not a transcript.
func Run(ctx context.Context, worktree string, planned []Check, out io.Writer) ([]Result, error) {
	var all []Result
	for _, tier := range []Tier{TierFast, TierStandard, TierHeavy} {
		batch := forTier(planned, tier)
		if len(batch) == 0 {
			continue
		}
		fmt.Fprintf(out, "\n%s (%d):\n", tier, len(batch))
		results := runBatch(ctx, worktree, batch, out)
		all = append(all, results...)
		if failedIn(results) {
			return all, errors.New("checks failed")
		}
	}
	return all, nil
}

// runBatch runs one tier's checks concurrently, printing each as it lands.
func runBatch(ctx context.Context, worktree string, batch []Check, out io.Writer) []Result {
	results := make([]Result, len(batch))
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for i, c := range batch {
		wg.Add(1)
		go func(i int, c Check) {
			defer wg.Done()
			r := runOne(ctx, worktree, c)
			results[i] = r
			mu.Lock()
			fmt.Fprintf(out, "  %s %-22s %5.1fs\n", mark(r), c.Name, r.Elapsed.Seconds())
			mu.Unlock()
		}(i, c)
	}
	wg.Wait()
	return results
}

// runOne executes a single check. A tool missing from PATH is not a failure: the
// check is skipped with a note, so a machine without cargo can still commit
// Python.
func runOne(ctx context.Context, worktree string, c Check) Result {
	start := time.Now()
	if _, err := exec.LookPath(c.Argv[0]); err != nil {
		return Result{Check: c, Elapsed: time.Since(start),
			Output: c.Argv[0] + " is not on PATH; check skipped"}
	}
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Dir = filepath.Join(worktree, c.Dir)
	cmd.Env = append(os.Environ(), c.Env...)
	body, err := cmd.CombinedOutput()
	return Result{Check: c, Err: err, Output: string(body), Elapsed: time.Since(start)}
}

// Report prints the failures with their captured output and the command that
// reproduces each one, then the bypass. It returns whether anything failed.
func Report(out io.Writer, results []Result, stage string) bool {
	var failures []Result
	for _, r := range results {
		if r.Err != nil {
			failures = append(failures, r)
		}
	}
	if len(failures) == 0 {
		return false
	}
	for _, r := range failures {
		fmt.Fprintf(out, "\n%s failed (mirrors CI job: %s)\n", r.Check.Name, r.Check.CIJob)
		if body := strings.TrimSpace(r.Output); body != "" {
			fmt.Fprintf(out, "%s\n", indent(body))
		}
		fmt.Fprintf(out, "\n  reproduce: %s\n", Reproduce(r.Check))
	}
	fmt.Fprintf(out, "\n%d check(s) failed; %s blocked.\n", len(failures), stage)
	fmt.Fprintf(out, "Bypass once: git %s --no-verify   (or RWT_HOOKS=0 git %s)\n",
		bypassVerb(stage), bypassVerb(stage))
	return true
}

// Reproduce renders the shell line that re-runs a check by hand, cwd included,
// so the fix loop is a copy-paste rather than a reconstruction.
func Reproduce(c Check) string {
	var b strings.Builder
	if c.Dir != "." && c.Dir != "" {
		fmt.Fprintf(&b, "cd %s && ", c.Dir)
	}
	for _, e := range c.Env {
		b.WriteString(e + " ")
	}
	b.WriteString(strings.Join(c.Argv, " "))
	return b.String()
}

func forTier(planned []Check, tier Tier) []Check {
	var out []Check
	for _, c := range planned {
		if c.Tier == tier {
			out = append(out, c)
		}
	}
	return out
}

func failedIn(results []Result) bool {
	for _, r := range results {
		if r.Err != nil {
			return true
		}
	}
	return false
}

func mark(r Result) string {
	if r.Err != nil {
		return "FAIL"
	}
	return "ok  "
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}

func bypassVerb(stage string) string {
	if stage == "pre-push" {
		return "push"
	}
	return "commit"
}
