//go:build linux

package productsetup

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func (f *fixture) installed() {
	f.t.Helper()
	if _, err := f.setup(); err != nil {
		f.t.Fatalf("setup: %v", err)
	}
	f.env.Prereqs = nil
}

func (f *fixture) newBinary(content string) string {
	f.t.Helper()
	p := filepath.Join(f.inputs, "tbound-new")
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func TestUpdateRefusesWhileATurnIsUnresolved(t *testing.T) {
	f := newFixture(t)
	f.installed()
	f.env.Turns = func() ([]OpenTurn, error) {
		return []OpenTurn{{ID: "psess-1/turn-1", State: "linked"}, {ID: "psess-2/turn-1", State: StateAwaitingReview}}, nil
	}
	rep, err := Update(f.env, f.newBinary("binary v2"))
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "psess-1/turn-1 (linked)") || strings.Contains(err.Error(), "psess-2") {
		t.Fatalf("an unresolved turn did not refuse the update by name: %v", err)
	}
	if f.read(BinaryPath) != "binary v1" {
		t.Fatal("the binary changed despite the refusal")
	}
	if _, err := os.Lstat(f.path(LastKnownGoodPath)); err == nil {
		t.Fatal("a last-known-good was written for a refused update")
	}
	if _, err := os.Lstat(f.path(stagedBinaryPath)); err == nil {
		t.Fatal("the staged binary was left behind")
	}
	if got := rep.Commands; !slices.Equal(got[len(got)-2:], []string{"systemctl stop " + UnitName, "systemctl start " + UnitName}) {
		t.Fatalf("the service must be stopped for the catalog check and started again after the refusal: %v", got)
	}
}

func TestUpdateKeepsLastKnownGoodAndAPendingReviewDoesNotBlock(t *testing.T) {
	f := newFixture(t)
	f.installed()
	if err := os.WriteFile(f.path(CatalogPath), []byte("catalog journal"), 0o640); err != nil {
		t.Fatal(err)
	}
	f.env.Turns = func() ([]OpenTurn, error) { return []OpenTurn{{ID: "psess-1/turn-2", State: StateAwaitingReview}}, nil }
	rep, err := Update(f.env, f.newBinary("binary v2"))
	if err != nil {
		t.Fatalf("update: %v\n%s", err, rep)
	}
	if f.read(BinaryPath) != "binary v2" || f.read(LastKnownGoodPath) != "binary v1" || f.mode(BinaryPath) != 0o755 {
		t.Fatalf("binary %q, last-known-good %q", f.read(BinaryPath), f.read(LastKnownGoodPath))
	}
	if _, err := os.Lstat(f.path(stagedBinaryPath)); err == nil {
		t.Fatal("the staged binary was left behind")
	}
	backups, _ := filepath.Glob(f.path(BackupsDir + "/catalog-*.jsonl"))
	if len(backups) != 1 {
		t.Fatalf("catalog backups = %v", backups)
	}
	if data, _ := os.ReadFile(backups[0]); string(data) != "catalog journal" {
		t.Fatalf("backup content %q", data)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "1 pending review(s) will be discarded with reason restart") {
		t.Fatalf("the pending review was not reported: %v", rep.Notes)
	}
	want := []string{"systemctl stop " + UnitName, "chown root:root " + LastKnownGoodPath, "chown " + ServiceUser + ":" + ServiceGroup,
		"systemctl start " + UnitName, BinaryPath + " product doctor --self-test"}
	for _, w := range want {
		if !slices.ContainsFunc(rep.Commands, func(c string) bool { return strings.HasPrefix(c, w) }) {
			t.Errorf("commands lack %q: %v", w, rep.Commands)
		}
	}
	again, err := Update(f.env, f.newBinary("binary v2"))
	if err != nil || again.Changed() || !slices.Contains(again.Commands, "systemctl start "+UnitName) {
		t.Fatalf("updating to the installed binary must restart without a swap: %v %v", err, again.Commands)
	}
}

