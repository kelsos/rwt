// Package envfile performs the one and only env mutation rwt makes: appending a
// plain INSTANCE_NAME=<name> line. It is NOT a merge engine — the app owns the
// managed-env block and absorbs this stray line on the first `pnpm dev:web`
// (its rewrite strips orphan managed keys living outside its marked block).
package envfile

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kelsos/rwt/internal/rotki"
)

// ApplyFlags upserts rwt-owned boolean dev flags into the worktree's
// .env.development.local: enabled -> KEY=true, disabled -> line removed. Thin
// wrapper over ApplyValues, which carries the semantics.
func ApplyFlags(worktree string, flags map[string]bool) error {
	values := make(map[string]string, len(flags))
	for key, on := range flags {
		if on {
			values[key] = "true"
		} else {
			values[key] = ""
		}
	}
	return ApplyValues(worktree, values)
}

// ApplyValues upserts rwt-owned env keys into the worktree's
// .env.development.local. For each key: a non-empty value -> ensure KEY=value
// (replacing any stale value, deduping repeats); the empty value -> remove the
// line entirely. Only the given keys are touched; every other line (and
// blank/comment line) is preserved verbatim and in place. None of these keys
// are in the app's MANAGED_ENV_KEYS, so the first `pnpm dev:web` leaves them
// alone.
//
// Removal, not KEY=, is what "off" means: the app tests some of these with
// `import.meta.env.KEY !== undefined` (VITE_DEMO_MODE), where an empty value
// still reads as set.
//
// The write is skipped entirely when nothing would change, so re-running on an
// already-correct worktree (the common `rwt refresh` case) is a clean no-op.
func ApplyValues(worktree string, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	path := filepath.Join(worktree, rotki.EnvFileRel)

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var lines []string
	if len(data) > 0 {
		lines = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}

	handled := map[string]bool{} // set keys already satisfied in place
	changed := false
	out := make([]string, 0, len(lines)+len(values))
	for _, line := range lines {
		key := lineKey(line)
		want, managed := values[key]
		if key == "" || !managed {
			out = append(out, line)
			continue
		}
		if want == "" {
			changed = true // off -> drop
			continue
		}
		if handled[key] {
			changed = true // drop duplicate of a set key
			continue
		}
		handled[key] = true
		desired := key + "=" + want
		if strings.TrimSpace(line) != desired {
			changed = true
		}
		out = append(out, desired)
	}

	// Append set keys not already present, in a stable order.
	var missing []string
	for key, want := range values {
		if want != "" && !handled[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	for _, key := range missing {
		out = append(out, key+"="+values[key])
		changed = true
	}

	if !changed {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content := strings.Join(out, "\n") + "\n"
	return os.WriteFile(path, []byte(content), 0o644)
}

// lineKey returns the env key of a KEY=value line, or "" for blanks, comments
// and malformed lines (which callers preserve untouched).
func lineKey(line string) string {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return ""
	}
	i := strings.IndexByte(t, '=')
	if i <= 0 {
		return ""
	}
	return strings.TrimSpace(t[:i])
}

// AppendInstanceName appends INSTANCE_NAME=<name> to the worktree's
// .env.development.local, creating the file if needed. It is idempotent: if a
// line with the correct value already exists it does nothing; a stale value is
// reported so the caller can warn rather than silently double-write.
func AppendInstanceName(worktree, name string) error {
	path := filepath.Join(worktree, rotki.EnvFileRel)
	want := rotki.InputKey + "=" + name

	if existing, ok, err := currentInstanceName(path); err != nil {
		return err
	} else if ok {
		if existing == name {
			return nil // already correct — no-op
		}
		// Different value already present. Leave it; the app manages this key
		// inside its block and overwriting by hand risks confusion.
		return fmt.Errorf("%s already set to %q in %s (leaving as-is)",
			rotki.InputKey, existing, rotki.EnvFileRel)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Ensure the appended line starts on its own line.
	prefix := ""
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		if !endsWithNewline(path) {
			prefix = "\n"
		}
	}
	_, err = fmt.Fprintf(f, "%s%s\n", prefix, want)
	return err
}

// ReadInstanceName returns the INSTANCE_NAME value written into a worktree's
// .env.development.local, if any. Read-only counterpart to AppendInstanceName,
// used by `rwt ls --live` to map a worktree to its registry slot.
func ReadInstanceName(worktree string) (string, bool, error) {
	return currentInstanceName(filepath.Join(worktree, rotki.EnvFileRel))
}

// currentInstanceName returns the value of an unmanaged or managed
// INSTANCE_NAME line if present.
func currentInstanceName(path string) (value string, found bool, err error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, rotki.InputKey+"=") {
			value = strings.TrimPrefix(line, rotki.InputKey+"=")
			found = true
		}
	}
	return value, found, sc.Err()
}

func endsWithNewline(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return true
	}
	buf := make([]byte, 1)
	if _, err := f.ReadAt(buf, info.Size()-1); err != nil {
		return true
	}
	return buf[0] == '\n'
}
