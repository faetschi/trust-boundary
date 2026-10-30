//go:build linux

package taskaccept

import (
	"testing"

	"golang.org/x/sys/unix"
)

// RootFS (the production view of the live root) reports *unix.Stat_t; fileOwner must read it,
// or the canonical fence is never recognised and every unchanged_except check fails.
func TestFileOwnerReadsTheRootFSStatType(t *testing.T) {
	uid, gid, ok := fileOwner(statInfo{name: "lost+found", st: unix.Stat_t{Uid: 995, Gid: 986}})
	if !ok || uid != 995 || gid != 986 {
		t.Fatalf("fileOwner(RootFS statInfo) = %d:%d ok=%v, want 995:986 ok=true", uid, gid, ok)
	}
}
