// Package cgroupguard holds the guard rails of the tests that churn processes in a real
// cgroup: an explicit opt-in, a refusal of base cgroups that hold the login session or
// init, and pids and memory caps on the cgroup a test creates, so a runaway fork loop
// cannot take down the machine.
package cgroupguard

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	// Env opts in to the real-cgroup tests. Without it they skip.
	Env = "TBOUND_REAL_CGROUP_TESTS"

	pidsMax   = "512"
	memoryMax = "2G"
)

// Enabled reports whether the owner opted in to the real-cgroup tests.
func Enabled() bool { return os.Getenv(Env) == "1" }

// RefuseBase explains why the cgroup at rel (as /proc/self/cgroup names it) must not be the
// parent of a churn test, or returns "" when it may be.
func RefuseBase(rel string) string {
	switch {
	case path.Clean("/"+rel) == "/":
		return "the base cgroup is the cgroup root"
	case strings.Contains(rel, "init.scope"):
		return "the base cgroup is init.scope"
	case strings.Contains(rel, "session-"):
		return "the base cgroup is a login session (session-*)"
	}
	return ""
}

// ApplyLimits caps the pids and memory of the cgroup directory cg, which a test just
// created below base. A cap the controller does not offer there is accepted only when base
// carries its own cap, which bounds the whole subtree. Otherwise the cgroup is uncapped and
// the call fails, so the caller must not start any churn.
func ApplyLimits(cg, base string) error {
	for _, limit := range [...]struct{ file, value string }{{"pids.max", pidsMax}, {"memory.max", memoryMax}} {
		err := writeLimit(filepath.Join(cg, limit.file), limit.value)
		if err == nil {
			continue
		}
		if inherited, readErr := os.ReadFile(filepath.Join(base, limit.file)); readErr == nil && strings.TrimSpace(string(inherited)) != "max" {
			continue
		}
		return fmt.Errorf("cannot set %s=%s on %s (%v) and the base cgroup has no %s cap: run under "+
			"`systemd-run --user --scope -p Delegate=yes -p TasksMax=%s -p MemoryMax=%s`",
			limit.file, limit.value, cg, err, limit.file, pidsMax, memoryMax)
	}
	return nil
}

// writeLimit opens an existing control file: a missing one must not be created.
func writeLimit(file, value string) error {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(value)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}
