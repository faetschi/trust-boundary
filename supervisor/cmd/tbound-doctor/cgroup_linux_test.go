package main

import (
	"context"
	"io/fs"
	"os"
	"strings"
	"testing"

	"tbound/supervisor/internal/piinstall"
)

func TestCgroupInventoryUsesCurrentScopeInsteadOfHierarchyControllers(t *testing.T) {
	files := map[string]string{
		"/proc/self/cgroup":                                              "0::/user.slice/session.scope\n",
		"/proc/self/mountinfo":                                           "31 20 0:28 / /sys/fs/cgroup rw - cgroup2 cgroup rw\n",
		"/sys/fs/cgroup/cgroup.controllers":                              "cpu memory pids\n",
		"/sys/fs/cgroup/user.slice/session.scope/cgroup.controllers":     "cpu\n",
		"/sys/fs/cgroup/user.slice/session.scope/cgroup.subtree_control": "\n",
		"/sys/fs/cgroup/user.slice/session.scope/cgroup.type":            "domain\n",
	}
	var reads []string
	got := cgroupCheckFrom(func(path string) ([]byte, error) {
		reads = append(reads, path)
		data, ok := files[path]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return []byte(data), nil
	}, func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist })
	if got.Status != warn || !strings.Contains(got.Detail, "/sys/fs/cgroup/user.slice/session.scope") ||
		!strings.Contains(got.Detail, "missing required controllers: memory, pids") {
		t.Fatalf("hierarchy controllers must not establish current-scope delegation: %+v", got)
	}
	for _, path := range reads {
		if path == "/sys/fs/cgroup/cgroup.controllers" {
			t.Fatalf("read hierarchy controllers instead of actual scope: %v", reads)
		}
	}
}

func TestCgroupInventoryReportsControlsWithoutClaimingDelegation(t *testing.T) {
	for _, tc := range []struct {
		name, kind  string
		enabled     string
		missingKill bool
		want        string
	}{
		{name: "domain", kind: "domain", want: "cgroup.kill: present (writability unverified)"},
		{name: "controllers enabled", kind: "domain", enabled: "memory pids", want: "controllers enabled for children: memory pids"},
		{name: "threaded", kind: "threaded", want: "required domain cgroup not established"},
		{name: "missing kill", kind: "domain", missingKill: true, want: "cgroup.kill unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{
				"/proc/self/cgroup":                           "0::/scope\n",
				"/proc/self/mountinfo":                        "31 20 0:28 / /sys/fs/cgroup rw - cgroup2 cgroup rw\n",
				"/sys/fs/cgroup/scope/cgroup.type":            tc.kind,
				"/sys/fs/cgroup/scope/cgroup.controllers":     "memory pids",
				"/sys/fs/cgroup/scope/cgroup.subtree_control": tc.enabled,
				"/sys/fs/cgroup/scope/memory.max":             "max",
				"/sys/fs/cgroup/scope/pids.max":               "128",
			}
			got := cgroupCheckFrom(func(path string) ([]byte, error) {
				value, ok := files[path]
				if !ok {
					return nil, fs.ErrNotExist
				}
				return []byte(value), nil
			}, func(path string) (os.FileInfo, error) {
				if path != "/sys/fs/cgroup/scope/cgroup.kill" {
					t.Fatalf("unexpected stat: %s", path)
				}
				if tc.missingKill {
					return nil, fs.ErrNotExist
				}
				return nil, nil
			})
			if got.Status != warn || !strings.Contains(got.Detail, tc.want) ||
				!strings.Contains(got.Detail, "memory.max: max") || !strings.Contains(got.Detail, "pids.max: 128") ||
				!strings.Contains(got.Detail, "writable delegation unverified") {
				t.Fatalf("inventory must describe presence without claiming delegation: %+v", got)
			}
		})
	}
}

func TestCgroupInventoryUnavailableProcData(t *testing.T) {
	for _, unavailable := range []string{"/proc/self/cgroup", "/proc/self/mountinfo"} {
		t.Run(unavailable, func(t *testing.T) {
			got := cgroupCheckFrom(func(path string) ([]byte, error) {
				if path == unavailable {
					return nil, fs.ErrPermission
				}
				return []byte("0::/scope\n"), nil
			}, func(string) (os.FileInfo, error) {
				t.Fatal("unresolved scope must not stat cgroup controls")
				return nil, fs.ErrNotExist
			})
			if got.Status != warn || !strings.Contains(got.Detail, "cannot locate current cgroup v2") || !strings.Contains(got.Detail, unavailable) {
				t.Fatalf("missing scope resolution failure: %+v", got)
			}
		})
	}
}

