//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
)

// TestProductionBinderRequiresContext proves the binder refuses an empty
// profile before touching any trusted operator artifact.
func TestProductionBinderRequiresContext(t *testing.T) {
	_, err := newProductionBinder().BindProductionPiSession(context.Background(), productionServeConfig{}, piruntime.HostProfile{})
	if err == nil {
		t.Fatal("production binder accepted an empty profile")
	}
}

// TestProductionOperatorArtifactsRequireProtectedGitApproval proves a
// provisioned composition cannot proceed without the root-owned Git approval;
// it never falls back to an unapproved system Git or a self-signed approval.
func TestProductionOperatorArtifactsRequireProtectedGitApproval(t *testing.T) {
	template := piruntime.DefaultD09CellTemplate()
	template.CgroupsPath = "/sys/fs/cgroup/tbound"
	_, err := loadProductionOperatorArtifacts(productionServeConfig{Source: "/var/lib/tbound/source"}, template)
	if err == nil || !strings.Contains(err.Error(), "Git approval") {
		t.Fatalf("missing protected Git approval = %v, want a Git-approval refusal", err)
	}
}

// TestProductionOperatorArtifactsRequireAbsoluteSource proves the bounded
// source input must be an absolute protected path.
func TestProductionOperatorArtifactsRequireAbsoluteSource(t *testing.T) {
	template := piruntime.DefaultD09CellTemplate()
	template.CgroupsPath = "/sys/fs/cgroup/tbound"
	if _, err := loadProductionOperatorArtifacts(productionServeConfig{}, template); err == nil {
		t.Fatal("production composition accepted an empty source")
	}
	if _, err := loadProductionOperatorArtifacts(productionServeConfig{Source: "relative/source"}, template); err == nil {
		t.Fatal("production composition accepted a relative source")
	}
	if _, err := loadProductionOperatorArtifacts(productionServeConfig{Source: "/var/lib/tbound/source", Workspace: "/var/lib/tbound/source"}, template); err == nil {
		t.Fatal("production composition accepted the same raw path as immutable source and live workspace")
	}
}

func TestProductionXattrVisibilityRefusesSyntheticCompleteAssertion(t *testing.T) {
	profile := piruntime.DefaultLinuxHostProfileTemplate()
	template := piruntime.DefaultD09CellTemplate()
	attestation, err := productionXattrVisibility(profile, template, nil)
	if !errors.Is(err, ErrProductionXattrVisibilityUnavailable) || attestation.Authority != "" {
		t.Fatalf("production xattr attestation synthesized trust: %+v err=%v", attestation, err)
	}
}

func TestProductionXattrVisibilityRequiresMatchingMeasuredTemplate(t *testing.T) {
	profile := piruntime.DefaultLinuxHostProfileTemplate()
	template := piruntime.DefaultD09CellTemplate()
	template.XattrVisibilityProfileDigest = "sha256:" + strings.Repeat("a", 64)
	if _, err := productionXattrVisibility(profile, template, nil); !errors.Is(err, ErrProductionXattrVisibilityUnavailable) {
		t.Fatalf("unbound measured template xattr profile = %v, want refusal", err)
	}
}

func TestProductionRunDirectoriesAreFreshPrivateAndUnique(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := createPrivateRunDirectory(parent, "session-first")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(first)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("created run directory info=%v err=%v", info, err)
	}
	if _, err := createPrivateRunDirectory(parent, "session-first"); err == nil {
		t.Fatal("production run directory constructor reused an existing session path")
	}
	if _, err := createPrivateRunDirectory(parent, "../escape"); err == nil {
		t.Fatal("production run directory constructor accepted traversal")
	}
}

func TestProductionSourceRequiresProtectedReadonlyFilesystem(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(directory, 0o700)
	if source, err := openProductionQuiescentSource(directory); err == nil {
		_ = source.Close()
		t.Fatal("read-only mode on a writable filesystem was treated as source quiescence")
	} else if !errors.Is(err, ErrProductionSourceNotQuiescent) {
		t.Fatalf("writable-filesystem source refusal = %v", err)
	}
}

// TestProductionBindingsConstructorRequiresVerifiedInputs proves the CLI cannot
// assemble worker bindings without the verified sealed exposure/assets and the
// verified launcher/settler; a caller cannot fabricate a role map.
func TestProductionBindingsConstructorRequiresVerifiedInputs(t *testing.T) {
	if _, _, err := piruntime.BindProductionPiWorkerBindings(context.Background(), piruntime.HostProfile{}, nil, nil, nil, nil); err == nil {
		t.Fatal("bindings constructor accepted unverified inputs")
	}
}

