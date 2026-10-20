//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

// EVQ-RECOVERY-LIFECYCLE-CUTPOINTS (02-1 section 8). Each test kills a real
// governed process at one lifecycle cut point (a child of this test binary
// exits with status 74 at the exact boundary), then recovers in a fresh process
// and checks the registered disposition against the durable journals, the
// generation directories, the repository head and the production inert replay
// (ReplayGovernedTranscript). LC-04 and LC-05 are covered by
// TestGovernedWriteCrashWithholdsProviderResult in governed_crash_linux_test.go.
//
// Limits, stated so the mapping is not read as more than it is: the harness is
// the offline governed session with a development provider and no workflow
// ledger or real command cell. LC-05 enters a fixture CommandRunner, not a Bash
// process. The effect oracle of LC-06 for a write is the promoted generation
// directory. Workflow budget accounting (LC-01, LC-02) and the host-controlled
// RETIRED_UNEXECUTED retirement are not exercised (the latter is not
// implemented; the pending lineage is retained instead). LC-08 cuts after the
// durable executor returned, which is after the head is durable and before the
// provider result is appended. Power loss is not simulated.

type lcArtifacts struct {
	root                                          string
	marker                                        []byte
	primary, provider, admissions, repositoryHead []byte
	primaryTrace, providerTrace, admissionTrace   audit.Trace
}

type lcRepositoryState struct {
	HeadGeneration    string             `json:"head_generation"`
	Generations       []json.RawMessage  `json:"generations"`
	GenerationEffects []string           `json:"generation_effects"`
	Ledger            []delta.Transition `json:"approved_delta_ledger"`
}

func lcRead(t *testing.T, root string) lcArtifacts {
	t.Helper()
	a := lcArtifacts{root: root}
	marker, err := os.ReadFile(filepath.Join(root, "crash.marker"))
	if err != nil {
		t.Fatalf("crash marker: %v", err)
	}
	a.marker = marker
	a.primary, a.primaryTrace = governedCrashReadTrace(t, filepath.Join(root, "session-audit.jsonl"))
	a.provider, a.providerTrace = governedCrashReadTrace(t, filepath.Join(root, "provider-audit.jsonl"))
	a.admissions, a.admissionTrace = governedCrashReadTrace(t, filepath.Join(root, "store", "delta-admission.jsonl"))
	a.repositoryHead, err = os.ReadFile(filepath.Join(root, "store", "repository-state.json"))
	if err != nil {
		t.Fatalf("repository state: %v", err)
	}
	return a
}

func (a lcArtifacts) state(t *testing.T) lcRepositoryState {
	t.Helper()
	var state lcRepositoryState
	if err := json.Unmarshal(a.repositoryHead, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func lcKinds(trace audit.Trace) []string {
	kinds := make([]string, 0, len(trace.Records))
	for _, record := range trace.Records {
		kinds = append(kinds, record.Event.Kind)
	}
	return kinds
}

func lcRequireKinds(t *testing.T, what string, trace audit.Trace, want ...string) {
	t.Helper()
	got := lcKinds(trace)
	if len(got) != len(want) {
		t.Fatalf("%s records=%v, want %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s records=%v, want %v", what, got, want)
		}
	}
}

func lcCount(trace audit.Trace, kind string) int {
	n := 0
	for _, record := range trace.Records {
		if record.Event.Kind == kind {
			n++
		}
	}
	return n
}

// lcRequireJournalsUnchanged: recovery appends nothing to any journal and
// dispatches no callback.
func lcRequireJournalsUnchanged(t *testing.T, before lcArtifacts) lcArtifacts {
	t.Helper()
	after := lcRead(t, before.root)
	if !bytes.Equal(before.primary, after.primary) || !bytes.Equal(before.provider, after.provider) ||
		!bytes.Equal(before.admissions, after.admissions) || !bytes.Equal(before.marker, after.marker) {
		t.Fatal("fresh-process recovery changed a journal or the crash marker")
	}
	if _, err := os.Stat(filepath.Join(before.root, "recovery-callback.marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery dispatched a callback or an effect lifecycle: %v", err)
	}
	return after
}

func lcGenerations(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "store", "generations"))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func lcRequireGenerations(t *testing.T, root string, want ...string) {
	t.Helper()
	got := lcGenerations(t, root)
	if len(got) != len(want) {
		t.Fatalf("generation directories=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("generation directories=%v, want %v", got, want)
		}
	}
}

