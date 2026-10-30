//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"syscall"
	"testing"

	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
)

// receiptInvocationProbe stands in for the D09 cell: inside the Store's real
// RunBash it hands the live CommandView to check, then refuses so nothing commits.
type receiptInvocationProbe struct {
	check func(*sessionrepo.CommandView, sessionrepo.CommandSpec)
}

func (receiptInvocationProbe) Profile() string                    { return piruntime.D09CommandRunnerProfile }
func (receiptInvocationProbe) RegisterLease(string, string) error { return nil }
func (receiptInvocationProbe) VerifySettlement(sessionrepo.CommandSettlement, string, string) error {
	return nil
}
func (p receiptInvocationProbe) Run(_ context.Context, view *sessionrepo.CommandView,
	spec sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	p.check(view, spec)
	return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.New("probe never settles")
}

// realGuestCommandReceipt builds the receipt a rootless Podman/crun D09 cell
// returns for this view. The entrypoint measures /work in the cell's own mount
// namespace, so its mount ID is a different mount from the supervisor's view
// descriptor (F7 guest: host source mount 519, cell consumer mount 706), while
// device and inode are the view's.
func realGuestCommandReceipt(t *testing.T, view *sessionrepo.CommandView, spec sessionrepo.CommandSpec,
	template piruntime.D09CellTemplate, templateDigest string) (piruntime.D09CommandReceipt, sessionrepo.CommandResult, sessionrepo.CommandSettlement) {
	t.Helper()
	source, err := view.MountSource()
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(source.Fd()), &stat); err != nil {
		t.Fatal(err)
	}
	hostMountID, err := productionDescriptorMountID(source.Fd())
	if err != nil {
		t.Fatal(err)
	}
	consumerMountID := hostMountID + 706 - 519
	result := sessionrepo.CommandResult{ExitObserved: true, ExitCode: 0}
	settlement := sessionrepo.CommandSettlement{LeaseID: "lease-real-guest", ViewID: view.ID(), ExitObserved: true,
		ProcessScopeEmpty: true, WritersStopped: true, MountDetached: true,
		EvidenceClass: piruntime.D09CommandRunnerProfile + ":kill-at-settlement"}
	digestOf := func(value any) string {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return digestBytes(encoded)
	}
	proof := piruntime.D09CommandProof{UID: template.CellUID, GID: template.CellGID, CapEff: "0000000000000000",
		NoNewPrivs: true, Seccomp: 2, LandlockABI: 8, LandlockRulesetDigest: template.LandlockRulesetDigest,
		LandlockTSYNC: true, CgroupV2: true, ConsumerMountsVerified: true, RawSourceFDsClosed: true,
		WorkerMountID: consumerMountID, RuntimeMountID: consumerMountID,
		WorkerDevice: uint64(stat.Dev), WorkerInode: stat.Ino, RuntimeDevice: uint64(stat.Dev), RuntimeInode: stat.Ino,
		NoExternalRoutes: true, CommandPath: spec.Executable, CommandDigest: template.CommandExecutableDigest,
		CommandWorkdir: ".", TemplateDigest: templateDigest}
	receipt := piruntime.D09CommandReceipt{LeaseID: settlement.LeaseID, ViewID: view.ID(),
		ImageRef: registeredD09ImageDigestRef(template), ImageDigest: template.ExpectedImageDigest, TemplateDigest: templateDigest,
		CommandSpecDigest: digestOf(spec), ResultDigest: digestOf(result), SettlementDigest: digestOf(settlement),
		Proof: proof, Settlement: settlement}
	return receipt, result, settlement
}

func TestProductionD09ReceiptInvocationAcceptsTheCellsOwnConsumerMount(t *testing.T) {
	profile, template, _ := productionProofFixture()
	template.CommandExecutable = "/bin/bash"
	template.CommandExecutableDigest = "sha256:" + strings.Repeat("6", 64)
	templateDigest, err := template.Digest()
	if err != nil {
		t.Fatal(err)
	}
	profile.ContainmentProfileDigest = templateDigest
	checked := false
	probe := receiptInvocationProbe{check: func(view *sessionrepo.CommandView, spec sessionrepo.CommandSpec) {
		checked = true
		receipt, result, settlement := realGuestCommandReceipt(t, view, spec, template, templateDigest)
		if err := validateProductionD09ReceiptInvocation(view, spec, result, settlement, receipt, profile, template,
			piruntime.D09CellDelta{}); err != nil {
			t.Errorf("real-guest receipt for the authorized view was refused: %v", err)
		}
		receipt.Proof.WorkerInode++
		if err := validateProductionD09ReceiptInvocation(view, spec, result, settlement, receipt, profile, template,
			piruntime.D09CellDelta{}); !errors.Is(err, ErrProductionCommandEvidence) {
			t.Errorf("receipt for another inode was accepted: %v", err)
		}
	}}
	store, g0 := devFixtureRunnerStore(t, probe)
	spec := sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", "sha256sum task.txt > build/task.sha256"},
		DeclaredOutputPaths: []string{"build/task.sha256"}}
	_, _ = store.RunBash(context.Background(), g0, delta.OperationIdentity{Kind: delta.OperationLease, LeaseID: "lease-real-guest"},
		delta.PolicyDecision{ID: "decision-real-guest", Outcome: delta.PolicyAllow,
			PolicyDigest: devFixtureStorePolicy, MetadataPolicyDigest: devFixtureMetadataPolicy}, spec, probe)
	if !checked {
		t.Fatal("Store never handed the probe a live command view")
	}
}
