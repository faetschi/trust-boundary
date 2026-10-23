//go:build linux

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"tbound/supervisor/internal/piruntime"
)

// TestMain lets the test binary act as the Landlock TSYNC child (landlockTSYNCChild) when
// TBOUND_LL_CHILD=1. Exit code 3 means the kernel lacks the required ABI (explicit unsupported
// skip).
func TestMain(m *testing.M) {
	if name := os.Getenv("TBOUND_LL_NET_CHILD"); name != "" {
		os.Exit(landlockNetChild(name))
	}
	if os.Getenv("TBOUND_LL_CHILD") == "1" {
		os.Exit(landlockTSYNCChild())
	}
	os.Exit(m.Run())
}

// landlockTSYNCChild applies the registered ruleset from one locked OS thread and requires that a
// second OS thread, running before the ruleset was applied, is confined too: its connect to
// 127.0.0.1:8081 must fail with EACCES. A ruleset applied to the calling thread only fails here.
// It exits 3 only when the kernel does not report the registered ABI, 1 when the ruleset cannot be
// applied and 12 when the other thread is not confined.
func landlockTSYNCChild() int {
	abi, _, errno := syscall.Syscall(uintptr(sysLandlockCreateRuleset), 0, 0, landlockCreateRulesetVersion)
	if errno != 0 || int(abi) != landlockTSYNCABI {
		fmt.Fprintf(os.Stderr, "the kernel reports Landlock ABI %d (%v), the registered ABI is %d\n", int(abi), errno, landlockTSYNCABI)
		return 3
	}
	runtime.LockOSThread()
	tid, start, result := make(chan int), make(chan struct{}), make(chan error)
	go func() {
		runtime.LockOSThread()
		tid <- syscall.Gettid()
		<-start
		result <- connectLoopback(8081)
	}()
	if <-tid == syscall.Gettid() {
		fmt.Fprintln(os.Stderr, "the second goroutine runs on the applying OS thread")
		return 1
	}
	if _, _, err := applyLandlock(8); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	close(start)
	if err := <-result; !errors.Is(err, syscall.EACCES) {
		fmt.Fprintf(os.Stderr, "a connect from another OS thread was not denied by Landlock: %v\n", err)
		return 12
	}
	return 0
}

// connectLoopback connects a TCP socket on the calling OS thread (no netpoller hand-off).
func connectLoopback(port int) error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	return syscall.Connect(fd, &syscall.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}})
}

func TestSplitPaths(t *testing.T) {
	if got := splitPaths(" /usr , /lib ,, "); len(got) != 2 || got[0] != "/usr" || got[1] != "/lib" {
		t.Fatalf("splitPaths trimmed incorrectly: %#v", got)
	}
	if got := splitPaths(""); len(got) != 0 {
		t.Fatalf("splitPaths of empty must be empty: %#v", got)
	}
}

func TestDigestLandlockRulesIsDeterministicAndOrderSensitive(t *testing.T) {
	first, err := piruntime.DigestD09LandlockRules([]string{"/usr", "/lib"}, []string{"/work", "/tmp"})
	if err != nil || first == "" {
		t.Fatalf("DigestD09LandlockRules: %v %q", err, first)
	}
	second, _ := piruntime.DigestD09LandlockRules([]string{"/usr", "/lib"}, []string{"/work", "/tmp"})
	if first != second {
		t.Fatalf("ruleset digest is not deterministic: %s vs %s", first, second)
	}
	reordered, _ := piruntime.DigestD09LandlockRules([]string{"/lib", "/usr"}, []string{"/work", "/tmp"})
	if reordered == first {
		t.Fatal("ruleset digest is not order-sensitive")
	}
	changed, _ := piruntime.DigestD09LandlockRules([]string{"/lib"}, []string{"/usr", "/work", "/tmp"})
	if changed == first {
		t.Fatal("ruleset digest is not sensitive to the effective path access")
	}
}

// A wrong flag value is not refused by the kernel: 1 is LOG_SAME_EXEC_OFF and restricts only the
// calling thread, so a Go goroutine that moves to another thread runs outside the ruleset.
func TestLandlockRestrictSelfFlagIsTheUAPITSYNCValue(t *testing.T) {
	if landlockRestrictSelfTSYNC != unix.LANDLOCK_RESTRICT_SELF_TSYNC {
		t.Fatalf("landlock_restrict_self flag = %#x, the uapi LANDLOCK_RESTRICT_SELF_TSYNC is %#x", landlockRestrictSelfTSYNC, unix.LANDLOCK_RESTRICT_SELF_TSYNC)
	}
}

func TestApplyLandlockRejectsNonTSYNCProfile(t *testing.T) {
	// A required ABI below the TSYNC ABI must be refused before any syscall.
	if _, _, err := applyLandlock(1); err == nil || !strings.Contains(err.Error(), "ABI 8") {
		t.Fatalf("non-TSYNC required ABI was not refused: %v", err)
	}
}

