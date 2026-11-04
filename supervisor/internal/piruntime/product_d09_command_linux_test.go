//go:build linux

package piruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestThesisD09RunnerNeverInvokesProductCallbacks(t *testing.T) {
	called := false
	runner := &D09CommandRunner{launcher: &D09PodmanCrunLauncher{productCommandOwner: func(D09ProductCommandOwnership) error { called = true; return errors.New("must not run") }}}
	for _, pid := range []int{0, os.Getpid()} {
		if err := runner.launcher.reportProductCommand(D09ProductCommandOwnership{}, nil, pid); err != nil {
			t.Fatal(err)
		}
	}
	if called {
		t.Fatal("thesis path invoked product callback")
	}
}

func TestProductD09OwnershipCallbackFailureAndHeldLimits(t *testing.T) {
	failed := errors.New("journal fsync failed")
	host := &D09ProductCommandHost{loaded: true}
	launcher := &D09PodmanCrunLauncher{productHost: host, productCommandOwner: func(D09ProductCommandOwnership) error { return failed }}
	if err := launcher.reportProductCommand(D09ProductCommandOwnership{}, nil, 0); !errors.Is(err, failed) {
		t.Fatalf("callback error lost: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "held")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"pids.max": "128\n", "memory.max": "1073741824\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	held, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := os.Rename(dir, dir+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pids.max"), []byte("999\n"), 0600); err != nil {
		t.Fatal(err)
	}
	limits, err := (D09ProductCommandOwnership{cgroup: held}).ReadLimits()
	if err != nil || limits.PidsMax != "128" || limits.MemoryMax != "1073741824" {
		t.Fatalf("not held-descriptor observation: %+v %v", limits, err)
	}
	if _, err := (D09ProductCommandOwnership{}).ReadLimits(); err == nil {
		t.Fatal("accepted no live descriptor")
	}
}

func TestProductD09ResidueRequiresRemovalAndNamespaceProof(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		inode                           uint64
		present, removeFails, wantError bool
	}{
		{"removed and namespace released", 1, true, false, false},
		{"already removed with namespace proof", 1, false, false, false},
		{"no started identity and absent after scope drain", 0, false, false, false},
		{"container removal failed", 1, true, true, true},
		{"namespace still referenced", 0, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			marker, log := filepath.Join(root, "present"), filepath.Join(root, "calls")
			if tc.present {
				if err := os.WriteFile(marker, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			name := d09CellNamePrefix + "cmd-test"
			removal := fmt.Sprintf("/bin/rm -f %q", marker)
			if tc.removeFails {
				removal = "exit 2"
			}
			script := fmt.Sprintf("#!/bin/bash\nset -euo pipefail\n[ \"$HOME\" = /var/lib/tbound-product/podman-home ] || exit 90\nprintf '%%s\\n' \"$*\" >> %q\ncase \"$1\" in\ncontainer) [ -f %q ];;\ninspect) printf '%%s\\n' '[{\"Name\":\"%s\",\"State\":{\"Status\":\"running\",\"Pid\":%d}}]' ;;\nrm) %s;;\n*) exit 3;;\nesac\n", log, marker, name, os.Getpid(), removal)
			podman := filepath.Join(root, "podman")
			if err := os.WriteFile(podman, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			owner := D09ProductCommandOwnership{ContainerName: name, LeaseID: "lease", ViewID: "view", ViewInode: 1, MountNSInode: tc.inode}
			scan := processMountNamespaceReferences
			if !tc.wantError {
				scan = func(_ context.Context, inode uint64) (bool, error) {
					if inode != 1 {
						t.Fatalf("lost namespace identity: %d", inode)
					}
					return false, nil
				}
			}
			err := reconcileProductD09CommandResidue(context.Background(), podman, productD09CommandEnvironment(), owner, scan)
			if (err != nil) != tc.wantError {
				t.Fatalf("cleanup error=%v, wantError=%v", err, tc.wantError)
			}
			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if tc.present && !strings.Contains(string(raw), "rm -f "+name) {
				t.Fatalf("container removal skipped: %s", raw)
			}
		})
	}
	if err := ReconcileProductD09CommandResidue(context.Background(), nil, D09ProductCommandOwnership{}); err == nil {
		t.Fatal("unverified recovery host accepted")
	}
}

func TestProductD09CallbacksPrecedeLaunchAndAck(t *testing.T) {
	source, err := os.ReadFile("d09_command_runner_linux.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	intent := strings.Index(text, "reportProductCommand(productOwner, nil, 0)")
	start := strings.Index(text, "command.Start()")
	started := strings.Index(text, "reportProductCommand(productOwner, scope.scope, startProof.LeaderPID)")
	ack := strings.Index(text, "ackSent = true")
	if intent < 0 || intent >= start || started <= start || started >= ack {
		t.Fatal("ownership journal no longer precedes launch and acknowledgement")
	}
}
