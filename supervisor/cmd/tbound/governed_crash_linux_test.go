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
	"strings"
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
	// LC-04: the operation authorization and exact effect intent are durable,
	// but the effect callback has not been entered. A fresh process must
	// quarantine the unresolved operation and refuse to reuse the provider
	// transcript.
	preEffectRoot := privateGovernedTestRoot(t)
	governedCrashProcess(t, "write-after-intent", preEffectRoot, governedCrashExit)
	preEffectMarkerPath := filepath.Join(preEffectRoot, "crash.marker")
	preEffectMarker, err := os.ReadFile(preEffectMarkerPath)
	if err != nil || string(preEffectMarker) != "intent-durable:callback-not-entered\n" {
		t.Fatalf("post-intent crash marker=%q err=%v", preEffectMarker, err)
	}
	preEffectPrimaryPath := filepath.Join(preEffectRoot, "session-audit.jsonl")
	preEffectProviderPath := filepath.Join(preEffectRoot, "provider-audit.jsonl")
	preEffectAdmissionPath := filepath.Join(preEffectRoot, "store", "delta-admission.jsonl")
	preEffectPrimary, preEffectTrace := governedCrashReadTrace(t, preEffectPrimaryPath)
	preEffectProvider, preEffectProviderTrace := governedCrashReadTrace(t, preEffectProviderPath)
	preEffectAdmissions, preEffectAdmissionTrace := governedCrashReadTrace(t, preEffectAdmissionPath)
	if len(preEffectTrace.Effects) != 2 || preEffectTrace.Effects[0].Outcome != "success" || preEffectTrace.Effects[0].Unresolved ||
		!preEffectTrace.Effects[1].Unresolved || preEffectTrace.Effects[1].Outcome != "" || preEffectTrace.Effects[1].OutcomeSequence != 0 {
		t.Fatalf("post-intent effects=%+v", preEffectTrace.Effects)
	}
	preEffect := preEffectTrace.Effects[1]
	var preEffectIntent struct {
		SchemaVersion string                       `json:"schema_version"`
		Request       sessionrepo.OperationRequest `json:"request"`
		Path          string                       `json:"path"`
		MaxBytes      int64                        `json:"max_bytes"`
		Command       sessionrepo.CommandSpec      `json:"command"`
		RunnerProfile string                       `json:"runner_profile"`
	}
	if err := unmarshalGovernedJSON(preEffect.Intent, &preEffectIntent); err != nil {
		t.Fatal(err)
	}
	if preEffectIntent.SchemaVersion != sessionrepo.OperationIntentSchemaVersion || preEffectIntent.Path != "task.txt" ||
		preEffectIntent.Request.Tool != "write" || preEffectIntent.Request.EffectID != preEffect.ID ||
		preEffectIntent.Request.InputGeneration != "g0" || preEffectIntent.Request.Operation.Kind != delta.OperationProposalCall {
		t.Fatalf("post-intent effect is not the authorized write: %+v", preEffectIntent)
	}
	governedCrashAssertOperationBinding(t, preEffect, preEffectTrace,
		preEffectProviderTrace, preEffectIntent.Request, "write", "crash-write")
	if len(preEffectAdmissionTrace.Records) != 0 {
		t.Fatalf("post-intent admission records=%d, want none", len(preEffectAdmissionTrace.Records))
	}
	if _, err := os.Stat(filepath.Join(preEffectRoot, "store", "generations", "g1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("post-intent crash created an output generation: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(preEffectRoot, "store", "generations", "g0", "task.txt")); err != nil || string(data) != "before\n" {
		t.Fatalf("post-intent baseline bytes=%q err=%v", data, err)
	}
	governedCrashProcess(t, "recover", preEffectRoot, 0)
	if _, err := os.Stat(filepath.Join(preEffectRoot, "recovery-callback.marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("post-intent recovery invoked an effect callback or verifier: %v", err)
	}
	for path, before := range map[string][]byte{
		preEffectPrimaryPath: preEffectPrimary, preEffectProviderPath: preEffectProvider,
		preEffectAdmissionPath: preEffectAdmissions, preEffectMarkerPath: preEffectMarker,
	} {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("post-intent recovery changed %s: %v", filepath.Base(path), err)
		}
	}

	t.Run("bash-runner-entry", func(t *testing.T) {
		root := privateGovernedTestRoot(t)
		actionPath := filepath.Join(root, "external-effect.marker")
		if !filepath.IsAbs(actionPath) {
			t.Fatalf("fixture action marker path is not absolute: %q", actionPath)
		}
		expectedCommand := governedCrashBashFixtureCommand(root)
		control := exec.Command("/bin/bash", "--noprofile", "--norc", "-lc", expectedCommand)
		control.Env = []string{"PATH=/usr/bin:/bin"}
		if output, err := control.CombinedOutput(); err != nil {
			t.Fatalf("Bash fixture command positive control: %v\n%s", err, output)
		}
		if data, err := os.ReadFile(actionPath); err != nil || string(data) != "observed\n" {
			t.Fatalf("Bash fixture command positive control marker=%q err=%v", data, err)
		}
		if err := os.Remove(actionPath); err != nil {
			t.Fatalf("remove positive-control marker before crash run: %v", err)
		}
		governedCrashProcess(t, "bash-runner-entry", root, governedCrashExit)

		runnerEntryPath := filepath.Join(root, "runner-entry.marker")
		runnerEntry, err := os.ReadFile(runnerEntryPath)
		if err != nil {
			t.Fatalf("Bash runner-entry marker=%q err=%v", runnerEntry, err)
		}
		if _, err := os.Stat(actionPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("fixture command's independent action marker exists after runner-entry crash: %v", err)
		}

		primaryPath := filepath.Join(root, "session-audit.jsonl")
		providerPath := filepath.Join(root, "provider-audit.jsonl")
		admissionPath := filepath.Join(root, "store", "delta-admission.jsonl")
		primary, trace := governedCrashReadTrace(t, primaryPath)
		provider, providerTrace := governedCrashReadTrace(t, providerPath)
		admissions, admissionTrace := governedCrashReadTrace(t, admissionPath)
		if len(trace.Effects) != 2 || trace.Effects[0].Outcome != "success" || trace.Effects[0].Unresolved ||
			!trace.Effects[1].Unresolved || trace.Effects[1].Outcome != "" || trace.Effects[1].OutcomeSequence != 0 {
			t.Fatalf("Bash runner-entry effects=%+v", trace.Effects)
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
		if intent.SchemaVersion != sessionrepo.OperationIntentSchemaVersion || intent.Request.Tool != "bash" ||
			intent.Request.EffectID != effect.ID || intent.Request.InputGeneration != "g0" ||
			intent.Request.Operation.Kind != delta.OperationLease || intent.Request.Operation.LeaseID == "" ||
			intent.Request.ViewID == "" || !reflect.DeepEqual(intent.Request.OutputScopePaths, []string{"task.txt"}) ||
			intent.Command.Executable != "/bin/bash" || !reflect.DeepEqual(intent.Command.Args, []string{"-lc", expectedCommand}) ||
			intent.RunnerProfile != "offline-crash-runner-v1" {
			t.Fatalf("Bash runner-entry intent is not the governed fixture command: %+v", intent)
		}
		var runnerEntryEvidence governedCrashRunnerEntryEvidence
		if err := unmarshalGovernedJSON(runnerEntry, &runnerEntryEvidence); err != nil {
			t.Fatal(err)
		}
		if runnerEntryEvidence.ViewID != intent.Request.ViewID || runnerEntryEvidence.LeaseID != intent.Request.Operation.LeaseID ||
			runnerEntryEvidence.CommandDigest != durableContentDigest([]byte(intent.Command.Args[1])) ||
			runnerEntryEvidence.SpecDigest != governedCrashCommandSpecDigest(intent.Command.Executable, intent.Command.Args,
				intent.Command.WorkingDirectory, intent.Request.OutputScopePaths) {
			t.Fatalf("Bash runner-entry marker does not bind the authorized view, lease, and command spec: marker=%+v intent=%+v", runnerEntryEvidence, intent)
		}

		_, exchange := governedCrashAssertOperationBinding(t, effect, trace, providerTrace,
			intent.Request, "bash", "crash-bash")
		if len(admissionTrace.Records) != 0 {
			t.Fatalf("Bash runner-entry admission records=%d, want none", len(admissionTrace.Records))
		}
		var capturedArguments struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(exchange.ToolCall.Arguments, &capturedArguments); err != nil || capturedArguments.Command != expectedCommand {
			t.Fatalf("captured provider command=%q err=%v, want exact positive-control command %q", capturedArguments.Command, err, expectedCommand)
		}
		for _, path := range []string{
			filepath.Join(root, "store", "generations", "g1"),
			filepath.Join(root, "store", "generations", ".stage-g1"),
		} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Bash runner-entry crash created %s: %v", path, err)
			}
		}
		if data, err := os.ReadFile(filepath.Join(root, "store", "generations", "g0", "task.txt")); err != nil || string(data) != "before\n" {
			t.Fatalf("Bash runner-entry baseline bytes=%q err=%v", data, err)
		}

		governedCrashProcess(t, "recover", root, 0)
		if _, err := os.Stat(filepath.Join(root, "recovery-callback.marker")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("fresh Bash runner-entry recovery invoked an effect lifecycle: %v", err)
		}
		if _, err := os.Stat(actionPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("fresh Bash runner-entry recovery created the fixture action marker: %v", err)
		}
		for path, before := range map[string][]byte{
			primaryPath: primary, providerPath: provider, admissionPath: admissions, runnerEntryPath: runnerEntry,
		} {
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("fresh Bash runner-entry recovery changed %s: %v", filepath.Base(path), err)
			}
		}
	})

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
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: governedCrashXattr, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}})
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

	// A second cut is after the real Store outcome is durable but before the
	// ResultingExecutor can append or release its provider result. This exercises
	// the same refusal helper as the constructor, not production session resume.
	postRoot := privateGovernedTestRoot(t)
	governedCrashProcess(t, "write-after-outcome", postRoot, governedCrashExit)
	postMarkerPath := filepath.Join(postRoot, "crash.marker")
	postMarker, err := os.ReadFile(postMarkerPath)
	if err != nil || string(postMarker) != "durable-outcome:g1\n" {
		t.Fatalf("post-outcome crash marker=%q err=%v", postMarker, err)
	}
	postPrimaryPath := filepath.Join(postRoot, "session-audit.jsonl")
	postProviderPath := filepath.Join(postRoot, "provider-audit.jsonl")
	postAdmissionPath := filepath.Join(postRoot, "store", "delta-admission.jsonl")
	postPrimary, postTrace := governedCrashReadTrace(t, postPrimaryPath)
	postProvider, postProviderTrace := governedCrashReadTrace(t, postProviderPath)
	postAdmissions, postAdmissionTrace := governedCrashReadTrace(t, postAdmissionPath)
	if len(postTrace.Effects) != 2 || postTrace.Effects[0].Outcome != "success" || postTrace.Effects[0].Unresolved ||
		postTrace.Effects[1].Outcome != "success" || postTrace.Effects[1].Unresolved ||
		postTrace.Effects[1].OutcomeSequence <= postTrace.Effects[1].IntentSequence {
		t.Fatalf("post-outcome crash effects=%+v", postTrace.Effects)
	}
	if len(postProviderTrace.Records) != 3 || len(postAdmissionTrace.Records) != 1 {
		t.Fatalf("post-outcome provider/admission records=%d/%d, want 3/1", len(postProviderTrace.Records), len(postAdmissionTrace.Records))
	}
	for index, kind := range []string{"provider_user_admitted", "provider_exchange", "provider_gate_decision"} {
		if postProviderTrace.Records[index].Event.Kind != kind {
			t.Fatalf("post-outcome provider record[%d]=%q want %q", index, postProviderTrace.Records[index].Event.Kind, kind)
		}
	}
	if data, err := os.ReadFile(filepath.Join(postRoot, "store", "generations", "g1", "task.txt")); err != nil || string(data) != "after\n" {
		t.Fatalf("post-outcome g1 bytes=%q err=%v", data, err)
	}
	governedCrashProcess(t, "recover-after-outcome", postRoot, 0)
	if _, err := os.Stat(filepath.Join(postRoot, "recovery-callback.marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("post-outcome recovery invoked effect lifecycle: %v", err)
	}
	for path, before := range map[string][]byte{
		postPrimaryPath: postPrimary, postProviderPath: postProvider,
		postAdmissionPath: postAdmissions, postMarkerPath: postMarker,
	} {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("post-outcome recovery changed %s: %v", filepath.Base(path), err)
		}
	}
}