// TestProductionCommandRunnerRefusesLegacyAndUnregistered proves the production
// runner never falls back to the legacy Alpine Podman profile or the
// development sandbox: it refuses Bash until a signed D09 cell runner is bound,
// reports a non-claim-bearing profile label, and refuses an unregistered view or
// an unretained settlement.
func TestProductionCommandRunnerRefusesLegacyAndUnregistered(t *testing.T) {
	runner := newProductionCommandRunner(nil)
	profile := runner.Profile()
	if strings.Contains(profile, "guest-podman") || strings.Contains(profile, "dev-wsl") {
		t.Fatalf("production runner reported a legacy/dev profile: %q", profile)
	}
	if _, _, err := runner.Run(context.Background(), nil, sessionrepo.CommandSpec{}); !errors.Is(err, ErrProductionBashRunnerUnavailable) {
		t.Fatalf("unbound production runner Run = %v, want ErrProductionBashRunnerUnavailable", err)
	}
	if err := runner.VerifySettlement(sessionrepo.CommandSettlement{}, "view-x", "lease-x"); err == nil {
		t.Fatal("production command runner accepted an unretained settlement")
	}
}

// TestProductionCommandRunnerRequiresRegisteredLease proves a bound signed
// runner still refuses a view the authority never registered.
func TestProductionCommandRunnerRequiresRegisteredLease(t *testing.T) {
	runner := newProductionCommandRunner(nil)
	if _, _, err := runner.Run(context.Background(), nil, sessionrepo.CommandSpec{}); !errors.Is(err, ErrProductionBashRunnerUnavailable) {
		t.Fatalf("unbound production runner Run = %v", err)
	}
	if err := runner.RegisterLease("", "lease-x"); err == nil {
		t.Fatal("production runner registered an empty view identity")
	}
}

func validTestProductionLifecycle() productionLifecycleSnapshot {
	return productionLifecycleSnapshot{
		proofDigest: "sha256:" + strings.Repeat("a", 64),
		proofSeq:    1, proofRunID: "run-test", proofRecordHash: "record-hash",
		proofEventID: "production-tcb-proof:run-test", workerLaunchAttempted: true, workerLaunched: true, workerPID: 41, workerClosed: true,
		settlement:   piruntime.Settlement{State: piruntime.SettlementStopped, ExitObserved: true, DescendantsSettled: true},
		taskAdmitted: true, turnCompleted: true, channelsJoined: true,
	}
}

func TestProductionLifecycleRejectsRunningWorker(t *testing.T) {
	evidence := validTestProductionLifecycle()
	evidence.workerClosed = false
	runner := newProductionCommandRunner(nil)
	if err := validateProductionLifecycle(evidence, "run-test", runner); !errors.Is(err, ErrProductionLifecycleUnsettled) {
		t.Fatalf("running worker lifecycle = %v, want unsettled refusal", err)
	}
}

func TestProductionCleanupRetainsScopeAfterUnsettledLaunchAttempt(t *testing.T) {
	evidence := productionLifecycleSnapshot{workerLaunchAttempted: true}
	if productionCleanupSafe(evidence, newProductionCommandRunner(nil)) {
		t.Fatal("failed/unobserved launch attempt was treated as safe to close cgroup/session scope")
	}
}

func TestProductionLifecycleRejectsPendingCommandLease(t *testing.T) {
	evidence := validTestProductionLifecycle()
	runner := newProductionCommandRunner(nil)
	runner.leases = map[string]productionCommandLease{"view-pending": {leaseID: "lease-pending"}}
	if err := validateProductionLifecycle(evidence, "run-test", runner); !errors.Is(err, ErrProductionLifecycleUnsettled) {
		t.Fatalf("pending command lifecycle = %v, want unsettled refusal", err)
	}
}

func TestProductionLifecycleRejectsUnknownAndMissingSettlement(t *testing.T) {
	runner := newProductionCommandRunner(nil)
	for name, mutate := range map[string]func(*productionLifecycleSnapshot){
		"unknown":             func(e *productionLifecycleSnapshot) { e.settlement.State = piruntime.SettlementUnknown },
		"missing descendants": func(e *productionLifecycleSnapshot) { e.settlement.DescendantsSettled = false },
		"missing exit":        func(e *productionLifecycleSnapshot) { e.settlement.ExitObserved = false },
		"missing proof":       func(e *productionLifecycleSnapshot) { e.proofSeq = 0 },
		"wrong run binding":   func(e *productionLifecycleSnapshot) { e.proofRunID = "other-run" },
	} {
		t.Run(name, func(t *testing.T) {
			evidence := validTestProductionLifecycle()
			mutate(&evidence)
			if err := validateProductionLifecycle(evidence, "run-test", runner); !errors.Is(err, ErrProductionLifecycleUnsettled) {
				t.Fatalf("lifecycle = %v, want unsettled refusal", err)
			}
		})
	}
}

