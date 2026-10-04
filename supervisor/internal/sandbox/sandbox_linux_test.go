//go:build linux

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const testCommandEnv = "TBOUND_SANDBOX_TESTCMD"

// init implements the in-cell test target. The sandbox re-executes this test
// binary as the command; when the test-command variable is present this init
// runs the probe and exits before the testing framework starts.
func init() {
	switch os.Getenv(testCommandEnv) {
	case "echo":
		_, _ = os.Stdout.WriteString("cell-stdout\n")
		_, _ = os.Stderr.WriteString("cell-stderr\n")
		os.Exit(0)
	case "exitcode":
		code, _ := strconv.Atoi(os.Getenv("TBOUND_PROBE_EXIT"))
		os.Exit(code)
	case "landlock":
		os.Exit(runLandlockProbe())
	case "network":
		os.Exit(runNetworkProbe())
	case "seccomp":
		os.Exit(runSeccompProbe())
	case "readonly":
		os.Exit(runReadOnlyProbe())
	case "memory":
		os.Exit(runMemoryProbe())
	}
}

func newWorkDir(t *testing.T) string {
	t.Helper()
	work, err := os.MkdirTemp(os.TempDir(), "tbound-sandbox-test-")
	if err != nil {
		t.Fatalf("create private work dir: %v", err)
	}
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatalf("chmod work dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(work) })
	return work
}

func openDir(t *testing.T, path string) *os.File {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func requireMechanisms(t *testing.T) Report {
	t.Helper()
	report, err := Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if err := report.require(); err != nil {
		t.Fatalf("required mechanisms unavailable: %v (%+v)", err, report)
	}
	return report
}

func TestProbeMechanisms(t *testing.T) {
	report, err := Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	t.Logf("probe report: %+v", report)
	t.Logf("evidence class: %s", report.EvidenceClass())
	if report.LandlockABI < 1 {
		t.Fatalf("Landlock ABI not observed: %+v", report)
	}
	if !report.SeccompBPF {
		t.Fatalf("seccomp-bpf not observed: %+v", report)
	}
	if !report.UserNamespaces || !report.MountNamespace || !report.PidNamespace || !report.NetworkNamespace {
		t.Fatalf("namespace composition incomplete: %+v", report)
	}
	if report.CgroupDelegated {
		t.Logf("cgroup v2 delegation is available")
	} else {
		t.Logf("cgroup v2 delegation is NOT available; cgroup limits are unestablished (honest)")
	}
}

func TestLaunchEchoAndExitCode(t *testing.T) {
	requireMechanisms(t)
	work := newWorkDir(t)

	t.Run("stdout and settlement", func(t *testing.T) {
		root := openDir(t, filepath.Join(work, "cell-a"))
		result, settlement, err := Launch(context.Background(), Request{
			Root: root,
			Argv: []string{"/proc/self/exe"},
			Env:  []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp", testCommandEnv + "=echo"},
		})
		if err != nil {
			t.Fatalf("Launch echo target: %v", err)
		}
		if result.ExitCode != 0 || !strings.Contains(string(result.Stdout), "cell-stdout") {
			t.Fatalf("unexpected result: %+v", result)
		}
		if !strings.Contains(string(result.Stderr), "cell-stderr") {
			t.Fatalf("stderr not captured: %q", result.Stderr)
		}
		if !settlement.ExitObserved || !settlement.ProcessScopeEmpty || !settlement.WritersStopped || !settlement.MountDetached {
			t.Fatalf("incomplete settlement: %+v", settlement)
		}
		if !strings.Contains(settlement.EvidenceClass, "non-claim-bearing") || !strings.Contains(settlement.EvidenceClass, "cgroup2=not-established") {
			t.Fatalf("evidence class is not honest: %q", settlement.EvidenceClass)
		}
	})

	t.Run("nonzero exit is a known result", func(t *testing.T) {
		root := openDir(t, filepath.Join(work, "cell-b"))
		result, settlement, err := Launch(context.Background(), Request{
			Root: root,
			Argv: []string{"/proc/self/exe"},
			Env:  []string{"PATH=/usr/bin:/bin", "HOME=/tmp", testCommandEnv + "=exitcode", "TBOUND_PROBE_EXIT=7"},
		})
		if err != nil {
			t.Fatalf("Launch exit-code target: %v", err)
		}
		if result.ExitCode != 7 || settlement.ExitCode != 7 {
			t.Fatalf("exit code not observed: result=%+v settlement=%+v", result, settlement)
		}
	})
}

func TestLandlockAllowsInsideDeniesOutside(t *testing.T) {
	requireMechanisms(t)
	work := newWorkDir(t)
	outsideDir := filepath.Join(work, "outside")
	if err := os.Mkdir(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(work, "cell")
	root := openDir(t, rootPath)
	result, _, err := Launch(context.Background(), Request{
		Root: root,
		Argv: []string{"/proc/self/exe"},
		Env: []string{
			"PATH=/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp", testCommandEnv + "=landlock",
			"TBOUND_PROBE_OUTSIDE=" + outsideFile,
		},
	})
	if err != nil {
		t.Fatalf("Launch landlock target: %v\n%s", err, result.Stderr)
	}
	if result.ExitCode != 0 {
		t.Fatalf("landlock probe failed (%d): %s", result.ExitCode, result.Stdout)
	}
	t.Logf("landlock probe: %s", strings.TrimSpace(string(result.Stdout)))
	// The cell wrote inside.txt into its writable root.
	if data, err := os.ReadFile(filepath.Join(rootPath, "inside.txt")); err != nil || string(data) != "inside-ok" {
		t.Fatalf("writable cell root was not writable: data=%q err=%v", data, err)
	}
}

func TestNoNetwork(t *testing.T) {
	requireMechanisms(t)
	work := newWorkDir(t)
	root := openDir(t, filepath.Join(work, "cell"))
	result, _, err := Launch(context.Background(), Request{
		Root: root,
		Argv: []string{"/proc/self/exe"},
		Env:  []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp", testCommandEnv + "=network"},
	})
	if err != nil {
		t.Fatalf("Launch network target: %v\n%s", err, result.Stderr)
	}
	if result.ExitCode != 0 {
		t.Fatalf("network probe failed (%d): %s", result.ExitCode, result.Stdout)
	}
	t.Logf("network probe: %s", strings.TrimSpace(string(result.Stdout)))
}

func TestSeccompFilter(t *testing.T) {
	requireMechanisms(t)
	work := newWorkDir(t)
	root := openDir(t, filepath.Join(work, "cell"))
	result, _, err := Launch(context.Background(), Request{
		Root: root,
		Argv: []string{"/proc/self/exe"},
		Env:  []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp", testCommandEnv + "=seccomp"},
	})
	if err != nil {
		t.Fatalf("Launch seccomp target: %v\n%s", err, result.Stderr)
	}
	if result.ExitCode != 0 {
		t.Fatalf("seccomp probe failed (%d): %s", result.ExitCode, result.Stdout)
	}
	t.Logf("seccomp probe: %s", strings.TrimSpace(string(result.Stdout)))
}

func TestPathEscapeDenied(t *testing.T) {
	requireMechanisms(t)
	work := newWorkDir(t)
	outsideDir := filepath.Join(work, "outside")
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(work, "cell")
	if err := os.MkdirAll(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(rootPath, "escape")); err != nil {
		t.Fatal(err)
	}
	root := openDir(t, rootPath)
	result, _, err := Launch(context.Background(), Request{
		Root: root,
		Argv: []string{"/proc/self/exe"},
		Env: []string{
			"PATH=/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp", testCommandEnv + "=landlock",
			"TBOUND_PROBE_OUTSIDE=" + outsideFile,
		},
	})
	if err != nil {
		t.Fatalf("Launch path-escape target: %v\n%s", err, result.Stderr)
	}
	if result.ExitCode != 0 {
		t.Fatalf("path-escape probe failed (%d): %s", result.ExitCode, result.Stdout)
	}
	t.Logf("path-escape probe: %s", strings.TrimSpace(string(result.Stdout)))
}

func TestSurvivingChildFailsClosed(t *testing.T) {
	requireMechanisms(t)
	work := newWorkDir(t)
	root := openDir(t, filepath.Join(work, "cell"))
	_, settlement, err := Launch(context.Background(), Request{
		Root: root,
		Argv: []string{"/bin/bash", "-c", "setsid sleep 300 >/dev/null 2>&1 & exit 0"},
		Env:  []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp"},
	})
	if err == nil {
		t.Fatalf("a surviving session-escaped child did not fail closed: settlement=%+v", settlement)
	}
	if !errors.Is(err, ErrNotSettled) {
		t.Fatalf("survivor error = %v, want ErrNotSettled", err)
	}
	t.Logf("fail-closed as required: %v", err)
}

func TestReadOnlyRuntimeAndWritableWorkspace(t *testing.T) {
	report := requireMechanisms(t)
	work := newWorkDir(t)
	root := openDir(t, filepath.Join(work, "cell"))
	result, _, err := Launch(context.Background(), Request{
		Root: root,
		Argv: []string{"/proc/self/exe"},
		Env:  []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp", testCommandEnv + "=readonly"},
	})
	if err != nil {
		t.Fatalf("Launch read-only target: %v\n%s", err, result.Stderr)
	}
	if result.ExitCode != 0 {
		t.Fatalf("read-only probe failed (%d): %s", result.ExitCode, result.Stdout)
	}
	t.Logf("read-only probe: %s", strings.TrimSpace(string(result.Stdout)))
	t.Logf("runtime read-only mechanisms: %s", strings.Join(report.mechanisms(), ","))
}

func TestCancelKillsWholeScope(t *testing.T) {
	requireMechanisms(t)
	work := newWorkDir(t)
	root := openDir(t, filepath.Join(work, "cell"))
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	start := time.Now()
	result, settlement, err := Launch(ctx, Request{
		Root: root,
		Argv: []string{"/bin/bash", "-c", "sleep 300"},
		Env:  []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp"},
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("canceled Launch did not settle: %v", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("scope was not killed promptly: %s", elapsed)
	}
	if result.ExitCode == 0 {
		t.Fatalf("canceled command reported success: %+v", result)
	}
	if !settlement.ProcessScopeEmpty || !settlement.MountDetached || !settlement.ExitObserved {
		t.Fatalf("cancel did not settle the scope: %+v", settlement)
	}
	t.Logf("canceled scope settled in %s: exit=%d class=%s", elapsed, result.ExitCode, settlement.EvidenceClass)
}

func TestCgroupLimitsOrExplicitSkip(t *testing.T) {
	report, err := Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !report.CgroupDelegated {
		t.Skipf("cgroup v2 memory/pids limits are NOT established on this host "+
			"(cgroup2 mount=%t, delegated=%t, unsupported=%v); this is an explicit "+
			"unsupported skip, not a silent pass", report.CgroupV2, report.CgroupDelegated, report.Unsupported)
	}
	// Delegated hosts must actually enforce the memory bound. The cell is
	// limited to 16 MiB and the target touches 64 MiB, so the OOM killer must
	// terminate it and the cell must still settle.
	work := newWorkDir(t)
	root := openDir(t, filepath.Join(work, "cell"))
	result, settlement, err := Launch(context.Background(), Request{
		Root:   root,
		Argv:   []string{"/proc/self/exe"},
		Env:    []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp", testCommandEnv + "=memory"},
		Limits: Limits{MemoryBytes: 16 << 20, PidsMax: 32, CPUWeight: 100},
	})
	if err != nil {
		t.Fatalf("Launch under cgroup limits: %v", err)
	}
	if result.ExitCode == 0 {
		t.Fatalf("memory limit was not enforced: %+v", result)
	}
	if !strings.Contains(settlement.EvidenceClass, "cgroup2-delegated") {
		t.Fatalf("delegated cgroup not reported: %q", settlement.EvidenceClass)
	}
	t.Logf("cgroup memory bound enforced: exit=%d class=%s", result.ExitCode, settlement.EvidenceClass)
}

// runLandlockProbe exercises Landlock confinement from inside the cell. It
// writes inside the cell root, then attempts an absolute outside open, a
// symlink escape, and a relative ".." escape, all of which must be denied.
func runLandlockProbe() int {
	outside := os.Getenv("TBOUND_PROBE_OUTSIDE")
	if err := os.WriteFile("inside.txt", []byte("inside-ok"), 0o600); err != nil {
		fmt.Printf("inside-write-FAILED:%v\n", err)
		return 1
	}
	fmt.Println("inside-write-OK")

	if data, err := os.ReadFile(outside); err == nil {
		fmt.Printf("absolute-outside-READ-ALLOWED:%q\n", data)
		return 1
	} else if !isDenied(err) {
		fmt.Printf("absolute-outside-wrong-error:%v\n", err)
		return 1
	} else {
		fmt.Printf("absolute-outside-DENIED:%v\n", err)
	}

	if data, err := os.ReadFile("escape/secret.txt"); err == nil {
		fmt.Printf("symlink-outside-READ-ALLOWED:%q\n", data)
		return 1
	} else if !isDenied(err) {
		fmt.Printf("symlink-outside-wrong-error:%v\n", err)
		return 1
	} else {
		fmt.Printf("symlink-outside-DENIED:%v\n", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Printf("getcwd-FAILED:%v\n", err)
		return 1
	}
	relative, err := filepath.Rel(cwd, outside)
	if err == nil {
		if data, err := os.ReadFile(relative); err == nil {
			fmt.Printf("dotdot-outside-READ-ALLOWED:%q\n", data)
			return 1
		} else if isDenied(err) {
			fmt.Printf("dotdot-outside-DENIED:%v\n", err)
		} else {
			// Resolution may fail before Landlock for a path outside the
			// mount namespace; treat any hard failure as confinement.
			fmt.Printf("dotdot-outside-BLOCKED:%v\n", err)
		}
	}
	return 0
}

func runNetworkProbe() int {
	connection, err := net.DialTimeout("tcp", "1.1.1.1:80", 3*time.Second)
	if err == nil {
		_ = connection.Close()
		fmt.Println("external-connect-ALLOWED")
		return 1
	}
	if !errors.Is(err, syscall.ENETUNREACH) && !errors.Is(err, syscall.EHOSTUNREACH) {
		fmt.Printf("external-connect-unexpected-error:%v\n", err)
		return 1
	}
	fmt.Printf("external-connect-DENIED:%v\n", err)

	if _, err := net.LookupHost("example.com"); err == nil {
		fmt.Println("dns-ALLOWED")
		return 1
	} else {
		fmt.Printf("dns-DENIED:%v\n", err)
	}

	addresses, err := net.InterfaceAddrs()
	if err != nil {
		fmt.Printf("interface-list-FAILED:%v\n", err)
		return 1
	}
	for _, address := range addresses {
		if strings.HasPrefix(address.String(), "127.") {
			fmt.Printf("loopback-present:%s\n", address)
		}
		if !address.(*net.IPNet).IP.IsLoopback() {
			fmt.Printf("non-loopback-interface-present:%s\n", address)
			return 1
		}
	}
	return 0
}

func runSeccompProbe() int {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		fmt.Printf("read-status-FAILED:%v\n", err)
		return 1
	}
	mode := -1
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "Seccomp:") {
			fields := strings.Fields(line)
			if len(fields) == 2 {
				mode, _ = strconv.Atoi(fields[1])
			}
		}
	}
	fmt.Printf("seccomp-status:%d\n", mode)
	// The seccomp(2) entry point is deliberately absent from the pinned
	// allowlist, so a permitted kernel would return EINVAL/EFAULT here while
	// the installed filter returns EPERM.
	_, _, errno := syscall.Syscall(317, 1, 0, 0)
	fmt.Printf("seccomp-syscall-errno:%v\n", errno)
	if mode != 2 || errno != syscall.EPERM {
		return 1
	}
	return 0
}

func runReadOnlyProbe() int {
	if err := os.WriteFile("/usr/tbound-cell-write-probe", []byte("x"), 0o600); err == nil {
		fmt.Println("runtime-write-ALLOWED")
		return 1
	} else {
		fmt.Printf("runtime-write-DENIED:%v\n", err)
	}
	if err := os.WriteFile("workspace-write.txt", []byte("ok"), 0o600); err != nil {
		fmt.Printf("workspace-write-FAILED:%v\n", err)
		return 1
	}
	fmt.Println("workspace-write-OK")
	return 0
}

func runMemoryProbe() int {
	block := make([]byte, 64<<20)
	for index := range block {
		block[index] = byte(index)
	}
	fmt.Println("memory-allocation-UNEXPECTEDLY-COMPLETED")
	return 0
}

func isDenied(err error) bool {
	return errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ELOOP) ||
		errors.Is(err, syscall.ENOTDIR)
}
