//go:build linux

package piruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// An exited process holds no namespace and no fd, yet its root-owned fd directory denies even its own user, so the
// scan must not report UNKNOWN for it. A live process denied the same way still fails closed.
func TestMountNamespaceScanSkipsExitedProcessWhoseFDDirectoryIsDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs an unprivileged user so that a denied fd directory is refused")
	}
	for _, tc := range []struct {
		state   string
		wantErr bool
	}{{"Z", false}, {"X", false}, {"S", true}} {
		t.Run("state "+tc.state, func(t *testing.T) {
			root := t.TempDir()
			const pid = "4242"
			task := filepath.Join(root, pid, "task", pid)
			if err := os.MkdirAll(task, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(task, "stat"), []byte(pid+" (fake) "+tc.state+" 1 0 0\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			fds := filepath.Join(root, pid, "fd")
			if err := os.Mkdir(fds, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(fds, 0o755) }) // lets t.TempDir remove the tree
			referenced, err := scanMountNamespaceReferences(context.Background(), root, 1)
			if referenced {
				t.Fatal("scan reported a reference from a process that holds no namespace")
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("scan error = %v, want an error present: %t", err, tc.wantErr)
			}
		})
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
