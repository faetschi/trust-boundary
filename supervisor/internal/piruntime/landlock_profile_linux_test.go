//go:build linux

package piruntime

import "testing"

const (
	// runbook R5-1 pin before the held-binary rule existed
	legacyRunbookLandlockDigest = "sha256:6bd3b65e82df1536f302c41085437e268b1aa6a51d6d901026698fc2b0c91e54"
	runbookLandlockDigest       = "sha256:6c7bcda1461c17b7847cd124a0bba03ba4d7779bb3b07c2bc83805e7859a06af"
)

var (
	runbookLandlockReadOnly  = []string{"/usr", "/lib", "/lib64", "/etc", "/runtime", "/var/lib/tbound/pi-worker", "/proc", "/dev"}
	runbookLandlockReadWrite = []string{"/work", "/tmp"}
)

func TestLandlockDigestCoversTheHeldBinaryRule(t *testing.T) {
	digest, err := DigestD09LandlockRules(runbookLandlockReadOnly, runbookLandlockReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	if digest == legacyRunbookLandlockDigest {
		t.Fatal("the digest still equals the one that predates the fd3 held-binary rule")
	}
	if digest != runbookLandlockDigest {
		t.Fatalf("runbook Landlock digest = %s, want the pinned %s", digest, runbookLandlockDigest)
	}
	if D09LandlockHeldBinaryAccess != D09LandlockFSExecute|D09LandlockFSReadFile || D09LandlockHeldBinaryFD != 3 {
		t.Fatal("the held-binary rule is not exactly execute|read on fd 3")
	}
	// The command-cell deltas hash the same rule.
	delta, err := DigestD09LandlockRulesNet(runbookLandlockReadOnly, runbookLandlockReadWrite, nil, 0)
	if err != nil || delta != digest {
		t.Fatalf("the delta digest function diverges from the base: %s vs %s (%v)", delta, digest, err)
	}
}
