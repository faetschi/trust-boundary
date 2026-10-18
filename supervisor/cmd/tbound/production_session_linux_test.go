//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/piruntime"
)

// TestProductionServeRefusesOnUnprovisionedHost proves the plain production
// route fails closed without starting a worker and without emitting a receipt
// when the protected profile, D09 runtime, or sealed bindings are absent. It
// must never return the old generic composition-unavailable error as a
// placeholder for a real launch.
func TestProductionServeRefusesOnUnprovisionedHost(t *testing.T) {
	var transcript bytes.Buffer
	err := runProductionPiServe(context.Background(), &transcript, productionServeConfig{}, newProductionBinder())
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

func TestProductionPublicationAttemptRequiresCompleteWorkerSettlement(t *testing.T) {
	valid := piruntime.Settlement{
		State: piruntime.SettlementStopped, ExitObserved: true, DescendantsSettled: true,
	}
	cases := []struct {
		name       string
		settlement piruntime.Settlement
		closeErr   error
	}{
		{name: "unknown", settlement: piruntime.Settlement{State: piruntime.SettlementUnknown, ExitObserved: true, DescendantsSettled: true}},
		{name: "exit not observed", settlement: piruntime.Settlement{State: piruntime.SettlementStopped, DescendantsSettled: true}},
		{name: "descendants not settled", settlement: piruntime.Settlement{State: piruntime.SettlementStopped, ExitObserved: true}},
		{name: "close failed", settlement: valid, closeErr: errors.New("worker close observation failed")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			err := runProductionPublicationAfterWorkerSettlement(tc.settlement, tc.closeErr, func() error {
				attempts++ // represents the actual evidence/finalize callback
				return nil
			})
			if !errors.Is(err, ErrProductionWorkerSettlementIncomplete) {
				t.Fatalf("publication gate error = %v, want incomplete-settlement refusal", err)
			}
			if attempts != 0 {
				t.Fatalf("publication callback ran %d times for incomplete settlement", attempts)
			}
		})
	}

	attempts := 0
	if err := runProductionPublicationAfterWorkerSettlement(valid, nil, func() error {
		attempts++
		return nil
	}); err != nil {
		t.Fatalf("fully settled worker did not allow publication callback: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("fully settled worker publication callback count=%d, want 1", attempts)
	}
}

func assertProductionSessionLease(t *testing.T, runtime *PublicationRuntime, wantHeld bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	err := runtime.finalizer.Admission().CloseAndWait(ctx)
	if wantHeld && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("admission drain error = %v, want deadline while the session lease is held", err)
	}
	if !wantHeld && err != nil {
		t.Fatalf("admission drain error = %v, want released session lease", err)
	}
}

func TestProductionLaunchFailureAdmissionUsesRealPublicationLease(t *testing.T) {
	stopped := piruntime.Settlement{
		State: piruntime.SettlementStopped, ExitObserved: true, DescendantsSettled: true,
	}
	unknown := piruntime.Settlement{State: piruntime.SettlementUnknown}
	tests := []struct {
		name      string
		launchErr error
		wantHeld  bool
	}{
		{"wrapped unknown typed failure", fmt.Errorf("launch: %w", &piruntime.PiWorkerLaunchError{Cause: errors.New("probe"), Settlement: unknown}), true},
		{"joined unknown typed failure", errors.Join(errors.New("other launch error"), &piruntime.PiWorkerLaunchError{Cause: errors.New("probe"), Settlement: unknown}), true},
		{"wrapped unknown sentinel", fmt.Errorf("launch: %w", piruntime.ErrUnknownSettlement), true},
		{"joined unknown sentinel", errors.Join(errors.New("other launch error"), piruntime.ErrUnknownSettlement), true},
		{"complete typed UNKNOWN cause", &piruntime.PiWorkerLaunchError{Cause: piruntime.ErrUnknownSettlement, Settlement: stopped}, false},
		{"wrapped complete typed UNKNOWN cause", fmt.Errorf("launch: %w", &piruntime.PiWorkerLaunchError{Cause: piruntime.ErrUnknownSettlement, Settlement: stopped}), false},
		{"joined sibling UNKNOWN after complete typed cause", errors.Join(
			&piruntime.PiWorkerLaunchError{Cause: piruntime.ErrUnknownSettlement, Settlement: stopped},
			piruntime.ErrUnknownSettlement), true},
		{"ordinary prelaunch error", errors.New("worker was not started"), false},
		{"typed STOPPED missing exit", &piruntime.PiWorkerLaunchError{Cause: errors.New("launch rejected"), Settlement: piruntime.Settlement{State: piruntime.SettlementStopped, DescendantsSettled: true}}, true},
		{"typed STOPPED missing descendants", &piruntime.PiWorkerLaunchError{Cause: errors.New("launch rejected"), Settlement: piruntime.Settlement{State: piruntime.SettlementStopped, ExitObserved: true}}, true},
		{"typed STOPPED cleanup error", &piruntime.PiWorkerLaunchError{Cause: errors.New("launch rejected"), Settlement: stopped, CleanupErr: errors.New("cleanup failed")}, true},
		{"typed complete settlement", &piruntime.PiWorkerLaunchError{Cause: errors.New("launch rejected"), Settlement: stopped}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newPublicationRuntimeFixture(t, nil, nil)
			release, err := fixture.runtime.BeginSession(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer release()

			released := releaseProductionSessionAdmissionIfSafe(release, tc.launchErr, false, piruntime.Settlement{}, nil)
			if released == tc.wantHeld {
				t.Fatalf("release decision = %t, want release=%t", released, !tc.wantHeld)
			}
			assertProductionSessionLease(t, fixture.runtime, tc.wantHeld)
			// For retained cases, release only after proving the real lease held
			// admission, so the fixture can finish teardown without shared state.
			if tc.wantHeld {
				release()
			}
		})
	}
}

