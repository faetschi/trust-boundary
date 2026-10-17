//go:build linux

package piruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func readPiServiceTemplate(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "infra", "guest", "systemd", name))
	if err != nil {
		t.Fatalf("read committed unit template %s: %v", name, err)
	}
	return data
}

// The committed templates are the canonical source for the installed units. The
// cmd/tbound handoff validator loads the same files
// (TestPiServiceTemplatesSatisfyHandoffValidators), so the two validators cannot
// drift apart without one of the two tests failing.
func TestPiServiceTemplatesSatisfyPiruntimeValidators(t *testing.T) {
	service := readPiServiceTemplate(t, "tbound-pi.service")
	if err := validateD09ManagedPiServiceUnit(service, D09ManagedPiServiceName, D09ManagedPiServiceGroup,
		"/usr/local/bin/tbound", testD09ManagedPiServiceConfig(), d09ManagedPiServiceWritablePaths()); err != nil {
		t.Fatalf("committed tbound-pi.service refused by the piruntime unit validator: %v", err)
	}
	mountUnit := []byte("[Mount]\nWhat=UUID=01234567-89ab-cdef-0123-456789abcdef\nWhere=/var/lib/tbound/live\nType=ext4\nOptions=rw,nosuid,nodev\n")
	if err := validateManagedSystemdPolicy(D09ManagedPiLiveRootPath, D09ManagedPiServiceName, D09ManagedPiServiceGroup,
		"/usr/local/bin/tbound", 0xef53, service, mountUnit); err != nil {
		t.Fatalf("committed tbound-pi.service refused by the managed-root systemd policy: %v", err)
	}
	socket := validManagedLiveRootRegistration().ServiceSocket
	if err := validateD09ManagedPiServiceSocketUnit(readPiServiceTemplate(t, "tbound-pi.socket"), socket); err != nil {
		t.Fatalf("committed tbound-pi.socket refused by the piruntime socket validator: %v", err)
	}
}
