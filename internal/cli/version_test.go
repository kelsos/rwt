package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestResolveVersionPrefersTheStampedValue pins the contract the Makefile
// depends on: the linker writes this variable, and nothing else may win over
// it. A rename here silently reverts every build to a bare commit SHA, because
// -X against a name that does not exist is not an error.
func TestResolveVersionPrefersTheStampedValue(t *testing.T) {
	orig := version
	version = "v1.2.3"
	t.Cleanup(func() { version = orig })

	if got := resolveVersion(); got != "v1.2.3" {
		t.Errorf("resolveVersion() = %q, want the stamped value", got)
	}
	var out bytes.Buffer
	if err := runVersion(&out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "rwt v1.2.3\n" {
		t.Errorf("runVersion wrote %q", got)
	}
}

// TestResolveVersionFallsBack covers a plain `go build`, where nothing is
// stamped: it has to report something rather than an empty string.
func TestResolveVersionFallsBack(t *testing.T) {
	orig := version
	version = ""
	t.Cleanup(func() { version = orig })

	got := resolveVersion()
	if strings.TrimSpace(got) == "" {
		t.Error("resolveVersion() with nothing stamped returned empty")
	}
}
