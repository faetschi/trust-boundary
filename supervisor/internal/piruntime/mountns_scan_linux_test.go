//go:build linux

package piruntime

import (
	"context"
	"errors"
	"os"
	"regexp"
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
	if err == nil {
		return
	}
	// A denial on one of this user's own processes still fails closed, so the error may remain. Host /proc is
	// transient, so the process it names may be a zombie or may have exited. The error must name such a process.
	match := procErrorPattern.FindStringSubmatch(err.Error())
	if match == nil {
		t.Fatalf("unexpected scan error shape: %v", err)
	}
	info, statErr := os.Stat("/proc/" + match[1])
	if errors.Is(statErr, os.ErrNotExist) || errors.Is(statErr, syscall.ESRCH) {
		t.Logf("tolerated: the named process exited during the scan: %v", err)
		return
	}
	if statErr != nil {
		t.Fatalf("cannot tell whose process the error names: %v", err)
	}
	if int(info.Sys().(*syscall.Stat_t).Uid) != os.Geteuid() {
		t.Fatalf("scan failed on another user's process: %v", err)
	}
	t.Logf("tolerated: the error names this user's own process: %v", err)
}

// procErrorPattern matches the scan errors that name a process: inspect, scan, and the fd-target read.
var procErrorPattern = regexp.MustCompile(`^(?:inspect|scan|read) /proc/([0-9]+)/`)

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
