//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/piruntime"
)

// The committed unit templates under infra/guest/systemd are the single
// canonical source for the installed tbound-pi units. The piruntime package
// loads the same files (TestPiServiceTemplatesSatisfyPiruntimeValidators), so a
// change to either validator that no longer accepts the templates fails here
// or there instead of surfacing on a guest.
func readPiServiceTemplate(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "infra", "guest", "systemd", name))
	if err != nil {
		t.Fatalf("read committed unit template %s: %v", name, err)
	}
	return data
}

func TestPiServiceTemplatesSatisfyHandoffValidators(t *testing.T) {
	registration, serviceConfig, writablePaths, _ := piServiceUnitFixture()
	if err := validatePiServiceUnit(readPiServiceTemplate(t, "tbound-pi.service"), registration, serviceConfig, writablePaths); err != nil {
		t.Fatalf("committed tbound-pi.service refused by the handoff validator: %v", err)
	}
	socket := piServiceSocketRegistration{
		SocketPath: piServiceSocketPath, ServiceUnit: filepath.Base(piServiceUnitPath),
		SocketUser: "root", SocketGroup: "tbound-pi-clients",
	}
	if err := validatePiServiceSocketUnit(readPiServiceTemplate(t, "tbound-pi.socket"), socket); err != nil {
		t.Fatalf("committed tbound-pi.socket refused by the handoff validator: %v", err)
	}
	if got := piruntime.D09ManagedPiRunnerCgroupLeaf; got != "runner" {
		t.Fatalf("piruntime runner cgroup leaf changed to %q; update the committed templates and docs together", got)
	}
}
