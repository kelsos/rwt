package rotki

import "testing"

func TestPortsForSlot(t *testing.T) {
	if got := PortsForSlot(0); got != DefaultPorts {
		t.Errorf("slot 0 = %+v, want DefaultPorts %+v", got, DefaultPorts)
	}
	// slot 1 packs from InstanceBasePort (13000), dev on the base.
	got := PortsForSlot(1)
	want := Ports{Dev: 13000, RestAPI: 13001, Proxy: 13002, Colibri: 13003}
	if got != want {
		t.Errorf("slot 1 = %+v, want %+v", got, want)
	}
	// slot 2 steps by InstanceSlotStep (10).
	if got := PortsForSlot(2); got.Dev != 13010 {
		t.Errorf("slot 2 dev = %d, want 13010", got.Dev)
	}
}

func TestBranchAndDir(t *testing.T) {
	if b := Branch("feat", "accounting-overlay"); b != "feat/accounting-overlay" {
		t.Errorf("Branch = %q", b)
	}
	if d := WorktreeDir("fix", "broken-thing"); d != "fix-broken-thing" {
		t.Errorf("WorktreeDir = %q", d)
	}
}

func TestIsPrefix(t *testing.T) {
	// Defaults derived from --from must also be valid --type values.
	for _, p := range BranchPrefix {
		if !IsPrefix(p) {
			t.Errorf("default prefix %q not in Prefixes", p)
		}
	}
	for _, p := range []string{"chore", "refactor", "docs", "perf"} {
		if !IsPrefix(p) {
			t.Errorf("IsPrefix(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"", "feature", "bugfix", "wip"} {
		if IsPrefix(p) {
			t.Errorf("IsPrefix(%q) = true, want false", p)
		}
	}
}
