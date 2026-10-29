//go:build linux

package piruntime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
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
	_, nsErr := os.Stat("/proc/1/ns/mnt")
	_, fdErr := os.ReadDir("/proc/1/fd")
	if !pid1Foreign || (nsErr == nil && fdErr == nil) {
		t.Log("the pid-1 assertion is vacuous here: pid 1 is not an inaccessible process of another user")
	}
	// A named pid that vanished, was reused, or changed owner proves nothing about ownership, so the scan is re-run.
	// The test skips rather than passes when every attempt is inconclusive.
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
			t.Fatalf("scan named pid 1, a root-owned process of another user: %v", err)
		}
		owner, conclusive := ownerOfNamedProcess(match[1], before)
		if !conclusive {
			t.Logf("attempt %d is inconclusive, so the scan is re-run: %v", attempt, err)
			continue
		}
		if int(owner) != os.Geteuid() {
			t.Fatalf("scan failed on another user's process: %v", err)
		}
		t.Logf("tolerated: the error names this user's own process: %v", err)
		return
	}
	t.Skip("every scan attempt named a process that vanished, was reused, or changed owner, so ownership is unproven")
}

// procErrorPattern matches the scan errors that name a process: inspect, scan, and the fd-target read.
var procErrorPattern = regexp.MustCompile(`^(?:inspect|scan|read) /proc/([0-9]+)/`)

// processIdentity is what a pid names at one moment. A reused pid gets a new start time.
type processIdentity struct {
	startTime string
	uid       uint32
}

// processIdentities records every visible process, so that a later scan error can be checked against the process it named.
func processIdentities() map[string]processIdentity {
	ids := make(map[string]processIdentity)
	entries, _ := os.ReadDir("/proc") // on failure the scan fails the same way, and the test reports that error
	for _, entry := range entries {
		if id, err := readProcessIdentity(entry.Name()); err == nil {
			ids[entry.Name()] = id
		}
	}
	return ids
}

// ownerOfNamedProcess returns the owner of pid only if pid still names the process it named before the scan.
// A missing, reused, or re-owned pid is inconclusive.
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
	// The fields after the parenthesised command name start with the state, so starttime (field 22) is index 19.
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
