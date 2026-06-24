package rotki

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The dev:web port registry is owned by the app (frontend/scripts/dev-instance).
// rwt only ever READS it — for `rwt ls --live` — and never writes it: its lock
// is an mkdir lockdir a Go process can't coordinate with safely. These mirror
// resolveInstanceParent()/PORT_INDEX_FILENAME in the app's paths.ts /
// port-registry.ts.
const (
	devInstancesSubdir = "rotki-dev"
	portIndexFilename  = ".port-index.json"
)

// InstanceParentDir resolves the directory holding the dev instances and their
// port registry: $ROTKI_DEV_INSTANCES_DIR, else <data-home>/rotki-dev where
// data-home is $XDG_DATA_HOME or ~/.local/share (the Linux layout the app uses).
func InstanceParentDir() string {
	if v := os.Getenv("ROTKI_DEV_INSTANCES_DIR"); v != "" {
		return v
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, devInstancesSubdir)
}

// PortIndexPath is the absolute path of the app's port registry file.
func PortIndexPath() string {
	parent := InstanceParentDir()
	if parent == "" {
		return ""
	}
	return filepath.Join(parent, portIndexFilename)
}

var sanitizeStrip = regexp.MustCompile(`[^\d._a-z-]+`)

// SanitizeInstanceName mirrors sanitizeName() in the app's paths.ts so a
// worktree's INSTANCE_NAME maps to the same key the registry stores it under:
// trim, lowercase, collapse disallowed runs to '-', trim leading/trailing '-',
// cap at 64. Returns "" if nothing survives (the app would reject such a name).
func SanitizeInstanceName(raw string) string {
	s := sanitizeStrip.ReplaceAllString(strings.ToLower(strings.TrimSpace(raw)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 64 {
		return s[:64]
	}
	return s
}

// portIndex mirrors PortIndexSchema in port-registry.ts: a name -> slot map.
type portIndex struct {
	Version int            `json:"version"`
	Slots   map[string]int `json:"slots"`
}

// ReadSlots returns the instance-name -> slot map from the port registry. A
// missing registry is not an error (no instances allocated yet) — it returns an
// empty map. Best-effort and read-only.
func ReadSlots() (map[string]int, error) {
	path := PortIndexPath()
	if path == "" {
		return map[string]int{}, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]int{}, nil
	}
	if err != nil {
		return nil, err
	}
	var idx portIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, err
	}
	if idx.Slots == nil {
		idx.Slots = map[string]int{}
	}
	return idx.Slots, nil
}