func TestProductionBashEvidenceRejectsMissingUnknownAndUnregistered(t *testing.T) {
	operation := sessionrepo.OperationEvidence{Tool: "bash", CommandRunnerProfile: piruntime.D09CommandRunnerProfile, CommandContainmentStatus: "established"}
	for name, mutate := range map[string]func(*sessionrepo.OperationEvidence){
		"missing status":  func(e *sessionrepo.OperationEvidence) { e.CommandContainmentStatus = "" },
		"unknown status":  func(e *sessionrepo.OperationEvidence) { e.CommandContainmentStatus = "unknown" },
		"not established": func(e *sessionrepo.OperationEvidence) { e.CommandContainmentStatus = "not-established" },
		"missing profile": func(e *sessionrepo.OperationEvidence) { e.CommandRunnerProfile = "" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := operation
			mutate(&candidate)
			if err := verifyProductionBashEvidence(context.Background(), profileOnlyCommandRunner{}, candidate); !errors.Is(err, ErrProductionCommandEvidence) {
				t.Fatalf("Bash evidence = %v, want registered signed evidence refusal", err)
			}
		})
	}
	if err := verifyProductionBashEvidence(context.Background(), profileOnlyCommandRunner{}, operation); !errors.Is(err, ErrProductionCommandEvidence) {
		t.Fatalf("profile string without per-command proof verifier = %v, want typed refusal", err)
	}
}

type profileOnlyCommandRunner struct{}

func (profileOnlyCommandRunner) Profile() string { return piruntime.D09CommandRunnerProfile }
func (profileOnlyCommandRunner) Run(context.Context, *sessionrepo.CommandView, sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.New("fixture runner must not execute")
}

func TestProductionCommandRunnerDoesNotAdmitProfileStringAlone(t *testing.T) {
	runner := newProductionCommandRunner(func(string) (sessionrepo.CommandRunner, error) {
		return profileOnlyCommandRunner{}, nil
	})
	if err := runner.RegisterLease("view-test", "lease-test"); !errors.Is(err, ErrProductionBashRunnerUnavailable) {
		t.Fatalf("profile-only runner lease registration = %v, want production refusal", err)
	}
}

func productionProofFixture() (piruntime.HostProfile, piruntime.D09CellTemplate, piruntime.D09TCBProof) {
	template := piruntime.DefaultD09CellTemplate()
	template.ExpectedImageDigest = "sha256:" + strings.Repeat("1", 64)
	template.SeccompProfileDigest = "sha256:" + strings.Repeat("2", 64)
	template.LandlockABI = 8
	template.LandlockRulesetDigest = "sha256:" + strings.Repeat("3", 64)
	template.EntrypointDigest = "sha256:" + strings.Repeat("4", 64)
	template.CPUs, template.PidsMax, template.MemoryMB = "2", 128, 1024
	template.CellUID, template.CellGID = os.Geteuid(), os.Getegid()
	digest, _ := template.Digest()
	profile := piruntime.HostProfile{
		ID: "test-production-profile", Mode: piruntime.ModeProduction,
		Digest:                   "sha256:" + strings.Repeat("5", 64),
		ContainmentProfile:       piruntime.D09ContainmentProfile,
		SettlementProfile:        piruntime.D09SettlementProfile,
		ContainmentProfileDigest: digest,
	}
	proof := piruntime.D09TCBProof{
		Schema: "tbound-pi-entrypoint/v1", UID: os.Geteuid(), GID: os.Getegid(),
		CapEff: "0000000000000000", NoNewPrivs: true, Seccomp: 2,
		LandlockABI: 8, LandlockRulesetDigest: template.LandlockRulesetDigest, LandlockTSYNC: true,
		CgroupV2: true, ContainerName: "cell-test", LeaderPID: 42, ContainerLeaderPID: 1,
		LauncherProcessPID: 41, ConsumerMountsVerified: true, RawSourceFDsClosed: true,
		WorkerMountID: 7, RuntimeMountID: 8, WorkerDevice: 1, WorkerInode: 2, RuntimeDevice: 3, RuntimeInode: 4,
		ImageRef:    "localhost/tbound/pi-cell@" + template.ExpectedImageDigest,
		ImageDigest: template.ExpectedImageDigest, TemplateDigest: digest,
	}
	return profile, template, proof
}

