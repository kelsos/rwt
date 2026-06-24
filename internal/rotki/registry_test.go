package rotki

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSlots(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ROTKI_DEV_INSTANCES_DIR", dir)

	// Missing registry is not an error — empty map.
	if got, err := ReadSlots(); err != nil || len(got) != 0 {
		t.Fatalf("ReadSlots() with no file = %v, %v; want empty map, nil", got, err)
	}

	if err := os.WriteFile(filepath.Join(dir, ".port-index.json"),
		[]byte(`{"version":1,"slots":{"snapshot":1,"b":10}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSlots()
	if err != nil {
		t.Fatal(err)
	}
	if got["snapshot"] != 1 || got["b"] != 10 {
		t.Errorf("ReadSlots() = %v", got)
	}
}

func TestSanitizeInstanceName(t *testing.T) {
	cases := map[string]string{
		"dark-mode":     "dark-mode",
		"Dark Mode":     "dark-mode",
		"  Feat/Foo  ":  "feat-foo",
		"--weird--":     "weird",
		"keep.dots_ok-": "keep.dots_ok",
	}
	for in, want := range cases {
		if got := SanitizeInstanceName(in); got != want {
			t.Errorf("SanitizeInstanceName(%q) = %q, want %q", in, got, want)
		}
	}
}
