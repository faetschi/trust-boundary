package install

import (
	"context"
	"errors"
	"os"
	"testing"
)

func fakeProbes(goos string) Probes {
	files := map[string]string{
		"/etc/os-release":                        "ID=ubuntu\nVERSION_ID=\"24.04\"\n",
		"/proc/sys/kernel/seccomp/actions_avail": "kill_thread kill_process trap errno user_notif trace log allow\n",
		"/proc/sys/user/max_user_namespaces":     "15000\n",
		"/etc/subuid":                            "trialuser:100000:65536\n",
		"/etc/subgid":                            "trialuser:100000:65536\n",
	}
	return Probes{
		GOOS: goos,
		ReadFile: func(name string) ([]byte, error) {
			if v, ok := files[name]; ok {
				return []byte(v), nil
			}
			return nil, os.ErrNotExist
		},
		Stat:           func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		LookPath:       func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Run:            func(context.Context, string, ...string) (string, error) { return "true", nil },
		KernelRelease:  func() (string, error) { return "6.8.0-146-generic", nil },
		LandlockABI:    func() (int, error) { return 4, nil },
		PidfdSupported: func() (bool, error) { return true, nil },
		UserName:       func() (string, error) { return "trialuser", nil },
		Cgroup2: func() (CgroupInfo, error) {
			return CgroupInfo{Unified: true, Controllers: "cpu memory pids", SubtreeControlWritable: true, CanKill: true}, nil
		},
		OwnerUID: func(string) (int, bool) { return 0, true },
	}
}

func optsFor(p Probes) Options {
	o := DefaultOptions()
	o.Probes = p
	o.User = "trialuser"
	return o
}

func TestPreflightReadyOnConformingHost(t *testing.T) {
	report := Preflight(context.Background(), optsFor(fakeProbes("linux")))
	if !report.Ready {
		t.Fatalf("expected ready, got:\n%s", report.Summary())
	}
}

func TestPreflightNonLinuxUnsupported(t *testing.T) {
	report := Preflight(context.Background(), optsFor(fakeProbes("windows")))
	if report.Ready {
		t.Fatal("non-Linux must not be ready")
	}
	for _, r := range report.Results {
		if r.Status != StatusUnsupported {
			t.Fatalf("expected unsupported on non-Linux, got %s for %s", r.Status, r.Name)
		}
	}
}

func TestPreflightFailsClosedOnMissingCapabilities(t *testing.T) {
	p := fakeProbes("linux")
	p.LandlockABI = func() (int, error) { return 1, nil }
	p.LookPath = func(name string) (string, error) {
		if name == "cosign" {
			return "", errors.New("missing")
		}
		return "/usr/bin/" + name, nil
	}
	p.Run = func(context.Context, string, ...string) (string, error) { return "false", nil }
	report := Preflight(context.Background(), optsFor(p))
	if report.Ready {
		t.Fatal("expected not ready")
	}
	failures := map[string]bool{}
	for _, r := range report.Results {
		if r.Status != StatusPass {
			failures[r.Name] = true
		}
	}
	for _, want := range []string{"landlock-abi", "cosign", "podman-rootless"} {
		if !failures[want] {
			t.Fatalf("expected %s to fail, log:\n%s", want, report.Summary())
		}
	}
	if err := report.RequireReady(); err == nil {
		t.Fatal("RequireReady must return an error when not ready")
	}
}
