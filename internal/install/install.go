// Package install warms a worktree's language environments (pnpm / uv / cargo)
// concurrently. It is the env-readiness core shared by `rwt new`, `rwt setup`
// and `rwt refresh`.
//
// Idempotency is the flags, not state: the --frozen* flags make an already-warm
// worktree a fast no-op, so there is no install-state file to track.
package install

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Step is one ecosystem's deps-only command. Nothing here compiles.
type Step struct {
	Name string   // log prefix, e.g. "pnpm"
	Dir  string   // working dir relative to the worktree root
	Argv []string // command + args
}

// DefaultSteps are the three ecosystem warmers. Every step is lockfile-based
// and refuses to mutate its lockfile (pnpm --frozen-lockfile, uv --frozen,
// cargo --locked). colibri gets a full `cargo build` so the compiled artifact
// is warm and the first `pnpm dev:web` doesn't pay the cold-build cost. The
// build implicitly fetches, so no separate fetch step is needed.
func DefaultSteps() []Step {
	return []Step{
		{Name: "pnpm", Dir: "frontend", Argv: []string{"pnpm", "install", "--frozen-lockfile", "--prefer-offline"}},
		{Name: "uv", Dir: ".", Argv: []string{"uv", "sync", "--frozen"}},
		{Name: "cargo", Dir: ".", Argv: []string{"cargo", "build", "--locked", "--manifest-path", "colibri/Cargo.toml"}},
	}
}

// Opts tunes a Run.
type Opts struct {
	Steps  []Step    // defaults to DefaultSteps() when nil
	Stdout io.Writer // defaults to os.Stdout
}

// Run executes every step concurrently against the worktree. It is fail-soft:
// a failed step does not abort the others. The returned error (if any) names
// exactly which ecosystems failed and the re-run command.
func Run(ctx context.Context, worktree string, opts Opts) error {
	steps := opts.Steps
	if steps == nil {
		steps = DefaultSteps()
	}
	out := opts.Stdout
	if out == nil {
		out = os.Stdout
	}

	// Pre-flight: every tool must be on PATH. Fail fast with an actionable
	// message rather than a cryptic exec error mid-run.
	var missing []string
	seen := map[string]bool{}
	for _, s := range steps {
		tool := s.Argv[0]
		if seen[tool] {
			continue
		}
		seen[tool] = true
		if _, err := exec.LookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("required tools not on PATH: %s", strings.Join(missing, ", "))
	}

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed []string
	)
	for _, s := range steps {
		wg.Add(1)
		go func(s Step) {
			defer wg.Done()
			if err := runStep(ctx, worktree, s, out, &mu); err != nil {
				mu.Lock()
				failed = append(failed, s.Name)
				fmt.Fprintf(out, "[%s] FAILED: %v\n", s.Name, err)
				mu.Unlock()
			}
		}(s)
	}
	wg.Wait()

	if len(failed) > 0 {
		return fmt.Errorf("install failed for: %s (re-run: rwt setup %s)",
			strings.Join(failed, ", "), filepath.Base(worktree))
	}
	return nil
}

// runStep runs one step, line-prefixing both streams so parallel logs stay
// readable. The mutex serialises writes to the shared output.
func runStep(ctx context.Context, worktree string, s Step, out io.Writer, mu *sync.Mutex) error {
	cmd := exec.CommandContext(ctx, s.Argv[0], s.Argv[1:]...)
	cmd.Dir = filepath.Join(worktree, s.Dir)

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			mu.Lock()
			fmt.Fprintf(out, "[%s] %s\n", s.Name, sc.Text())
			mu.Unlock()
		}
	}()

	err := cmd.Run()
	pw.Close()
	return err
}

// NeedsWarm reports whether a worktree looks cold (missing node_modules or
// .venv) and should be (re)warmed. Used by `rwt refresh` to seed cold bases.
func NeedsWarm(worktree string) bool {
	for _, marker := range []string{"node_modules", ".venv"} {
		if _, err := os.Stat(filepath.Join(worktree, marker)); os.IsNotExist(err) {
			return true
		}
	}
	return false
}
