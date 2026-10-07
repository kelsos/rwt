package install

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ciEnvFile and ciWorkflowFile are where a rotki base declares the Python its CI
// runs: the version in the env file, and whether it is the free-threaded build
// in the workflow's setup-python step.
const (
	ciEnvFile      = ".github/.env.ci"
	ciWorkflowFile = ".github/workflows/rotki_ci.yml"
)

var pythonVersionLine = regexp.MustCompile(`(?m)^PYTHON_VERSION=(\d+)\.(\d+)`)

// uvPythonRequest is the --python request that gives a worktree the same
// interpreter build its base's CI uses.
//
// No base ships a .python-version, so a bare uv sync takes whatever satisfies
// requires-python, and uv prefers its newest managed build, which can be the
// free-threaded one. That is right for a base whose CI runs free-threaded, and
// wrong for one that does not: bugfixes still locks packages with no
// free-threaded wheel, whose source builds then fail. "3.14t" asks for the
// free-threaded build and "3.14+gil" excludes it, so each base gets what its CI
// tests against. When the files are missing or unreadable it returns false and
// uv chooses as before.
func uvPythonRequest(worktree string) (string, bool) {
	env, err := os.ReadFile(filepath.Join(worktree, ciEnvFile))
	if err != nil {
		return "", false
	}
	m := pythonVersionLine.FindSubmatch(env)
	if m == nil {
		return "", false
	}
	minor := string(m[1]) + "." + string(m[2])

	workflow, err := os.ReadFile(filepath.Join(worktree, ciWorkflowFile))
	if err != nil {
		return "", false
	}
	if strings.Contains(string(workflow), "freethreaded: true") {
		return minor + "t", true
	}
	return minor + "+gil", true
}
