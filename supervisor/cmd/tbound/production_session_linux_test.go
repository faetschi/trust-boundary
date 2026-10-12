//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/piruntime"
)

// TestProductionServeRefusesOnUnprovisionedHost proves the plain production
// route fails closed without starting a worker and without emitting a receipt
// when the protected profile, D09 runtime, or sealed bindings are absent. It
// must never return the old generic composition-unavailable error as a
// placeholder for a real launch.
func TestProductionServeRefusesOnUnprovisionedHost(t *testing.T) {
	var transcript bytes.Buffer
	err := runGovernedPiServe(context.Background(), &transcript, productionServeConfig{})
	if err == nil {
		t.Fatal("unprovisioned production route returned success")
	}
	if transcript.Len() != 0 {
		t.Fatalf("unprovisioned production route wrote a receipt: %q", transcript.String())
	}
	if errors.Is(err, piruntime.ErrD09SessionCompositionUnavailable) {
		t.Fatalf("production route still returns the placeholder composition-unavailable error: %v", err)
	}
	if !errors.Is(err, piruntime.ErrProductionLauncherUnavailable) &&
		!errors.Is(err, piruntime.ErrHostProfileMissing) &&
		!errors.Is(err, piruntime.ErrHostAttestationMissing) &&
		!errors.Is(err, ErrProductionSealedRuntime) {
		t.Fatalf("unprovisioned production refusal is not a typed production refusal: %v", err)
	}
}

// TestProductionServeSelfSignedProfileCannotRootAdmit proves a self-signed
// operator profile is refused: the verification key is always read from the
// fixed root-owned trust root, so --native-host-profile cannot select its own
// trust root.
func TestProductionServeSelfSignedProfileCannotRootAdmit(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	profile := piruntime.DefaultLinuxHostProfileTemplate()
	profile.ID = "operator-self-signed-attempt"
	profileJSON, signature, _, err := piruntime.SignLinuxHostProfile(profile, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pi-host-profile.json"), profileJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pi-host-profile.ed25519"), signature, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := piruntime.LoadLinuxSignedHostProfile(dir); err == nil {
		t.Fatal("self-signed operator profile was admitted without the fixed root-owned trust root")
	}
}

// TestProductionSealedPrerequisiteErrorNamesBindings proves the typed refusal
// names the exact trusted bindings the sealed composition owners must supply.
func TestProductionSealedPrerequisiteErrorNamesBindings(t *testing.T) {
	err := &ProductionPrerequisiteError{Missing: productionMissingBindings()}
	if !errors.Is(err, ErrProductionSealedRuntime) {
		t.Fatal("prerequisite error does not unwrap to the sealed-runtime sentinel")
	}
	message := err.Error()
	for _, want := range []string{"worker exposure", "runtime-bundle", "cgroup"} {
		if !strings.Contains(message, want) {
			t.Fatalf("prerequisite error %q does not name %q", message, want)
		}
	}
}

// TestProductionServeRejectsRelativeConfigurationPaths proves the bounded plain
// CLI inputs are validated before any protected artifact is read.
func TestProductionServeRejectsRelativeConfigurationPaths(t *testing.T) {
	err := runProductionPiServe(context.Background(), io.Discard, productionServeConfig{Workspace: "relative/workspace"}, nil)
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative --workspace = %v, want an absolute-path refusal", err)
	}
}

func TestProductionServeRequiresBoundedTaskRequestIdentity(t *testing.T) {
	base := productionServeConfig{Source: "/var/lib/tbound/source", TaskID: "registered-task", TaskPrompt: "read task.txt"}
	if err := requireProductionLaunchInputs(base); err == nil {
		t.Fatal("production launch accepted a task without a request identity")
	}
	base.TaskRequestID = "request-1"
	if err := requireProductionLaunchInputs(base); err != nil {
		t.Fatalf("bounded task request identity refused: %v", err)
	}
}