func TestReopenReadOnlyDirectoryRejectsReservedAndClosedFds(t *testing.T) {
	if _, err := reopenReadOnlyDirectory(2); err == nil {
		t.Fatal("reserved fd2 was accepted")
	}
	if _, err := reopenReadOnlyDirectory(4); err == nil {
		t.Fatal("closed fd4 was accepted")
	}
}

func TestSignedNodeEnvironmentStripsHelperVars(t *testing.T) {
	t.Setenv("HOME", "/work")
	t.Setenv("TMPDIR", "/work/.tmp")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("PI_OFFLINE", "1")
	t.Setenv("TBOUND_CELL_UID", "1000")
	t.Setenv("TBOUND_LANDLOCK_ABI", "8")
	env := signedNodeEnvironment()
	if len(env) != 4 {
		t.Fatalf("signed Node environment must be exactly four fields, got %#v", env)
	}
	seen := map[string]string{}
	for _, item := range env {
		name, value, _ := splitNameValue(item)
		seen[name] = value
	}
	if seen["HOME"] != "/work" || seen["TMPDIR"] != "/work/.tmp" || seen["LANG"] != "C.UTF-8" || seen["PI_OFFLINE"] != "1" {
		t.Fatalf("signed Node environment fields are wrong: %#v", seen)
	}
	if _, ok := seen["TBOUND_CELL_UID"]; ok {
		t.Fatal("helper variable leaked into the Node environment")
	}
}

// TestLandlockTSYNCReachesEnforcement runs a real re-exec of this test binary as
// the TSYNC child. It demonstrates that a genuine multi-threaded Go process can
// install the registered ABI8 TSYNC ruleset, or explicitly skips when the kernel
// lacks the required ABI. It is not a unit fake of /proc/self/task.
func TestLandlockTSYNCReachesEnforcement(t *testing.T) {
	command := exec.Command(os.Args[0])
	command.ExtraFiles = heldBinaryStandIn(t)
	command.Env = append(os.Environ(),
		"TBOUND_LL_CHILD=1",
		"TBOUND_LANDLOCK_ABI=8",
		"TBOUND_LANDLOCK_RO_PATH=/",
		"TBOUND_LANDLOCK_RW_PATH=/tmp",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 3 {
			t.Skipf("kernel lacks the registered Landlock ABI8 TSYNC profile; explicit unsupported skip: %s", strings.TrimSpace(string(output)))
		}
		t.Fatalf("Landlock TSYNC child failed: %v: %s", err, strings.TrimSpace(string(output)))
	}
}

func splitNameValue(item string) (string, string, bool) {
	for i := 0; i < len(item); i++ {
		if item[i] == '=' {
			return item[:i], item[i+1:], true
		}
	}
	return item, "", false
}

// landlockNetChild applies the Landlock ruleset of the named delta and reports, as its exit code,
// the outcome of a TCP connect to 127.0.0.1 port 8080 and to port 8081: 0 when 8080 is not denied
// by Landlock and 8081 is denied with EACCES, 10 when 8080 is denied, 11 when 8081 is not denied,
// 3 when the kernel lacks the registered ABI.
func landlockNetChild(name string) int {
	if name == "base" {
		name = ""
	}
	delta, err := piruntime.ParseD09CellDelta(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if _, _, err := applyLandlockFor(8, delta); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	denied := func(port int) bool {
		connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			_ = connection.Close()
			return false
		}
		return errors.Is(err, syscall.EACCES)
	}
	switch {
	case denied(8080) != !delta.NetSink():
		return 10
	case !denied(8081):
		return 11
	}
	return 0
}

// TestLandlockConnectRightIsPort8080OnlyAndOnlyForNetSink checks the rule in the kernel: with
// net-sink/v1 a connect to port 8080 is not denied and every other port is; without it every
// port is denied. It skips explicitly on a kernel without the registered ABI.
func TestLandlockConnectRightIsPort8080OnlyAndOnlyForNetSink(t *testing.T) {
	for _, name := range []string{"", piruntime.D09CellDeltaNoReap, piruntime.D09CellDeltaNetSink} {
		command := exec.Command(os.Args[0])
		command.ExtraFiles = heldBinaryStandIn(t)
		command.Env = append(os.Environ(), "TBOUND_LL_NET_CHILD="+name, "TBOUND_LANDLOCK_ABI=8",
			"TBOUND_LANDLOCK_RO_PATH=/", "TBOUND_LANDLOCK_RW_PATH=/tmp")
		if name == "" {
			command.Env = append(command.Env, "TBOUND_LL_NET_CHILD=base")
		}
		output, err := command.CombinedOutput()
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 3 {
			t.Skipf("kernel lacks the registered Landlock ABI8 profile; the connect rule is NOT verified here: %s", strings.TrimSpace(string(output)))
		}
		if err != nil {
			t.Errorf("delta %q: %v: %s", name, err, strings.TrimSpace(string(output)))
		}
	}
}

