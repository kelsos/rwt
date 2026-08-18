package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDemo(t *testing.T) {
	for _, in := range []string{"off", "AUTO", " minor ", "patch"} {
		if _, err := ParseDemo(in); err != nil {
			t.Errorf("ParseDemo(%q): %v", in, err)
		}
	}
	if got, _ := ParseDemo("AUTO"); got != DemoAuto {
		t.Errorf("ParseDemo(%q) = %q, want normalized %q", "AUTO", got, DemoAuto)
	}
	if _, err := ParseDemo("major"); err == nil {
		t.Error("expected an error for an unknown mode")
	}
}

func TestDemoRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	// Default is off, and off is not written out — an existing file keeps its
	// shape when the user never touches demo.
	cfg := Default()
	if cfg.Demo != DemoOff {
		t.Errorf("default demo = %q, want %q", cfg.Demo, DemoOff)
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "rwt", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "demo") {
		t.Errorf("default demo should not be serialized:\n%s", data)
	}

	cfg.Demo = DemoAuto
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Demo != DemoAuto {
		t.Errorf("loaded demo = %q, want %q", loaded.Demo, DemoAuto)
	}
}

func TestLoadIgnoresUnknownDemo(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "rwt", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// A value from a newer rwt (or a hand-edit typo) falls back to the default
	// rather than failing the load, matching how unknown flag aliases behave.
	if err := os.WriteFile(path, []byte(`{"flags":{},"demo":"major"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Demo != DemoOff {
		t.Errorf("demo = %q, want fallback to %q", cfg.Demo, DemoOff)
	}
}
