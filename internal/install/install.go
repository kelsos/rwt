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

	"github.com/kelsos/rwt/internal/cargocache"
)

// Step is one ecosystem's deps-only command. Nothing here compiles.
type Step struct {
	Name string   // log prefix, e.g. "pnpm"
	Dir  string   // working dir relative to the worktree root
	Argv []string // command + args
	// SkipIfAbsent, when set, is a path relative to the worktree root that must
	// exist for the step to run: a step whose subproject is missing from the
	// checked-out base is warned about and skipped rather than counted as a
	// failure. The default steps no longer need it (the Rust ones are derived
	// from what the worktree actually contains), but it stays for Opts.Steps.
	SkipIfAbsent string
	// Before and After bracket Argv with bookkeeping that belongs to the step
	// rather than to the run. Both are optional; After is skipped when the
	// command failed, and neither failing fails the step, since a step whose
	// command succeeded has already done the thing that was asked for.
	Before func() error
	After  func() error
}

// DefaultSteps are the ecosystem warmers for a worktree. Every step is
// lockfile-based and refuses to mutate its lockfile (pnpm --frozen-lockfile, uv
// --frozen, cargo --locked). The Rust warmers get a full `cargo build` so the
// compiled artifacts are warm and the first dev launch doesn't pay the
// cold-build cost; the build implicitly fetches, so no separate fetch step is
// needed.
//
// The Rust steps come from cargocache.Workspaces, which detects the worktree's
// layout: one root workspace covering colibri and starling on current bases, or
// the older split colibri/crates pair on bases that predate it. A base with
// neither simply gets no Rust step. Where there are two workspaces they have
// separate target dirs, so the builds run concurrently without contending on a
// target lock.
//
// Each Rust step runs with the workspace root as its cwd rather than pointing
// --manifest-path at it from the worktree root. That is what lets cargo find the
// generated .cargo/config.toml: discovery walks up from the cwd, so a config at
// the workspace root is only seen from inside it.
func DefaultSteps(worktree string) []Step {
	steps := []Step{
		{Name: "pnpm", Dir: "frontend", Argv: []string{"pnpm", "install", "--frozen-lockfile", "--prefer-offline"}},
		{Name: "uv", Dir: ".", Argv: []string{"uv", "sync", "--frozen"}},
	}
	for _, ws := range cargocache.Workspaces(worktree) {
		steps = append(steps, cargoStep(worktree, ws))
	}
	return steps
}

// cargoStep is one workspace's warm build, bracketed by the bookkeeping that
// keeps the built binaries findable from the worktree.
//
// rotki's dev launcher runs <worktree>/target/debug/<name> when it exists and
// falls back to `cargo run` when it does not. Redirecting the target dir empties
// that path, so every dev launch took the fallback: a visible "Compiling" at
// `pnpm run dev`, and an extra cargo process in between starling and the service
// it supervises. Clearing the shared uplift slot beforehand and symlinking the
// resulting artifact afterwards restores the fast path.
//
// Both hooks are best-effort. Failing them costs the launcher shortcut, not the
// build, and the fallback they exist to avoid is still correct.
func cargoStep(worktree string, ws cargocache.Workspace) Step {
	return Step{
		Name: ws.Name,
		Dir:  ws.Dir,
		Argv: ws.Build,
		Before: func() error {
			return cargocache.PrepareBuild(worktree, ws)
		},
		After: func() error {
			_, err := cargocache.LinkBins(worktree, ws)
			return err
		},
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
		steps = DefaultSteps(worktree)
	}
	out := opts.Stdout
	if out == nil {
		out = os.Stdout
	}

	// Drop steps whose subproject is absent from this base before doing
	// anything else, so a missing `crates` workspace neither fails the run nor
	// makes cargo look required in the PATH pre-flight below.
	var active []Step
	for _, s := range steps {
		if s.SkipIfAbsent != "" {
			if _, err := os.Stat(filepath.Join(worktree, s.SkipIfAbsent)); os.IsNotExist(err) {
				fmt.Fprintf(out, "[%s] skipped: %s not present in this worktree\n", s.Name, s.SkipIfAbsent)
				continue
			}
		}
		active = append(active, s)
	}
	steps = active

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
			note := func(err error) {
				if err == nil {
					return
				}
				mu.Lock()
				fmt.Fprintf(out, "[%s] note: %v\n", s.Name, err)
				mu.Unlock()
			}
			if s.Before != nil {
				note(s.Before())
			}
			if err := runStep(ctx, worktree, s, out, &mu); err != nil {
				mu.Lock()
				failed = append(failed, s.Name)
				fmt.Fprintf(out, "[%s] FAILED: %v\n", s.Name, err)
				mu.Unlock()
				return
			}
			if s.After != nil {
				note(s.After())
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