func lcRequireBaselineBytes(t *testing.T, root string) {
	t.Helper()
	if data, err := os.ReadFile(filepath.Join(root, "store", "generations", "g0", "task.txt")); err != nil || string(data) != "before\n" {
		t.Fatalf("sealed baseline bytes=%q err=%v", data, err)
	}
}

func lcRequireWriteIntent(t *testing.T, a lcArtifacts, wantOutcome bool) audit.EffectTrace {
	t.Helper()
	if len(a.primaryTrace.Effects) != 2 || a.primaryTrace.Effects[0].Outcome != "success" || a.primaryTrace.Effects[0].Unresolved {
		t.Fatalf("effects=%+v, want the settled seed effect and one write effect", a.primaryTrace.Effects)
	}
	effect := a.primaryTrace.Effects[1]
	if wantOutcome != (effect.Outcome == "success" && !effect.Unresolved && effect.OutcomeSequence > effect.IntentSequence) {
		t.Fatalf("write effect outcome state is wrong: %+v (outcome durable wanted=%t)", effect, wantOutcome)
	}
	return effect
}

type lcReplay struct {
	OpenError       string                        `json:"open_error"`
	OpenQuarantined bool                          `json:"open_quarantined"`
	Error           string                        `json:"error"`
	Projection      *GovernedTranscriptProjection `json:"projection"`
}

func writeLCReplay(t *testing.T, root string, result lcReplay) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(root, "replay.json"))
	if err := governedCrashWriteSynced(filepath.Join(root, "replay.json"), encoded); err != nil {
		t.Fatal(err)
	}
}

func lcReadReplay(t *testing.T, root string) lcReplay {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "replay.json"))
	if err != nil {
		t.Fatalf("replay result: %v", err)
	}
	var replay lcReplay
	if err := json.Unmarshal(data, &replay); err != nil {
		t.Fatal(err)
	}
	return replay
}

