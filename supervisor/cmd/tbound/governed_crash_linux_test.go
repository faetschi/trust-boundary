//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

const governedCrashPhase = "TBOUND_GOVERNED_CRASH_PHASE"
const governedCrashRoot = "TBOUND_GOVERNED_CRASH_ROOT"
const governedCrashExit = 74
const governedCrashWorkflow = "offline-crash-workflow"
const governedCrashConversation = "offline-crash-conversation"
const governedCrashProfile = "synthetic-offline-profile-v1"
const governedCrashPolicy = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const governedCrashMetadata = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const governedCrashXattr = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

func TestGovernedWriteCrashWithholdsProviderResult(t *testing.T) {
	if os.Getenv(governedCrashPhase) != "" {
		governedCrashChild(t)
		return
	}
	root := privateGovernedTestRoot(t)
	governedCrashProcess(t, "write", root, governedCrashExit)
	markerPath := filepath.Join(root, "crash.marker")
	marker, err := os.ReadFile(markerPath)
	if err != nil || string(marker) != "callback-return:g1\n" {
		t.Fatalf("crash marker=%q err=%v", marker, err)
	}
	primaryPath := filepath.Join(root, "session-audit.jsonl")
	providerPath := filepath.Join(root, "provider-audit.jsonl")
	admissionPath := filepath.Join(root, "store", "delta-admission.jsonl")
	primary, trace := governedCrashReadTrace(t, primaryPath)
	provider, providerTrace := governedCrashReadTrace(t, providerPath)
	admissions, admissionTrace := governedCrashReadTrace(t, admissionPath)
	if len(trace.Effects) != 2 || trace.Effects[0].Outcome != "success" || trace.Effects[0].Unresolved ||
		!trace.Effects[1].Unresolved || trace.Effects[1].Outcome != "" || trace.Effects[1].OutcomeSequence != 0 {
		t.Fatalf("unexpected effects: %+v", trace.Effects)
	}
	effect := trace.Effects[1]
	var intent struct {
		SchemaVersion string                       `json:"schema_version"`
		Request       sessionrepo.OperationRequest `json:"request"`
		Path          string                       `json:"path"`
		MaxBytes      int64                        `json:"max_bytes"`
		Command       sessionrepo.CommandSpec      `json:"command"`
		RunnerProfile string                       `json:"runner_profile"`
	}
	if err := unmarshalGovernedJSON(effect.Intent, &intent); err != nil {
		t.Fatal(err)
	}
	request := intent.Request
	if intent.SchemaVersion != sessionrepo.OperationIntentSchemaVersion || intent.Path != "task.txt" || request.Tool != "write" ||
		request.EffectID != effect.ID || request.InputGeneration != "g0" || !reflect.DeepEqual(request.OutputScopePaths, []string{"task.txt"}) ||
		request.Operation.Kind != delta.OperationProposalCall {
		t.Fatalf("unexpected intent: %+v", intent)
	}
	var authorization governedOperationAdmissionRecord
	var authorized audit.Record
	for _, record := range trace.Records {
		if record.Event.Kind != governedOperationAuthorizationKind {
			continue
		}
		if authorized.Sequence != 0 {
			t.Fatal("duplicate governed authorization")
		}
		authorized = record
		if err := unmarshalGovernedJSON(record.Event.Data, &authorization); err != nil {
			t.Fatal(err)
		}
	}
	if authorized.Sequence == 0 || authorized.Sequence >= effect.IntentSequence || authorization.SchemaVersion != "tbound-governed-operation-admission/v2" ||
		authorization.EffectID != effect.ID || authorization.DecisionID != request.Decision.ID || authorization.Operation != request.Operation ||
		authorization.Tool != "write" || !reflect.DeepEqual(authorization.OutputScopePaths, request.OutputScopePaths) ||
		authorization.TypedArgumentDigest != request.ArgumentDigest || authorization.InputGeneration != "g0" {
		t.Fatalf("invalid authorization: %+v", authorization)
	}
	want := []string{"provider_user_admitted", "provider_exchange", "provider_gate_decision"}
	if len(providerTrace.Records) != len(want) {
		t.Fatalf("provider results or extra records: %+v", providerTrace.Records)
	}
	for i, kind := range want {
		if providerTrace.Records[i].Event.Kind != kind {
			t.Fatalf("provider kind=%q want=%q", providerTrace.Records[i].Event.Kind, kind)
		}
	}
	var exchange providerExchangeEvidence
	if err := unmarshalGovernedJSON(providerTrace.Records[1].Event.Data, &exchange); err != nil {
		t.Fatal(err)
	}
	if exchange.WorkflowID != governedCrashWorkflow || exchange.Turn != 1 || exchange.ResponseID != "crash-response" ||
		exchange.ToolCall == nil || exchange.ToolCall.ID != "crash-write" || exchange.ToolCall.Name != "write" {
		t.Fatalf("unexpected captured proposal: %+v", exchange)
	}
	var decision gate.Decision
	if err := unmarshalGovernedJSON(providerTrace.Records[2].Event.Data, &decision); err != nil {
		t.Fatal(err)
	}
	if decision.Verdict != gate.Allow || decision.ToolCallID != "crash-write" || decision.Sequence != 1 ||
		authorization.ProviderDecisionSequence != providerTrace.Records[2].Sequence || authorization.ProviderDecisionRecordHash != providerTrace.Records[2].Hash ||
		!reflect.DeepEqual(authorization.GateDecision, decision) {
		t.Fatal("authorization did not bind the captured gate decision")
	}
	seedBytes, err := os.ReadFile(filepath.Join(root, "seed-bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var seed sessionrepo.EvidenceBundle
	if err := json.Unmarshal(seedBytes, &seed); err != nil {
		t.Fatal(err)
	}
	if len(admissionTrace.Records) != 1 {
		t.Fatal("expected one durable candidate admission")
	}
	var admission struct {
		SchemaVersion         string                       `json:"schema_version"`
		Request               sessionrepo.OperationRequest `json:"request"`
		IntentDigest          string                       `json:"intent_digest"`
		PolicyDigest          string                       `json:"policy_digest"`
		MetadataPolicyDigest  string                       `json:"metadata_policy_digest"`
		AdmissionPolicyDigest string                       `json:"admission_policy_digest"`
		Input                 delta.TreeManifest           `json:"input"`
		Output                delta.TreeManifest           `json:"output"`
		Candidate             delta.Transition             `json:"candidate"`
		Outcome               string                       `json:"outcome"`
	}
	if err := unmarshalGovernedJSON(admissionTrace.Records[0].Event.Data, &admission); err != nil {
		t.Fatal(err)
	}
	if admission.SchemaVersion != sessionrepo.AdmissionSchemaVersion || admission.Outcome != "allow" || !reflect.DeepEqual(admission.Request, request) ||
		!reflect.DeepEqual(admission.Input, seed.Baseline.Manifest) || admission.Output.Generation != "g1" || admission.Candidate.OutputGeneration != "g1" {
		t.Fatalf("unexpected admission: %+v", admission)
	}
	g1, err := os.Open(filepath.Join(root, "store", "generations", "g1"))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := workspace.Scan(g1, workspace.Options{Generation: "g1", MetadataPolicyDigest: governedCrashMetadata, Limits: workspace.DefaultLimits(), QuiescentRoot: true,
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: governedCrashXattr, Complete: true}})
	g1.Close()
	if err != nil || !reflect.DeepEqual(actual.Manifest, admission.Output) || actual.TreeDigest != admission.Candidate.OutputTreeDigest {
		t.Fatalf("promoted candidate mismatch: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "store", "generations", "g1", "task.txt")); err != nil || string(data) != "after\n" {
		t.Fatalf("candidate bytes=%q err=%v", data, err)
	}
	seed.AuditJournal, seed.AdmissionJournal = primary, admissions
	seed.AuditRecordCount, seed.AdmissionRecordCount = uint64(len(trace.Records)), uint64(len(admissionTrace.Records))
	seed.AuditHeadHash, seed.AdmissionHeadHash = trace.Records[len(trace.Records)-1].Hash, admissionTrace.Records[0].Hash
	disposition, err := sessionrepo.Reconstruct(seed)
	if err != nil || !disposition.Quarantined || disposition.Sealed != "g0" || len(disposition.Ledger) != 0 || len(disposition.Operations) != 0 {
		t.Fatalf("crash disposition=%+v err=%v", disposition, err)
	}
	governedCrashProcess(t, "recover", root, 0)
	if _, err := os.Stat(filepath.Join(root, "recovery-callback.marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forbidden recovery callback: %v", err)
	}
	for path, before := range map[string][]byte{primaryPath: primary, providerPath: provider, admissionPath: admissions, markerPath: marker} {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("recovery changed %s: %v", filepath.Base(path), err)
		}
	}
}

func governedCrashChild(t *testing.T) {
	phase, root := os.Getenv(governedCrashPhase), os.Getenv(governedCrashRoot)
	if phase != "write" && phase != "recover" || root == "" {
		t.Fatal("invalid child inputs")
	}
	if phase == "recover" {
		journal, err := audit.Open(filepath.Join(root, "session-audit.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		defer journal.Close()
		called := func() error {
			return errors.Join(errors.New("forbidden recovery callback"), governedCrashWriteSynced(filepath.Join(root, "recovery-callback.marker"), []byte("called\n")))
		}
		options := sessionrepo.Options{PolicyDigest: governedCrashPolicy, MetadataPolicyDigest: governedCrashMetadata, Limits: workspace.DefaultLimits(),
			XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: governedCrashXattr, Complete: true}, Journal: journal,
			AuthorizeOperation: func(sessionrepo.OperationRequest) error { return called() }, VerifyDecision: func(delta.PolicyDecision, delta.TransitionBinding) error { return called() },
			VerifySettlement: func(sessionrepo.CommandSettlement, string, string) error { return called() }}
		dir, err := os.Open(filepath.Join(root, "store"))
		if err != nil {
			t.Fatal(err)
		}
		defer dir.Close()
		store, err := sessionrepo.Open(dir, options)
		if store != nil {
			store.Close()
		}
		if store != nil || !errors.Is(err, sessionrepo.ErrQuarantined) {
			t.Fatalf("fresh recovery=(%v,%v), want nil quarantine", store, err)
		}
		return
	}
	git, gitProfile := pinGovernedTestGit(t)
	defer git.Close()
	for _, name := range []string{"store", "source", "admin"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "source", "task.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "source", "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	armed := false
	journal, err := audit.OpenWithLifecycleHook(filepath.Join(root, "session-audit.jsonl"), func(boundary audit.EffectBoundary) error {
		if !armed || boundary != audit.EffectBoundaryAfterCallbackReturn {
			return nil
		}
		data, err := os.ReadFile(filepath.Join(root, "store", "generations", "g1", "task.txt"))
		if err != nil || string(data) != "after\n" {
			return errors.New("candidate not promoted")
		}
		if err := governedCrashWriteSynced(filepath.Join(root, "crash.marker"), []byte("callback-return:g1\n")); err != nil {
			return err
		}
		os.Exit(governedCrashExit)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	providerJournal, err := audit.Open(filepath.Join(root, "provider-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer providerJournal.Close()
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{{Tool: "write", Effect: gate.EffectAllow}}})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := NewSessionOperationAuthority(journal, providerJournal, governedCrashWorkflow, governedCrashConversation, governedCrashProfile, policy, governedCrashPolicy, governedCrashMetadata, nil)
	if err != nil {
		t.Fatal(err)
	}
	storeRoot, err := os.Open(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	managed, err := authority.CreateStore(storeRoot, workspace.DefaultLimits(), workspace.XattrVisibilityAttestation{ProfileDigest: governedCrashXattr, Complete: true}, nil)
	storeRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	store := managed.Store()
	source, err := os.Open(filepath.Join(root, "source"))
	if err != nil {
		t.Fatal(err)
	}
	g0, err := store.Seed(source, sessionrepo.RootAttestation{Quiescent: true})
	source.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.BindBaseline(store, g0); err != nil {
		t.Fatal(err)
	}
	adminRoot, err := os.Open(filepath.Join(root, "admin"))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sessionlaunch.OpenAdminParent(adminRoot)
	adminRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	prepared, err := sessionlaunch.Prepare(context.Background(), managed, g0, admin, git, gitProfile)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if err := authority.BindPreparedSession(prepared); err != nil {
		t.Fatal(err)
	}
	durable, err := NewDurableExecutor(DurableExecutorConfig{Store: store, Tip: g0, CallIssuer: governedCallIssuer, PolicyDigest: governedCrashPolicy, MetadataPolicyDigest: governedCrashMetadata})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	seedJSON, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := governedCrashWriteSynced(filepath.Join(root, "seed-bundle.json"), seedJSON); err != nil {
		t.Fatal(err)
	}
	conversation, err := providerbridge.NewDevelopmentOfflineConversation(providerbridge.Config{ConversationID: governedCrashConversation, WorkflowID: governedCrashWorkflow, ProfileID: governedCrashProfile,
		InitialGenerationID: g0.ID(), InitialTreeDigest: g0.TreeDigest(), SystemPrompt: "Synthetic crash test.", DeveloperPrompt: "Use the registered write tool."}, providerJournal,
		[]providerbridge.DevelopmentProviderTurn{{ResponseID: "crash-response", CallID: "crash-write", Tool: "write", Arguments: json.RawMessage(`{"path":"task.txt","content":"after\n"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	defer conversation.Close()
	authority.mu.Lock()
	authority.conversation = conversation
	authority.mu.Unlock()
	ctx := context.Background()
	if err := authority.AdmitPrompt(conversation, durable, "crash-task", "Write task.txt."); err != nil {
		t.Fatal(err)
	}
	turn, err := conversation.NextTurn(ctx)
	if err != nil || turn.ToolCall == nil {
		t.Fatalf("captured proposal=%+v err=%v", turn, err)
	}
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: turn.ToolCall.ID, Tool: turn.ToolCall.Name, Arguments: append(json.RawMessage(nil), turn.ToolCall.Arguments...)}
	matched, err := conversation.Correlate(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	decision := gate.Evaluate(proposal, matched, policy)
	if decision.Verdict != gate.Allow {
		t.Fatalf("proposal not allowed: %+v", decision)
	}
	if err := conversation.RecordDecision(ctx, proposal, decision); err != nil {
		t.Fatal(err)
	}
	executor, err := providerbridge.NewExecutor(conversation, durable)
	if err != nil {
		t.Fatal(err)
	}
	armed = true
	_, err = executor.Execute(ctx, proposal, decision)
	t.Fatalf("crash hook did not terminate executor: %v", err)
}

func governedCrashProcess(t *testing.T, phase, root string, wantExit int) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestGovernedWriteCrashWithholdsProviderResult$")
	// This explicit environment contains no inherited credentials or Git config.
	cmd.Env = []string{governedCrashPhase + "=" + phase, governedCrashRoot + "=" + root, "TMPDIR=" + os.TempDir(), "PATH=" + filepath.Dir(gitPath)}
	output, err := cmd.CombinedOutput()
	if wantExit == 0 {
		if err != nil {
			t.Fatalf("recovery child: %v\n%s", err, output)
		}
		return
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != wantExit {
		t.Fatalf("crash child: %v want %d\n%s", err, wantExit, output)
	}
}

func governedCrashReadTrace(t *testing.T, path string) ([]byte, audit.Trace) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := audit.Verify(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return raw, trace
}

func governedCrashWriteSynced(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