func governedCrashChild(t *testing.T) {
	phase, root := os.Getenv(governedCrashPhase), os.Getenv(governedCrashRoot)
	if phase != "write-after-intent" && phase != "bash-runner-entry" && phase != "write" && phase != "recover" && phase != "write-after-outcome" && phase != "recover-after-outcome" || root == "" {
		t.Fatal("invalid child inputs")
	}
	if phase == "recover" || phase == "recover-after-outcome" {
		journal, err := audit.OpenWithLifecycleHook(filepath.Join(root, "session-audit.jsonl"), func(audit.EffectBoundary) error {
			markerErr := governedCrashWriteSynced(filepath.Join(root, "recovery-callback.marker"), []byte("unexpected recovery effect lifecycle\n"))
			return errors.Join(errors.New("recovery attempted an effect lifecycle"), markerErr)
		})
		if err != nil {
			t.Fatal(err)
		}
		defer journal.Close()
		called := func() error {
			return errors.Join(errors.New("forbidden recovery callback"), governedCrashWriteSynced(filepath.Join(root, "recovery-callback.marker"), []byte("called\n")))
		}
		authorizeOperation := func(sessionrepo.OperationRequest) error { return called() }
		verifyDecision := func(delta.PolicyDecision, delta.TransitionBinding) error { return called() }
		verifySettlement := func(sessionrepo.CommandSettlement, string, string) error { return called() }
		if phase == "recover-after-outcome" {
			// These test-only verifiers permit revalidation of retained evidence.
			// The audit lifecycle sentinel above refuses any new effect execution.
			authorizeOperation = func(sessionrepo.OperationRequest) error { return nil }
			verifyDecision = func(delta.PolicyDecision, delta.TransitionBinding) error { return nil }
			verifySettlement = func(sessionrepo.CommandSettlement, string, string) error { return nil }
		}
		options := sessionrepo.Options{PolicyDigest: governedCrashPolicy, MetadataPolicyDigest: governedCrashMetadata, Limits: workspace.DefaultLimits(),
			XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: governedCrashXattr, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}, Journal: journal,
			AuthorizeOperation: authorizeOperation, VerifyDecision: verifyDecision, VerifySettlement: verifySettlement}
		dir, err := os.Open(filepath.Join(root, "store"))
		if err != nil {
			t.Fatal(err)
		}
		defer dir.Close()
		store, err := sessionrepo.Open(dir, options)
		if phase == "recover" && (store != nil || !errors.Is(err, sessionrepo.ErrQuarantined)) {
			if store != nil {
				_ = store.Close()
			}
			t.Fatalf("fresh pre-effect recovery=(%v,%v), want nil quarantine", store, err)
		}
		if phase == "recover" {
			trace, err := journal.Trace()
			if err != nil || len(trace.Effects) != 2 || !trace.Effects[1].Unresolved {
				t.Fatalf("fresh pre-effect journal trace=%+v err=%v", trace.Effects, err)
			}
			for _, attempt := range []struct {
				id   string
				want error
			}{
				{id: trace.Effects[1].ID, want: audit.ErrUnresolvedEffect},
				{id: "lc04-fresh-effect", want: audit.ErrQuarantined},
			} {
				_, runErr := journal.RunEffect(attempt.id, []byte("must not run"), func() ([]byte, error) {
					return nil, called()
				})
				if !errors.Is(runErr, attempt.want) {
					t.Fatalf("fresh pre-effect RunEffect(%q) err=%v, want %v", attempt.id, runErr, attempt.want)
				}
			}
		}
		if phase == "recover-after-outcome" && (err != nil || store == nil) {
			if store != nil {
				_ = store.Close()
			}
			t.Fatalf("fresh post-outcome Store reopen=(%v,%v)", store, err)
		}
		providerJournal, err := audit.Open(filepath.Join(root, "provider-audit.jsonl"))
		if err != nil {
			if store != nil {
				_ = store.Close()
			}
			t.Fatal(err)
		}
		policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{{Tool: "write", Effect: gate.EffectAllow}}})
		if err != nil {
			_ = providerJournal.Close()
			if store != nil {
				_ = store.Close()
			}
			t.Fatal(err)
		}
		authority, err := NewSessionOperationAuthority(journal, providerJournal,
			governedCrashWorkflow, governedCrashConversation, governedCrashProfile,
			policy, governedCrashPolicy, governedCrashMetadata, nil)
		if err != nil {
			_ = providerJournal.Close()
			if store != nil {
				_ = store.Close()
			}
			t.Fatal(err)
		}
		_, conversationErr := authority.NewProviderConversation(providerbridge.Config{
			WorkflowID: governedCrashWorkflow, ConversationID: governedCrashConversation, ProfileID: governedCrashProfile,
		})
		_ = providerJournal.Close()
		if !errors.Is(conversationErr, ErrProviderRecoveryRequired) {
			if store != nil {
				_ = store.Close()
			}
			t.Fatalf("fresh provider constructor err=%v, want recovery required", conversationErr)
		}
		if phase == "recover-after-outcome" {
			defer store.Close()
			bundle, err := store.EvidenceBundle()
			if err != nil || len(bundle.Generations) != 2 || bundle.Generations[1].ID != "g1" {
				t.Fatalf("fresh post-outcome Store generations=%+v err=%v", bundle.Generations, err)
			}
			if data, err := os.ReadFile(filepath.Join(root, "store", "generations", "g1", "task.txt")); err != nil || string(data) != "after\n" {
				t.Fatalf("fresh post-outcome g1 bytes=%q err=%v", data, err)
			}
			return
		}
		if store != nil {
			store.Close()
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
		if !armed {
			return nil
		}
		if phase == "write-after-intent" && boundary == audit.EffectBoundaryAfterIntentSync {
			if err := governedCrashWriteSynced(filepath.Join(root, "crash.marker"), []byte("intent-durable:callback-not-entered\n")); err != nil {
				return err
			}
			os.Exit(governedCrashExit)
		}
		if phase != "write" || boundary != audit.EffectBoundaryAfterCallbackReturn {
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
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{{Tool: "write", Effect: gate.EffectAllow}, {Tool: "bash", Effect: gate.EffectAllow}}})
	if err != nil {
		t.Fatal(err)
	}
	var authorityRunner governedCommandRunner
	var commandRunner sessionrepo.CommandRunner
	var bashOutputPaths []string
	var command string
	var crashRunner *governedCrashCommandRunner
	if phase == "bash-runner-entry" {
		command = governedCrashBashFixtureCommand(root)
		crashRunner = &governedCrashCommandRunner{t: t, root: root, command: command, leases: make(map[string]string)}
		authorityRunner, commandRunner, bashOutputPaths = crashRunner, crashRunner, []string{"task.txt"}
	}
	var authorityArgs [][]string
	if bashOutputPaths != nil {
		authorityArgs = append(authorityArgs, bashOutputPaths)
	}
	authority, err := NewSessionOperationAuthority(journal, providerJournal, governedCrashWorkflow, governedCrashConversation, governedCrashProfile, policy, governedCrashPolicy, governedCrashMetadata, authorityRunner, authorityArgs...)
	if err != nil {
		t.Fatal(err)
	}
	storeRoot, err := os.Open(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	managed, err := authority.CreateStore(storeRoot, workspace.DefaultLimits(), workspace.XattrVisibilityAttestation{ProfileDigest: governedCrashXattr, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}, nil)
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
	durable, err := NewDurableExecutor(DurableExecutorConfig{Store: store, Tip: g0, CallIssuer: governedCallIssuer, PolicyDigest: governedCrashPolicy, MetadataPolicyDigest: governedCrashMetadata,
		BashOutputPaths: bashOutputPaths, CommandRunner: commandRunner})
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
	tool, callID := "write", "crash-write"
	arguments := json.RawMessage(`{"path":"task.txt","content":"after\n"}`)
	if phase == "bash-runner-entry" {
		tool, callID = "bash", "crash-bash"
		encodedArguments, err := json.Marshal(struct {
			Command string `json:"command"`
		}{command})
		if err != nil {
			t.Fatal(err)
		}
		arguments = encodedArguments
	}
	conversation, err := providerbridge.NewDevelopmentOfflineConversation(providerbridge.Config{ConversationID: governedCrashConversation, WorkflowID: governedCrashWorkflow, ProfileID: governedCrashProfile,
		InitialGenerationID: g0.ID(), InitialTreeDigest: g0.TreeDigest(), SystemPrompt: "Synthetic crash test.", DeveloperPrompt: "Use the registered tool."}, providerJournal,
		[]providerbridge.DevelopmentProviderTurn{{ResponseID: "crash-response", CallID: callID, Tool: tool, Arguments: arguments}})
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
	var inner providerbridge.Executor = durable
	if phase == "write-after-outcome" {
		inner = governedCrashAfterDurableExecutor{inner: durable, root: root}
	}
	executor, err := providerbridge.NewExecutor(conversation, inner)
	if err != nil {
		t.Fatal(err)
	}
	armed = true
	_, err = executor.Execute(ctx, proposal, decision)
	t.Fatalf("crash hook did not terminate executor: %v", err)
}

