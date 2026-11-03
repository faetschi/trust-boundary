//go:build linux

package piruntime

import (
	"context"
	"errors"
	"os"
	"os/exec"
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

// productD09FakePodmanInfo is a podman info document that readInfo accepts.
const productD09FakePodmanInfo = `{"host":{"cgroupVersion":"v2","ociRuntime":{"name":"crun","version":"1.14.1"},"security":{"rootless":true}}}`

// productD09EnvironmentFake writes an executable that records the environment it was started with, then prints output that
// every D09 helper accepts. The record is the kernel's copy of the child environment, so a shell's own exports never appear
// in it. Each run writes <dir>/env-<first argument>. verifyOutput is what `verify` prints; only cosign runs it.
func productD09EnvironmentFake(t *testing.T, dir, name, verifyOutput string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\n" +
		"cat /proc/$$/environ > '" + dir + "'/env-\"$1\"\n" +
		"case \"$1\" in\n" +
		"info) echo '" + productD09FakePodmanInfo + "' ;;\n" +
		"verify) echo '" + verifyOutput + "' ;;\n" +
		`*) echo '[{"Name":"cell","State":{"Status":"exited","ExitCode":0,"Pid":0}}]' ;;` + "\n" +
		"esac\n"
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

// poisonProductD09InheritedEnvironment sets every variable d09ProbeEnv forwards, and every Podman override, to a value the
// product environment never uses. Call it only after all temporary directories and executables exist, because t.TempDir
// reads TMPDIR. The poison stays set until the test ends, so every child inherits it unless the product environment replaces it.
func poisonProductD09InheritedEnvironment(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"CONTAINERS_CONF":          "/tmp/attacker/containers.conf",
		"CONTAINERS_STORAGE_CONF":  "/tmp/attacker/storage.conf",
		"CONTAINERS_FOO":           "attacker",
		"XDG_RUNTIME_DIR":          "/run/user/4242",
		"XDG_CACHE_HOME":           "/tmp/attacker/cache",
		"XDG_CONFIG_HOME":          "/tmp/attacker/config",
		"XDG_DATA_HOME":            "/tmp/attacker/data",
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=/tmp/attacker/bus",
		"HOME":                     "/home/attacker",
		"USER":                     "attacker",
		"LOGNAME":                  "attacker",
		"TMPDIR":                   "/tmp/attacker",
		"LANG":                     "en_US.UTF-8",
		"LC_ALL":                   "en_US.UTF-8",
	} {
		t.Setenv(key, value)
	}
}

func TestProductD09EnvironmentReachesEveryPodmanPhase(t *testing.T) {
	// Every fixture is allocated before the poison: t.TempDir and exec.LookPath read the inherited environment. completeD09Host
	// allocates the OCI layout and the cosign key, and its config names their digests.
	dir := t.TempDir()
	config, _ := completeD09Host(t)
	verifyOutput := `[{"critical":{"image":{"docker-manifest-digest":"` + config.ExpectedImageDigest + `"}}}]`
	podman := productD09EnvironmentFake(t, dir, "podman", "")
	cosign := productD09EnvironmentFake(t, dir, "cosign", verifyOutput)
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	poisonProductD09InheritedEnvironment(t)

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
	requireProductD09Environment := func(t *testing.T, phase string, got []string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Errorf("%s ran with %q, want the product environment %q", phase, got, want)
		}
	}

	t.Run("probe reads podman info through the product runner", func(t *testing.T) {
		requireProductD09Environment(t, "probe", productD09RecordedEnvironment(t, dir, "info", func() {
			if _, err := launcher.readInfo(ctx, podman); err != nil {
				t.Fatalf("probe did not succeed: %v", err)
			}
		}))
	})

	t.Run("offline cosign verification runs with the product environment", func(t *testing.T) {
		requireProductD09Environment(t, "verify", productD09RecordedEnvironment(t, dir, "verify", func() {
			if err := launcher.verifyOfflineSignature(ctx, config, cosign, false); err != nil {
				t.Fatalf("offline cosign verification did not succeed: %v", err)
			}
		}))
	})

	t.Run("container helpers receive the product environment", func(t *testing.T) {
		requireProductD09Environment(t, "inspect", productD09RecordedEnvironment(t, dir, "inspect", func() {
			if _, err := inspectContainer(ctx, podman, "cell", launcher.commandEnvironment()); err != nil {
				t.Fatalf("inspect did not succeed: %v", err)
			}
		}))
		requireProductD09Environment(t, "exists", productD09RecordedEnvironment(t, dir, "container", func() {
			if exists, err := containerExists(ctx, podman, "cell", launcher.commandEnvironment()); err != nil || !exists {
				t.Fatalf("exists did not succeed: exists=%t err=%v", exists, err)
			}
		}))
		requireProductD09Environment(t, "removal", productD09RecordedEnvironment(t, dir, "rm", func() {
			if err := removeContainer(ctx, podman, "cell", launcher.commandEnvironment()); err != nil {
				t.Fatalf("removal did not succeed: %v", err)
			}
		}))
	})

	t.Run("induced post-start failure is cleaned up with the product environment", func(t *testing.T) {
		// The started sleep stands in for a launched worker that fails after start: the cleanup must kill it and remove its container.
		worker := exec.Command(sleep, "30")
		if err := worker.Start(); err != nil {
			t.Fatal(err)
		}
		requireProductD09Environment(t, "failure cleanup", productD09RecordedEnvironment(t, dir, "rm", func() {
			if err := launcher.cleanupFailedContainer(worker, podman, "cell"); !errors.Is(err, ErrUnknownSettlement) {
				t.Fatalf("failed launch without a settlement scope returned %v, want ErrUnknownSettlement", err)
			}
		}))
		// The unsettled failed launch keeps its record, so Close must still refuse, as its contract requires.
		if err := launcher.Close(ctx); !errors.Is(err, ErrUnknownSettlement) {
			t.Fatalf("Close with an unsettled failed launch returned %v, want ErrUnknownSettlement", err)
		}
	})

	t.Run("direct cleanups receive the product environment", func(t *testing.T) {
		requireProductD09Environment(t, "failure cleanup", productD09RecordedEnvironment(t, dir, "rm", func() {
			launcher.cleanupContainer(podman, "cell")
		}))
		requireProductD09Environment(t, "command cleanup", productD09RecordedEnvironment(t, dir, "rm", func() {
			(&D09CommandRunner{launcher: launcher}).cleanupCommand(nil, podman, "cell")
		}))
	})

	t.Run("Close of an idle product launcher succeeds", func(t *testing.T) {
		idle := &D09PodmanCrunLauncher{productHost: &D09ProductCommandHost{loaded: true}}
		if err := idle.Close(ctx); err != nil {
			t.Fatalf("Close of an idle product launcher returned %v", err)
		}
	})
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
