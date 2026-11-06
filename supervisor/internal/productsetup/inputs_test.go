//go:build linux

package productsetup

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// hostInputSpy is a CheckHostInputs seam that records its call and the state of the root at that moment.
type hostInputSpy struct {
	calls   []HostInputs
	rootHad []bool // whether /etc existed in the root when the check ran
	checks  []Check
}

func (s *hostInputSpy) seam(f *fixture) func(HostInputs) []Check {
	return func(in HostInputs) []Check {
		s.calls = append(s.calls, in)
		_, err := os.Lstat(f.path("/etc"))
		s.rootHad = append(s.rootHad, err == nil)
		return s.checks
	}
}

func TestSetupChecksTheHostInputsBeforeAnyWriteWithTheInstalledPathsAndKey(t *testing.T) {
	f := newFixture(t)
	spy := &hostInputSpy{checks: []Check{{Name: "input git", Status: Pass, Detail: "ok"}}}
	f.env.CheckHostInputs = spy.seam(f)
	f.env.VerifyRegistration = func(reg, _, _ []byte) (RegistrationSummary, error) {
		return RegistrationSummary{ID: "reg-1", Digest: "sha256:abc", Projects: []string{"alpha", "beta"},
			PolicyDigests: map[string]string{"alpha": "sha256:" + strings.Repeat("1", 64), "beta": "sha256:" + strings.Repeat("2", 64)}}, nil
	}
	rep, err := f.setup()
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, rep)
	}
	if len(spy.calls) != 1 || spy.rootHad[0] {
		t.Fatalf("the check must run once, before the root is written: calls %d, root existed %v", len(spy.calls), spy.rootHad)
	}
	in := spy.calls[0]
	if in.GatePolicyPaths["alpha"] != f.path(ConfigDir+"/policy-alpha.json") || in.GitPath != f.path(BinaryDir+"/git") ||
		in.XattrCalibrationPath != f.path(ConfigDir+"/xattr-calibration.json") || in.GitDigest != f.cfg.GitDigest ||
		in.PolicyDigests["beta"] != "sha256:"+strings.Repeat("2", 64) {
		t.Fatalf("the check got the wrong inputs: %+v", in)
	}
	if string(in.PublicKey) != "pub\n" || in.PublicKeyPath != f.cfg.RegistrationPublicKey {
		t.Fatalf("the check must see the key setup installs and its source path: %q %q", in.PublicKey, in.PublicKeyPath)
	}
	if !slices.Contains(rep.Checks, spy.checks[0]) {
		t.Fatalf("the report lacks the input check:\n%s", rep)
	}
}

// On a first install only root may own an input. When the account exists its UID joins the set. A test root
// also admits the test process, because setup cannot chown there.
func TestSetupHostInputOwnersAreRootAndTheExistingServiceAccount(t *testing.T) {
	first := newFixture(t)
	spy := &hostInputSpy{}
	first.env.CheckHostInputs = spy.seam(first)
	if _, err := first.setup(); err != nil {
		t.Fatal(err)
	}
	if len(spy.calls) != 1 {
		t.Fatalf("the check ran %d times, want once", len(spy.calls))
	}
	self := uint32(os.Geteuid())
	if got := spy.calls[0].Owners; !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values([]uint32{0, self}))) {
		t.Fatalf("first install owners = %v, want root and the test process", got)
	}

	existing := newFixture(t)
	if err := os.MkdirAll(existing.path("/etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	for file, line := range map[string]string{"/etc/group": ServiceGroup + ":x:4321:\n", "/etc/passwd": ServiceUser + ":x:4321:4321::" + StateDir + ":/usr/sbin/nologin\n"} {
		if err := os.WriteFile(existing.path(file), []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	again := &hostInputSpy{}
	existing.env.CheckHostInputs = again.seam(existing)
	if _, err := existing.setup(); err != nil {
		t.Fatal(err)
	}
	if len(again.calls) != 1 {
		t.Fatalf("the check ran %d times, want once", len(again.calls))
	}
	if got := again.calls[0].Owners; !slices.Contains(got, 4321) || !slices.Contains(got, 0) {
		t.Fatalf("owners with an existing account = %v, want root and 4321", got)
	}
}

func TestSetupRefusesAFailedHostInputNamingItBeforeAnyWriteAndDryRunMatches(t *testing.T) {
	failing := []Check{{Name: "input git", Status: Pass, Detail: "ok"},
		{Name: "input policy alpha", Status: Fail, Detail: "/etc/tbound-product/policy-alpha.json: host input must be a protected bounded regular file"}}
	var refusals []string
	for _, dry := range []bool{false, true} {
		f := newFixture(t)
		f.env.CheckHostInputs = (&hostInputSpy{checks: failing}).seam(f)
		f.env.DryRun = dry
		rep, err := f.setup()
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "input policy alpha") || !strings.Contains(err.Error(), "protected bounded regular file") ||
			!strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("dry run %v: setup was not refused naming the input: %v", dry, err)
		}
		if !slices.Contains(rep.Checks, failing[1]) {
			t.Errorf("dry run %v: the report lacks the failed check", dry)
		}
		if entries, _ := os.ReadDir(f.root); len(entries) != 0 {
			t.Fatalf("dry run %v: a refused setup wrote %v", dry, entries)
		}
		refusals = append(refusals, err.Error())
	}
	if refusals[0] != refusals[1] {
		t.Fatalf("a dry run must report the same refusal:\n%s\n%s", refusals[0], refusals[1])
	}
}

func TestDoctorReportsTheHostInputChecksOfTheInstalledSetup(t *testing.T) {
	f := newFixture(t)
	if _, err := f.setup(); err != nil {
		t.Fatal(err)
	}
	spy := &hostInputSpy{checks: []Check{{Name: "input git", Status: Pass, Detail: "ok"}, {Name: "input calibration", Status: Fail, Detail: "altered"}}}
	f.env.CheckHostInputs = spy.seam(f)
	f.env.VerifyRegistration = func(reg, _, _ []byte) (RegistrationSummary, error) {
		return RegistrationSummary{ID: "reg-1", Digest: "sha256:abc", Projects: []string{"alpha", "beta"},
			PolicyDigests: map[string]string{"alpha": "sha256:" + strings.Repeat("1", 64)}}, nil
	}
	rep := Doctor(f.env, false)
	if len(spy.calls) != 1 {
		t.Fatalf("doctor checked the host inputs %d times", len(spy.calls))
	}
	in := spy.calls[0]
	if in.GitPath != f.path(BinaryDir+"/git") || in.GatePolicyPaths["beta"] != f.path(ConfigDir+"/policy-beta.json") ||
		in.PolicyDigests["alpha"] != "sha256:"+strings.Repeat("1", 64) || in.PublicKeyPath != f.path(RegistrationPublicKeyPath) || string(in.PublicKey) != "pub\n" {
		t.Fatalf("doctor checked the wrong inputs: %+v", in)
	}
	for _, want := range spy.checks {
		if !slices.Contains(rep.Checks, want) {
			t.Errorf("doctor lacks %+v:\n%s", want, rep)
		}
	}
	if !rep.Failed() {
		t.Error("a failed input check must fail the doctor")
	}
}

// Failed reports whether any check failed or a requested self-test did not pass.
func (r DoctorReport) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return true
		}
	}
	return r.SelfTestRequested && r.SelfTestErr != nil
}