func TestProductionUnknownWorkerSettlementBlocksGateAndRetainsRealLease(t *testing.T) {
	tests := []struct {
		name       string
		settlement piruntime.Settlement
		closeErr   error
	}{
		{"unknown stop", piruntime.Settlement{State: piruntime.SettlementUnknown}, nil},
		{"unknown close", piruntime.Settlement{State: piruntime.SettlementStopped, ExitObserved: true, DescendantsSettled: true}, piruntime.ErrUnknownSettlement},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newPublicationRuntimeFixture(t, nil, nil)
			release, err := fixture.runtime.BeginSession(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer release()

			attempts := 0
			err = runProductionPublicationAfterWorkerSettlement(tc.settlement, tc.closeErr, func() error {
				attempts++
				return nil
			})
			if !errors.Is(err, ErrProductionWorkerSettlementIncomplete) || attempts != 0 {
				t.Fatalf("publication gate error=%v callback count=%d; want incomplete settlement and no callback", err, attempts)
			}
			if releaseProductionSessionAdmissionIfSafe(release, nil, true, tc.settlement, tc.closeErr) {
				t.Fatal("incompletely settled worker released production admission")
			}
			assertProductionSessionLease(t, fixture.runtime, true)
			// This test cleanup release occurs after the retention assertion; the
			// production defer would intentionally leave this lease for recovery.
			release()
		})
	}
}

func TestProductionUnsafeSessionCloseReachesBinderQuarantine(t *testing.T) {
	evidence := productionLifecycleSnapshot{workerLaunchAttempted: true}
	runner := newProductionCommandRunner(nil)
	var quarantine productionCleanupQuarantineState
	closeCalls := 0
	owners := []func() error{func() error {
		closeCalls++
		return nil
	}}
	binderClose := func() error {
		if retainProductionCleanupIfUnsafe(&quarantine, evidence, runner, owners) {
			return ErrProductionLifecycleUnsettled
		}
		return nil
	}

	err := productionSessionCloseIfUnsafe(evidence, runner, binderClose)
	if !errors.Is(err, ErrProductionLifecycleUnsettled) {
		t.Fatalf("unsafe session close = %v, want unsettled refusal", err)
	}
	if closeCalls != 0 {
		t.Fatalf("unsafe close ran %d owner closers, want none", closeCalls)
	}
	quarantine.Lock()
	held := len(quarantine.holds)
	heldClosers := 0
	if held != 0 {
		heldClosers = len(quarantine.holds[0])
	}
	quarantine.Unlock()
	if held != 1 || heldClosers != len(owners) {
		t.Fatalf("quarantine holds=%d closers=%d, want one hold with %d owner closers", held, heldClosers, len(owners))
	}
}