// governedCrashReplayChild is the fresh-process recovery of the lifecycle
// tests: it reopens both journals and the Store with a sentinel that refuses
// any effect lifecycle, runs the production inert replay and records its
// classification. Strict mode also refuses every Store authority callback;
// permissive mode lets Open revalidate the receipts of a retained ledger (the
// effect-lifecycle sentinel still refuses any new effect).
func governedCrashReplayChild(t *testing.T, root string, permissive bool) {
	sentinel := func() error {
		return errors.Join(errors.New("recovery attempted an effect"), governedCrashWriteSynced(filepath.Join(root, "recovery-callback.marker"), []byte("called\n")))
	}
	journal, err := audit.OpenWithLifecycleHook(filepath.Join(root, "session-audit.jsonl"), func(audit.EffectBoundary) error { return sentinel() })
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	providerJournal, err := audit.Open(filepath.Join(root, "provider-audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer providerJournal.Close()
	authorize := func(sessionrepo.OperationRequest) error { return sentinel() }
	verifyDecision := func(delta.PolicyDecision, delta.TransitionBinding) error { return sentinel() }
	verifySettlement := func(sessionrepo.CommandSettlement, string, string) error { return sentinel() }
	if permissive {
		authorize = func(sessionrepo.OperationRequest) error { return nil }
		verifyDecision = func(delta.PolicyDecision, delta.TransitionBinding) error { return nil }
		verifySettlement = func(sessionrepo.CommandSettlement, string, string) error { return nil }
	}
	options := sessionrepo.Options{PolicyDigest: governedCrashPolicy, MetadataPolicyDigest: governedCrashMetadata, Limits: workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: governedCrashXattr, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted},
		Journal:         journal, AuthorizeOperation: authorize, VerifyDecision: verifyDecision, VerifySettlement: verifySettlement}
	dir, err := os.Open(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	store, err := sessionrepo.Open(dir, options)
	if err != nil || store == nil {
		if store != nil {
			_ = store.Close()
		}
		// A Store that refuses to open is itself the recovery classification.
		writeLCReplay(t, root, lcReplay{OpenError: fmt.Sprint(err), OpenQuarantined: store == nil && errors.Is(err, sessionrepo.ErrQuarantined)})
		return
	}
	defer store.Close()
	bundle, err := store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: governedCrashRules()})
	if err != nil {
		t.Fatal(err)
	}
	config := providerbridge.Config{ConversationID: governedCrashConversation, WorkflowID: governedCrashWorkflow, ProfileID: governedCrashProfile,
		InitialGenerationID: bundle.Baseline.ID, InitialTreeDigest: bundle.Baseline.TreeDigest,
		SystemPrompt: "Synthetic crash test.", DeveloperPrompt: "Use the registered tool."}
	system, developer := config.SystemPrompt, config.DeveloperPrompt
	pins := GovernedReplayPins{
		Provider: providerbridge.RecoveryPins{Config: config, Model: providerbridge.ApprovedModel, Policy: policy},
		Profile: broker.Profile{ID: config.ProfileID, Model: providerbridge.ApprovedModel,
			Messages:     providerbridge.InitialPromptMessages(openrouter.Route{}, system, developer),
			ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: governedCallIssuer, ResponseIDIssuer: governedResponseIssuer, Generation: bundle.Baseline.ID},
		Baseline: bundle.Baseline, StorePolicyDigest: governedCrashPolicy, MetadataPolicyDigest: governedCrashMetadata, BashOutputPaths: []string{"task.txt"},
	}
	projection, replayErr := ReplayGovernedTranscript(providerJournal, journal, store, pins)
	result := lcReplay{}
	if replayErr != nil {
		result.Error = replayErr.Error()
	} else {
		result.Projection = &projection
	}
	writeLCReplay(t, root, result)
}

func TestLifecycleCutpointLC01ReceiptBeforeDecision(t *testing.T) {
	if os.Getenv(governedCrashPhase) != "" {
		governedCrashChild(t)
		return
	}
	root := privateGovernedTestRoot(t)
	governedCrashProcess(t, "lc01-receipt", root, governedCrashExit)
	before := lcRead(t, root)
	if string(before.marker) != "exchange-durable:decision-not-recorded\n" {
		t.Fatalf("crash marker=%q", before.marker)
	}
	lcRequireKinds(t, "provider", before.providerTrace, "provider_user_admitted", "provider_exchange")
	if lcCount(before.primaryTrace, governedOperationAuthorizationKind) != 0 || len(before.admissionTrace.Records) != 0 {
		t.Fatal("an operation authorization or admission exists before the correlated decision")
	}
	if len(before.primaryTrace.Effects) != 1 || before.primaryTrace.Effects[0].Unresolved {
		t.Fatalf("effects=%+v, want only the settled seed", before.primaryTrace.Effects)
	}
	lcRequireGenerations(t, root, "g0")
	governedCrashProcess(t, "replay-strict", root, 0)
	lcRequireJournalsUnchanged(t, before)
	replay := lcReadReplay(t, root)
	if replay.Error != "" || replay.OpenError != "" || replay.Projection == nil {
		t.Fatalf("the provider journal must replay: %+v", replay)
	}
	p := replay.Projection.Provider
	// The captured call stays pending: RETIRED_UNEXECUTED needs a host-controlled
	// retirement that this recovery does not perform, so the lineage is retained.
	if p.Disposition != providerbridge.DispositionAwaitingGateDecision || p.PendingCall == nil || p.RetiredCall != nil ||
		p.PendingCall.ToolCallID != "crash-write" || p.PendingCall.ToolName != "write" ||
		p.CapturedCallCount != 1 || p.DecisionCount != 0 || p.ResultCount != 0 || p.ProviderGenerationID != "g0" ||
		replay.Projection.StoreOperationCount != 0 || replay.Projection.StoreHeadGeneration != "g0" {
		t.Fatalf("pending provider lineage was not retained: %+v", replay.Projection)
	}
	lcRequireBaselineBytes(t, root)
}

