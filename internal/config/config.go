// Package config holds rwt's per-user, per-machine state: the rotki umbrella
// path (rwt assumes no location — the user sets it once) and which optional dev
// env flags to assert into a worktree's .env.development.local.
//
// Persisted as JSON at $XDG_CONFIG_HOME/rwt/config.json (falling back to
// ~/.config/rwt/config.json). Absent file == no umbrella configured + every
// flag on.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Flag is one toggleable dev env var: a friendly alias the user types and the
// real .env key it controls. None of these are in the app's MANAGED_ENV_KEYS,
// so dev:web preserves them verbatim.
type Flag struct {
	Alias  string // user-facing name, e.g. "persist"
	EnvKey string // .env key written, e.g. "VITE_PERSIST_STORE"
	Desc   string // one-line help
}

// Flags is the known set. Extend this slice to add a flag; existing config
// files round-trip unknown aliases away and pick up new ones at their default.
var Flags = []Flag{
	{Alias: "dev-tools", EnvKey: "ENABLE_DEV_TOOLS", Desc: "in-app Vue/dev tooling"},
	{Alias: "logs", EnvKey: "VITE_DEV_LOGS", Desc: "verbose local dev logs"},
	{Alias: "persist", EnvKey: "VITE_PERSIST_STORE", Desc: "persist store across restarts (stay logged in)"},
}

// Config is the loaded user state: the rotki umbrella path override (empty means
// "use the built-in default") and each known flag alias mapped to on/off.
type Config struct {
	Umbrella string          // rotki umbrella path override ("" = built-in default)
	Flags    map[string]bool // keyed by alias
}

// fileShape is the on-disk JSON layout, kept separate from Config so the public
// type can evolve without leaking json tags.
type fileShape struct {
	Umbrella string          `json:"umbrella,omitempty"`
	Flags    map[string]bool `json:"flags"`
}

// Default returns the all-on baseline used when no file exists. These match the
// flags a freshly cloned develop worktree already carries.
func Default() Config {
	m := make(map[string]bool, len(Flags))
	for _, f := range Flags {
		m[f.Alias] = true
	}
	return Config{Flags: m}
}

// Set toggles a flag by alias. Caller is expected to have validated the alias.
func (c Config) Set(alias string, on bool) {
	c.Flags[alias] = on
}

// EnvFlags projects the config onto real env keys for envfile.ApplyFlags. Every
// known flag is included — disabled ones map to false so ApplyFlags removes any
// stale line rather than leaving it on.
func (c Config) EnvFlags() map[string]bool {
	out := make(map[string]bool, len(Flags))
	for _, f := range Flags {
		out[f.EnvKey] = c.Flags[f.Alias]
	}
	return out
}

// Lookup finds a known flag by alias.
func Lookup(alias string) (Flag, bool) {
	for _, f := range Flags {
		if f.Alias == alias {
			return f, true
		}
	}
	return Flag{}, false
}

// AliasList is a comma-joined list of known aliases, for error messages.
func AliasList() string {
	names := make([]string, len(Flags))
	for i, f := range Flags {
		names[i] = f.Alias
	}
	return strings.Join(names, ", ")
}

// Path is the config file location, honoring XDG_CONFIG_HOME.
func Path() (string, error) {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "rwt", "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "rwt", "config.json"), nil
}

// Load reads the config, starting from the all-on default and overlaying any
// values found on disk. A missing file is not an error. Unknown aliases in the
// file are ignored so downgrades don't choke.
func Load() (Config, error) {
	cfg := Default()
	path, err := Path()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	var onDisk fileShape
	if err := json.Unmarshal(data, &onDisk); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.Umbrella = onDisk.Umbrella
	for alias, v := range onDisk.Flags {
		if _, ok := Lookup(alias); ok {
			cfg.Flags[alias] = v
		}
	}
	return cfg, nil
}

// Save writes the config as a flat alias->bool JSON map (keys sorted by the
// encoder), creating the directory if needed.
func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(fileShape{Umbrella: cfg.Umbrella, Flags: cfg.Flags}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
