package checks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/kelsos/rwt/internal/rotki"
)

// targetedTests maps changed files to the tests worth running for them.
//
// The mapping is deliberately conservative in one direction only: it never
// widens to a suite. A changed file that maps to nothing yields no targets at
// all, and the caller drops the check with a note that CI covers it. Running the
// full pytest or vitest suite from a hook would cost minutes on every push and
// train you to reach for --no-verify, which is worse than not gating at all.
func targetedTests(worktree string, c Check, changed []string) []string {
	var out []string
	switch c.Group {
	case rotki.GroupFrontend:
		out = frontendSpecs(worktree, changed)
	case rotki.GroupBackend:
		out = backendTests(worktree, changed)
	default:
		// Rust: cargo has no cheap per-file selection, so the crate's own test
		// command is the narrowest useful unit and takes no path arguments.
		if hasGroupChange(changed, c.Group) {
			return []string{}
		}
		return nil
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// vitestRoot is the directory vitest resolves its filters against. The check
// runs `pnpm run test:unit` from frontend/, but that script is a workspace
// filter (`pnpm run --filter rotki test:unit:run`), so vitest itself starts in
// frontend/app. A path relative to frontend/ matches no test file there, and
// vitest treats "no file matched" as a failed run, which red-gates the push.
const vitestRoot = "frontend/app"

// frontendSpecs resolves changed frontend files to vitest specs, returned
// relative to vitestRoot.
//
// A changed spec runs itself. A changed source file runs its sibling spec if
// there is one, and otherwise every spec sitting in the same directory, which is
// where this repo keeps them.
//
// Only the app package maps: it holds every spec test:unit can reach.
// frontend/common has no specs at all, and dev-proxy's three are the test:proxy
// check's, not this one's. A change confined to either maps to nothing and
// drops the check, which is the honest outcome rather than a filter that
// matches nothing.
func frontendSpecs(worktree string, changed []string) []string {
	var out []string
	for _, p := range changed {
		if !strings.HasPrefix(p, vitestRoot+"/") || !isTS(p) {
			continue
		}
		rel := strings.TrimPrefix(p, vitestRoot+"/")
		if isSpec(rel) {
			out = append(out, rel)
			continue
		}
		sibling := strings.TrimSuffix(rel, filepath.Ext(rel)) + ".spec.ts"
		if exists(worktree, vitestRoot, sibling) {
			out = append(out, sibling)
			continue
		}
		out = append(out, specsIn(worktree, filepath.Dir(rel))...)
	}
	return out
}

// specsIn lists the spec files directly inside a directory, relative to
// vitestRoot.
func specsIn(worktree, dir string) []string {
	entries, err := os.ReadDir(filepath.Join(worktree, vitestRoot, dir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && isSpec(e.Name()) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// backendTests resolves changed Python files to pytest paths, relative to the
// worktree root.
//
// A changed test file runs itself. A changed module runs the same-named test
// file if one exists under rotkehlchen/tests/; the tree there does not mirror the
// package layout, so the name is matched rather than the path, and a module with
// no such file maps to nothing.
func backendTests(worktree string, changed []string) []string {
	var out []string
	var index map[string][]string
	for _, p := range changed {
		if filepath.Ext(p) != ".py" {
			continue
		}
		if strings.HasPrefix(p, "rotkehlchen/tests/") {
			out = append(out, p)
			continue
		}
		if !strings.HasPrefix(p, "rotkehlchen/") {
			continue
		}
		if index == nil {
			index = testIndex(worktree)
		}
		out = append(out, index["test_"+filepath.Base(p)]...)
	}
	return out
}

// testIndex maps every test file's basename to its paths under
// rotkehlchen/tests/. Built once per plan and only when a non-test module
// changed, since it walks a large tree.
func testIndex(worktree string) map[string][]string {
	index := map[string][]string{}
	root := filepath.Join(worktree, "rotkehlchen", "tests")
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "test_") ||
			filepath.Ext(d.Name()) != ".py" {
			return nil //nolint:nilerr // an unreadable subtree just contributes nothing
		}
		rel, relErr := filepath.Rel(worktree, path)
		if relErr != nil {
			return nil
		}
		index[d.Name()] = append(index[d.Name()], rel)
		return nil
	})
	return index
}

// hasGroupChange reports whether any changed path falls in a group.
func hasGroupChange(changed []string, group string) bool {
	for _, p := range changed {
		if inGroup(p, group) {
			return true
		}
	}
	return false
}

func isSpec(path string) bool {
	base := filepath.Base(path)
	return strings.HasSuffix(base, ".spec.ts") || strings.HasSuffix(base, ".spec.tsx")
}

func isTS(path string) bool {
	switch filepath.Ext(path) {
	case ".ts", ".tsx", ".vue":
		return true
	}
	return false
}

func exists(parts ...string) bool {
	_, err := os.Stat(filepath.Join(parts...))
	return err == nil
}

// readScripts returns the script names declared in a package.json. Any problem
// reading or parsing it yields an empty set, which skips the script-gated checks
// instead of running a command that may not exist.
func readScripts(path string) map[string]bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(body, &pkg); err != nil {
		return nil
	}
	out := make(map[string]bool, len(pkg.Scripts))
	for name := range pkg.Scripts {
		out[name] = true
	}
	return out
}
