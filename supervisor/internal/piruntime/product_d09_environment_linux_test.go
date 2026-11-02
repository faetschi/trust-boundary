//go:build linux

package piruntime

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// productD09PinnedEnvironment is the product Podman environment, sorted, as the product spec pins it. The product code
// builds the same list from productsetup constants, so this literal checks those constants.
func productD09PinnedEnvironment() []string {
	return []string{
		"CONTAINERS_CONF=/etc/tbound-product/podman/containers.conf",
		"CONTAINERS_STORAGE_CONF=/etc/tbound-product/podman/storage.conf",
		"HOME=/var/lib/tbound-product/podman-home",
		"LANG=C",
		"LC_ALL=C",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"TMPDIR=/run/tbound-product/tmp",
		"XDG_CACHE_HOME=/run/tbound-product/cache",
		"XDG_RUNTIME_DIR=/run/tbound-product",
	}
}

// productD09EnvironmentFake writes an executable that records the environment it was started with, then prints a container
// state that every D09 helper accepts. The record is the kernel's copy of the child environment, so a shell's own exports
// never appear in it. Each run writes <dir>/env-<first argument>.
func productD09EnvironmentFake(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\n" +
		"cat /proc/$$/environ > '" + dir + "'/env-\"$1\"\n" +
		`echo '[{"Name":"cell","State":{"Status":"exited","ExitCode":0,"Pid":0}}]'` + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// productD09RecordedEnvironment runs one step after removing its record, so a stale record cannot pass for this step.
// It returns the sorted environment the step's child started with.
func productD09RecordedEnvironment(t *testing.T, dir, operation string, step func()) []string {
	t.Helper()
	record := filepath.Join(dir, "env-"+operation)
	if err := os.Remove(record); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	step()
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("%s did not start the fake Podman: %v", operation, err)
	}
	environment := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	slices.Sort(environment)
	return environment
}

func TestProductD09EnvironmentReachesEveryPodmanPhase(t *testing.T) {
	// Every override a product child must not inherit is set in the parent, so only the product environment can reach a child.
	for key, value := range map[string]string{
		"CONTAINERS_CONF":          "/tmp/attacker/containers.conf",
		"CONTAINERS_STORAGE_CONF":  "/tmp/attacker/storage.conf",
		"CONTAINERS_FOO":           "attacker",
		"XDG_RUNTIME_DIR":          "/run/user/4242",
		"XDG_CACHE_HOME":           "/tmp/attacker/cache",
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=/tmp/attacker/bus",
		"HOME":                     "/home/attacker",
		"TMPDIR":                   "/tmp/attacker",
		"LANG":                     "en_US.UTF-8",
		"LC_ALL":                   "en_US.UTF-8",
	} {
		t.Setenv(key, value)
	}
	dir := t.TempDir()
	podman := productD09EnvironmentFake(t, dir, "podman")
	cosign := productD09EnvironmentFake(t, dir, "cosign")
	want := productD09PinnedEnvironment()
	launcher := &D09PodmanCrunLauncher{
		runner:      execD09CommandRunner{environment: productD09PinnedEnvironment()},
		productHost: &D09ProductCommandHost{loaded: true},
	}
	ctx := context.Background()

	selected := launcher.commandEnvironment()
	slices.Sort(selected)
	if !slices.Equal(selected, want) {
		t.Fatalf("product launcher selected %q, want %q", selected, want)
	}
	check := func(phase string, got []string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Errorf("%s ran with %q, want the product environment %q", phase, got, want)
		}
	}
	check("probe", productD09RecordedEnvironment(t, dir, "info", func() {
		if result := launcher.runner.Run(ctx, podman, "info", "--format", "json"); result.Err != nil || result.ExitCode != 0 {
			t.Fatalf("probe did not succeed: %+v", result)
		}
	}))
	check("cosign", productD09RecordedEnvironment(t, dir, "version", func() {
		if result := launcher.runner.Run(ctx, cosign, "version"); result.Err != nil || result.ExitCode != 0 {
			t.Fatalf("cosign did not succeed: %+v", result)
		}
	}))
	check("inspect", productD09RecordedEnvironment(t, dir, "inspect", func() {
		if _, err := inspectContainer(ctx, podman, "cell", launcher.commandEnvironment()); err != nil {
			t.Fatalf("inspect did not succeed: %v", err)
		}
	}))
	check("exists", productD09RecordedEnvironment(t, dir, "container", func() {
		if exists, err := containerExists(ctx, podman, "cell", launcher.commandEnvironment()); err != nil || !exists {
			t.Fatalf("exists did not succeed: exists=%t err=%v", exists, err)
		}
	}))
	check("removal", productD09RecordedEnvironment(t, dir, "rm", func() {
		if err := removeContainer(ctx, podman, "cell", launcher.commandEnvironment()); err != nil {
			t.Fatalf("removal did not succeed: %v", err)
		}
	}))
	check("failure cleanup", productD09RecordedEnvironment(t, dir, "rm", func() {
		launcher.cleanupContainer(podman, "cell")
	}))
	check("command cleanup", productD09RecordedEnvironment(t, dir, "rm", func() {
		(&D09CommandRunner{launcher: launcher}).cleanupCommand(nil, podman, "cell")
	}))
}

// TestThesisD09EnvironmentIsUnchanged pins a thesis launcher and its runner to the unit 0 golden environment.
func TestThesisD09EnvironmentIsUnchanged(t *testing.T) {
	for _, key := range thesisD09GoldenEnvKeys {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range thesisD09GoldenInheritedEnv() {
		t.Setenv(key, value)
	}
	want := []string{
		"CONTAINERS_CONF=/etc/tbound/podman-config/containers.conf",
		"CONTAINERS_STORAGE_CONF=/etc/tbound/podman-config/storage.conf",
		"HOME=/home/tbound-golden",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"LOGNAME=tbound-golden",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"TMPDIR=/tmp",
		"USER=tbound-golden",
		"XDG_CACHE_HOME=/run/tbound-pi/cache",
		"XDG_CONFIG_HOME=/home/tbound-golden/.config",
		"XDG_DATA_HOME=/home/tbound-golden/.local/share",
		"XDG_RUNTIME_DIR=/run/user/4242",
	}
	slices.Sort(want)
	launcher := &D09PodmanCrunLauncher{runner: newExecD09CommandRunner()}
	for name, environment := range map[string][]string{
		"launcher": launcher.commandEnvironment(),
		"runner":   execD09CommandRunner{}.commandEnvironment(),
	} {
		slices.Sort(environment)
		if !slices.Equal(environment, want) {
			t.Errorf("thesis %s environment drifted:\n got %q\nwant %q", name, environment, want)
		}
	}
}
