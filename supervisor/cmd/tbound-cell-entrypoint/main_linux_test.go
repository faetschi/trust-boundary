//go:build linux

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"tbound/supervisor/internal/piruntime"
)

// TestMain lets the test binary act as the Landlock TSYNC child: when
// TBOUND_LL_CHILD=1 it applies the registered ABI8 TSYNC ruleset and exits,
// proving the real (multi-threaded) Go process can reach enforcement. Exit code
// 3 means the kernel lacks the required ABI (explicit unsupported skip).
func TestMain(m *testing.M) {
	if name := os.Getenv("TBOUND_LL_NET_CHILD"); name != "" {
		os.Exit(landlockNetChild(name))
	}
	if os.Getenv("TBOUND_LL_CHILD") == "1" {
		if _, _, err := applyLandlock(8); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
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