func lcLogReplay(t *testing.T, name string, replay lcReplay) {
	t.Helper()
	encoded, _ := json.MarshalIndent(replay, "", " ")
	t.Logf("%s replay: %s", name, encoded)
}

func TestLifecycleCutpointLC02DecisionBeforeAuthorization(t *testing.T) {
	if os.Getenv(governedCrashPhase) != "" {
		governedCrashChild(t)
		return
	}
	root := privateGovernedTestRoot(t)
	governedCrashProcess(t, "lc02-decision", root, governedCrashExit)
	before := lcRead(t, root)
	if string(before.marker) != "decision-durable:authorization-not-appended\n" {
		t.Fatalf("crash marker=%q", before.marker)
	}
	lcRequireKinds(t, "provider", before.providerTrace, "provider_user_admitted", "provider_exchange", "provider_gate_decision")
	if lcCount(before.primaryTrace, governedOperationAuthorizationKind) != 0 || len(before.admissionTrace.Records) != 0 || len(before.primaryTrace.Effects) != 1 {
		t.Fatalf("authorization/effect exists before the Store was entered: kinds=%v effects=%+v", lcKinds(before.primaryTrace), before.primaryTrace.Effects)
	}
	lcRequireGenerations(t, root, "g0")
	governedCrashProcess(t, "replay-strict", root, 0)
	lcRequireJournalsUnchanged(t, before)
	lcRequireReplayRefusal(t, lcReadReplay(t, root), "ALLOW provider decision has no unique Store authorization")
	lcRequireDecisionCorrelation(t, before)
	lcRequireBaselineBytes(t, root)
}

func TestLifecycleCutpointLC03AuthorizationBeforeEffectIntent(t *testing.T) {
	if os.Getenv(governedCrashPhase) != "" {
		governedCrashChild(t)
		return
	}
	t.Run("write", func(t *testing.T) {
		root := privateGovernedTestRoot(t)
		governedCrashProcess(t, "lc03-before-intent", root, governedCrashExit)
		before := lcRead(t, root)
		if string(before.marker) != "authorization-durable:intent-not-appended\n" {
			t.Fatalf("crash marker=%q", before.marker)
		}
		lcRequireKinds(t, "provider", before.providerTrace, "provider_user_admitted", "provider_exchange", "provider_gate_decision")
		if lcCount(before.primaryTrace, governedOperationAuthorizationKind) != 1 || len(before.primaryTrace.Effects) != 1 ||
			len(before.admissionTrace.Records) != 0 {
			t.Fatalf("want one durable authorization and no effect intent: kinds=%v effects=%+v", lcKinds(before.primaryTrace), before.primaryTrace.Effects)
		}
		lcRequireGenerations(t, root, "g0")
		governedCrashProcess(t, "replay-strict", root, 0)
		lcRequireJournalsUnchanged(t, before)
		lcRequireReplayRefusal(t, lcReadReplay(t, root), "authorized Store effect is absent, unresolved, or unsuccessful")
		lcRequireDecisionCorrelation(t, before)
		lcRequireBaselineBytes(t, root)
		// Nothing was consumed durably: the Store reopens, and no staging output exists.
		lcRequireGenerations(t, root, "g0")
	})
	t.Run("bash", func(t *testing.T) {
		root := privateGovernedTestRoot(t)
		governedCrashProcess(t, "lc03-bash-before-intent", root, governedCrashExit)
		before := lcRead(t, root)
		if string(before.marker) != "authorization-durable:intent-not-appended\n" {
			t.Fatalf("crash marker=%q", before.marker)
		}
		if lcCount(before.primaryTrace, governedOperationAuthorizationKind) != 1 || len(before.primaryTrace.Effects) != 1 {
			t.Fatalf("want one durable authorization and no effect intent: kinds=%v", lcKinds(before.primaryTrace))
		}
		if _, err := os.Stat(filepath.Join(root, "runner-entry.marker")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the command runner was entered before the effect intent: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, "external-effect.marker")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the external action happened before the effect intent: %v", err)
		}
		governedCrashProcess(t, "replay-strict", root, 0)
		lcRequireJournalsUnchanged(t, before)
		// The unfinished writable view cannot be proven unused by the Store, so
		// recovery keeps the Store quarantined (UNKNOWN) rather than classifying
		// the lease as unexecuted. Nothing ran.
		if replay := lcReadReplay(t, root); !replay.OpenQuarantined || replay.Projection != nil {
			t.Fatalf("an orphan Bash view must quarantine the Store: %+v", replay)
		}
		if entries, err := os.ReadDir(filepath.Join(root, "store", "views")); err != nil || len(entries) != 1 {
			t.Fatalf("views=%d err=%v, want the one unconsumed command view", len(entries), err)
		}
	})
}

