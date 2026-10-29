//go:build linux

package piruntime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// The service runs unprivileged: other users' processes (pid 1 above all) deny access to /proc/<pid>/ns/mnt.
// They cannot join the cell's mount namespace without privilege, so the scan must not report UNKNOWN for them.
func TestMountNamespaceScanSkipsOtherUsersProcessesItCannotInspect(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs an unprivileged user so that other users' /proc entries deny access")
	}
	pid1, err := os.Stat("/proc/1")
	if err != nil {
		t.Fatalf("pid 1 is always present: %v", err)
	}
	pid1Foreign := int(pid1.Sys().(*syscall.Stat_t).Uid) != os.Geteuid()
	// A vanished, reused, or re-owned pid proves nothing about ownership; retry without counting it as a pass.
	for attempt := 1; attempt <= 3; attempt++ {
		before := processIdentities()
		referenced, err := processMountNamespaceReferences(context.Background(), 1) // no namespace has inode 1
		if referenced {
			t.Fatal("scan of an unused namespace reported a reference")
		}
		if err == nil {
			return
		}
		match := procErrorPattern.FindStringSubmatch(err.Error())
		if match == nil {
			t.Fatalf("unexpected scan error shape: %v", err)
		}
		if match[1] == "1" && pid1Foreign {
			t.Fatalf("scan named pid 1, a process of another user: %v", err)
		}
		owner, conclusive := ownerOfNamedProcess(match[1], before)
		if !conclusive {
			t.Logf("attempt %d has inconclusive ownership, so the scan is re-run: %v", attempt, err)
			continue
		}
		if int(owner) != os.Geteuid() {
			t.Fatalf("scan failed on another user's process: %v", err)
		}
		t.Logf("fail-closed error names this user's own process: %v", err)
		return
	}
	t.Fatal("every scan attempt named a process that vanished, was reused, or changed owner; ownership is unproven")
}

var procErrorPattern = regexp.MustCompile(`^(?:inspect|scan|read) /proc/([0-9]+)/`)

type processIdentity struct {
	startTime string
	uid       uint32
}

func processIdentities() map[string]processIdentity {
	ids := make(map[string]processIdentity)
	entries, _ := os.ReadDir("/proc") // On failure the scan reports the same error.
	for _, entry := range entries {
		if id, err := readProcessIdentity(entry.Name()); err == nil {
			ids[entry.Name()] = id
		}
	}
	return ids
}

func ownerOfNamedProcess(pid string, before map[string]processIdentity) (uint32, bool) {
	was, seen := before[pid]
	if !seen {
		return 0, false
	}
	now, err := readProcessIdentity(pid)
	if err != nil || now != was {
		return 0, false
	}
	return now.uid, true
}

func readProcessIdentity(pid string) (processIdentity, error) {
	stat, err := os.ReadFile(filepath.Join("/proc", pid, "stat"))
	if err != nil {
		return processIdentity{}, err
	}
	dir, err := os.Stat(filepath.Join("/proc", pid))
	if err != nil {
		return processIdentity{}, err
	}
	// Fields after the parenthesised command start with state; starttime (field 22) is index 19.
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return processIdentity{}, errors.New("malformed stat line")
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 20 {
		return processIdentity{}, errors.New("malformed stat line")
	}
	return processIdentity{startTime: fields[19], uid: dir.Sys().(*syscall.Stat_t).Uid}, nil
}

func TestMountNamespaceScanErrorOwnershipRequiresStableProcess(t *testing.T) {
	pid := strconv.Itoa(os.Getpid())
	before := processIdentities()
	identity, seen := before[pid]
	if !seen {
		t.Fatal("snapshot missed this process")
	}
	owner, conclusive := ownerOfNamedProcess(pid, before)
	if !conclusive || int(owner) != os.Geteuid() {
		t.Fatalf("stable process owner=%d conclusive=%v", owner, conclusive)
	}
	for _, test := range []struct {
		name     string
		pid      string
		identity processIdentity
	}{
		{"unobserved", pid, identity},
		{"vanished", "2147483647", identity},
		{"reused", pid, processIdentity{startTime: identity.startTime + "0", uid: identity.uid}},
		{"reowned", pid, processIdentity{startTime: identity.startTime, uid: identity.uid + 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := map[string]processIdentity{test.pid: test.identity}
			if test.name == "unobserved" {
				delete(snapshot, test.pid)
			}
			if _, conclusive := ownerOfNamedProcess(test.pid, snapshot); conclusive {
				t.Fatal("ownership must be inconclusive")
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
