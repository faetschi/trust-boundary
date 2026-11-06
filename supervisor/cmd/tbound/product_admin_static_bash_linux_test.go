//go:build linux

package main

import (
	"strings"
	"testing"

	"tbound/supervisor/internal/productsetup"
)

// doctorCheckLine is the report line of the doctor check with exactly this name, or empty when there is none.
func doctorCheckLine(out, name string) string {
	for _, line := range strings.Split(out, "\n") {
		if _, rest, found := strings.Cut(line, "] "); found && strings.HasPrefix(rest, name+" ") {
			return line
		}
	}
	return ""
}

// Static Bash inputs are configuration, never readiness. Doctor passes the Podman configuration that setup installs and
// still establishes no readiness. Drift in a configuration fails doctor by name, and a policy that allows Bash is refused
// while the static layout is in place.
func TestProductAdminStaticBashInputsAreNotReadiness(t *testing.T) {
	h := newAdminHost(t, privateGovernedTestRoot(t))
	if out, err := h.setup(); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	healthy, _ := adminRun("product", "doctor", "--root", h.root)
	for _, path := range []string{productsetup.ContainersConfPath, productsetup.StorageConfPath} {
		if line := doctorCheckLine(healthy, path+" content"); !strings.Contains(line, "[PASS]") {
			t.Fatalf("%s is not a passing static input: %q\n%s", path, line, healthy)
		}
	}
	if !strings.Contains(healthy, "governed readiness: NOT established") {
		t.Fatalf("the static inputs were reported as readiness:\n%s", healthy)
	}

	writeHostFile(t, h.path(productsetup.StorageConfPath), "[storage]\ndriver = \"vfs\"\n")
	drifted, _ := adminRun("product", "doctor", "--root", h.root)
	if line := doctorCheckLine(drifted, productsetup.StorageConfPath+" content"); !strings.Contains(line, "[FAIL]") {
		t.Fatalf("a drifted storage.conf is not refused by doctor: %q\n%s", line, drifted)
	}

	bashHost := newAdminHost(t, privateGovernedTestRoot(t))
	if out, err := bashHost.setup(); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	adminBreakages()["policy allows bash without a contained runner"].damage(t, bashHost)
	out, err := bashHost.setup()
	if adminExit(err) != 2 || !strings.Contains(err.Error(), "allows bash") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("a Bash policy with the static layout in place was not refused: exit %d: %v\n%s", adminExit(err), err, out)
	}
}
