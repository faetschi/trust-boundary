package cgroupguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefuseBase(t *testing.T) {
	for name, tc := range map[string]struct {
		rel     string
		refused bool
	}{
		"the cgroup root":           {"/", true},
		"the empty path":            {"", true},
		"a dot path":                {"/./", true},
		"init.scope":                {"/init.scope", true},
		"nested init.scope":         {"/system.slice/init.scope/x", true},
		"a login session":           {"/user.slice/user-1000.slice/session-3.scope", true},
		"a session below the scope": {"/user.slice/user-1000.slice/session-3.scope/app", true},
		"a delegated user scope":    {"/user.slice/user-1000.slice/user@1000.service/app.slice/run-r1.scope", false},
		"a delegated system scope":  {"/system.slice/run-u12.service", false},
	} {
		if got := RefuseBase(tc.rel); (got != "") != tc.refused {
			t.Errorf("%s: RefuseBase(%q) = %q, refused want %v", name, tc.rel, got, tc.refused)
		}
	}
}

func TestEnabledNeedsExactlyOne(t *testing.T) {
	for value, want := range map[string]bool{"1": true, "": false, "0": false, "true": false, "yes": false} {
		t.Setenv(Env, value)
		if Enabled() != want {
			t.Errorf("%s=%q: Enabled() = %v, want %v", Env, value, !want, want)
		}
	}
}

func fakeCgroup(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestApplyLimitsWritesTheCaps(t *testing.T) {
	cg := fakeCgroup(t, map[string]string{"pids.max": "max", "memory.max": "max"})
	base := fakeCgroup(t, nil)
	if err := ApplyLimits(cg, base); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{"pids.max": "512", "memory.max": "2G"} {
		got, _ := os.ReadFile(filepath.Join(cg, file))
		if string(got) != want {
			t.Errorf("%s = %q, want %q", file, got, want)
		}
	}
}

func TestApplyLimitsRefusesAnUncappedCgroup(t *testing.T) {
	// The controllers are not delegated: there are no limit files, and the base has none.
	err := ApplyLimits(fakeCgroup(t, nil), fakeCgroup(t, map[string]string{"pids.max": "max", "memory.max": "max"}))
	if err == nil || !strings.Contains(err.Error(), "pids.max") {
		t.Fatalf("err = %v, want a refusal naming pids.max", err)
	}
}

func TestApplyLimitsAcceptsLimitsTheBaseAlreadyHas(t *testing.T) {
	base := fakeCgroup(t, map[string]string{"pids.max": "1024", "memory.max": "4294967296"})
	if err := ApplyLimits(fakeCgroup(t, nil), base); err != nil {
		t.Fatalf("a base scope with its own TasksMax and MemoryMax already bounds the subtree: %v", err)
	}
}