func TestLifecycleCutpointLC06EffectObservedBeforeOutcome(t *testing.T) {
	if os.Getenv(governedCrashPhase) != "" {
		governedCrashChild(t)
		return
	}
	root := privateGovernedTestRoot(t)
	governedCrashProcess(t, "write", root, governedCrashExit)
	before := lcRead(t, root)
	if string(before.marker) != "callback-return:g1\n" {
		t.Fatalf("crash marker=%q", before.marker)
	}
	effect := lcRequireWriteIntent(t, before, false)
	if !effect.Unresolved || effect.Outcome != "" {
		t.Fatalf("effect=%+v, want an unresolved intent without a terminal outcome", effect)
	}
	// The independent oracle is the promoted generation itself: the effect is
	// observable exactly once, as g1 with the candidate bytes.
	lcRequireGenerations(t, root, "g0", "g1")
	if data, err := os.ReadFile(filepath.Join(root, "store", "generations", "g1", "task.txt")); err != nil || string(data) != "after\n" {
		t.Fatalf("observed effect bytes=%q err=%v", data, err)
	}
	if len(before.admissionTrace.Records) != 1 {
		t.Fatalf("admission records=%d, want the one candidate record", len(before.admissionTrace.Records))
	}
	state := before.state(t)
	if state.HeadGeneration != "g0" || len(state.Ledger) != 0 || len(state.Generations) != 1 {
		t.Fatalf("an unsettled effect was promoted into the head or ledger: %+v", state)
	}
	lcRequireKinds(t, "provider", before.providerTrace, "provider_user_admitted", "provider_exchange", "provider_gate_decision")
	for round := 0; round < 2; round++ {
		governedCrashProcess(t, "recover", root, 0)
		governedCrashProcess(t, "replay-strict", root, 0)
		replay := lcReadReplay(t, root)
		if !replay.OpenQuarantined || replay.Projection != nil {
			t.Fatalf("round %d: the unresolved effect must quarantine the Store: %+v", round, replay)
		}
		after := lcRequireJournalsUnchanged(t, before)
		if !bytes.Equal(after.repositoryHead, before.repositoryHead) {
			t.Fatal("recovery changed the head of a quarantined Store")
		}
		lcRequireGenerations(t, root, "g0", "g1")
		if data, err := os.ReadFile(filepath.Join(root, "store", "generations", "g1", "task.txt")); err != nil || string(data) != "after\n" {
			t.Fatalf("recovery altered or re-created the observed effect: %q err=%v", data, err)
		}
	}
}

