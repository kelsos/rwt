package envfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kelsos/rwt/internal/rotki"
)

func TestAppendInstanceName(t *testing.T) {
	wt := t.TempDir()
	envPath := filepath.Join(wt, rotki.EnvFileRel)

	// First append creates the file and the line.
	if err := AppendInstanceName(wt, "demo"); err != nil {
		t.Fatalf("first append: %v", err)
	}
	assertLineCount(t, envPath, "INSTANCE_NAME=demo", 1)

	// Idempotent: same value is a no-op (still exactly one line).
	if err := AppendInstanceName(wt, "demo"); err != nil {
		t.Fatalf("idempotent append: %v", err)
	}
	assertLineCount(t, envPath, "INSTANCE_NAME=demo", 1)

	// A different value is refused, not double-written.
	if err := AppendInstanceName(wt, "other"); err == nil {
		t.Fatal("expected error appending a conflicting value")
	}
	assertLineCount(t, envPath, "INSTANCE_NAME=other", 0)
}

func TestAppendPreservesExistingContentWithNewline(t *testing.T) {
	wt := t.TempDir()
	envPath := filepath.Join(wt, rotki.EnvFileRel)
	if err := os.MkdirAll(filepath.Dir(envPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Existing file without a trailing newline — append must not glue lines.
	if err := os.WriteFile(envPath, []byte("FOO=bar"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AppendInstanceName(wt, "demo"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(envPath)
	if !strings.Contains(string(data), "FOO=bar\nINSTANCE_NAME=demo") {
		t.Errorf("lines glued or missing newline:\n%s", data)
	}
}

func TestApplyFlags(t *testing.T) {
	wt := t.TempDir()
	envPath := filepath.Join(wt, rotki.EnvFileRel)
	if err := os.MkdirAll(filepath.Dir(envPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Seed a realistic file: one flag already on (correct), one stale, an
	// unmanaged line, a blank line and a comment that must all survive.
	seed := "VITE_DEV_LOGS=true\nENABLE_DEV_TOOLS=false\nFOO=bar\n\n#keep me\n"
	if err := os.WriteFile(envPath, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	flags := map[string]bool{
		"ENABLE_DEV_TOOLS":   true, // stale false -> flipped to true
		"VITE_DEV_LOGS":      true, // already true -> untouched in place
		"VITE_PERSIST_STORE": true, // absent -> appended
	}
	if err := ApplyFlags(wt, flags); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got := readFile(t, envPath)
	for _, want := range []string{"ENABLE_DEV_TOOLS=true", "VITE_DEV_LOGS=true", "VITE_PERSIST_STORE=true", "FOO=bar", "#keep me"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ENABLE_DEV_TOOLS=false") {
		t.Errorf("stale value not replaced:\n%s", got)
	}

	// Idempotent: a second identical apply leaves the bytes unchanged.
	before := got
	if err := ApplyFlags(wt, flags); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if after := readFile(t, envPath); after != before {
		t.Errorf("apply not idempotent:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// Disabling removes only that line; unmanaged content stays.
	flags["VITE_DEV_LOGS"] = false
	if err := ApplyFlags(wt, flags); err != nil {
		t.Fatalf("disable: %v", err)
	}
	got = readFile(t, envPath)
	if strings.Contains(got, "VITE_DEV_LOGS") {
		t.Errorf("disabled flag not removed:\n%s", got)
	}
	if !strings.Contains(got, "FOO=bar") || !strings.Contains(got, "#keep me") {
		t.Errorf("unmanaged content lost on disable:\n%s", got)
	}
}

func TestApplyFlagsCreatesFile(t *testing.T) {
	wt := t.TempDir()
	envPath := filepath.Join(wt, rotki.EnvFileRel)
	if err := ApplyFlags(wt, map[string]bool{"VITE_PERSIST_STORE": true}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(readFile(t, envPath), "VITE_PERSIST_STORE=true") {
		t.Error("expected flag written to a freshly created file")
	}

	// An all-disabled apply against a missing file writes nothing.
	wt2 := t.TempDir()
	if err := ApplyFlags(wt2, map[string]bool{"VITE_PERSIST_STORE": false}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt2, rotki.EnvFileRel)); !os.IsNotExist(err) {
		t.Error("disabled-only apply should not create the file")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertLineCount(t *testing.T, path, line string, want int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := 0
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == line {
			got++
		}
	}
	if got != want {
		t.Errorf("line %q count = %d, want %d (file:\n%s)", line, got, want, data)
	}
}
