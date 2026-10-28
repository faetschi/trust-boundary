//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/drainproof"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
	"tbound/supervisor/internal/workspace"
)

// These tests drive the production prepareGovernedRecovery/Activate pair across
// fresh processes. Each process reopens the real SQLite owner, retained Store,
// private Git identity, and provider journals; no test reuses in-memory ledger
// state from the process that created the checkpoint.

const (
	combinedRecoveryPhaseEnv    = "TBOUND_COMBINED_RECOVERY_PHASE"
	combinedRecoveryRootEnv     = "TBOUND_COMBINED_RECOVERY_ROOT"
	combinedRecoveryStepEnv     = "TBOUND_COMBINED_RECOVERY_STEP"
	combinedRecoveryScenarioEnv = "TBOUND_COMBINED_RECOVERY_SCENARIO"
	combinedRecoveryCrashExit   = 74
	combinedRecoveryWorkflow    = "combined-recovery-workflow"
	combinedRecoveryProfile     = "combined-recovery-profile"
	combinedRecoveryTaskID      = "combined-recovery-task"
	combinedRecoveryPrompt      = "Read the pinned fixture file once."
	combinedRecoveryTokens      = uint64(1024)
)

type combinedRecoveryScenario struct {
	maxPreEffectRetries uint64
	maxCleanRestarts    uint64
	deadline            time.Duration
	// withoutCleanupReceipt models a superseded owner whose cleanup scope is
	// absent and has no durable cleanup receipt. Every other scenario seeds one.
	withoutCleanupReceipt bool
	// binderShapedPins pins every retained transcript the way the production recovery binder does: no start
	// generation or tree in the provider config or the profile; the replay derives them.
	binderShapedPins bool
	// idleCrash ends the original process at the idle provider boundary after the host prompt is admitted, with
	// no provider call captured.
	idleCrash bool
	// advanceStore has the original conversation write a file before its last call is captured, so the retained
	// history has a Store generation other than the baseline.
	advanceStore bool
	// productionBash runs one Bash call through the production D09 runner in the original process, pins the production
	// profile and command history, and checks publication after activation (governed_recovery_production_bash_linux_test.go).
	productionBash bool
}

var combinedRecoveryScenarios = map[string]combinedRecoveryScenario{
	"two-cycles":      {maxPreEffectRetries: 2, maxCleanRestarts: 2, deadline: 10 * time.Minute},
	"retry-exhausted": {maxPreEffectRetries: 0, maxCleanRestarts: 2, deadline: 10 * time.Minute},
	"deadline":        {maxPreEffectRetries: 2, maxCleanRestarts: 2, deadline: 8 * time.Second},
	"absent-scope-no-receipt": {maxPreEffectRetries: 2, maxCleanRestarts: 2, deadline: 10 * time.Minute,
		withoutCleanupReceipt: true},
	"binder-pins":               {maxPreEffectRetries: 2, maxCleanRestarts: 2, deadline: 10 * time.Minute, binderShapedPins: true},
	"binder-pins-idle":          {maxPreEffectRetries: 2, maxCleanRestarts: 2, deadline: 10 * time.Minute, binderShapedPins: true, idleCrash: true},
	"binder-pins-advanced":      {maxPreEffectRetries: 2, maxCleanRestarts: 2, deadline: 10 * time.Minute, binderShapedPins: true, advanceStore: true},
	"binder-pins-advanced-idle": {maxPreEffectRetries: 2, maxCleanRestarts: 2, deadline: 10 * time.Minute, binderShapedPins: true, advanceStore: true, idleCrash: true},
	combinedProductionScenario:  {maxPreEffectRetries: 2, maxCleanRestarts: 2, deadline: 10 * time.Minute, productionBash: true},
	combinedResumedScenario:     {maxPreEffectRetries: 2, maxCleanRestarts: 2, deadline: 10 * time.Minute, productionBash: true, binderShapedPins: true},
}

type combinedRecoveryPaths struct {
	root, source, store, admin, recoveryRoot, ownerStore string
	rootDB, ownerDB, storeJournal, admission, evidence   string
	commandJournal                                       string
}

func combinedRecoveryPathsFor(root string) combinedRecoveryPaths {
	return combinedRecoveryPaths{
		root: root, source: filepath.Join(root, "source"), store: filepath.Join(root, "store"),
		admin: filepath.Join(root, "admin"), recoveryRoot: filepath.Join(root, "recovery-root"),
		ownerStore:     filepath.Join(root, "owner-store"),
		rootDB:         filepath.Join(root, "recovery-root", "workflow-root.sqlite"),
		ownerDB:        filepath.Join(root, "owner-store", "workflow.sqlite"),
		storeJournal:   filepath.Join(root, "store-audit.jsonl"),
		admission:      filepath.Join(root, "store", "delta-admission.jsonl"),
		evidence:       filepath.Join(root, "crash-evidence.json"),
		commandJournal: filepath.Join(root, "command-audit.jsonl"),
	}
}

// providerJournal returns the held provider transcript path for a workflow
// ordinal. Ordinal 1 is the original conversation; later ordinals are the
// clean conversations created by each recovery.
func (p combinedRecoveryPaths) providerJournal(ordinal uint64) string {
	if ordinal == 1 {
		return filepath.Join(p.root, "provider-audit.jsonl")
	}
	return filepath.Join(p.root, fmt.Sprintf("provider-audit-ord%d.jsonl", ordinal))
}

// combinedRecoveryScopeRecord is the host's durable record for one owner epoch's
// cleanup scope. ScopeIdentity is the binding recorded when the scope was
// created. CleanupReceipt is the receipt written when that scope was torn down,
// and it is empty when no cleanup completed.
type combinedRecoveryScopeRecord struct {
	ScopeIdentity  string `json:"scope_identity"`
	CleanupReceipt string `json:"cleanup_receipt,omitempty"`
}

const combinedRecoveryScopeRecordsName = "cleanup-scope-records.json"

func combinedRecoveryReadScopeRecords(root string) (map[string]combinedRecoveryScopeRecord, error) {
	records := map[string]combinedRecoveryScopeRecord{}
	data, err := os.ReadFile(filepath.Join(root, combinedRecoveryScopeRecordsName))
	if errors.Is(err, os.ErrNotExist) {
		return records, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	return records, nil
}

// combinedRecoverySeedScope records, when an owner process exits, the scope
// binding for its epoch and, if the scenario models a completed cleanup, the
// matching cleanup receipt. Without a receipt the scope is absent and undrained.
func combinedRecoverySeedScope(t *testing.T, root string, epoch uint64, withReceipt bool) {
	t.Helper()
	records, err := combinedRecoveryReadScopeRecords(root)
	if err != nil {
		t.Fatal(err)
	}
	identity := digestBytes([]byte(fmt.Sprintf("combined-recovery-scope/%s/epoch-%d", combinedRecoveryWorkflow, epoch)))
	record := combinedRecoveryScopeRecord{ScopeIdentity: identity}
	if withReceipt {
		record.CleanupReceipt = identity
	}
	records[strconv.FormatUint(epoch, 10)] = record
	combinedRecoveryWriteJSON(t, filepath.Join(root, combinedRecoveryScopeRecordsName), records)
}

// combinedRecoveryHostObserver is the fake host observation for these fixtures.
// Process facts are read from procfs. The cleanup-scope facts come from the
// durable records above. No cgroup exists in these fixtures, so every superseded
// owner's scope is observed as absent.
type combinedRecoveryHostObserver struct {
	root string
}

func (o combinedRecoveryHostObserver) observe(owner workflowrecovery.OwnerEpochIdentity) (drainproof.OwnerObservation, error) {
	records, err := combinedRecoveryReadScopeRecords(o.root)
	if err != nil {
		return drainproof.OwnerObservation{}, err
	}
	bootData, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return drainproof.OwnerObservation{}, err
	}
	bootID := strings.TrimSpace(string(bootData))
	observedStart, present, err := combinedRecoveryProcessStartTicks(owner.Incarnation.PID)
	if err != nil {
		return drainproof.OwnerObservation{}, err
	}
	live := present && bootID == owner.Incarnation.BootID && observedStart == owner.Incarnation.StartTimeTicks
	record := records[strconv.FormatUint(owner.Epoch, 10)]
	return drainproof.OwnerObservation{
		Owner: owner, BootID: owner.Incarnation.BootID, ObservedBootID: bootID,
		StartTicks: owner.Incarnation.StartTimeTicks, ObservedStartTicks: observedStart,
		ProcessLive: live, Scope: drainproof.ScopeObservation{Absent: true},
		CleanupReceiptIdentity: record.CleanupReceipt, ExpectedScopeIdentity: record.ScopeIdentity,
	}, nil
}