func TestLifecycleCutpointLC07OutcomeBeforeCheckpoint(t *testing.T) {
	if os.Getenv(governedCrashPhase) != "" {
		governedCrashChild(t)
		return
	}
	t.Run("mutation", func(t *testing.T) {
		root := privateGovernedTestRoot(t)
		governedCrashProcess(t, "lc07-outcome-sync", root, governedCrashExit)
		before := lcRead(t, root)
		if string(before.marker) != "outcome-durable:checkpoint-not-reconciled\n" {
			t.Fatalf("crash marker=%q", before.marker)
		}
		lcRequireWriteIntent(t, before, true)
		lcRequireGenerations(t, root, "g0", "g1")
		if len(before.admissionTrace.Records) != 1 {
			t.Fatalf("admission records=%d, want 1", len(before.admissionTrace.Records))
		}
		state := before.state(t)
		if state.HeadGeneration != "g0" || len(state.Ledger) != 0 || len(state.Generations) != 1 {
			t.Fatalf("the head was persisted before the cut: %+v", state)
		}
		governedCrashProcess(t, "replay-permissive", root, 0)
		after := lcRequireJournalsUnchanged(t, before)
		replay := lcReadReplay(t, root)
		if replay.OpenError != "" {
			t.Fatalf("the committed prefix must reopen: %+v", replay)
		}
		repaired := after.state(t)
		if repaired.HeadGeneration != "g1" || len(repaired.Ledger) != 1 || len(repaired.Generations) != 2 || len(repaired.GenerationEffects) != 2 ||
			repaired.GenerationEffects[1] != before.primaryTrace.Effects[1].ID {
			t.Fatalf("recovery did not advance the head to the durable outcome exactly once: %+v", repaired)
		}
		lcRequireGenerations(t, root, "g0", "g1")
		lcRequirePendingProviderResult(t, replay, after, "g1", 1)
		// A second fresh process finds the repaired head and changes nothing.
		governedCrashProcess(t, "replay-permissive", root, 0)
		again := lcRequireJournalsUnchanged(t, before)
		if !bytes.Equal(again.repositoryHead, after.repositoryHead) {
			t.Fatal("a second recovery rewrote an already repaired head")
		}
	})
	t.Run("read", func(t *testing.T) {
		root := privateGovernedTestRoot(t)
		governedCrashProcess(t, "lc07-read-outcome-sync", root, governedCrashExit)
		before := lcRead(t, root)
		if string(before.marker) != "outcome-durable:checkpoint-not-reconciled\n" {
			t.Fatalf("crash marker=%q", before.marker)
		}
		if len(before.primaryTrace.Effects) != 2 || before.primaryTrace.Effects[1].Unresolved || before.primaryTrace.Effects[1].Outcome != "success" {
			t.Fatalf("effects=%+v, want a durable read outcome", before.primaryTrace.Effects)
		}
		lcRequireGenerations(t, root, "g0")
		governedCrashProcess(t, "replay-permissive", root, 0)
		after := lcRequireJournalsUnchanged(t, before)
		replay := lcReadReplay(t, root)
		if replay.OpenError != "" || !bytes.Equal(after.repositoryHead, before.repositoryHead) || after.state(t).HeadGeneration != "g0" {
			t.Fatalf("a read must not create a checkpoint: %+v head changed=%t", replay, !bytes.Equal(after.repositoryHead, before.repositoryHead))
		}
		lcRequireGenerations(t, root, "g0")
		lcRequirePendingProviderResult(t, replay, after, "g0", 0)
	})
}

