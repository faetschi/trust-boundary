//go:build linux

package piruntime

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/productsetup"
)

// The configurations and the environment that setup installs are the ones the command-host loader pins and the
// product Podman children inherit. A drift in either package fails here, before a service could start on it.
func TestProductSetupInstallsTheConfigsAndEnvironmentTheLoaderPins(t *testing.T) {
	if got := productsetup.RenderProductContainersConf(); !bytes.Equal(got, []byte(productD09ContainersConf)) {
		t.Fatalf("setup renders containers.conf %q, but the loader pins %q", got, productD09ContainersConf)
	}
	if got := productsetup.RenderProductStorageConf(); !bytes.Equal(got, []byte(productD09StorageConf)) {
		t.Fatalf("setup renders storage.conf %q, but the loader pins %q", got, productD09StorageConf)
	}
	var unitEnvironment []string
	for _, line := range strings.Split(string(productsetup.RenderUnit()), "\n") {
		if value, ok := strings.CutPrefix(line, "Environment="); ok {
			unitEnvironment = append(unitEnvironment, strings.Fields(value)...)
		}
	}
	properties := map[string]string{
		"LoadState": "loaded", "NeedDaemonReload": "no", "FragmentPath": productsetup.UnitPath,
		"DropInPaths": "", "Environment": strings.Join(unitEnvironment, " "), "EnvironmentFiles": "",
		"User": productsetup.ServiceUser, "Group": productsetup.ServiceGroup,
	}
	if err := productD09ValidateEffectiveUnit(properties); err != nil {
		t.Fatalf("loader refuses setup's effective environment: %v", err)
	}
	if want := productD09CommandEnvironment(); !slices.Equal(unitEnvironment, want) {
		t.Fatalf("the unit gives the service %v, but the product Podman children inherit %v", unitEnvironment, want)
	}
}