func TestDoctorReportsReuseUnverifiedWithExplicitRemediation(t *testing.T) {
	opts := piinstall.Options{LookPath: func(string) (string, error) { return "", fs.ErrNotExist }}
	rep := buildReport(context.Background(), "test", t.TempDir(), opts)
	for _, got := range rep.Checks {
		if got.Name != "cgroup-reuse" {
			continue
		}
		if got.Status != warn || !strings.Contains(got.Detail, "unverified") ||
			!strings.Contains(got.Remediation, "authorized delegated scope") ||
			!strings.Contains(got.Remediation, "tbound-profile cgroup-reuse-check --confirm-disposable-cgroup") {
			t.Fatalf("reuse remediation must be explicit and scoped: %+v", got)
		}
		if rep.GovReady {
			t.Fatal("read-only inventory must not grant governed readiness")
		}
		return
	}
	t.Fatal("doctor omitted unverified cgroup reuse check")
}

func TestCgroupInventoryReportsMissingControls(t *testing.T) {
	files := map[string]string{
		"/proc/self/cgroup":            "0::/scope\n",
		"/proc/self/mountinfo":         "31 20 0:28 / /cg rw - cgroup2 cgroup rw\n",
		"/cg/scope/cgroup.type":        "domain",
		"/cg/scope/cgroup.controllers": "memory pids",
	}
	got := cgroupCheckFrom(func(path string) ([]byte, error) {
		value, ok := files[path]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return []byte(value), nil
	}, func(string) (os.FileInfo, error) { return nil, fs.ErrPermission })
	for _, detail := range []string{"cgroup.subtree_control unreadable", "memory.max unreadable", "pids.max unreadable", "cgroup.kill unavailable"} {
		if got.Status != warn || !strings.Contains(got.Detail, detail) {
			t.Fatalf("missing %q: %+v", detail, got)
		}
	}
}

func TestCurrentCgroupMountResolution(t *testing.T) {
	for _, tc := range []struct {
		name, membership, mounts, directory string
	}{
		{name: "full hierarchy", membership: "/user.slice/a.scope", mounts: "31 20 0:28 / /cg rw - cgroup2 cgroup rw\n", directory: "/cg/user.slice/a.scope"},
		{name: "subtree mount", membership: "/user.slice/a.scope", mounts: "31 20 0:28 /user.slice /delegated rw - cgroup2 cgroup rw\n", directory: "/delegated/a.scope"},
		{name: "most specific mount", membership: "/user.slice/a.scope", mounts: "31 20 0:28 / /cg rw - cgroup2 cgroup rw\n32 20 0:28 /user.slice /delegated rw - cgroup2 cgroup rw\n", directory: "/delegated/a.scope"},
		{name: "escaped mount", membership: "/scope", mounts: `31 20 0:28 / /cg\040space rw - cgroup2 cgroup rw` + "\n", directory: "/cg space/scope"},
		{name: "membership root", membership: "/", mounts: "31 20 0:28 / /cg rw - cgroup2 cgroup rw\n", directory: "/cg"},
		{name: "not a mount prefix", membership: "/user.slice-other/a.scope", mounts: "31 20 0:28 /user.slice /cg rw - cgroup2 cgroup rw\n"},
		{name: "unresolved membership", membership: "/../scope", mounts: "31 20 0:28 / /cg rw - cgroup2 cgroup rw\n"},
		{name: "no unified mount", membership: "/scope", mounts: "31 20 0:28 / /cg rw - cgroup cgroup rw\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, directory, err := currentCgroup(func(path string) ([]byte, error) {
				switch path {
				case "/proc/self/cgroup":
					return []byte("0::" + tc.membership + "\n"), nil
				case "/proc/self/mountinfo":
					return []byte(tc.mounts), nil
				default:
					t.Fatalf("unexpected read: %s", path)
					return nil, fs.ErrNotExist
				}
			})
			if tc.directory == "" {
				if err == nil {
					t.Fatalf("expected unresolved scope, got %q", directory)
				}
			} else if err != nil || directory != tc.directory {
				t.Fatalf("directory=%q err=%v, want %q", directory, err, tc.directory)
			}
		})
	}
}