func TestLifecycleCutpointLC08AfterCheckpoint(t *testing.T) {
	if os.Getenv(governedCrashPhase) != "" {
		governedCrashChild(t)
		return
	}
	root := privateGovernedTestRoot(t)
	governedCrashProcess(t, "write-after-outcome", root, governedCrashExit)
	before := lcRead(t, root)
	if string(before.marker) != "durable-outcome:g1\n" {
		t.Fatalf("crash marker=%q", before.marker)
	}
	effect := lcRequireWriteIntent(t, before, true)
	state := before.state(t)
	if state.HeadGeneration != "g1" || len(state.Ledger) != 1 || len(state.Generations) != 2 ||
		len(state.GenerationEffects) != 2 || state.GenerationEffects[1] != effect.ID {
		t.Fatalf("the checkpoint is not durable at the cut: %+v", state)
	}
	lcRequireKinds(t, "provider", before.providerTrace, "provider_user_admitted", "provider_exchange", "provider_gate_decision")
	governedCrashProcess(t, "recover-after-outcome", root, 0)
	governedCrashProcess(t, "replay-permissive", root, 0)
	after := lcRequireJournalsUnchanged(t, before)
	if !bytes.Equal(after.repositoryHead, before.repositoryHead) {
		t.Fatal("recovery rewrote a durable checkpoint")
	}
	lcRequireGenerations(t, root, "g0", "g1")
	replay := lcReadReplay(t, root)
	lcRequirePendingProviderResult(t, replay, after, "g1", 1)
}

func lcRequireReplayRefusal(t *testing.T, replay lcReplay, want string) {
	t.Helper()
	if replay.OpenError != "" || replay.Projection != nil || !strings.Contains(replay.Error, want) {
		t.Fatalf("production replay must refuse with %q: %+v", want, replay)
	}
}

// lcRequireDecisionCorrelation: the durable gate decision still names the
// captured call and its exact arguments digest.
func lcRequireDecisionCorrelation(t *testing.T, a lcArtifacts) {
	t.Helper()
	var exchange providerExchangeEvidence
	var decision gate.Decision
	if err := unmarshalGovernedJSON(a.providerTrace.Records[1].Event.Data, &exchange); err != nil {
		t.Fatal(err)
	}
	if err := unmarshalGovernedJSON(a.providerTrace.Records[2].Event.Data, &decision); err != nil {
		t.Fatal(err)
	}
	digest, err := protocol.CanonicalArgumentsDigest(exchange.ToolCall.Name, exchange.ToolCall.Arguments)
	if err != nil || decision.Verdict != gate.Allow || decision.ToolCallID != exchange.ToolCall.ID || decision.ToolCallID != "crash-write" ||
		decision.Tool != "write" || decision.CanonicalArgumentsDigest != digest {
		t.Fatalf("decision does not stay bound to the captured call: decision=%+v digest=%q err=%v", decision, digest, err)
	}
}

// lcRequirePendingProviderResult: the Store holds the durable operation and
// the provider result is still unreleased; counters agree with the journals.
func lcRequirePendingProviderResult(t *testing.T, replay lcReplay, a lcArtifacts, wantHead string, wantAdmissions uint64) {
	t.Helper()
	if replay.OpenError != "" || replay.Error != "" || replay.Projection == nil {
		t.Fatalf("recovery must reopen and replay: %+v", replay)
	}
	p := replay.Projection
	if p.Provider.Disposition != providerbridge.DispositionPendingProviderResult || p.Provider.ProviderGenerationID != "g0" ||
		p.Provider.CapturedCallCount != 1 || p.Provider.DecisionCount != 1 || p.Provider.ResultCount != 0 ||
		p.StoreHeadGeneration != wantHead || p.StoreOperationCount != 1 || p.StoreEffectCount != 1 ||
		p.StoreDispositionQuarantined || p.AdmissionRecordCount != wantAdmissions ||
		p.StoreAuditRecordCount != uint64(len(a.primaryTrace.Records)) ||
		p.StoreAuditHead != a.primaryTrace.Records[len(a.primaryTrace.Records)-1].Hash ||
		!VerifyGovernedTranscriptProjectionCommitment(*p) {
		t.Fatalf("checkpoint identity, lineage or counters are wrong: head=%s want=%s %+v", p.StoreHeadGeneration, wantHead, p)
	}
}