// combinedRecoveryProcessStartTicks reads a PID's start ticks from procfs. It
// reports present=false when no such process exists.
func combinedRecoveryProcessStartTicks(pid int) (uint64, bool, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	closeParen := strings.LastIndexByte(string(data), ')')
	if closeParen < 0 {
		return 0, false, errors.New("malformed process stat")
	}
	fields := strings.Fields(string(data[closeParen+1:]))
	if len(fields) <= 19 {
		return 0, false, errors.New("process stat lacks its start time")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return 0, false, errors.New("process stat has an invalid start time")
	}
	return start, true, nil
}

// combinedRecoveryDrain verifies superseded owners with drainproof over the fake
// host observer. A superseded owner is drained only when its process has exited,
// its scope is absent, and a cleanup receipt matches the recorded scope binding.
type combinedRecoveryDrain struct {
	root string
}

func (d combinedRecoveryDrain) VerifyOwnerCellsDrainedWithProof(ctx context.Context, _ string,
	owners []workflowrecovery.OwnerEpochIdentity) ([]string, error) {
	if ctx == nil || len(owners) == 0 {
		return nil, errors.New("no superseded owner to drain")
	}
	observer := combinedRecoveryHostObserver{root: d.root}
	digests := make([]string, 0, len(owners))
	for _, owner := range owners {
		observation, err := observer.observe(owner)
		if err != nil {
			return nil, err
		}
		proof, err := drainproof.Prove(observation)
		if err != nil {
			return nil, err
		}
		digests = append(digests, proof.Digest)
	}
	return digests, nil
}

type combinedRecoveryGitName struct {
	RepositoryName string `json:"repository_name"`
}

type combinedRecoveryFileEvidence struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type combinedRecoveryResult struct {
	Step               string                    `json:"step"`
	PrepareError       string                    `json:"prepare_error,omitempty"`
	ActivateError      string                    `json:"activate_error,omitempty"`
	Activated          bool                      `json:"activated"`
	Captured           bool                      `json:"captured"`
	RetainedError      string                    `json:"retained_error,omitempty"`
	Deadline           time.Time                 `json:"deadline"`
	OwnerEpoch         uint64                    `json:"owner_epoch"`
	RecoveryGeneration uint64                    `json:"recovery_generation"`
	Counters           workflowrecovery.Counters `json:"counters"`
	Transcripts        int                       `json:"transcripts"`
	StoreEffects       int                       `json:"store_effects"`
	// FenceEpoch is the protected root's accepted fence high-water mark.
	FenceEpoch uint64 `json:"fence_epoch"`
	// StoreBaselineGeneration and StoreTipGeneration are the Store generations the original process ended on.
	StoreBaselineGeneration string `json:"store_baseline_generation,omitempty"`
	StoreTipGeneration      string `json:"store_tip_generation,omitempty"`
	// Publication names the check where the production publication path stopped after activation.
	Publication string `json:"publication,omitempty"`
}

func combinedRecoveryFileDigest(path string, limit int64) (combinedRecoveryFileEvidence, error) {
	file, err := os.Open(path)
	if err != nil {
		return combinedRecoveryFileEvidence{}, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.CopyN(hash, file, limit)
	if err != nil && !errors.Is(err, io.EOF) {
		return combinedRecoveryFileEvidence{}, err
	}
	return combinedRecoveryFileEvidence{Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func combinedRecoveryWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func combinedRecoveryPolicy(t *testing.T, sc combinedRecoveryScenario) gate.Policy {
	t.Helper()
	rules := []gate.Rule{{Tool: "read", Effect: gate.EffectAllow}}
	if sc.advanceStore || sc.productionBash {
		rules = append(rules, gate.Rule{Tool: "write", Effect: gate.EffectAllow})
	}
	if sc.productionBash {
		rules = append(rules, gate.Rule{Tool: "bash", Effect: gate.EffectAllow})
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func combinedRecoveryProviderConfig(conversationID, generationID, treeDigest string) providerbridge.Config {
	return providerbridge.Config{WorkflowID: combinedRecoveryWorkflow, ConversationID: conversationID,
		ProfileID: combinedRecoveryProfile, InitialGenerationID: generationID, InitialTreeDigest: treeDigest,
		SystemPrompt: governedReplaySystem, DeveloperPrompt: governedReplayDeveloper}
}

func combinedRecoveryProviderProfile(generationID string) broker.Profile {
	systemPrompt, developerPrompt := governedReplaySystem, governedReplayDeveloper
	return broker.Profile{ID: combinedRecoveryProfile, Model: providerbridge.ApprovedModel,
		Messages:     providerbridge.InitialPromptMessages(openrouter.Route{}, systemPrompt, developerPrompt),
		ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: governedCallIssuer,
		ResponseIDIssuer: governedResponseIssuer, Generation: generationID}
}

func combinedRecoveryReadCallTurns(prefix string) []providerbridge.DevelopmentProviderTurn {
	return []providerbridge.DevelopmentProviderTurn{{ResponseID: prefix + "-response", CallID: prefix + "-tool-call",
		Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)}}
}

// combinedRecoveryRunChild re-executes this test binary as a fresh process for
// one phase and returns the result that phase recorded before exiting.
func combinedRecoveryRunChild(t *testing.T, root, scenario, phase, step string, wantExit int) combinedRecoveryResult {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestGovernedRecoveryCombinedChildEntry$")
	// This explicit environment contains no inherited credentials or Git config.
	cmd.Env = []string{combinedRecoveryPhaseEnv + "=" + phase, combinedRecoveryRootEnv + "=" + root,
		combinedRecoveryStepEnv + "=" + step, combinedRecoveryScenarioEnv + "=" + scenario,
		"TMPDIR=" + os.TempDir(), "PATH=" + filepath.Dir(gitPath)}
	output, err := cmd.CombinedOutput()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("%s child: %v\n%s", step, err, output)
	}
	var result combinedRecoveryResult
	data, readErr := os.ReadFile(filepath.Join(root, "result-"+step+".json"))
	if readErr == nil {
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
	}
	if code != wantExit {
		t.Fatalf("%s child exit=%d want %d result=%+v\n%s", step, code, wantExit, result, output)
	}
	if readErr != nil {
		t.Fatalf("%s child result: %v\n%s", step, readErr, output)
	}
	return result
}

// combinedRecoveryTemplate carries the request fields that determine the
// retained runtime and identity pins. The original checkpoint is derived from it
// with the same functions recovery uses, so no protected digest is hand-copied.
func combinedRecoveryTemplate(t *testing.T, p combinedRecoveryPaths, owner workflowrecovery.Owner,
	policy gate.Policy, gitPin sessionlaunch.RecoveryGitPin) governedRecoveryRequest {
	t.Helper()
	storeRoot, err := openExistingPrivateRecoveryDirectory(p.store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeRoot.Close() })
	adminRoot, err := openExistingPrivateRecoveryDirectory(p.admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adminRoot.Close() })
	return governedRecoveryRequest{
		Owner: owner, WorkflowID: combinedRecoveryWorkflow,
		StoreRoot: storeRoot, StoreJournalPath: p.storeJournal, AdminRoot: adminRoot,
		StoreOptions: sessionrepo.Options{PolicyDigest: governedReplayStorePolicy,
			MetadataPolicyDigest: governedReplayMetaPolicy, Limits: workspace.DefaultLimits(),
			XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: governedReplayXattrPolicy, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}},
		GitPin: gitPin, GatePolicy: policy,
		ProviderProfileDigest: digestBytes([]byte("combined-recovery-provider-profile/v1")),
		ImageDigest:           digestBytes([]byte("combined-recovery-image/v1")),
		RuntimeProfileDigest:  digestBytes([]byte("combined-recovery-runtime/v1")),
		SystemPrompt:          governedReplaySystem, DeveloperPrompt: governedReplayDeveloper,
		StorePolicyDigest: governedReplayStorePolicy, MetadataPolicyDigest: governedReplayMetaPolicy,
		BashOutputPaths: []string{"task.txt"},
	}
}