func TestUpdateRollsBackWhenTheSelfTestFails(t *testing.T) {
	f := newFixture(t)
	f.installed()
	f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
	f.env.SelfTestHook = func() error { return errors.New("the synthetic turn failed") }
	rep, err := Update(f.env, f.newBinary("binary v2"))
	if !errors.Is(err, ErrSelfTest) || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("a failed self-test did not roll back: %v", err)
	}
	if f.read(BinaryPath) != "binary v1" || f.read(LastKnownGoodPath) != "binary v1" {
		t.Fatalf("binary %q, last-known-good %q after the rollback", f.read(BinaryPath), f.read(LastKnownGoodPath))
	}
	var systemctl []string
	for _, c := range rep.Commands {
		if strings.HasPrefix(c, "systemctl") {
			systemctl = append(systemctl, strings.TrimPrefix(c, "systemctl "))
		}
	}
	if got := strings.Join(systemctl, ","); got != "stop "+UnitName+",start "+UnitName+",stop "+UnitName+",start "+UnitName {
		t.Fatalf("systemctl sequence %s", got)
	}
}

func TestUpdateWithoutAConfiguredSelfTestProceedsAndSaysSo(t *testing.T) {
	f := newFixture(t)
	f.installed()
	f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
	f.env.SelfTestHook = func() error { return ErrSelfTestNotConfigured }
	rep, err := Update(f.env, f.newBinary("binary v2"))
	if err != nil || f.read(BinaryPath) != "binary v2" {
		t.Fatalf("update: %v", err)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "governed readiness is not established") {
		t.Fatalf("an unconfigured self-test must not look like readiness: %v", rep.Notes)
	}
}

func TestRollbackSwapsBackAndNeedsALastKnownGood(t *testing.T) {
	f := newFixture(t)
	f.installed()
	f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
	if _, err := Rollback(f.env); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "no last-known-good") {
		t.Fatalf("a rollback without a last-known-good was not refused: %v", err)
	}
	if _, err := Update(f.env, f.newBinary("binary v2")); err != nil {
		t.Fatal(err)
	}
	if _, err := Rollback(f.env); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if f.read(BinaryPath) != "binary v1" || f.read(LastKnownGoodPath) != "binary v2" {
		t.Fatalf("binary %q, last-known-good %q after the rollback", f.read(BinaryPath), f.read(LastKnownGoodPath))
	}
	f.env.Turns = func() ([]OpenTurn, error) { return []OpenTurn{{ID: "psess-1/turn-1", State: "admitted"}}, nil }
	if _, err := Rollback(f.env); !errors.Is(err, ErrRefused) || f.read(BinaryPath) != "binary v1" {
		t.Fatalf("a rollback with an unresolved turn was not refused: %v", err)
	}
}

func TestUpdateDryRunStopsNothingAndChangesNothing(t *testing.T) {
	f := newFixture(t)
	f.installed()
	f.env.DryRun = true
	rep, err := Update(f.env, f.newBinary("binary v2"))
	if err != nil || f.read(BinaryPath) != "binary v1" || !slices.Contains(rep.Commands, "systemctl stop "+UnitName) {
		t.Fatalf("dry run: %v %v", err, rep.Commands)
	}
	if _, err := os.Lstat(f.path(stagedBinaryPath)); err == nil {
		t.Fatal("a dry run staged a file")
	}
}

func TestUninstallKeepsDataAndPurgeNeedsConfirmation(t *testing.T) {
	f := newFixture(t)
	f.installed()
	f.env.Turns = func() ([]OpenTurn, error) { return []OpenTurn{{ID: "psess-9/turn-1", State: "admitted"}}, nil }
	rep, err := Uninstall(f.env, false)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	for _, gone := range []string{UnitPath, BinaryPath} {
		if _, err := os.Lstat(f.path(gone)); err == nil {
			t.Errorf("%s survived the uninstall", gone)
		}
	}
	for _, kept := range []string{ConfigDir, StateDir, RegistrationPath} {
		if _, err := os.Lstat(f.path(kept)); err != nil {
			t.Errorf("%s was removed without --purge", kept)
		}
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "psess-9/turn-1 (admitted)") {
		t.Fatalf("unresolved turns were not reported: %v", rep.Notes)
	}
	for _, c := range []string{"systemctl stop " + UnitName, "systemctl disable " + UnitName, "systemctl daemon-reload"} {
		if !slices.Contains(rep.Commands, c) {
			t.Errorf("commands lack %q: %v", c, rep.Commands)
		}
	}
	if _, err := Uninstall(f.env, false); err != nil {
		t.Fatalf("a second uninstall must succeed: %v", err)
	}

	f.installed()
	if _, err := Uninstall(f.env, true); !errors.Is(err, ErrRefused) {
		t.Fatalf("a purge without confirmation was not refused: %v", err)
	}
	if _, err := os.Lstat(f.path(UnitPath)); err != nil {
		t.Fatal("a refused purge changed the installation")
	}
	declined := false
	f.env.Confirm = func(string) (bool, error) { return declined, nil }
	if _, err := Uninstall(f.env, true); !errors.Is(err, ErrRefused) {
		t.Fatalf("a declined purge was not refused: %v", err)
	}
	declined = true
	if _, err := Uninstall(f.env, true); err != nil {
		t.Fatalf("purge: %v", err)
	}
	for _, gone := range []string{ConfigDir, StateDir} {
		if _, err := os.Lstat(f.path(gone)); err == nil {
			t.Errorf("%s survived the purge", gone)
		}
	}
}

