//go:build linux

package productsetup

import (
	"os"
	"slices"
	"strings"
	"testing"
)

const bootLink = "/etc/systemd/system/multi-user.target.wants/" + UnitName

func TestUnitHasABootActivationSectionAndTheValidatorPinsIt(t *testing.T) {
	const install = "[Install]\nWantedBy=multi-user.target\n"
	unit := string(RenderUnit())
	if !strings.HasSuffix(unit, "\n"+install) {
		t.Fatalf("the unit has no boot activation:\n%s", unit)
	}
	if err := ValidateUnit(RenderUnit()); err != nil {
		t.Fatal(err)
	}
	for name, mutated := range map[string]string{
		"no install section":        strings.TrimSuffix(unit, "\n"+install),
		"another target":            strings.Replace(unit, "WantedBy=multi-user.target", "WantedBy=graphical.target", 1),
		"two targets":               strings.Replace(unit, "WantedBy=multi-user.target", "WantedBy=multi-user.target sockets.target", 1),
		"an alias":                  unit + "Alias=other.service\n",
		"the install section twice": unit + install,
		"required by":               strings.Replace(unit, "WantedBy=", "RequiredBy=", 1),
	} {
		if err := ValidateUnit([]byte(mutated)); err == nil {
			t.Errorf("%s: the validator accepted the unit", name)
		}
	}
}

func TestSetupEnablesTheUnitAtBootOnceAndDryRunPlansIt(t *testing.T) {
	f := newFixture(t)
	f.env.DryRun = true
	rep, err := f.setup()
	if err != nil || !slices.Contains(rep.Commands, "systemctl enable "+UnitName) || !slices.Contains(rep.Steps, Step{"enable", UnitName, "created", "WantedBy=multi-user.target"}) {
		t.Fatalf("the dry run did not plan the activation: %v\n%s", err, rep)
	}
	if entries, _ := os.ReadDir(f.root); len(entries) != 0 {
		t.Fatalf("a dry run wrote %v", entries)
	}

	f.env.DryRun = false
	rep, err = f.setup()
	if err != nil {
		t.Fatal(err)
	}
	reload, enable := slices.Index(rep.Commands, "systemctl daemon-reload"), slices.Index(rep.Commands, "systemctl enable "+UnitName)
	if reload < 0 || enable < reload {
		t.Fatalf("the unit must be enabled after the reload: %v", rep.Commands)
	}
	if target, err := os.Readlink(f.path(bootLink)); err != nil || target != UnitPath {
		t.Fatalf("the test root does not show the enabled unit: %q %v", target, err)
	}

	rep, err = f.setup()
	if err != nil || rep.Changed() || len(rep.Commands) != 0 {
		t.Fatalf("a rerun on an enabled unit must change nothing: %v\n%s", err, rep)
	}
	if err := os.Remove(f.path(bootLink)); err != nil {
		t.Fatal(err)
	}
	rep, err = f.setup()
	if err != nil || !slices.Equal(rep.Commands, []string{"systemctl enable " + UnitName}) {
		t.Fatalf("a disabled unit must be enabled again, and only that: %v %v", err, rep.Commands)
	}
}

func TestDoctorFlagsAUnitThatIsNotEnabledAtBoot(t *testing.T) {
	f := newFixture(t)
	f.installed()
	if text := Doctor(f.env, false).String(); !strings.Contains(text, "[PASS] unit enabled") {
		t.Fatalf("an enabled unit:\n%s", text)
	}
	if err := os.Remove(f.path(bootLink)); err != nil {
		t.Fatal(err)
	}
	if text := Doctor(f.env, false).String(); !strings.Contains(text, "[FAIL] unit enabled") || !strings.Contains(text, "tbound product setup") {
		t.Fatalf("a unit that does not start at boot:\n%s", text)
	}
}

func TestUninstallDisablesTheUnit(t *testing.T) {
	f := newFixture(t)
	f.installed()
	f.env.Turns = func() ([]OpenTurn, error) { return nil, nil }
	rep, err := Uninstall(f.env, false)
	if err != nil || !slices.Contains(rep.Commands, "systemctl disable "+UnitName) {
		t.Fatalf("uninstall: %v %v", err, rep.Commands)
	}
	if _, err := os.Lstat(f.path(bootLink)); err == nil {
		t.Fatal("the unit is still enabled after the uninstall")
	}
}
