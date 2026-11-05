package productsetup

import (
	"errors"
	"strings"
	"testing"
)

// serviceDirectives returns the directive lines of the [Service] section of a rendered unit, in order.
func serviceDirectives(unit string) []string {
	var lines []string
	inService := false
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "[") {
			inService = line == "[Service]"
			continue
		}
		if inService && line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestProductUnitPinsCommandCellEnvironment pins the command cell's delegated subgroup and its one environment line,
// and refuses every change, removal and override of the pinned directives. A drop-in or an edit must not be able to
// widen the environment that the product Podman children inherit or the cgroup the service runs in.
func TestProductUnitPinsCommandCellEnvironment(t *testing.T) {
	const environment = "Environment=CONTAINERS_CONF=/etc/tbound-product/podman/containers.conf " +
		"CONTAINERS_STORAGE_CONF=/etc/tbound-product/podman/storage.conf HOME=/var/lib/tbound-product/podman-home LANG=C LC_ALL=C " +
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin TMPDIR=/run/tbound-product/tmp " +
		"XDG_CACHE_HOME=/run/tbound-product/cache XDG_RUNTIME_DIR=/run/tbound-product"
	unit := string(RenderUnit())
	if err := ValidateUnit(RenderUnit()); err != nil {
		t.Fatalf("the rendered unit is refused: %v", err)
	}
	for _, want := range []string{"DelegateSubgroup=runner", environment,
		"BindReadOnlyPaths=-/usr/bin/podman -/usr/bin/crun -/usr/local/bin/cosign -/etc/tbound-product/podman"} {
		if !strings.Contains(unit, "\n"+want+"\n") {
			t.Fatalf("the unit does not carry %q as its own line:\n%s", want, unit)
		}
	}
	for _, line := range serviceDirectives(unit) {
		key, _, _ := strings.Cut(line, "=")
		for name, replacement := range map[string]string{
			"value changed":      line + " extra",
			"directive repeated": line + "\n" + line,
			"override appended":  line + "\n" + key + "=/override",
			"directive removed":  "",
		} {
			mutated := strings.Replace(unit, "\n"+line+"\n", "\n"+replacement+"\n", 1)
			if err := ValidateUnit([]byte(mutated)); !errors.Is(err, ErrRefused) {
				t.Errorf("%s of %q: the validator accepted the unit: %v", name, line, err)
			}
		}
	}
	for name, extra := range map[string]string{
		"private network":        "PrivateNetwork=yes",
		"cgroup write exception": "ReadWritePaths=/sys/fs/cgroup/system.slice/tbound-product.service",
		"environment override":   "Environment=PATH=/tmp/attacker",
	} {
		mutated := strings.Replace(unit, "[Service]\n", "[Service]\n"+extra+"\n", 1)
		if err := ValidateUnit([]byte(mutated)); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: the validator accepted the unit: %v", name, err)
		}
	}
}

func TestProductUnitReadOnlyInputsKeepRuntimeStorageWritable(t *testing.T) {
	for _, artifact := range []string{CommandSeccompPath, CommandCosignPublicKeyPath, CommandOCILayoutPath} {
		if !PathInside(artifact, PodmanConfigDir) {
			t.Errorf("command input %s is outside the read-only Podman bind", artifact)
		}
	}
	if !PathInside(CommandOCILayoutPath, PodmanConfigDir) || CommandOCILayoutPath == PodmanConfigDir {
		t.Fatal("the OCI layout must be strictly below the bind, without a nested mount")
	}
	for _, writable := range []string{PodmanHomeDir, PodmanGraphRootDir, PodmanRunrootDir, PodmanCacheDir, PodmanTmpDir} {
		if PathInside(writable, PodmanConfigDir) || (!PathInside(writable, StateDir) && !PathInside(writable, RuntimeDir)) {
			t.Errorf("Podman writable path %s is outside the unit's write exceptions", writable)
		}
	}
}