func TestDoctorReportsAdvisoryChecksNeverKeysAndOnlyASelfTestEstablishesReadiness(t *testing.T) {
	short, err := os.MkdirTemp("/tmp", "tbd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	f := newFixture(t)
	f.root = short // a short root keeps the socket path under the sun_path limit
	f.env.Root = short
	f.installed()
	const sentinel = "sk-SENTINEL-KEY-0123456789"
	if err := os.WriteFile(f.path(CredentialsDir+"/opencode-go-engineering.key"), []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	f.env.CredentialStatus = func(profile string) (string, error) { return "present mode 0600 fingerprint 1a2b3c4d", nil }

	before := Doctor(f.env, false)
	if !before.Failed() || !strings.Contains(before.String(), "no listener") {
		t.Fatalf("a service that is not listening must fail the reachability check:\n%s", before)
	}

	if err := os.MkdirAll(f.path(RuntimeDir), 0o750); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", f.path(SocketPath))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	after := Doctor(f.env, false)
	if after.Failed() {
		t.Fatalf("a complete installation failed the advisory checks:\n%s", after)
	}
	text := after.String()
	for _, want := range []string{"credential opencode-go-engineering", "reg-1", "governed readiness: NOT established"} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, sentinel) {
		t.Fatal("the advisory report leaked the key")
	}

	notConfigured := Doctor(f.env, true)
	if !notConfigured.Failed() || !errors.Is(notConfigured.SelfTestErr, ErrSelfTestNotConfigured) ||
		!strings.Contains(notConfigured.String(), "self-test not configured") || strings.Contains(notConfigured.String(), "PASSED") {
		t.Fatalf("an unconfigured self-test must be reported as such:\n%s", notConfigured)
	}

	f.cfg.SelfTest = &SelfTest{ProjectID: "alpha", ProviderProfile: "opencode-go-engineering"}
	f.env.Replace = true
	if _, err := f.setup(); err != nil {
		t.Fatal(err)
	}
	var ran SelfTest
	f.env.RunSelfTest = func(s SelfTest) (string, error) { ran = s; return "on project alpha", nil }
	passed := Doctor(f.env, true)
	if passed.Failed() || ran.ProjectID != "alpha" || !strings.Contains(passed.String(), "self-test: PASSED on project alpha") {
		t.Fatalf("a passing self-test:\n%s", passed)
	}
	f.env.RunSelfTest = func(SelfTest) (string, error) { return "", errors.New("worker did not answer") }
	failed := Doctor(f.env, true)
	if !failed.Failed() || !errors.Is(failed.SelfTestErr, ErrSelfTest) || strings.Contains(failed.String(), "PASSED") {
		t.Fatalf("a failing self-test:\n%s", failed)
	}
}

func TestDoctorFlagsAWeakenedModeAndABadUnit(t *testing.T) {
	f := newFixture(t)
	f.installed()
	if err := os.Chmod(f.path(CredentialsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path(UnitPath), []byte(strings.Replace(string(RenderUnit()), "User=tbound-product", "User=root", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	text := Doctor(f.env, false).String()
	for _, want := range []string{"[FAIL] " + CredentialsDir, "mode 0755, want 0700", "[FAIL] unit content"} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
}

func TestInstalledSetupRecordCarriesNoSourcePaths(t *testing.T) {
	f := newFixture(t)
	f.cfg.SelfTest = &SelfTest{ProjectID: "alpha", ProviderProfile: "p"}
	f.installed()
	cfg, err := parseInstalled([]byte(f.read(SetupConfigPath)))
	if err != nil || cfg.SelfTest == nil || cfg.SelfTest.ProjectID != "alpha" || cfg.Registration != "" {
		t.Fatalf("installed record: %+v %v", cfg, err)
	}
}