func TestProductionLaunchProofJournalBindsProfileRunAndSequence(t *testing.T) {
	root := privateGovernedTestRoot(t)
	journal, err := audit.Open(filepath.Join(root, "launch-proof.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	profile, template, proof := productionProofFixture()
	evidence := &productionLifecycleEvidence{}
	const runID, sessionID, workflowID, conversationID, generationID = "run-proof-1", "session-proof", "workflow-proof", "conversation-proof", "g0"
	const sourceBindingDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := journalProductionLaunchProof(journal, evidence, runID, sessionID, workflowID, conversationID, generationID, sourceBindingDigest, profile, template, proof.LauncherProcessPID, proof); err != nil {
		t.Fatal(err)
	}
	snapshot := evidence.snapshot()
	snapshot.workerPID = proof.LauncherProcessPID
	if snapshot.proofRunID != "run-proof-1" || snapshot.proofSeq == 0 || !validDigestText(snapshot.proofDigest) {
		t.Fatalf("launch proof journal reference = %+v", snapshot)
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Records) != 1 || trace.Records[0].Event.Kind != "production_tcb_proof" || trace.Records[0].Sequence != snapshot.proofSeq {
		t.Fatalf("launch proof journal records = %+v", trace.Records)
	}
	var event productionLaunchTCBProofEvent
	if err := json.Unmarshal(trace.Records[0].Event.Data, &event); err != nil {
		t.Fatal(err)
	}
	if event.RunID != runID || event.SessionID != sessionID || event.WorkflowID != workflowID ||
		event.ConversationID != conversationID || event.SourceGenerationID != generationID || event.SourceBindingDigest != sourceBindingDigest ||
		event.ProfileID != profile.ID || event.ProfileDigest != profile.Digest || event.ProofDigest != snapshot.proofDigest {
		t.Fatalf("journaled launch proof is not bound to this run/profile: %+v", event)
	}
	if err := verifyProductionLaunchProofJournal(journal, snapshot, profile, template, runID, sessionID, workflowID, conversationID, generationID, sourceBindingDigest); err != nil {
		t.Fatalf("durable proof record did not verify: %v", err)
	}
	if err := verifyProductionLaunchProofJournal(journal, snapshot, profile, template, "other-run", sessionID, workflowID, conversationID, generationID, sourceBindingDigest); !errors.Is(err, ErrProductionLifecycleUnsettled) {
		t.Fatalf("proof record replayed for another run: %v", err)
	}
}

func TestProductionLaunchProofRejectsForeignTemplateAndJournalFailure(t *testing.T) {
	root := privateGovernedTestRoot(t)
	journal, err := audit.Open(filepath.Join(root, "launch-proof-fail.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	profile, template, proof := productionProofFixture()
	evidence := &productionLifecycleEvidence{}
	foreign := proof
	foreign.ImageDigest = "sha256:" + strings.Repeat("f", 64)
	const runID, sessionID, workflowID, conversationID, generationID = "run-proof-2", "session-proof", "workflow-proof", "conversation-proof", "g0"
	const sourceBindingDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := journalProductionLaunchProof(journal, evidence, runID, sessionID, workflowID, conversationID, generationID, sourceBindingDigest, profile, template, proof.LauncherProcessPID, foreign); err == nil {
		t.Fatal("foreign-image TCB proof was journaled")
	}
	if evidence.snapshot().proofSeq != 0 {
		t.Fatal("rejected proof created a launch-proof journal reference")
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := journalProductionLaunchProof(journal, evidence, runID, sessionID, workflowID, conversationID, generationID, sourceBindingDigest, profile, template, proof.LauncherProcessPID, proof); err == nil {
		t.Fatal("closed journal accepted a TCB proof")
	}
	if evidence.snapshot().proofSeq != 0 {
		t.Fatal("failed journal append released a launch-proof reference")
	}
}

func TestProductionLaunchProofRejectsMissingInvalidAndWrongRunReferences(t *testing.T) {
	root := privateGovernedTestRoot(t)
	journal, err := audit.Open(filepath.Join(root, "launch-proof-shape.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	profile, template, proof := productionProofFixture()
	for name, mutate := range map[string]func(*piruntime.D09TCBProof){
		"missing image digest":  func(p *piruntime.D09TCBProof) { p.ImageDigest = "" },
		"wrong landlock digest": func(p *piruntime.D09TCBProof) { p.LandlockRulesetDigest = "sha256:" + strings.Repeat("9", 64) },
		"no tsync":              func(p *piruntime.D09TCBProof) { p.LandlockTSYNC = false },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := proof
			mutate(&candidate)
			if err := journalProductionLaunchProof(journal, &productionLifecycleEvidence{}, "run-invalid", "session", "workflow", "conversation", "g0", "sha256:"+strings.Repeat("a", 64), profile, template, proof.LauncherProcessPID, candidate); err == nil {
				t.Fatal("invalid D09 proof was journaled")
			}
		})
	}
	evidence := &productionLifecycleEvidence{}
	if err := journalProductionLaunchProof(journal, evidence, "run-correct", "session", "workflow", "conversation", "g0", "sha256:"+strings.Repeat("a", 64), profile, template, proof.LauncherProcessPID, proof); err != nil {
		t.Fatal(err)
	}
	if err := validateProductionLifecycle(evidence.snapshot(), "run-other", newProductionCommandRunner(nil)); !errors.Is(err, ErrProductionLifecycleUnsettled) {
		t.Fatalf("proof from a different run was accepted: %v", err)
	}
}

// A route-probe cell (the dr run) reports its probe in the launch proof and never starts Pi, so its launch
// yields no worker. The run still fails, but only after that proof is the run's one production_tcb_proof
// record: the guest-route-direct-runtime producer reads nothing else.
func TestRouteProbeLaunchJournalsItsProofAndStillFails(t *testing.T) {
	root := privateGovernedTestRoot(t)
	journal, err := audit.Open(filepath.Join(root, "route-probe-launch-proof.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	profile, template, proof := productionProofFixture()
	template.RouteProbe = &piruntime.D09RouteProbe{Name: piruntime.D09RouteProbeName, Target: "/home/node/.profile", Control: "/work/src/release.txt"}
	digest, err := template.Digest()
	if err != nil {
		t.Fatal(err)
	}
	profile.ContainmentProfileDigest, proof.TemplateDigest = digest, digest
	proof.RouteProbe = &piruntime.D09RouteProbeResult{Name: template.RouteProbe.Name, Target: template.RouteProbe.Target, Control: template.RouteProbe.Control}
	evidence := &productionLifecycleEvidence{}
	journalLaunchProof := func(launcherPID int, p piruntime.D09TCBProof) error {
		return journalProductionLaunchProof(journal, evidence, "run-dr", "session-dr", "workflow-dr", "conversation-dr", "g0",
			"sha256:"+strings.Repeat("a", 64), profile, template, launcherPID, p)
	}
	settled := piruntime.Settlement{State: piruntime.SettlementStopped, ExitObserved: true, DescendantsSettled: true}

	other := &piruntime.PiWorkerLaunchError{Cause: errors.New("wait for Pi worker readiness: deadline exceeded"), Settlement: settled}
	if err := productionLaunchFailure(other, journalLaunchProof); err == nil {
		t.Fatal("a failed launch did not fail the run")
	}
	probeLaunch := &piruntime.PiWorkerLaunchError{Cause: piruntime.ErrD09RouteProbeLaunched, Settlement: settled,
		RouteProbe: &piruntime.D09RouteProbeLaunch{LauncherPID: proof.LauncherProcessPID, Proof: proof}}
	if err := productionLaunchFailure(probeLaunch, journalLaunchProof); !errors.Is(err, piruntime.ErrD09RouteProbeLaunched) {
		t.Fatalf("route-probe launch ended the run with %v, want a failure caused by ErrD09RouteProbeLaunched", err)
	}

	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Records) != 1 || trace.Records[0].Event.Kind != "production_tcb_proof" {
		t.Fatalf("journal holds %d records, want only the route-probe launch proof", len(trace.Records))
	}
	var event productionLaunchTCBProofEvent
	if err := json.Unmarshal(trace.Records[0].Event.Data, &event); err != nil {
		t.Fatal(err)
	}
	if event.RunID != "run-dr" || event.ProfileDigest != profile.Digest || event.Proof.RouteProbe == nil ||
		event.Proof.RouteProbe.Target != template.RouteProbe.Target || evidence.snapshot().proofSeq != trace.Records[0].Sequence {
		t.Fatalf("journaled launch proof is not the route-probe cell's proof of this run: %+v", event)
	}
}
