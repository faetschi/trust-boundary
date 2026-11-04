//go:build linux

package productsetup

import (
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
)

const (
	testContainersConf = "[engine]\ncgroup_manager = \"cgroupfs\"\nevents_logger = \"file\"\nruntime = \"crun\"\n"
	testGraphRoot      = "/var/lib/tbound-product/podman-storage/graphroot"
	testStorageConf    = "[storage]\ndriver = \"overlay\"\ngraphroot = \"" + testGraphRoot + "\"\n" +
		"rootless_storage_path = \"" + testGraphRoot + "\"\nrunroot = \"/run/tbound-product/containers\"\n\n" +
		"[storage.options]\nignore_chown_errors = \"true\"\n"
)

var installationFormatField = regexp.MustCompile(`"installation_format": \d+,`)

// useFormatTwoInstallation rewrites the installed unit and setup record into format 2, which predates the Podman
// layout: the unit has no DelegateSubgroup or Environment directive, and the record says installation_format 2.
func (f *fixture) useFormatTwoInstallation() {
	f.t.Helper()
	var lines []string
	for _, line := range strings.Split(string(RenderUnit()), "\n") {
		if !strings.HasPrefix(line, "DelegateSubgroup=") && !strings.HasPrefix(line, "Environment=") && !strings.HasPrefix(line, "BindReadOnlyPaths=") {
			lines = append(lines, line)
		}
	}
	record := installationFormatField.ReplaceAllString(f.read(SetupConfigPath), `"installation_format": 2,`)
	for path, content := range map[string]string{UnitPath: strings.Join(lines, "\n"), SetupConfigPath: record} {
		if err := os.WriteFile(f.path(path), []byte(content), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
}

// TestProductSetupPodmanLayoutAndFormatBoundary pins what setup creates for the product Podman layout, and the format
// boundary that comes with it. Setup records format 3. Update refuses a format 2 installation until setup --replace
// rewrites it, and that replacement keeps an existing Podman store.
func TestProductSetupPodmanLayoutAndFormatBoundary(t *testing.T) {
	t.Run("fresh layout has the pinned modes, owners and configuration bytes", func(t *testing.T) {
		f := newFixture(t)
		rep, err := f.setup()
		if err != nil {
			t.Fatalf("setup: %v\n%s", err, rep)
		}
		for logical, want := range map[string]os.FileMode{
			"/etc/tbound-product/podman":             0o755,
			"/var/lib/tbound-product/podman-home":    0o700,
			"/var/lib/tbound-product/podman-storage": 0o700,
			testGraphRoot:                            0o700,
			ContainersConfPath:                       0o644,
			StorageConfPath:                          0o644,
		} {
			if got := f.mode(logical); got != want {
				t.Errorf("%s mode = %04o, want %04o", logical, got, want)
			}
		}
		if got := f.read(ContainersConfPath); got != testContainersConf {
			t.Errorf("containers.conf:\n%q\nwant:\n%q", got, testContainersConf)
		}
		if got := f.read(StorageConfPath); got != testStorageConf {
			t.Errorf("storage.conf:\n%q\nwant:\n%q", got, testStorageConf)
		}
		for _, want := range []string{
			"chown root:root /etc/tbound-product/podman",
			"chown root:root " + ContainersConfPath,
			"chown root:root " + StorageConfPath,
			"chown " + ServiceUser + ":" + ServiceGroup + " /var/lib/tbound-product/podman-home",
			"chown " + ServiceUser + ":" + ServiceGroup + " " + testGraphRoot,
		} {
			if !strings.Contains(strings.Join(rep.Commands, "\n"), want) {
				t.Errorf("recorded commands lack %q:\n%v", want, rep.Commands)
			}
		}
		if rep, err := f.setup(); err != nil || rep.Changed() {
			t.Fatalf("a rerun on the converged layout must change nothing: %v\n%s", err, rep)
		}
	})

	t.Run("the record names format 3", func(t *testing.T) {
		if InstallationFormat != 3 {
			t.Fatalf("InstallationFormat = %d, want 3 for the Podman layout", InstallationFormat)
		}
		f := newFixture(t)
		f.installed()
		if !strings.Contains(f.read(SetupConfigPath), "\"installation_format\": 3,") {
			t.Fatalf("the installed record does not carry format 3:\n%s", f.read(SetupConfigPath))
		}
	})

	t.Run("update refuses a format 2 installation", func(t *testing.T) {
		f := newFixture(t)
		f.installed()
		f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
		f.useFormatTwoInstallation()
		f.env.BinaryFormat = func(io.ReadSeeker) (int, error) { return InstallationFormat, nil }
		rep, err := Update(f.env, f.newBinary("binary v2"))
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "installation format") || !strings.Contains(err.Error(), "setup --replace") {
			t.Fatalf("a format 2 installation was updated without setup --replace: %v", err)
		}
		if got := systemctlCommands(rep.Commands); len(got) != 0 {
			t.Fatalf("a refused update touched the service: %v", got)
		}
		if f.read(BinaryPath) != "binary v1" {
			t.Fatal("a refused update replaced the binary")
		}
	})

	t.Run("replace moves a format 2 installation to the layout and keeps its store", func(t *testing.T) {
		f := newFixture(t)
		f.installed()
		f.useFormatTwoInstallation()
		if err := os.MkdirAll(f.path(testGraphRoot), 0o700); err != nil {
			t.Fatal(err)
		}
		const marker = "retained image layers"
		if err := os.WriteFile(f.path(testGraphRoot+"/marker"), []byte(marker), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env.Replace = true
		if rep, err := f.setup(); err != nil {
			t.Fatalf("setup --replace over format 2: %v\n%s", err, rep)
		}
		if got := f.read(ContainersConfPath); got != testContainersConf {
			t.Fatalf("containers.conf after the replacement: %q", got)
		}
		if got := f.read(testGraphRoot + "/marker"); got != marker {
			t.Fatalf("the replacement changed the existing store: %q", got)
		}
	})

	t.Run("a host input may not sit on the layout", func(t *testing.T) {
		for _, p := range []string{ContainersConfPath, StorageConfPath, testGraphRoot + "/input"} {
			if err := checkNotWritten("input", p); !errors.Is(err, ErrRefused) {
				t.Errorf("%s: a host input on the Podman layout was accepted: %v", p, err)
			}
		}
	})
}