// Lines measured in a --network=none namespace on the guest kernel.
const (
	rejectDefaultIPv6 = "00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 ffffffff 00000001 00000000 00200200       lo"
	loopbackIPv6      = "00000000000000000000000000000001 80 00000000000000000000000000000000 00 00000000000000000000000000000000 00000000 00000002 00000000 80200001       lo"
	ipv4Header        = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT"
)

func TestNoExternalRoutesAcceptsTheKernelsLoopbackRejectDefaults(t *testing.T) {
	ipv6 := rejectDefaultIPv6 + "\n" + rejectDefaultIPv6 + "\n" + loopbackIPv6 + "\n"
	if err := externalRouteIn(ipv4Header+"\n", ipv6); err != nil {
		t.Fatalf("the empty IPv4 table with the kernel's reject ::/0 entries on lo was refused: %v", err)
	}
}

func TestNoExternalRoutesStillRefusesRealDefaultsAndForeignInterfaces(t *testing.T) {
	const accept = "00000000000000000000000000000001 80 00000000000000000000000000000000 00 00000000000000000000000000000000 00000000 00000002 00000000 80200001       lo"
	for name, test := range map[string]struct{ ipv4, ipv6 string }{
		"default IPv6 route without RTF_REJECT": {ipv4Header + "\n", "00000000000000000000000000000000 00 00000000000000000000000000000000 00 fe800000000000000000000000000001 00000400 00000001 00000000 00000003       lo\n"},
		"unicast default flags 0x1 (RTF_UP)":    {ipv4Header + "\n", "00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000400 00000001 00000000 00000001       lo\n"},
		"reject default on another interface":   {ipv4Header + "\n", strings.Replace(rejectDefaultIPv6, "       lo", "       eth0", 1) + "\n"},
		"unparsable flags":                      {ipv4Header + "\n", strings.Replace(rejectDefaultIPv6, "00200200", "zzzzzzzz", 1) + "\n"},
		"IPv6 route on eth0":                    {ipv4Header + "\n", strings.Replace(accept, "       lo", "       eth0", 1) + "\n"},
		"default IPv4 route":                    {ipv4Header + "\nlo\t00000000\t0100007F\t0003\t0\t0\t0\t00000000\t0\t0\t0\n", rejectDefaultIPv6 + "\n"},
		"IPv4 route on eth0":                    {ipv4Header + "\neth0\t0000A8C0\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n", rejectDefaultIPv6 + "\n"},
	} {
		if err := externalRouteIn(test.ipv4, test.ipv6); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// heldBinaryStandIn is the descriptor the real launch passes at fd3 (the held Node binary): the
// Landlock ruleset has a rule on it, so a child that applies the ruleset must have one open.
func heldBinaryStandIn(t *testing.T) []*os.File {
	t.Helper()
	file, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return []*os.File{file}
}

// The held Node binary is on a host mount no path rule covers, so exec of /proc/self/fd/3 is
// denied unless the ruleset names that descriptor, with exactly execute and read.
func TestLandlockPlanAllowsExactlyExecuteAndReadOnTheHeldBinary(t *testing.T) {
	for _, delta := range []piruntime.D09CellDelta{{}, mustDelta(t, piruntime.D09CellDeltaNoReap), mustDelta(t, piruntime.D09CellDeltaNetSink)} {
		plan := planLandlock(delta, []string{"/usr"}, []string{"/work"})
		if len(plan.heldFDRules) != 1 || plan.heldFDRules[0].fd != 3 ||
			plan.heldFDRules[0].access != piruntime.D09LandlockFSExecute|piruntime.D09LandlockFSReadFile {
			t.Fatalf("delta %q: held descriptor rules = %+v, want exactly fd 3 with execute|read", delta.Name(), plan.heldFDRules)
		}
		if err := checkHeldFDRule(plan.heldFDRules[0]); err != nil {
			t.Fatalf("the planned rule was refused: %v", err)
		}
	}
}

func TestLandlockHeldDescriptorRuleRefusesMoreRightsAndOtherDescriptors(t *testing.T) {
	execRead := piruntime.D09LandlockFSExecute | piruntime.D09LandlockFSReadFile
	for name, rule := range map[string]landlockFDRule{
		"write added":    {3, execRead | piruntime.D09LandlockFSWriteFile},
		"truncate added": {3, execRead | piruntime.D09LandlockFSTruncate},
		"read-dir added": {3, execRead | piruntime.D09LandlockFSReadDir},
		"all rights":     {3, piruntime.D09LandlockHandledFS},
		"no rights":      {3, 0},
		"another fd":     {4, execRead},
		"standard input": {0, execRead},
		"negative fd":    {-1, execRead},
	} {
		if err := checkHeldFDRule(rule); err == nil {
			t.Errorf("%s: rule %+v was accepted", name, rule)
		}
	}
}

func mustDelta(t *testing.T, name string) piruntime.D09CellDelta {
	t.Helper()
	delta, err := piruntime.ParseD09CellDelta(name)
	if err != nil {
		t.Fatal(err)
	}
	return delta
}
