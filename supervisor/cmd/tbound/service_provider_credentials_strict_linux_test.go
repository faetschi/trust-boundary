//go:build linux

package main

import (
	"reflect"
	"testing"

	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/researchroute"
)

// Both service keys go through the strict ancestor walk (every ancestor a real
// root-owned directory that is not group or other writable, no symlink on any
// component). The openrouter set must be exactly the two code-defined key
// paths of the engineering and research routes, so neither can be left on the
// final-component-only rule. The walk itself is tested in internal/broker/
// openrouter (credential_ancestors_linux_test.go), including the root-only
// production-policy cases.
func TestServiceCredentialStrictSetCoversBothServiceKeys(t *testing.T) {
	want := []string{piruntime.D09ManagedPiProviderCredentialPath, piruntime.D09ManagedPiResearchCredentialPath}
	if got := openrouter.ServiceCredentialPaths(); !reflect.DeepEqual(got, want) {
		t.Fatalf("strict service credential set = %v, want %v", got, want)
	}
	if researchroute.KeyPath != piruntime.D09ManagedPiResearchCredentialPath {
		t.Fatalf("research key path %q drifted from %q", researchroute.KeyPath, piruntime.D09ManagedPiResearchCredentialPath)
	}
}