func TestGovernedRecoveryCombinedChildEntry(t *testing.T) {
	phase := os.Getenv(combinedRecoveryPhaseEnv)
	if phase == "" {
		return
	}
	root, step, scenario := os.Getenv(combinedRecoveryRootEnv), os.Getenv(combinedRecoveryStepEnv), os.Getenv(combinedRecoveryScenarioEnv)
	if _, ok := combinedRecoveryScenarios[scenario]; !ok || root == "" || step == "" {
		t.Fatal("invalid combined recovery child inputs")
	}
	switch phase {
	case "original":
		combinedRecoveryOriginal(t, root, step, combinedRecoveryScenarios[scenario])
	case "recover", "recover-capture", "recover-write", "recover-bash", "recover-again":
		combinedRecoveryRecover(t, root, step, phase, phase == "recover-capture", combinedRecoveryScenarios[scenario])
	default:
		t.Fatalf("unknown combined recovery phase %q", phase)
	}
}

// combinedRecoveryOriginal creates the retained workflow, starts one provider
// call, records the captured-but-unexecuted state, and crashes before any gate
// decision, Store effect, or provider result.
func combinedRecoveryOriginal(t *testing.T, root, step string, sc combinedRecoveryScenario) {
	t.Helper()
	ctx := context.Background()
	p := combinedRecoveryPathsFor(root)
	for _, dir := range []string{p.source, p.store, p.admin, p.recoveryRoot, p.ownerStore} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(p.source, "task.txt"), []byte("combined recovery fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	storeJournal, err := audit.Open(p.storeJournal)
	if err != nil {
		t.Fatal(err)
	}
	providerPath := p.providerJournal(1)
	providerJournal, err := audit.Open(providerPath)
	if err != nil {
		t.Fatal(err)
	}
	policy := combinedRecoveryPolicy(t, sc)
	var runner combinedCommandRunner = newDevelopmentFixtureCommandRunner()
	var production *productionCommandRunner
	var commandJournal *audit.Journal
	var productionProfile piruntime.HostProfile
	var d09Template piruntime.D09CellTemplate
	if sc.productionBash {
		commandJournal, err = audit.Open(p.commandJournal)
		if err != nil {
			t.Fatal(err)
		}
		productionProfile, d09Template = combinedProductionProfile(t)
		production = combinedProductionRunner(t, commandJournal, productionProfile, d09Template,
			combinedOriginalSession, combinedOriginalConversation)
		runner = production
	}
	authority, err := NewSessionOperationAuthority(storeJournal, providerJournal,
		combinedRecoveryWorkflow, "combined-recovery-conversation-g1", combinedRecoveryProfile,
		policy, governedReplayStorePolicy, governedReplayMetaPolicy, runner, []string{"task.txt"})
	if err != nil {
		t.Fatal(err)
	}
	authority.requireEvidencedBashContainment = sc.productionBash
	storeRoot, err := os.Open(p.store)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := authority.CreateStore(storeRoot, workspace.DefaultLimits(),
		workspace.XattrVisibilityAttestation{ProfileDigest: governedReplayXattrPolicy, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}, nil)
	_ = storeRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	store := managed.Store()
	source, err := os.Open(p.source)
	if err != nil {
		t.Fatal(err)
	}
	g0, err := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true})
	_ = source.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.BindBaseline(store, g0); err != nil {
		t.Fatal(err)
	}
	adminRoot, err := os.Open(p.admin)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sessionlaunch.OpenAdminParent(adminRoot)
	_ = adminRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	git, gitProfile := pinGovernedTestGit(t)
	prepared, err := sessionlaunch.Prepare(ctx, managed, g0, admin, git, gitProfile)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.BindPreparedSession(prepared); err != nil {
		t.Fatal(err)
	}
	durable, err := NewDurableExecutor(DurableExecutorConfig{
		Store: store, Tip: g0, CallIssuer: governedCallIssuer,
		PolicyDigest: governedReplayStorePolicy, MetadataPolicyDigest: governedReplayMetaPolicy,
		BashOutputPaths: []string{"task.txt"}, CommandRunner: runner,
		AfterDurableOperation: authority.SettleDurableOperation,
	})
	if err != nil {
		t.Fatal(err)
	}
	d06Digest, gitIdentity, repositoryName, err := prepared.RecoveryIdentity()
	if err != nil {
		t.Fatal(err)
	}
	combinedRecoveryWriteJSON(t, filepath.Join(root, "git-identity.json"), combinedRecoveryGitName{RepositoryName: repositoryName})

	deadline := time.Now().UTC().Add(sc.deadline).Round(0)
	freshPolicy := &verticalFreshWorkflowPolicy{workflow: combinedRecoveryWorkflow, deadline: deadline}
	boundary, err := workflowrecovery.NewLinuxHostBoundary(p.ownerStore, uint32(os.Geteuid()), verticalNoPriorOwnerDrain{}, freshPolicy)
	if err != nil {
		t.Fatal(err)
	}
	protectedRoot, err := workflowrecovery.CreateFreshProtectedRoot(p.rootDB, p.ownerStore, boundary)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := workflowrecovery.CreateFreshSQLiteOwner(p.ownerDB, protectedRoot)
	if err != nil {
		t.Fatal(err)
	}
	ownerStoreIdentity, err := owner.OwnerStoreIdentity(ctx, combinedRecoveryWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	freshPolicy.root = owner.TrustedRootIdentityDigest()
	freshPolicy.incarnation = owner.OwnerIncarnation()
	freshPolicy.store = ownerStoreIdentity
	if _, err := protectedRoot.NextOwnerEpoch(ctx, combinedRecoveryWorkflow); err != nil {
		t.Fatal(err)
	}

	gitPin := sessionlaunch.RecoveryGitPin{RepositoryName: repositoryName,
		ProvenanceDigest:         sha256DigestFromBarePin(bareWorkflowDigest(d06Digest)),
		RepositoryIdentityDigest: sha256DigestFromBarePin(bareWorkflowDigest(gitIdentity))}
	template := combinedRecoveryTemplate(t, p, owner, policy, gitPin)
	if sc.productionBash {
		// The checkpoint pins the empty command journal: the Bash call is appended after the checkpoint.
		commandIdentity, identityErr := recoveryFileIdentityDigest(p.commandJournal, false)
		if identityErr != nil {
			t.Fatal(identityErr)
		}
		template.ProductionProfile, template.D09Template = productionProfile, d09Template
		template.CommandHistories = []productionRecoveryCommandJournal{{Journal: commandJournal, Path: p.commandJournal,
			Pin:      productionHistoricalCommandPin{IdentityDigest: commandIdentity},
			Sessions: []productionRecoveryCommandSession{{RunID: combinedOriginalSession, SessionID: combinedOriginalSession, ConversationID: combinedOriginalConversation}}}}
	}
	identityPins, err := collectGovernedRecoveryIdentityPins(template)
	if err != nil {
		t.Fatal(err)
	}
	runtimeDigest, err := governedRecoveryRuntimeDigest(template)
	if err != nil {
		t.Fatal(err)
	}
	storeTrace, err := storeJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	admissionAudit, err := audit.Verify(strings.NewReader(string(bundle.AdmissionJournal)))
	if err != nil {
		t.Fatal(err)
	}
	providerIdentity, err := recoveryFileIdentityDigest(providerPath, false)
	if err != nil {
		t.Fatal(err)
	}
	taskDigest := digestBytes([]byte("combined recovery task source identity"))
	limits := workflowrecovery.Limits{MaxPreEffectRetries: sc.maxPreEffectRetries, MaxCleanRestarts: sc.maxCleanRestarts,
		MaxHostCalls: 8, MaxTokens: 8 * combinedRecoveryTokens, Deadline: deadline}
	pins := workflowrecovery.Pins{
		WorkflowID: combinedRecoveryWorkflow, SessionID: "combined-recovery-session-g1",
		RecoveryGeneration: 1, OwnerEpoch: 1,
		OwnerIncarnationDigest: owner.OwnerIncarnationDigest(), RootIdentityDigest: owner.TrustedRootIdentityDigest(),
		StoreIdentityDigest: identityPins.StoreIdentityDigest, AdmissionIdentityDigest: identityPins.AdmissionIdentityDigest,
		CommandIdentityDigest: identityPins.CommandIdentityDigest, AdminIdentityDigest: identityPins.AdminIdentityDigest,
		TaskDigest: bareWorkflowDigest(taskDigest), PromptDigest: bareWorkflowDigest(digestBytes([]byte(combinedRecoveryPrompt))),
		PolicyDigest: barePolicyDigest(policy.Digest()), ProfileDigest: bareWorkflowDigest(combinedPinnedProfileDigest(template)),
		ImageDigest: bareWorkflowDigest(template.ImageDigest), RuntimeDigest: runtimeDigest,
		D06ProvenanceDigest: bareWorkflowDigest(d06Digest), PrivateGitIdentityDigest: bareWorkflowDigest(gitIdentity),
		StoreBaselineGenerationID: g0.ID(), StoreBaselineTreeDigest: strings.TrimPrefix(g0.TreeDigest(), delta.TreeDigestProfile),
		StoreTipGenerationID: g0.ID(), StoreTipTreeDigest: strings.TrimPrefix(g0.TreeDigest(), delta.TreeDigestProfile),
		StoreJournalHead: auditHeadHash(storeTrace.Records), StoreJournalCount: uint64(len(storeTrace.Records)),
		AdmissionJournalHead: auditHeadHash(admissionAudit.Records), AdmissionJournalCount: uint64(len(admissionAudit.Records)),
		ProviderProjectionDigest: workflowrecovery.TranscriptPinsDigest(nil),
		ProviderDisposition:      workflowrecovery.DispositionNone, PendingCallDisposition: workflowrecovery.PendingCallNone,
	}
	pins.RecoveryLineageDigest = workflowrecovery.ComputeRecoveryLineageDigest("", workflowrecovery.Pins{}, pins)
	if _, err := workflowrecovery.InitializeFresh(ctx, owner, pins, limits); err != nil {
		t.Fatalf("initialize the original checkpoint: %v", err)
	}
	ledger, err := workflowrecovery.Open(ctx, owner, workflowrecovery.OpenOptions{
		WorkflowID: combinedRecoveryWorkflow, OwnerEpoch: 1, RetainedPins: pins, Limits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	transcript := workflowrecovery.TranscriptPin{Ordinal: 1, ConversationID: "combined-recovery-conversation-g1",
		JournalIdentityDigest: providerIdentity, Disposition: workflowrecovery.DispositionAwaitingHostPrompt}
	if _, err := ledger.RegisterInitialTranscript(ctx, verticalInitialTranscriptProgress{
		pins: pins, transcript: transcript, store: store, storeJournal: storeJournal,
		providerJournal: providerJournal, baseline: g0,
	}, transcript); err != nil {
		t.Fatalf("register the original provider transcript: %v", err)
	}
	checkpoint, _ := ledger.Snapshot()
	if err := authority.BindWorkflowLedger(ledger, checkpoint.Pins, combinedRecoveryTokens); err != nil {
		t.Fatal(err)
	}
	if err := authority.BindInitialWorkflowPrompt(combinedRecoveryTaskID, combinedRecoveryPrompt, taskDigest); err != nil {
		t.Fatal(err)
	}
	config := combinedRecoveryProviderConfig("combined-recovery-conversation-g1", g0.ID(), g0.TreeDigest())
	conversation, err := authority.newProviderConversation(config, func(config providerbridge.Config,
		journal *audit.Journal) (*providerbridge.Conversation, error) {
		turns := combinedRecoveryReadCallTurns("combined-original")
		if sc.productionBash {
			turns = append([]providerbridge.DevelopmentProviderTurn{combinedProductionBashTurn()}, turns...)
		}
		if sc.advanceStore {
			write := providerbridge.DevelopmentProviderTurn{ResponseID: "combined-original-write-response", CallID: "combined-original-write-call",
				Tool: "write", Arguments: json.RawMessage(`{"path":"task.txt","content":"advanced\n"}`)}
			turns = append([]providerbridge.DevelopmentProviderTurn{write}, turns...)
		}
		return providerbridge.NewDevelopmentOfflineConversation(config, journal, turns)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.AdmitPrompt(conversation, durable, combinedRecoveryTaskID, combinedRecoveryPrompt); err != nil {
		t.Fatal(err)
	}
	if sc.advanceStore {
		step := &workflowVerticalFixture{ctx: ctx, conversation: conversation, policy: policy}
		proposal, decision := ledgerDenyTurn(t, step, "combined-original-write-call", gate.Allow)
		executor, err := providerbridge.NewExecutor(conversation, durable)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := executor.Execute(ctx, proposal, decision); err != nil {
			t.Fatalf("original write did not advance the Store: %v", err)
		}
	}
	if sc.productionBash {
		combinedBashCall(t, root, combinedProductionCall, 1, conversation, durable, policy)
	}
	if !sc.idleCrash {
		turn, err := conversation.NextTurn(ctx)
		if err != nil || turn.ToolCall == nil || turn.ToolCall.Name != "read" {
			t.Fatalf("original provider turn did not capture its read: turn=%+v err=%v", turn, err)
		}
	}
	// The crash marker is the byte-level state that must survive every recovery.
	evidence := map[string]combinedRecoveryFileEvidence{}
	for name, path := range map[string]string{"store-audit.jsonl": p.storeJournal,
		"store/delta-admission.jsonl": p.admission, "provider-audit.jsonl": providerPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := combinedRecoveryFileDigest(path, info.Size())
		if err != nil {
			t.Fatal(err)
		}
		evidence[name] = digest
	}
	combinedRecoveryWriteJSON(t, p.evidence, evidence)
	combinedRecoverySeedScope(t, root, 1, !sc.withoutCleanupReceipt)
	combinedRecoveryWriteJSON(t, filepath.Join(root, "result-"+step+".json"), combinedRecoveryResult{
		Step: step, Deadline: deadline, OwnerEpoch: 1, RecoveryGeneration: 1, Captured: !sc.idleCrash,
		Counters: checkpoint.Counters, Transcripts: 1, StoreEffects: len(storeTrace.Effects),
		StoreBaselineGeneration: g0.ID(), StoreTipGeneration: store.TipGeneration().ID(),
	})
	os.Exit(combinedRecoveryCrashExit)
}

// combinedRecoveryRecover runs one fresh recovery process. It verifies the
// retained crash evidence, performs the production prepare/activate sequence,
// and records the durable checkpoint it observes.
func combinedRecoveryRecover(t *testing.T, root, step, phase string, capture bool, sc combinedRecoveryScenario) {
	t.Helper()
	ctx := context.Background()
	p := combinedRecoveryPathsFor(root)
	boundary, err := workflowrecovery.NewLinuxHostBoundary(p.ownerStore, uint32(os.Geteuid()), combinedRecoveryDrain{root: root}, &verticalFreshWorkflowPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	protectedRoot, err := workflowrecovery.OpenExistingProtectedRoot(p.rootDB, p.ownerStore, boundary)
	if err != nil {
		t.Fatal(err)
	}
	defer protectedRoot.Close()
	owner, err := workflowrecovery.OpenExistingSQLiteOwner(p.ownerDB, protectedRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	before, _, exists, err := owner.LoadCheckpoint(ctx, combinedRecoveryWorkflow)
	if err != nil || !exists {
		// The protected boundary refuses checkpoint loads after the original deadline.
		// The refusal is recorded before any reservation is attempted.
		combinedRecoveryWriteJSON(t, filepath.Join(root, "result-"+step+".json"), combinedRecoveryResult{Step: step,
			PrepareError:  fmt.Sprintf("load retained checkpoint: exists=%v err=%v", exists, err),
			RetainedError: combinedRecoveryRetainedError(root)})
		return
	}
	storeRoot, err := openExistingPrivateRecoveryDirectory(p.store)
	if err != nil {
		t.Fatal(err)
	}
	defer storeRoot.Close()
	storeJournal, err := openExistingRecoveryJournal(p.storeJournal)
	if err != nil {
		t.Fatal(err)
	}
	defer storeJournal.Close()
	storeTrace, err := storeJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	// productionRecoveryGitPin validates the bare checkpoint digests with the
	// sha256:-prefixed validator and so cannot resolve them. The test takes the
	// repository name the original process retained and lets prepare verify the
	// identity against the checkpoint and the Store's private Git provenance.
	var gitName combinedRecoveryGitName
	gitData, err := os.ReadFile(filepath.Join(root, "git-identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gitData, &gitName); err != nil {
		t.Fatal(err)
	}
	gitPin := sessionlaunch.RecoveryGitPin{RepositoryName: gitName.RepositoryName,
		ProvenanceDigest:         sha256DigestFromBarePin(before.Pins.D06ProvenanceDigest),
		RepositoryIdentityDigest: sha256DigestFromBarePin(before.Pins.PrivateGitIdentityDigest)}
	policy := combinedRecoveryPolicy(t, sc)
	adminRoot, err := openExistingPrivateRecoveryDirectory(p.admin)
	if err != nil {
		t.Fatal(err)
	}
	defer adminRoot.Close()
	g0ID, g0Tree := before.Pins.StoreBaselineGenerationID, delta.TreeDigestProfile+before.Pins.StoreBaselineTreeDigest
	transcripts := make([]GovernedRecoveryTranscript, 0, len(before.Pins.ProviderTranscripts))
	for _, pin := range before.Pins.ProviderTranscripts {
		path := p.providerJournal(pin.Ordinal)
		journal, err := openExistingRecoveryJournal(path)
		if err != nil {
			t.Fatal(err)
		}
		defer journal.Close()
		identity, err := recoveryFileIdentityDigest(path, false)
		if err != nil {
			t.Fatal(err)
		}
		providerConfig, replayProfile := combinedRecoveryProviderConfig(pin.ConversationID, g0ID, g0Tree), combinedRecoveryProviderProfile(g0ID)
		if sc.binderShapedPins {
			providerConfig.InitialGenerationID, providerConfig.InitialTreeDigest, replayProfile.Generation = "", "", ""
		}
		replay := GovernedReplayPins{
			Provider: providerbridge.RecoveryPins{Config: providerConfig,
				Model: providerbridge.ApprovedModel, Policy: policy},
			Profile:           replayProfile,
			StorePolicyDigest: governedReplayStorePolicy, MetadataPolicyDigest: governedReplayMetaPolicy,
			BashOutputPaths: []string{"task.txt"},
		}
		transcripts = append(transcripts, GovernedRecoveryTranscript{Journal: journal, JournalPath: path,
			IdentityDigest: identity, Pin: pin, Replay: replay})
	}
	gen := before.Pins.RecoveryGeneration + 1
	newConversation := fmt.Sprintf("%s-conversation-g%d", combinedRecoveryWorkflow, gen)
	newSession := fmt.Sprintf("%s-session-g%d", combinedRecoveryWorkflow, gen)
	newPath := p.providerJournal(uint64(len(before.Pins.ProviderTranscripts)) + 1)
	freshJournal, err := openNewRecoveryJournal(newPath)
	if err != nil {
		t.Fatal(err)
	}
	defer freshJournal.Close()
	freshIdentity, err := recoveryFileIdentityDigest(newPath, false)
	if err != nil {
		t.Fatal(err)
	}
	var runner combinedCommandRunner = newDevelopmentFixtureCommandRunner()
	var production *productionCommandRunner
	var commandJournal *audit.Journal
	var commandHistories []productionRecoveryCommandJournal
	var productionProfile piruntime.HostProfile
	var d09Template piruntime.D09CellTemplate
	if sc.productionBash {
		productionProfile, d09Template = combinedProductionProfile(t)
		commandJournal, commandHistories = combinedRecoveryCommandHistory(t, p.commandJournal, combinedPreviousSessions(gen))
		production = combinedProductionRunner(t, commandJournal, productionProfile, d09Template, newSession, newConversation)
		runner = production
	}
	turns := combinedRecoveryReadCallTurns(fmt.Sprintf("combined-recovered-g%d", gen))
	if phase == "recover-write" {
		turns = []providerbridge.DevelopmentProviderTurn{{ResponseID: "resumed-write-response", CallID: "resumed-write-call",
			Tool: "write", Arguments: json.RawMessage(`{"path":"task.txt","content":"resumed\n"}`)}}
	}
	if phase == "recover-bash" {
		turns = append([]providerbridge.DevelopmentProviderTurn{combinedResumedBashTurn()}, turns...)
	}
	providerProfile := combinedRecoveryProviderProfile("")
	request := governedRecoveryRequest{
		Owner: owner, WorkflowID: combinedRecoveryWorkflow, NewOwnerEpoch: before.Pins.OwnerEpoch + 1,
		NewSessionID: fmt.Sprintf("%s-session-g%d", combinedRecoveryWorkflow, gen), NewConversationID: newConversation,
		Limits: before.Limits, StoreRoot: storeRoot, StoreJournalPath: p.storeJournal,
		StoreOptions: sessionrepo.Options{PolicyDigest: governedReplayStorePolicy, MetadataPolicyDigest: governedReplayMetaPolicy,
			Limits: workspace.DefaultLimits(), XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: governedReplayXattrPolicy, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted},
			Journal: storeJournal},
		AdminRoot: adminRoot, GitPin: gitPin, Transcripts: transcripts,
		ProviderProfile: providerProfile, ProviderProfileDigest: digestBytes([]byte("combined-recovery-provider-profile/v1")),
		ImageDigest: digestBytes([]byte("combined-recovery-image/v1")), RuntimeProfileDigest: digestBytes([]byte("combined-recovery-runtime/v1")),
		SystemPrompt: governedReplaySystem, DeveloperPrompt: governedReplayDeveloper,
		StorePolicyDigest: governedReplayStorePolicy, MetadataPolicyDigest: governedReplayMetaPolicy,
		BashOutputPaths: []string{"task.txt"}, GatePolicy: policy,
		CommandRunner: runner, GovernedCommandRunner: runner, ProviderTokenUpperBound: combinedRecoveryTokens,
		FreshProviderJournal: freshJournal, FreshProviderJournalPath: newPath, FreshJournalIdentity: freshIdentity,
		ConversationFactory: func(config providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
			return providerbridge.NewDevelopmentOfflineConversation(config, journal, turns)
		},
	}
	result := combinedRecoveryResult{Step: step, Deadline: before.Limits.Deadline, Transcripts: len(transcripts),
		StoreEffects: len(storeTrace.Effects)}
	if sc.productionBash {
		request.ProductionProfile, request.D09Template = productionProfile, d09Template
		request.CommandHistories, request.RequireEvidencedBash = commandHistories, true
	}
	candidate, err := prepareGovernedRecovery(ctx, request)
	if err != nil {
		result.PrepareError = err.Error()
	} else {
		session, activateErr := activateProductionRecoverySession(ctx, candidate)
		if activateErr != nil {
			result.ActivateError = activateErr.Error()
			_ = candidate.Close()
		} else {
			result.Activated = true
			if sc.advanceStore {
				combinedRecoveryCheckRetainedHistory(t, session, candidate)
				combinedRecoveryCheckPublicationOrigins(t, session)
			}
			// This process now owns the new epoch. Its scope record is written when
			// it exits, so the next recovery can observe it.
			combinedRecoverySeedScope(t, root, request.NewOwnerEpoch, !sc.withoutCleanupReceipt)
			if err := session.Authority.AdmitPrompt(session.Conversation, session.Durable,
				session.RecoveryPromptTask, session.RecoveryPrompt); err != nil {
				t.Fatal(err)
			}
			if phase == "recover-write" {
				step := &workflowVerticalFixture{ctx: ctx, conversation: session.Conversation, policy: policy}
				proposal, decision := ledgerDenyTurn(t, step, "resumed-write-call", gate.Allow)
				executor, err := providerbridge.NewExecutor(session.Conversation, session.Durable)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := executor.Execute(ctx, proposal, decision); err != nil {
					t.Fatalf("resumed write: %v", err)
				}
				chain, err := session.Store.Verify()
				if err != nil || chain.TransitionCount != 2 {
					t.Fatalf("resumed chain: %+v err=%v", chain, err)
				}
				evidence, err := session.Store.EvidenceBundle()
				if err != nil {
					t.Fatal(err)
				}
				fresh := evidence.ApprovedDeltaLedger[len(evidence.ApprovedDeltaLedger)-1]
				if err := session.Authority.VerifyTransition(fresh.Decision, governedTransitionBinding(fresh)); err != nil {
					t.Fatalf("activated authority refused its new transition: %v", err)
				}
				if err := session.Authority.VerifyOrigin(evidence, fresh); err != nil {
					t.Fatalf("activated authority refused the origin of its new transition: %v", err)
				}
			}
			if phase == "recover-bash" {
				combinedBashCall(t, root, combinedResumedCall, gen, session.Conversation, session.Durable, policy)
			}
			if sc.productionBash {
				result.Publication = combinedRecoveryPublish(t, root, session, production, productionProfile, d09Template,
					newSession, newConversation)
				switch phase {
				case "recover":
					combinedRecoveryTerminalOrigin(t, root, session, 1)
					combinedRecoveryCommandRefusals(t, root, session, production, commandJournal)
				case "recover-bash":
					combinedRecoveryTerminalOrigin(t, root, session, gen)
				case "recover-again":
					combinedRecoveryTerminalOrigin(t, root, session, gen-1)
				}
			}
			if capture {
				turn, err := session.Conversation.NextTurn(ctx)
				if err != nil || turn.ToolCall == nil {
					t.Fatalf("recovered provider turn did not capture its read: turn=%+v err=%v", turn, err)
				}
				result.Captured = true
			}
			// The captured-call crash below exits with os.Exit, which skips this close on purpose.
			defer func() {
				if err := session.Close(); err != nil {
					t.Errorf("close recovered session: %v", err)
				}
			}()
		}
	}
	// Observe the durable checkpoint and the retained crash evidence from this fresh process.
	result.RetainedError = combinedRecoveryRetainedError(root)
	after, _, _, err := owner.LoadCheckpoint(ctx, combinedRecoveryWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	result.OwnerEpoch, result.RecoveryGeneration, result.Counters = after.Pins.OwnerEpoch, after.Pins.RecoveryGeneration, after.Counters
	next, err := protectedRoot.NextOwnerEpoch(ctx, combinedRecoveryWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	result.FenceEpoch = next - 1
	combinedRecoveryWriteJSON(t, filepath.Join(root, "result-"+step+".json"), result)
	if result.Captured {
		os.Exit(combinedRecoveryCrashExit)
	}
}

// combinedRecoveryRetainedError checks that every crash-time evidence prefix is
// still byte-identical. Recovery may append its own records, never rewrite these.
func combinedRecoveryRetainedError(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "crash-evidence.json"))
	if err != nil {
		return err.Error()
	}
	var evidence map[string]combinedRecoveryFileEvidence
	if err := json.Unmarshal(data, &evidence); err != nil {
		return err.Error()
	}
	for name, want := range evidence {
		got, digestErr := combinedRecoveryFileDigest(filepath.Join(root, name), want.Size)
		if digestErr != nil || got != want {
			return fmt.Sprintf("%s retained prefix changed (err=%v)", name, digestErr)
		}
	}
	return ""
}

func combinedRecoveryRequireCounters(t *testing.T, got combinedRecoveryResult, want workflowrecovery.Counters) {
	t.Helper()
	if got.Counters != want {
		t.Fatalf("%s durable counters = %+v, want %+v (prepare=%q activate=%q)",
			got.Step, got.Counters, want, got.PrepareError, got.ActivateError)
	}
}

// TestGovernedRecoveryCombinedRestartRetryTwoCyclesExhaustsFreshProcess exercises
// the production prepare/activate path for two retired provider calls and then a
// refused third recovery. Each recovery that retires a captured call must charge
// exactly one clean restart and exactly one pre-effect retry, and the refused
// recovery must charge nothing.
func TestGovernedRecoveryCombinedRestartRetryTwoCyclesExhaustsFreshProcess(t *testing.T) {
	root := privateGovernedTestRoot(t)
	const scenario = "two-cycles"
	original := combinedRecoveryRunChild(t, root, scenario, "original", "o", combinedRecoveryCrashExit)
	first := combinedRecoveryRunChild(t, root, scenario, "recover-capture", "r1", combinedRecoveryCrashExit)
	if first.PrepareError != "" || first.ActivateError != "" || !first.Activated || !first.Captured {
		t.Fatalf("first recovery did not activate and capture a new call: prepare=%q activate=%q captured=%v",
			first.PrepareError, first.ActivateError, first.Captured)
	}
	if first.StoreEffects != original.StoreEffects {
		t.Fatalf("first recovery changed Store effect history: before=%d after=%d", original.StoreEffects, first.StoreEffects)
	}
	combinedRecoveryRequireCounters(t, first, workflowrecovery.Counters{PreEffectRetries: 1, CleanRestarts: 1,
		HostCalls: 2, Tokens: 2 * combinedRecoveryTokens})
	if first.RetainedError != "" {
		t.Fatalf("first recovery changed retained crash evidence: %s", first.RetainedError)
	}
	second := combinedRecoveryRunChild(t, root, scenario, "recover-capture", "r2", combinedRecoveryCrashExit)
	if second.PrepareError != "" || second.ActivateError != "" || !second.Activated || !second.Captured {
		t.Fatalf("second recovery did not activate and capture a new call: prepare=%q activate=%q captured=%v",
			second.PrepareError, second.ActivateError, second.Captured)
	}
	combinedRecoveryRequireCounters(t, second, workflowrecovery.Counters{PreEffectRetries: 2, CleanRestarts: 2,
		HostCalls: 3, Tokens: 3 * combinedRecoveryTokens})
	third := combinedRecoveryRunChild(t, root, scenario, "recover", "r3", 0)
	if third.PrepareError == "" || third.Activated {
		t.Fatalf("third recovery was not refused by the exhausted restart allowance: prepare=%q activate=%q",
			third.PrepareError, third.ActivateError)
	}
	if !strings.Contains(third.PrepareError, workflowrecovery.ErrExhausted.Error()) {
		t.Fatalf("third recovery refusal = %q, want exhausted restart allowance", third.PrepareError)
	}
	combinedRecoveryRequireCounters(t, third, second.Counters)
}

// TestGovernedRecoveryDirectoryPathIdentityMatchesHeldDescriptor pins the
// identity contract that every production recovery open depends on: a retained
// private directory opened by path must compare equal to the held descriptor.
func TestGovernedRecoveryDirectoryPathIdentityMatchesHeldDescriptor(t *testing.T) {
	dir := filepath.Join(privateGovernedTestRoot(t), "private-store")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	pathIdentity, err := recoveryFileIdentityDigest(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	file, err := openExistingPrivateRecoveryDirectory(dir)
	if err != nil {
		t.Fatalf("open retained private directory by path: %v", err)
	}
	defer file.Close()
	descriptorIdentity, err := recoveryDirectoryIdentityDigest(file)
	if err != nil {
		t.Fatal(err)
	}
	if pathIdentity != descriptorIdentity {
		t.Fatalf("path identity %s differs from held descriptor identity %s", pathIdentity, descriptorIdentity)
	}
}

// TestGovernedRecoveryCombinedRetryExhaustionChargesNoRestart checks atomicity:
// when the workflow-wide pre-effect retry allowance is exhausted, recovery must
// refuse before consuming a clean restart or activating a new session.
func TestGovernedRecoveryCombinedRetryExhaustionChargesNoRestart(t *testing.T) {
	root := privateGovernedTestRoot(t)
	const scenario = "retry-exhausted"
	combinedRecoveryRunChild(t, root, scenario, "original", "o", combinedRecoveryCrashExit)
	result := combinedRecoveryRunChild(t, root, scenario, "recover", "r1", 0)
	if result.Activated || (result.PrepareError == "" && result.ActivateError == "") {
		t.Fatalf("recovery activated with the retry allowance exhausted: prepare=%q activate=%q",
			result.PrepareError, result.ActivateError)
	}
	combinedRecoveryRequireCounters(t, result, workflowrecovery.Counters{HostCalls: 1, Tokens: combinedRecoveryTokens})
}

// TestGovernedRecoveryCombinedAbsentScopeWithoutReceiptRefusesRecovery is the
// negative twin of the restart scenarios. The superseded owner's process has
// exited and its cleanup scope is absent, but no cleanup receipt was recorded.
// That owner is not drained, so recovery must be refused by the owner fence and
// must not acquire the next owner epoch.
func TestGovernedRecoveryCombinedAbsentScopeWithoutReceiptRefusesRecovery(t *testing.T) {
	root := privateGovernedTestRoot(t)
	const scenario = "absent-scope-no-receipt"
	combinedRecoveryRunChild(t, root, scenario, "original", "o", combinedRecoveryCrashExit)
	// The capturing phase scripts a provider turn, so an accepted drain would
	// activate and capture. A refusal must therefore be the fence, not a
	// fixture shortfall.
	result := combinedRecoveryRunChild(t, root, scenario, "recover-capture", "r1", 0)
	if result.Activated || (result.PrepareError == "" && result.ActivateError == "") {
		t.Fatalf("recovery activated over an undrained superseded owner: prepare=%q activate=%q",
			result.PrepareError, result.ActivateError)
	}
	refusal := result.PrepareError + result.ActivateError
	if !strings.Contains(refusal, workflowrecovery.ErrOwnerFence.Error()) {
		t.Fatalf("recovery refusal = %q, want the owner fence refusal", refusal)
	}
	if result.OwnerEpoch != 1 || result.FenceEpoch != 1 {
		t.Fatalf("refused recovery moved the owner fence: checkpoint epoch=%d fence epoch=%d, want 1 and 1",
			result.OwnerEpoch, result.FenceEpoch)
	}
}

// TestGovernedRecoveryCombinedDeadlineChargesNothing checks that recovery after
// the original deadline is refused by the protected boundary before any ledger
// reservation, and that the retained crash evidence is unchanged. The durable
// counters are unreadable after the deadline, so the refusal path is the proof.
func TestGovernedRecoveryCombinedDeadlineChargesNothing(t *testing.T) {
	root := privateGovernedTestRoot(t)
	const scenario = "deadline"
	original := combinedRecoveryRunChild(t, root, scenario, "original", "o", combinedRecoveryCrashExit)
	if wait := time.Until(original.Deadline) + 500*time.Millisecond; wait > 0 {
		time.Sleep(wait)
	}
	result := combinedRecoveryRunChild(t, root, scenario, "recover", "r1", 0)
	if result.Activated || !strings.HasPrefix(result.PrepareError, "load retained checkpoint:") ||
		!strings.Contains(result.PrepareError, "deadline") {
		t.Fatalf("recovery after the workflow deadline was not refused before reservation: prepare=%q activate=%q",
			result.PrepareError, result.ActivateError)
	}
	if result.RetainedError != "" {
		t.Fatalf("deadline refusal changed retained crash evidence: %s", result.RetainedError)
	}
}

// The production recovery binder pins each retained transcript without its start generation and tree (see
// production_recovery_binder_linux.go); the replay derives them. Retiring the last transcript's lineage must
// use the pins the replay validated, so a resume that needs retirement is not refused for the binder's own pins.
func TestGovernedRecoveryCombinedRetiresACapturedCallUnderBinderShapedPins(t *testing.T) {
	root := privateGovernedTestRoot(t)
	const scenario = "binder-pins"
	combinedRecoveryRunChild(t, root, scenario, "original", "o", combinedRecoveryCrashExit)
	first := combinedRecoveryRunChild(t, root, scenario, "recover-capture", "r1", combinedRecoveryCrashExit)
	if first.PrepareError != "" || first.ActivateError != "" || !first.Activated || !first.Captured {
		t.Fatalf("recovery under binder-shaped pins did not retire the captured call: prepare=%q activate=%q captured=%v",
			first.PrepareError, first.ActivateError, first.Captured)
	}
	combinedRecoveryRequireCounters(t, first, workflowrecovery.Counters{PreEffectRetries: 1, CleanRestarts: 1,
		HostCalls: 2, Tokens: 2 * combinedRecoveryTokens})
	// Two retained transcripts now: the last one is the recovered conversation.
	second := combinedRecoveryRunChild(t, root, scenario, "recover-capture", "r2", combinedRecoveryCrashExit)
	if second.PrepareError != "" || second.ActivateError != "" || !second.Activated || !second.Captured || second.Transcripts != 2 {
		t.Fatalf("second recovery under binder-shaped pins: prepare=%q activate=%q captured=%v transcripts=%d",
			second.PrepareError, second.ActivateError, second.Captured, second.Transcripts)
	}
	combinedRecoveryRequireCounters(t, second, workflowrecovery.Counters{PreEffectRetries: 2, CleanRestarts: 2,
		HostCalls: 3, Tokens: 3 * combinedRecoveryTokens})
}

// The same for a crash at the idle provider boundary, which retires the lineage without a captured call.
func TestGovernedRecoveryCombinedRetiresAnIdleLineageUnderBinderShapedPins(t *testing.T) {
	root := privateGovernedTestRoot(t)
	const scenario = "binder-pins-idle"
	combinedRecoveryRunChild(t, root, scenario, "original", "o", combinedRecoveryCrashExit)
	first := combinedRecoveryRunChild(t, root, scenario, "recover-capture", "r1", combinedRecoveryCrashExit)
	if first.PrepareError != "" || first.ActivateError != "" || !first.Activated || !first.Captured {
		t.Fatalf("recovery under binder-shaped pins did not retire the idle lineage: prepare=%q activate=%q captured=%v",
			first.PrepareError, first.ActivateError, first.Captured)
	}
	if first.Counters.CleanRestarts != 1 || first.Counters.PreEffectRetries != 0 {
		t.Fatalf("idle retirement must charge one clean restart and no pre-effect retry: %+v", first.Counters)
	}
}

// A conversation that changed the Store before its last call was captured replays through a released result and
// a promoted transition, so retiring that call replays the journal with a verifier that has already followed it
// to the new tip. Each retirement pass must start from the pinned start. Only prepareGovernedRecovery is
// driven here through activation.
func TestGovernedRecoveryCombinedRetiresACallCapturedAfterTheStoreAdvanced(t *testing.T) {
	root := privateGovernedTestRoot(t)
	const scenario = "binder-pins-advanced"
	combinedRecoveryRunChild(t, root, scenario, "original", "o", combinedRecoveryCrashExit)
	first := combinedRecoveryRunChild(t, root, scenario, "recover", "r1", 0)
	if first.StoreEffects < 2 {
		t.Fatalf("the retained history did not advance the Store: %d Store effects", first.StoreEffects)
	}
	if first.PrepareError != "" || first.ActivateError != "" || !first.Activated {
		t.Fatalf("recovery could not activate after the Store advanced: prepare=%q activate=%q", first.PrepareError, first.ActivateError)
	}
	if first.Counters.CleanRestarts != 1 || first.Counters.PreEffectRetries != 1 {
		t.Fatalf("retiring the captured call must charge one clean restart and one pre-effect retry: %+v", first.Counters)
	}
}

// A recovery whose conversation starts after an earlier conversation promoted a write must retire it from that
// start, not from the Store baseline. Both recoveries activate and admit the real clean recovery prompt.
func TestGovernedRecoveryCombinedRetiresTheLastTranscriptFromItsOwnStartAboveTheBaseline(t *testing.T) {
	root := privateGovernedTestRoot(t)
	const scenario = "binder-pins-advanced-idle"
	original := combinedRecoveryRunChild(t, root, scenario, "original", "o", combinedRecoveryCrashExit)
	if original.StoreTipGeneration == "" || original.StoreTipGeneration == original.StoreBaselineGeneration {
		t.Fatalf("the original process did not promote a write: baseline=%q tip=%q", original.StoreBaselineGeneration, original.StoreTipGeneration)
	}
	first := combinedRecoveryRunChild(t, root, scenario, "recover", "r1", 0)
	if first.PrepareError != "" || first.ActivateError != "" || !first.Activated || first.Transcripts != 1 {
		t.Fatalf("first recovery: prepare=%q activate=%q transcripts=%d", first.PrepareError, first.ActivateError, first.Transcripts)
	}
	second := combinedRecoveryRunChild(t, root, scenario, "recover", "r2", 0)
	if second.PrepareError != "" || second.ActivateError != "" || !second.Activated || second.Transcripts != 2 {
		t.Fatalf("second recovery could not retire the conversation that starts above the baseline: prepare=%q transcripts=%d",
			second.PrepareError, second.Transcripts)
	}
	if second.Counters.CleanRestarts != 2 || second.Counters.PreEffectRetries != 0 {
		t.Fatalf("second recovery counters = %+v, want two clean restarts and no pre-effect retry", second.Counters)
	}
}

func TestGovernedRecoveryCombinedPromotesANewWriteAfterAPromotedWrite(t *testing.T) {
	root := privateGovernedTestRoot(t)
	const scenario = "binder-pins-advanced"
	combinedRecoveryRunChild(t, root, scenario, "original", "o", combinedRecoveryCrashExit)
	resumed := combinedRecoveryRunChild(t, root, scenario, "recover-write", "r1", 0)
	if resumed.PrepareError != "" || resumed.ActivateError != "" || !resumed.Activated {
		t.Fatalf("resume after promoted write: prepare=%q activate=%q", resumed.PrepareError, resumed.ActivateError)
	}
}

func TestGovernedRecoveryCombinedRecoversAgainAfterRetiringACapturedCall(t *testing.T) {
	root := privateGovernedTestRoot(t)
	const scenario = "binder-pins"
	combinedRecoveryRunChild(t, root, scenario, "original", "o", combinedRecoveryCrashExit)
	first := combinedRecoveryRunChild(t, root, scenario, "recover", "r1", 0)
	if first.PrepareError != "" || first.ActivateError != "" || !first.Activated {
		t.Fatalf("first recovery: %+v", first)
	}
	second := combinedRecoveryRunChild(t, root, scenario, "recover", "r2", 0)
	if second.PrepareError != "" || second.ActivateError != "" || !second.Activated {
		t.Fatalf("second recovery: %+v", second)
	}
	combinedRecoveryRequireCounters(t, second, workflowrecovery.Counters{PreEffectRetries: 1, CleanRestarts: 2,
		HostCalls: 1, Tokens: combinedRecoveryTokens})
}

func TestProductionRecoveryBinderBindsPreparedSessionOnce(t *testing.T) {
	root := privateGovernedTestRoot(t)
	combinedRecoveryRunChild(t, root, "binder-pins-idle", "original", "o", combinedRecoveryCrashExit)
	resumed := combinedRecoveryRunChild(t, root, "binder-pins-idle", "recover", "r1", 0)
	if resumed.PrepareError != "" || resumed.ActivateError != "" || !resumed.Activated {
		t.Fatalf("production binder handoff: prepare=%q activate=%q", resumed.PrepareError, resumed.ActivateError)
	}
}

// The candidate and all of its evidence were reopened by this child process.
func combinedRecoveryCheckRetainedHistory(t *testing.T, session *governedRecoverySession, candidate *governedRecoveryCandidate) {
	t.Helper()
	bundle, err := session.Store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	original := bundle.ApprovedDeltaLedger[0]
	binding := governedTransitionBinding(original)
	if err := session.Authority.VerifyDecision(original.Decision, binding); err == nil {
		t.Fatal("fresh authority accepted historical decision")
	}
	if err := session.Historical.verifyDecision(original.Decision, binding, session.Authority.VerifyDecision); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"ID", "Tool", "ArgumentDigest", "ViewID", "ExecutionContextDigest", "InputGeneration", "OutputGeneration", "InputTreeDigest", "OutputTreeDigest"} {
		t.Run("retained-binding-"+field, func(t *testing.T) {
			changed := binding
			value := reflect.ValueOf(&changed).Elem().FieldByName(field)
			value.SetString(value.String() + "-changed")
			if err := session.Historical.verifyDecision(original.Decision, changed, func(delta.PolicyDecision, delta.TransitionBinding) error {
				t.Fatal("historical mismatch reached live verifier")
				return nil
			}); err == nil {
				t.Fatalf("accepted changed historical %s", field)
			}
		})
	}
	changed := binding
	changed.Changes = append([]delta.ObjectChange(nil), binding.Changes...)
	changed.Changes[0].Path += "-changed"
	if err := session.Historical.verifyDecision(original.Decision, changed, session.Authority.VerifyDecision); err == nil {
		t.Fatal("accepted changed historical change")
	}
	changed = binding
	changed.Operation.CallID += "-changed"
	if err := session.Historical.verifyDecision(original.Decision, changed, session.Authority.VerifyDecision); err == nil {
		t.Fatal("accepted changed historical operation")
	}
	decision := original.Decision
	decision.ID += "-changed"
	if err := session.Historical.verifyDecision(decision, binding, session.Authority.VerifyDecision); err == nil {
		t.Fatal("accepted changed historical decision")
	}
	changed = binding
	changed.Sequence++
	if err := session.Historical.verifyDecision(original.Decision, changed, session.Authority.VerifyDecision); err == nil {
		t.Fatal("new transition borrowed historical authorization")
	}
	changed.Sequence = 0
	if err := session.Historical.verifyDecision(original.Decision, changed, session.Authority.VerifyDecision); err == nil {
		t.Fatal("zero transition borrowed historical authorization")
	}
	// The prefix comes from the pre-activation journal the historical verifier held, not the D06 tail activation appended.
	trace := candidate.historical.initialTrace
	journal, err := marshalGovernedAuditRecords(trace.Records)
	if err != nil {
		t.Fatal(err)
	}
	ledger := make([]delta.Transition, len(bundle.ApprovedDeltaLedger))
	for i, transition := range bundle.ApprovedDeltaLedger {
		transition.Changes = append([]delta.ObjectChange(nil), transition.Changes...)
		ledger[i] = transition
	}
	prefix := bundle
	prefix.AuditJournal = journal
	prefix.AuditRecordCount, prefix.AuditHeadHash = uint64(len(trace.Records)), auditHeadHash(trace.Records)
	prefix.ApprovedDeltaLedger = ledger
	shorter := prefix
	shorter.ApprovedDeltaLedger = nil
	if _, err := newGovernedRetainedHistory(shorter, candidate.next, candidate.historical, candidate.commandHistory); err == nil {
		t.Fatal("froze an empty prefix over a journal with one approved transition")
	}
	extra := ledger[0]
	extra.ID, extra.Sequence = extra.ID+"-extra", 2
	longer := prefix
	longer.ApprovedDeltaLedger = append(append([]delta.Transition(nil), ledger...), extra)
	if _, err := newGovernedRetainedHistory(longer, candidate.next, candidate.historical, candidate.commandHistory); err == nil {
		t.Fatal("froze a prefix longer than the verified journal")
	}
	for _, offset := range []int64{-1, 1} {
		pins := candidate.next
		pins.StoreJournalCount = uint64(int64(pins.StoreJournalCount) + offset)
		if _, err := newGovernedRetainedHistory(prefix, pins, candidate.historical, candidate.commandHistory); err == nil {
			t.Fatal("froze a prefix whose journal count differs from the recovery pins")
		}
	}
	frozen, err := newGovernedRetainedHistory(prefix, candidate.next, candidate.historical, candidate.commandHistory)
	if err != nil {
		t.Fatal(err)
	}
	ledger[0].Changes[0].Path += "-changed"
	if err := frozen.verifyDecision(original.Decision, binding, session.Authority.VerifyDecision); err != nil {
		t.Fatalf("frozen history aliases its source ledger: %v", err)
	}
}

// Recovered publication verifies each retained transition and its origin through the activated authority.
func combinedRecoveryCheckPublicationOrigins(t *testing.T, session *governedRecoverySession) {
	t.Helper()
	bundle, err := session.Store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	original := bundle.ApprovedDeltaLedger[0]
	binding := governedTransitionBinding(original)
	if err := session.Authority.VerifyTransition(original.Decision, binding); err != nil {
		t.Fatalf("activated authority refused its retained transition: %v", err)
	}
	if err := session.Authority.VerifyOrigin(bundle, original); err != nil {
		t.Fatalf("activated authority refused the retained origin of its transition: %v", err)
	}
	changed := binding
	changed.Changes = append([]delta.ObjectChange(nil), binding.Changes...)
	changed.Changes[0].Path += "-changed"
	if err := session.Authority.VerifyTransition(original.Decision, changed); err == nil {
		t.Fatal("activated authority accepted a changed retained transition")
	}
	borrowed := binding
	borrowed.Sequence++
	if err := session.Authority.VerifyTransition(original.Decision, borrowed); err == nil {
		t.Fatal("a later sequence borrowed the retained authorization")
	}
}
