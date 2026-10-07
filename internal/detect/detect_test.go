package detect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kelsos/rwt/internal/rotki"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCapabilityWithoutAnIndexBarrel pins the develop layout: the dev-instance
// module has no index.ts, only its split files. Probing for index.ts reported
// every develop worktree incapable, so rwt new skipped INSTANCE_NAME.
func TestCapabilityWithoutAnIndexBarrel(t *testing.T) {
	wt := t.TempDir()
	writeFile(t, filepath.Join(wt, rotki.DevInstanceDirRel, "instance.ts"), "")
	writeFile(t, filepath.Join(wt, rotki.StartDevRel), "import { x } from './dev-instance/instance';\n")

	if got := Capability(wt); !got.Capable {
		t.Errorf("develop layout reported incapable: %s", got.Reason)
	}
}

func TestCapabilityWithoutTheModule(t *testing.T) {
	wt := t.TempDir()
	writeFile(t, filepath.Join(wt, rotki.StartDevRel), "")

	if got := Capability(wt); got.Capable {
		t.Error("a checkout without dev-instance/ reported capable")
	}
}

func TestCapabilityWhenStartDevDoesNotUseIt(t *testing.T) {
	wt := t.TempDir()
	writeFile(t, filepath.Join(wt, rotki.DevInstanceDirRel, "instance.ts"), "")
	writeFile(t, filepath.Join(wt, rotki.StartDevRel), "console.log('no instances');\n")

	if got := Capability(wt); got.Capable {
		t.Error("dev-instance/ that start-dev.ts never imports reported capable")
	}
}
