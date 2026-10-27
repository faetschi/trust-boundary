//go:build linux

package productsetup

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// useLegacyInstallation rewrites the installed unit and setup record into the c6fbd5f format: a unit with
// [Unit] and [Service] only, and a setup record without installation_format.
func (f *fixture) useLegacyInstallation() {
	f.t.Helper()
	unit := strings.SplitN(string(RenderUnit()), "\n[Install]", 2)[0] + "\n"
	record := strings.Replace(f.read(SetupConfigPath), "  \"installation_format\": 2,\n", "", 1)
	if strings.Contains(record, "installation_format") {
		f.t.Fatalf("the installed setup record does not carry installation_format 2:\n%s", record)
	}
	for path, content := range map[string]string{UnitPath: unit, SetupConfigPath: record} {
		if err := os.WriteFile(f.path(path), []byte(content), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
}

func systemctlCommands(commands []string) []string {
	var out []string
	for _, c := range commands {
		if strings.HasPrefix(c, "systemctl") {
			out = append(out, c)
		}
	}
	return out
}

func TestUpdateRefusesABinaryOfAnotherInstallationFormatBeforeStoppingTheService(t *testing.T) {
	for name, tc := range map[string]struct {
		legacyInstallation bool
		candidateFormat    int
	}{
		"a new binary over a c6fbd5f installation": {true, InstallationFormat},
		"an old binary over a new installation":    {false, LegacyInstallationFormat},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.installed()
			f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
			if tc.legacyInstallation {
				f.useLegacyInstallation()
			}
			f.env.BinaryFormat = func(string) (int, error) { return tc.candidateFormat, nil }
			rep, err := Update(f.env, f.newBinary("binary v2"))
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "installation format") || !strings.Contains(err.Error(), "setup --replace") {
				t.Fatalf("a cross-format update was not refused with the way out: %v", err)
			}
			if got := systemctlCommands(rep.Commands); len(got) != 0 {
				t.Fatalf("a refused update touched the service: %v", got)
			}
			for _, written := range []string{stagedBinaryPath, LastKnownGoodPath} {
				if _, err := os.Lstat(f.path(written)); err == nil {
					t.Fatalf("a refused update wrote %s", written)
				}
			}
			if f.read(BinaryPath) != "binary v1" {
				t.Fatal("a refused update replaced the binary")
			}
		})
	}
}

func TestRollbackRefusesALastKnownGoodOfAnotherInstallationFormatBeforeStoppingTheService(t *testing.T) {
	f := newFixture(t)
	f.installed()
	f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
	f.env.BinaryFormat = func(string) (int, error) { return InstallationFormat, nil }
	if _, err := Update(f.env, f.newBinary("binary v2")); err != nil {
		t.Fatalf("a same-format update: %v", err)
	}
	f.env.BinaryFormat = func(string) (int, error) { return LegacyInstallationFormat, nil }
	rep, err := Rollback(f.env)
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "installation format") || !strings.Contains(err.Error(), "setup --replace") {
		t.Fatalf("a cross-format rollback was not refused with the way out: %v", err)
	}
	if got := systemctlCommands(rep.Commands); len(got) != 0 {
		t.Fatalf("a refused rollback touched the service: %v", got)
	}
	if f.read(BinaryPath) != "binary v2" || f.read(LastKnownGoodPath) != "binary v1" {
		t.Fatalf("a refused rollback moved the binaries: installed %q, last-known-good %q", f.read(BinaryPath), f.read(LastKnownGoodPath))
	}
	f.env.BinaryFormat = func(string) (int, error) { return 0, errors.New("cannot run it") }
	if _, err := Rollback(f.env); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "cannot run it") {
		t.Fatalf("a binary that cannot report its format must be refused: %v", err)
	}
}

func TestSetupRecordsTheInstallationFormatAndRefusesItInTheAdministratorConfig(t *testing.T) {
	f := newFixture(t)
	f.installed()
	if !strings.Contains(f.read(SetupConfigPath), "\"installation_format\": 2,") {
		t.Fatalf("the installed record does not carry the installation format:\n%s", f.read(SetupConfigPath))
	}
	if _, err := ParseConfig([]byte(`{"schema_version":"` + ConfigSchema + `","installation_format":2}`)); !errors.Is(err, ErrRefused) {
		t.Fatalf("an administrator config that sets installation_format was accepted: %v", err)
	}
}
