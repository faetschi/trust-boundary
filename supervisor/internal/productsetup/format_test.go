//go:build linux

package productsetup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
			f.env.BinaryFormat = func(*os.File) (int, error) { return tc.candidateFormat, nil }
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
	f.env.BinaryFormat = func(*os.File) (int, error) { return InstallationFormat, nil }
	if _, err := Update(f.env, f.newBinary("binary v2")); err != nil {
		t.Fatalf("a same-format update: %v", err)
	}
	f.env.BinaryFormat = func(*os.File) (int, error) { return LegacyInstallationFormat, nil }
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
	f.env.BinaryFormat = func(*os.File) (int, error) { return 0, errors.New("cannot read it") }
	if _, err := Rollback(f.env); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "cannot read it") {
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

// The marker is built with a format verb so that this test binary holds no well-formed one of its own.
const markerLine = "tbound-product-installation-format=%d;"

func TestInstallationMarkerNamesTheInstallationFormat(t *testing.T) {
	if InstallationMarker != fmt.Sprintf(markerLine, InstallationFormat) {
		t.Fatalf("the marker %q does not name InstallationFormat %d", InstallationMarker, InstallationFormat)
	}
}

func scanBytes(t *testing.T, content []byte) (int, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "candidate")
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return ScanInstallationFormat(f)
}

func TestScanInstallationFormatReadsTheFirstWellFormedMarker(t *testing.T) {
	prefix := fmt.Sprintf(markerLine, 7)[:strings.IndexByte(markerLine, '=')+1]
	for name, tc := range map[string]struct {
		content string
		want    int
	}{
		"a marker":                         {"binary " + fmt.Sprintf(markerLine, 2) + " binary", 2},
		"a marker of another format":       {fmt.Sprintf(markerLine, 1), 1},
		"a marker at the very end":         {"x" + fmt.Sprintf(markerLine, 3), 3},
		"no marker":                        {"binary v1", LegacyInstallationFormat},
		"an empty file":                    {"", LegacyInstallationFormat},
		"a prefix without digits":          {prefix + ";" + prefix + "x;", LegacyInstallationFormat},
		"zero":                             {prefix + "0;", LegacyInstallationFormat},
		"too many digits":                  {prefix + "12345;", LegacyInstallationFormat},
		"no terminator":                    {prefix + "2", LegacyInstallationFormat},
		"a malformed prefix then a marker": {prefix + "x;" + fmt.Sprintf(markerLine, 2), 2},
		"the first of two markers":         {fmt.Sprintf(markerLine, 2) + fmt.Sprintf(markerLine, 1), 2},
	} {
		if got, err := scanBytes(t, []byte(tc.content)); err != nil || got != tc.want {
			t.Errorf("%s: format %d, %v, want %d", name, got, err, tc.want)
		}
	}
}

func TestScanInstallationFormatRefusesAFileBeyondTheBinaryBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(maxBinaryBytes + 1); err != nil {
		t.Skipf("no sparse file of %d bytes here: %v", int64(maxBinaryBytes+1), err)
	}
	if _, err := ScanInstallationFormat(f); !errors.Is(err, ErrRefused) {
		t.Fatalf("a file beyond the bound was scanned: %v", err)
	}
}

func TestUpdateTakesACandidateFormatFromItsBytes(t *testing.T) {
	declared := func(format int) string { return "binary v2 " + fmt.Sprintf(markerLine, format) }
	for name, tc := range map[string]struct {
		legacyInstallation bool
		candidate          string
		refused            bool
	}{
		"a marker of the installation's format":      {false, declared(InstallationFormat), false},
		"a marker of another format":                 {false, declared(LegacyInstallationFormat), true},
		"no marker over a new installation":          {false, "binary v2", true},
		"a new marker over a legacy installation":    {true, declared(InstallationFormat), true},
		"no marker over a legacy installation":       {true, "binary v2", false},
		"a legacy marker over a legacy installation": {true, declared(LegacyInstallationFormat), false},
	} {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, dry run %v", name, dryRun), func(t *testing.T) {
				f := newFixture(t)
				f.installed()
				f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
				if tc.legacyInstallation {
					f.useLegacyInstallation()
				}
				f.env.BinaryFormat = ScanInstallationFormat
				f.env.DryRun = dryRun
				rep, err := Update(f.env, f.newBinary(tc.candidate))
				if tc.refused {
					if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "installation format") {
						t.Fatalf("a candidate of another format was not refused: %v", err)
					}
					if got := systemctlCommands(rep.Commands); len(got) != 0 {
						t.Fatalf("a refused update touched the service: %v", got)
					}
					if _, err := os.Lstat(f.path(stagedBinaryPath)); err == nil {
						t.Fatal("a refused update staged the binary")
					}
				} else if err != nil {
					t.Fatalf("a candidate of the installation's format was refused: %v", err)
				}
				if f.read(BinaryPath) == tc.candidate && (tc.refused || dryRun) {
					t.Fatal("the binary was replaced")
				}
			})
		}
	}
}

func TestScanInstallationFormatFindsAMarkerAcrossReadBoundaries(t *testing.T) {
	marker := fmt.Sprintf(markerLine, 2)
	// The first read ends at chunk+keep and the next one chunk later, so a marker is cut around both ends.
	const chunk = 1 << 20
	keep := strings.IndexByte(marker, '=') + 1 + 4
	for _, end := range []int{chunk + keep, 2*chunk + keep} {
		for at := end - len(marker) - 3; at <= end+3; at++ {
			content := make([]byte, end+chunk/2)
			copy(content[at:], marker)
			if got, err := scanBytes(t, content); err != nil || got != 2 {
				t.Fatalf("a marker at %d (a read ends at %d): format %d, %v", at, end, got, err)
			}
		}
	}
}

func TestSetupTakesTheBinaryFormatFromItsBytesAndRefusesAnotherFormatBeforeChangingAnything(t *testing.T) {
	declared := func(format int) string { return "binary v1 " + fmt.Sprintf(markerLine, format) }
	for name, tc := range map[string]struct {
		binary  string
		refused bool
	}{
		"a marker of this setup's format": {declared(InstallationFormat), false},
		"a marker of another format":      {declared(LegacyInstallationFormat), true},
		"no marker is the legacy format":  {"binary v1", true},
	} {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, dry run %v", name, dryRun), func(t *testing.T) {
				f := newFixture(t)
				f.env.BinaryFormat = ScanInstallationFormat
				f.env.DryRun = dryRun
				if err := os.WriteFile(f.binary, []byte(tc.binary), 0o755); err != nil {
					t.Fatal(err)
				}
				rep, err := f.setup()
				if !tc.refused {
					if err != nil {
						t.Fatalf("a binary of this setup's format was refused: %v", err)
					}
					return
				}
				if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "installation format") {
					t.Fatalf("a binary of another installation format was not refused: %v", err)
				}
				if entries, _ := os.ReadDir(f.root); len(entries) != 0 || len(rep.Commands) != 0 {
					t.Fatalf("a refused setup changed the host: %v %v", entries, rep.Commands)
				}
			})
		}
	}
}