type governedCrashCommandRunner struct {
	t       *testing.T
	root    string
	command string
	leases  map[string]string
}

func (*governedCrashCommandRunner) Profile() string { return "offline-crash-runner-v1" }

func (r *governedCrashCommandRunner) RegisterLease(viewID, leaseID string) error {
	if viewID == "" || leaseID == "" {
		return errors.New("crash fixture received an empty Bash view or lease")
	}
	if _, exists := r.leases[viewID]; exists {
		return errors.New("crash fixture received a duplicate Bash view registration")
	}
	r.leases[viewID] = leaseID
	return nil
}

func (*governedCrashCommandRunner) VerifySettlement(sessionrepo.CommandSettlement, string, string) error {
	return errors.New("crash fixture must exit before settlement")
}

func (r *governedCrashCommandRunner) Run(_ context.Context, view *sessionrepo.CommandView, spec sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	if view == nil || view.ID() == "" || spec.Executable != "/bin/bash" ||
		!reflect.DeepEqual(spec.Args, []string{"-lc", r.command}) || spec.WorkingDirectory != "" ||
		!reflect.DeepEqual(spec.DeclaredOutputPaths, []string{"task.txt"}) {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.New("crash fixture received a mismatched command view or spec")
	}
	leaseID, exists := r.leases[view.ID()]
	if !exists || leaseID == "" {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.New("crash fixture received an unregistered Bash view")
	}
	marker, err := json.Marshal(governedCrashRunnerEntryEvidence{
		ViewID: view.ID(), LeaseID: leaseID,
		CommandDigest: durableContentDigest([]byte(spec.Args[1])),
		SpecDigest:    governedCrashCommandSpecDigest(spec.Executable, spec.Args, spec.WorkingDirectory, spec.DeclaredOutputPaths),
	})
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	marker = append(marker, '\n')
	if err := governedCrashWriteSynced(filepath.Join(r.root, "runner-entry.marker"), marker); err != nil {
		r.t.Fatal(err)
	}
	os.Exit(governedCrashExit)
	return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, nil
}

func governedCrashShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

type governedCrashRunnerEntryEvidence struct {
	ViewID        string `json:"view_id"`
	LeaseID       string `json:"lease_id"`
	CommandDigest string `json:"command_digest"`
	SpecDigest    string `json:"spec_digest"`
}

func governedCrashCommandSpecDigest(executable string, args []string, workingDirectory string, outputScope []string) string {
	encoded, err := json.Marshal(struct {
		Executable          string   `json:"executable"`
		Args                []string `json:"args"`
		WorkingDirectory    string   `json:"working_directory,omitempty"`
		DeclaredOutputPaths []string `json:"declared_output_paths"`
	}{executable, args, workingDirectory, outputScope})
	if err != nil {
		return ""
	}
	return durableContentDigest(encoded)
}

func governedCrashBashFixtureCommand(root string) string {
	return "printf 'observed\\n' > " + governedCrashShellQuote(filepath.Join(root, "external-effect.marker"))
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

func governedCrashAssertOperationBinding(t *testing.T, effect audit.EffectTrace, storeTrace audit.Trace,
	providerTrace audit.Trace, request sessionrepo.OperationRequest, tool, callID string) (governedOperationAdmissionRecord, providerExchangeEvidence) {
	t.Helper()
	if len(providerTrace.Records) != 3 {
		t.Fatalf("provider journal has %d records, want capture, exchange, and decision", len(providerTrace.Records))
	}
	for index, kind := range []string{"provider_user_admitted", "provider_exchange", "provider_gate_decision"} {
		if providerTrace.Records[index].Event.Kind != kind {
			t.Fatalf("provider record[%d]=%q, want %q", index, providerTrace.Records[index].Event.Kind, kind)
		}
	}
	var exchange providerExchangeEvidence
	if err := unmarshalGovernedJSON(providerTrace.Records[1].Event.Data, &exchange); err != nil {
		t.Fatal(err)
	}
	if exchange.WorkflowID != governedCrashWorkflow || exchange.Turn != 1 || exchange.ResponseID != "crash-response" ||
		exchange.ToolCall == nil || exchange.ToolCall.ID != callID || exchange.ToolCall.Name != tool {
		t.Fatalf("provider capture does not bind %s/%s: %+v", tool, callID, exchange)
	}
	var decision gate.Decision
	if err := unmarshalGovernedJSON(providerTrace.Records[2].Event.Data, &decision); err != nil {
		t.Fatal(err)
	}
	capturedDigest, err := protocol.CanonicalArgumentsDigest(tool, exchange.ToolCall.Arguments)
	if err != nil || decision.Verdict != gate.Allow || decision.Tool != tool || decision.ToolCallID != callID ||
		decision.CanonicalArgumentsDigest != capturedDigest {
		t.Fatalf("provider decision does not bind the captured %s/%s arguments: decision=%+v digest=%q err=%v", tool, callID, decision, capturedDigest, err)
	}

	var authorization governedOperationAdmissionRecord
	var authorizationRecord audit.Record
	for _, record := range storeTrace.Records {
		if record.Event.Kind != governedOperationAuthorizationKind {
			continue
		}
		if authorizationRecord.Sequence != 0 {
			t.Fatal("duplicate governed operation authorization")
		}
		authorizationRecord = record
		if err := unmarshalGovernedJSON(record.Event.Data, &authorization); err != nil {
			t.Fatal(err)
		}
	}
	if authorizationRecord.Sequence == 0 || authorizationRecord.Sequence >= effect.IntentSequence ||
		authorization.EffectID != effect.ID || authorization.DecisionID != request.Decision.ID ||
		authorization.Operation != request.Operation || authorization.Tool != tool ||
		authorization.TypedArgumentDigest != request.ArgumentDigest || authorization.InputGeneration != request.InputGeneration ||
		!reflect.DeepEqual(authorization.OutputScopePaths, request.OutputScopePaths) ||
		authorization.ProviderDecisionSequence != providerTrace.Records[2].Sequence ||
		authorization.ProviderDecisionRecordHash != providerTrace.Records[2].Hash ||
		!reflect.DeepEqual(authorization.GateDecision, decision) {
		t.Fatalf("operation authorization does not bind the exact %s request and correlated ALLOW: %+v", tool, authorization)
	}
	return authorization, exchange
}

type governedCrashAfterDurableExecutor struct {
	inner providerbridge.Executor
	root  string
}

func (e governedCrashAfterDurableExecutor) Execute(ctx context.Context, proposal protocol.Proposal, decision gate.Decision) (json.RawMessage, error) {
	output, err := e.inner.Execute(ctx, proposal, decision)
	if err != nil {
		return nil, err
	}
	var summary durableMutationSummaryPayload
	if err := unmarshalGovernedJSON(output, &summary); err != nil || summary.Tool != "write" || summary.Outcome != "success" || summary.Generation.ID != "g1" {
		return nil, errors.Join(errors.New("durable executor did not return successful g1"), err)
	}
	if err := governedCrashWriteSynced(filepath.Join(e.root, "crash.marker"), []byte("durable-outcome:g1\n")); err != nil {
		return nil, err
	}
	os.Exit(governedCrashExit)
	return output, nil
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
