//go:build linux

package piruntime

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"testing"
)

// The service runs unprivileged: other users' processes (pid 1 above all) deny access to /proc/<pid>/ns/mnt.
// They cannot join the cell's mount namespace without privilege, so the scan must not report UNKNOWN for them.
func TestMountNamespaceScanSkipsOtherUsersProcessesItCannotInspect(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs an unprivileged user so that other users' /proc entries deny access")
	}
	referenced, err := processMountNamespaceReferences(context.Background(), 1) // no namespace has inode 1
	if referenced {
		t.Fatal("scan of an unused namespace reported a reference")
	}
	// A denial on one of this user's own processes (e.g. a capability-holding systemd --user on a dev host)
	// still fails closed; any remaining error must name such a process, never another user's.
	if err != nil {
		var pid int
		if _, scanErr := fmt.Sscanf(err.Error(), "inspect /proc/%d/", &pid); scanErr != nil {
			t.Fatalf("unexpected scan error: %v", err)
		}
		info, statErr := os.Stat(fmt.Sprintf("/proc/%d", pid))
		if statErr != nil || int(info.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
			t.Fatalf("scan failed on another user's process: %v", err)
		}
	}
}

func TestMountNamespaceScanStillFindsAReferencingProcess(t *testing.T) {
	info, err := os.Stat("/proc/self/ns/mnt")
	if err != nil {
		t.Fatal(err)
	}
	referenced, err := processMountNamespaceReferences(context.Background(), info.Sys().(*syscall.Stat_t).Ino)
	if err != nil || !referenced {
		t.Fatalf("scan of this process's own namespace: referenced=%v err=%v, want true, nil", referenced, err)
	}
}
