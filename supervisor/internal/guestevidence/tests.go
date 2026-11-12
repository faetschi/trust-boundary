package guestevidence

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// TestSpec names the test binary a guest test-run artifact covers and the tests that must pass in it.
type TestSpec struct {
	Package  string
	Required []string
}

var testSpecs = map[string]TestSpec{
	// EVQ-ORACLE-PROCESS: the process oracle against real cgroups. These tests skip where cgroups
	// are not delegated, so only the guest can establish them.
	KindProcOracleTests: {
		Package: "tbound/supervisor/internal/procoracle",
		Required: []string{
			"TestRealCleanTeardownIsNoEffect",
			"TestRealDetachedSleepSurvivingIsProc01",
			"TestRealProcessMovedOutOfTheCellIsProc02",
			"TestRealReparentedSurvivorIsProc01",
			"TestRealResidualListenerIsProc03",
			"TestRealCellUserNamespaceProcessIsCellIdentity",
		},
	},
	// EVQ-NETWORK-SINK-GUEST: the Landlock TCP connect right of the command cell. It skips below
	// Landlock ABI 8 and requires enforcement on a second thread, so it must run on the guest kernel.
	KindLandlockConnectTests: {
		Package:  "tbound/supervisor/cmd/tbound-cell-entrypoint",
		Required: []string{"TestLandlockConnectRightIsPort8080OnlyAndOnlyForNetSink", "TestLandlockTSYNCReachesEnforcement"},
	},
}

// SpecFor returns the package and the required tests of a guest test-run kind.
func SpecFor(kind string) (TestSpec, bool) {
	s, ok := testSpecs[kind]
	return s, ok
}

// TestPackages lists the packages whose test binary the capture builds for the guest, sorted.
func TestPackages() []string {
	var out []string
	for _, s := range testSpecs {
		out = append(out, s.Package)
	}
	slices.Sort(out)
	return out
}

// TestRunArgs is the argument vector the producer runs the test binary with.
var TestRunArgs = []string{"-test.v", "-test.count=1"}

// Test results of a guest test run.
const (
	TestPass = "pass"
	TestFail = "fail"
	TestSkip = "skip"
)

// TestRun is the data of a guest test-run artifact: the result of running, on the guest, the test
// binary that the capture of the frozen commit built.
type TestRun struct {
	Package          string       `json:"package"`
	TestBinarySHA256 string       `json:"test_binary_sha256"`
	Args             []string     `json:"args"`
	ExitCode         int          `json:"exit_code"`
	KernelRelease    string       `json:"kernel_release"`
	OutputSHA256     string       `json:"output_sha256"`
	Results          []TestResult `json:"results"`
}

// TestResult is the result of one top-level test.
type TestResult struct {
	Test   string `json:"test"`
	Action string `json:"action"`
}

func validateTests(kind string, raw []byte, e Expect) error {
	spec, ok := testSpecs[kind]
	if !ok {
		return fmt.Errorf("kind %s has no test specification", kind)
	}
	var r TestRun
	if err := DecodeStrict(raw, &r); err != nil {
		return err
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if r.Package != spec.Package {
		add("package %q, want %q", r.Package, spec.Package)
	}
	want := e.TestBinaries[spec.Package]
	if !IsDigest(want) {
		add("the capture of the frozen commit recorded no test binary for %s", spec.Package)
	} else if r.TestBinarySHA256 != want {
		add("the test binary is %q, the capture of the frozen commit built %q", r.TestBinarySHA256, want)
	}
	if !slices.Equal(r.Args, TestRunArgs) {
		add("test arguments %q, want %q", r.Args, TestRunArgs)
	}
	if r.ExitCode != 0 {
		add("the test binary exited %d", r.ExitCode)
	}
	if strings.TrimSpace(r.KernelRelease) == "" || !IsDigest(r.OutputSHA256) {
		add("kernel release or output digest is missing")
	}
	seen := map[string]string{}
	for _, res := range r.Results {
		if res.Test == "" || (res.Action != TestPass && res.Action != TestFail && res.Action != TestSkip) {
			add("result %+v is malformed", res)
			continue
		}
		if prev, dup := seen[res.Test]; dup {
			add("test %s is reported twice (%s, %s)", res.Test, prev, res.Action)
			continue
		}
		seen[res.Test] = res.Action
		if res.Action == TestFail {
			add("test %s failed", res.Test)
		}
	}
	for _, name := range spec.Required {
		if seen[name] != TestPass {
			add("required test %s did not pass (%q)", name, seen[name])
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}
