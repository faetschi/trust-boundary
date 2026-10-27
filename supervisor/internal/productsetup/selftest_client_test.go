//go:build linux

package productsetup

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestSetupSelfTestNeedsARegisteredNonRootClientUID(t *testing.T) {
	for name, tc := range map[string]struct {
		uid  int
		want string
	}{
		"none configured": {0, "client_uid"},
		"negative":        {-5, "client_uid"},
		"not registered":  {4242, "not an allowed principal"},
	} {
		for _, dryRun := range []bool{false, true} {
			f := newFixture(t)
			f.env.DryRun = dryRun
			f.cfg.SelfTest = &SelfTest{ProjectID: "alpha", ProviderProfile: "opencode-go-engineering", ClientUID: tc.uid}
			if _, err := f.setup(); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s (dry run %v): not refused with %q: %v", name, dryRun, tc.want, err)
			}
			if entries, _ := os.ReadDir(f.root); len(entries) != 0 {
				t.Fatalf("%s (dry run %v): a refused setup changed the root: %v", name, dryRun, entries)
			}
		}
	}
	f := newFixture(t)
	f.cfg.SelfTest = &SelfTest{ProjectID: "alpha", ProviderProfile: "opencode-go-engineering", ClientUID: 1000}
	if _, err := f.setup(); err != nil {
		t.Fatalf("a registered client UID: %v", err)
	}
	if !strings.Contains(f.read(SetupConfigPath), `"client_uid": 1000`) {
		t.Fatalf("the installed record does not carry the client UID:\n%s", f.read(SetupConfigPath))
	}
}

// retarget rewrites the installed setup record's self-test client UID.
func (f *fixture) retarget(uid int) {
	f.t.Helper()
	var record map[string]any
	if err := json.Unmarshal([]byte(f.read(SetupConfigPath)), &record); err != nil {
		f.t.Fatal(err)
	}
	record["self_test"].(map[string]any)["client_uid"] = uid
	data, _ := json.Marshal(record)
	if err := os.WriteFile(f.path(SetupConfigPath), data, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func TestUpdateAndRollbackRefuseAnInstalledSelfTestWithoutARegisteredClientBeforeChangingAnything(t *testing.T) {
	f := newFixture(t)
	f.cfg.SelfTest = &SelfTest{ProjectID: "alpha", ProviderProfile: "opencode-go-engineering", ClientUID: 1000}
	f.installed()
	f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
	selfTests := 0
	f.env.SelfTestHook = func() error { selfTests++; return nil }
	if _, err := Update(f.env, f.newBinary("binary v2")); err != nil || selfTests != 1 {
		t.Fatalf("an update with a registered client UID: %v (self-tests %d)", err, selfTests)
	}
	for name, uid := range map[string]int{"none configured": 0, "not registered": 4242} {
		f.retarget(uid)
		for command, run := range map[string]func() (Report, error){
			"update":   func() (Report, error) { return Update(f.env, f.newBinary("binary v3")) },
			"rollback": func() (Report, error) { return Rollback(f.env) },
		} {
			rep, err := run()
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "client_uid") {
				t.Fatalf("%s with %s: not refused for the client UID: %v", command, name, err)
			}
			if len(rep.Commands) != 0 || f.read(BinaryPath) != "binary v2" || f.read(LastKnownGoodPath) != "binary v1" || selfTests != 1 {
				t.Fatalf("%s with %s changed something before refusing: %v %q %q", command, name, rep.Commands, f.read(BinaryPath), f.read(LastKnownGoodPath))
			}
			if _, err := os.Lstat(f.path(stagedBinaryPath)); err == nil {
				t.Fatalf("%s with %s staged a binary", command, name)
			}
		}
	}
}
