//go:build linux

package guestevidence

import (
	"strings"
	"testing"

	"tbound/supervisor/internal/piruntime"
)

// The validators duplicate the few values the service pins, so that the host tool does not link the
// Linux runtime. These tests keep the copies equal to the originals.
func TestPinnedValuesEqualTheRuntimes(t *testing.T) {
	if NetSinkRuleDigest != piruntime.D09NetSinkRuleDigest {
		t.Errorf("rule digest %s != %s", NetSinkRuleDigest, piruntime.D09NetSinkRuleDigest)
	}
	if NetSinkAttestSchema != piruntime.D09NetSinkAttestSchema {
		t.Errorf("attestation schema %s != %s", NetSinkAttestSchema, piruntime.D09NetSinkAttestSchema)
	}
	path, _, err := piruntime.D09NetSinkNftMatch("/sys/fs/cgroup" + CellsCgroup)
	if err != nil || path != strings.TrimPrefix(CellsCgroup, "/")+"/net-sink" {
		t.Errorf("cells cgroup %s is not one the net-sink rule is built on: %q %v", CellsCgroup, path, err)
	}
}

func TestNetSinkAttestationMirrorsTheServices(t *testing.T) {
	a := piruntime.D09NetSinkAttestation{Schema: piruntime.D09NetSinkAttestSchema, BootID: "b", ParentDev: 1, ParentIno: 2, RuleDigest: piruntime.D09NetSinkRuleDigest}
	raw, err := a.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var mirror NetSinkAttestation
	if err := DecodeStrict(raw, &mirror); err != nil {
		t.Fatalf("the service's attestation is not the mirror's shape: %v", err)
	}
	if mirror != (NetSinkAttestation{Schema: a.Schema, BootID: a.BootID, ParentDev: a.ParentDev, ParentIno: a.ParentIno, RuleDigest: a.RuleDigest}) {
		t.Fatalf("mirror = %+v", mirror)
	}
}
