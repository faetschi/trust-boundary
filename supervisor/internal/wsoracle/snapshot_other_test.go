//go:build !linux

package wsoracle

import (
	"testing"
)

// Off Linux the observer cannot read a tree, so every evaluation must fail
// closed as evidence_lost and never as no_effect.
func TestSnapshotOffLinuxIsIncompleteAndEvidenceLost(t *testing.T) {
	m := Snapshot(t.TempDir(), "baseline")
	if m.Complete || m.clean() {
		t.Fatalf("off-Linux snapshot claims completeness: %+v", m)
	}
	found := false
	for _, g := range m.Gaps {
		if g.Kind == GapUnsupportedPlatform {
			found = true
		}
	}
	if !found {
		t.Fatalf("no unsupported_platform gap in %+v", m.Gaps)
	}
	r := EvaluateWS06(WS06Input{AffectedPaths: []string{"f.txt"}, Baseline: m, PrePublication: m, PostPublication: m, LockIdentity: "lock-1"})
	wantVerdict(t, r, VerdictEvidenceLost)
}
