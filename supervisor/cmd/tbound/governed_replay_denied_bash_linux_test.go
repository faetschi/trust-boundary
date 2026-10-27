//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
	"tbound/supervisor/internal/workspace"
)

const (
	replayDeniedBashInputsEnv = "TBOUND_TEST_REPLAY_DENIED_BASH_INPUTS"
	replayDeniedBashRunID     = "run-replay-denied-bash"
)

type replayDeniedBashCase struct {
	Name                string
	ProviderJournalPath string
	// CommandJournalPath is empty when the history is replayed without an old command journal.
	CommandJournalPath string
	// Refusal is a fragment of the error the replay must return; empty means the history must be accepted.
	Refusal string
	// RepinProvider pins the forged transcript at its own journal head with no projection, as a host that
	// checkpointed the forgery would, so that only the Store cross-link can refuse it.
	RepinProvider bool
}

type replayDeniedBashInputs struct {
	StoreRootPath    string
	StoreJournalPath string
	Profile          piruntime.HostProfile
	Template         piruntime.D09CellTemplate
	// Live is the workflow checkpoint the live process left behind after the final answer.
	Live           workflowrecovery.Pins
	DeniedEffectID string
	ReadEffectID   string
	Cases          []replayDeniedBashCase
}

// A real governed history: an ALLOWed Bash command runs in a D09 cell and delta admission refuses its change
// (denied_after_effect), then the conversation continues with an allowed read and a final answer. A fresh
// process replays the persisted provider transcript, Store and command journal through the recovery entry.
func TestGovernedReplayRecoversADeniedD09BashInAFreshProcess(t *testing.T) {
	if path := os.Getenv(replayDeniedBashInputsEnv); path != "" {
		replayDeniedBashChild(t, path)
		return
	}
	inputs := buildReplayDeniedBashInputs(t)
	inputPath := filepath.Join(privateGovernedTestRoot(t), "replay-inputs.json")
	if err := os.WriteFile(inputPath, mustJSON(inputs), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^TestGovernedReplayRecoversADeniedD09BashInAFreshProcess$", "-test.v", "-test.count=1")
	child.Env = []string{replayDeniedBashInputsEnv + "=" + inputPath, "TMPDIR=" + os.TempDir()}
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("fresh-process replay: %v\n%s", err, output)
	}
	for _, test := range inputs.Cases {
		if !bytes.Contains(output, []byte("--- PASS: TestGovernedReplayRecoversADeniedD09BashInAFreshProcess/"+test.Name+" ")) {
			t.Fatalf("fresh process did not run case %q to a pass:\n%s", test.Name, output)
		}
	}
	t.Logf("fresh-process replay:\n%s", output)
}

func buildReplayDeniedBashInputs(t *testing.T) replayDeniedBashInputs {
	t.Helper()
	commandJournalPath := filepath.Join(privateGovernedTestRoot(t), "command.jsonl")
	commandJournal, err := audit.Open(commandJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = commandJournal.Close() })
	profile, template := newBashRunProfile(t)
	templateDigest, err := template.Digest()
	if err != nil {
		t.Fatal(err)
	}
	runner := newProductionCommandRunner(func(leaseID string) (sessionrepo.CommandRunner, error) {
		cell := &sealedD09Cell{leaseID: leaseID, template: template, templateDigest: templateDigest}
		return newProductionD09BashRunnerWithInvoker(leaseID, cell, commandJournal, replayDeniedBashRunID, workflowVerticalSessionID,
			workflowVerticalID, workflowVerticalConversation, profile, template), nil
	})
	// The fixture authorizes only out.txt, so the cell's task.txt write is refused after execution. Two spare
	// retries lift the host-call and token limits to cover the three turns.
	f := newWorkflowVerticalFixture(t, false, 2, []providerbridge.DevelopmentProviderTurn{
		{ResponseID: "replay-denied-r1", CallID: "replay-denied-bash", Tool: "bash",
			Arguments: json.RawMessage(`{"command":"printf 'bash-stage\\n' >> task.txt"}`)},
		{ResponseID: "replay-denied-r2", CallID: "replay-denied-read", Tool: "read", Arguments: json.RawMessage(`{"path":"task.txt"}`)},
		{ResponseID: "replay-denied-r3", Text: "done"},
	}, runner)
	f.authority.requireEvidencedBashContainment = true
	executor, err := providerbridge.NewExecutor(f.conversation, f.durable)
	if err != nil {
		t.Fatal(err)
	}
	bashProposal, bashDecision := ledgerDenyTurn(t, f, "replay-denied-bash", gate.Allow)
	var denied *protocol.AdmissionDeniedError
	if _, err := executor.Execute(f.ctx, bashProposal, bashDecision); !errors.As(err, &denied) ||
		denied.ReasonCode != sessionrepo.DenyReasonUncoveredPath {
		t.Fatalf("the refused Bash command was not released as an admission denial: %v", err)
	}
	readProposal, readDecision := ledgerDenyTurn(t, f, "replay-denied-read", gate.Allow)
	readOutput, err := executor.Execute(f.ctx, readProposal, readDecision)
	if err != nil || !bytes.Contains(readOutput, []byte("vertical fixture")) || bytes.Contains(readOutput, []byte("bash-stage")) {
		t.Fatalf("the continuation read must see the unpromoted baseline: %s err=%v", readOutput, err)
	}
	requireFinalAnswer(t, f, "done", 5, 3072)

	bundle, err := f.store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Operations) != 2 || !deniedAfterEffect(bundle.Operations[0]) || bundle.Operations[1].Outcome != "success" ||
		len(bundle.ApprovedDeltaLedger) != 0 || len(bundle.Generations) != 1 {
		t.Fatalf("live history is not a denied Bash followed by a read: %+v", bundle.Operations)
	}
	live, _ := f.ledger.Snapshot()
	inputs := replayDeniedBashInputs{
		StoreRootPath:    filepath.Join(filepath.Dir(f.providerJournalPath), "store"),
		StoreJournalPath: filepath.Join(filepath.Dir(f.providerJournalPath), "store-audit.jsonl"),
		Profile:          profile, Template: template, Live: live.Pins,
		DeniedEffectID: bundle.Operations[0].EffectID, ReadEffectID: bundle.Operations[1].EffectID,
	}
	providerTrace, err := f.providerJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	commandTrace, err := commandJournal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	if len(settledRecords(t, commandJournal)) != 1 {
		t.Fatal("the live command journal must hold exactly the denied settlement")
	}

	f.conversation.Close()
	for _, closer := range []func() error{f.store.Close, f.storeJournal.Close, f.providerJournal.Close, commandJournal.Close} {
		if err := closer(); err != nil {
			t.Fatal(err)
		}
	}

	root := privateGovernedTestRoot(t)
	derive := func(name string, source audit.Trace, keep func(int, audit.Event) (audit.Event, bool)) string {
		t.Helper()
		derived := derivedJournal(t, source, keep)
		trace, err := derived.Trace()
		if err != nil {
			t.Fatal(err)
		}
		data, err := marshalGovernedAuditRecords(trace.Records)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, name+".jsonl")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	forgeDenial := func(name string, mutate func(*providerAdmissionDeniedEvidence)) string {
		t.Helper()
		forged := false
		path := derive(name, providerTrace, func(_ int, event audit.Event) (audit.Event, bool) {
			if event.Kind == "provider_admission_denied" {
				var evidence providerAdmissionDeniedEvidence
				if err := unmarshalGovernedJSON(event.Data, &evidence); err != nil {
					t.Fatal(err)
				}
				mutate(&evidence)
				event.Data, forged = mustJSON(evidence), true
			}
			return event, true
		})
		if !forged {
			t.Fatal("the provider transcript holds no admission denial to forge")
		}
		return path
	}
	missing := derive("command-missing", commandTrace, func(_ int, event audit.Event) (audit.Event, bool) {
		return event, event.Kind != productionD09SettledKind
	})
	promotedShape := derive("command-promoted-shape", commandTrace, func(_ int, event audit.Event) (audit.Event, bool) {
		if event.Kind == productionD09SettledKind {
			var payload productionD09DeniedCommandSettledEvent
			if err := unmarshalGovernedJSON(event.Data, &payload); err != nil {
				t.Fatal(err)
			}
			payload.Operation.Outcome = "success"
			event.Data = mustJSON(productionD09CommandSettledEvent{SchemaVersion: productionD09SettledSchemaSuccess,
				RunID: payload.RunID, SessionID: payload.SessionID, WorkflowID: payload.WorkflowID,
				ConversationID: payload.ConversationID, ProfileID: payload.ProfileID, ProfileDigest: payload.ProfileDigest,
				ProviderCall: payload.ProviderCall, Operation: payload.Operation, Transition: payload.DeniedCandidate,
				ReceiptDigest: payload.ReceiptDigest, Receipt: payload.Receipt})
		}
		return event, true
	})
	const denialRefusal = "admission denial has no unique Store-denied decision with this effect and reason"
	// The accepted case runs last: recovery appends its idle lineage retirement to the original provider journal.
	inputs.Cases = []replayDeniedBashCase{
		{Name: "settlement_missing_from_command_journal", ProviderJournalPath: f.providerJournalPath, CommandJournalPath: missing,
			Refusal: "historical provider Bash result has no retained D09 command receipt"},
		{Name: "promoted_shape_settlement_for_denied_bash", ProviderJournalPath: f.providerJournalPath, CommandJournalPath: promotedShape,
			Refusal: "old D09 command receipt does not bind the exact provider proposal and Store operation"},
		{Name: "no_old_command_context", ProviderJournalPath: f.providerJournalPath,
			Refusal: "historical Bash effects require their independently held old command context"},
		{Name: "released_denial_names_another_reason", CommandJournalPath: commandJournalPath, Refusal: denialRefusal, RepinProvider: true,
			ProviderJournalPath: forgeDenial("provider-other-reason", func(evidence *providerAdmissionDeniedEvidence) {
				result, err := protocol.DecodeResult(evidence.Result)
				if err != nil {
					t.Fatal(err)
				}
				result.ReasonCode, evidence.ReasonCode = sessionrepo.DenyReasonProtectedPath, sessionrepo.DenyReasonProtectedPath
				if evidence.Result, err = protocol.MarshalResult(result); err != nil {
					t.Fatal(err)
				}
				evidence.ResultDigest = digestBytes(evidence.Result)
			})},
		{Name: "released_denial_names_another_effect", CommandJournalPath: commandJournalPath, Refusal: denialRefusal, RepinProvider: true,
			ProviderJournalPath: forgeDenial("provider-other-effect", func(evidence *providerAdmissionDeniedEvidence) {
				evidence.EffectID = "sessionrepo-effect-another"
			})},
		{Name: "denied_bash_then_read_and_final_answer", ProviderJournalPath: f.providerJournalPath, CommandJournalPath: commandJournalPath},
	}
	return inputs
}

func replayDeniedBashChild(t *testing.T, inputPath string) {
	t.Helper()
	data, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	var inputs replayDeniedBashInputs
	if err := json.Unmarshal(data, &inputs); err != nil {
		t.Fatal(err)
	}
	if len(inputs.Cases) != 6 {
		t.Fatalf("fresh process received %d cases, want the continued history and five refusals", len(inputs.Cases))
	}
	for _, test := range inputs.Cases {
		t.Run(test.Name, func(t *testing.T) {
			recovered, err := recoverReplayDeniedBash(t, inputs, test)
			if test.Refusal != "" {
				if err == nil || !strings.Contains(err.Error(), test.Refusal) {
					t.Fatalf("want replay refusal containing %q, got %v", test.Refusal, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("the untouched denied history was refused: %v", err)
			}
			requireReplayedDeniedBash(t, inputs, recovered)
		})
	}
}

type replayedDeniedBash struct {
	projection  GovernedRecoveryProjection
	verifier    *governedStoreReplayVerifier
	bundle      sessionrepo.EvidenceBundle
	transcripts []GovernedRecoveryTranscript
	storeJ      *audit.Journal
	store       *sessionrepo.Store
	command     governedRecoveryCommandVerifier
	historical  *governedHistoricalStoreVerifier
}

// recoverReplayDeniedBash opens the retained artifacts as the production recovery does (read-only Store
// preview, held journals, host-pinned transcript, old command history) and runs ReplayGovernedHistoryForRecovery.
func recoverReplayDeniedBash(t *testing.T, inputs replayDeniedBashInputs, test replayDeniedBashCase) (replayedDeniedBash, error) {
	t.Helper()
	var recovered replayedDeniedBash
	open := func(path string) *audit.Journal {
		t.Helper()
		journal, err := openExistingRecoveryJournal(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = journal.Close() })
		return journal
	}
	storeJournal, providerJournal := open(inputs.StoreJournalPath), open(test.ProviderJournalPath)
	storeRoot, err := openExistingPrivateRecoveryDirectory(inputs.StoreRootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer storeRoot.Close()
	managed, err := sessionlaunch.OpenRecoveryPreview(storeRoot, sessionrepo.Options{Journal: storeJournal,
		PolicyDigest: governedReplayStorePolicy, MetadataPolicyDigest: governedReplayMetaPolicy, Limits: workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: governedReplayXattrPolicy,
			Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}})
	if err != nil {
		t.Fatalf("recovery preview of the persisted Store: %v", err)
	}
	t.Cleanup(func() { _ = managed.Close() })
	if recovered.bundle, err = managed.Store().EvidenceBundle(); err != nil {
		t.Fatal(err)
	}
	policy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "read", Effect: gate.EffectAllow}, {Tool: "bash", Effect: gate.EffectAllow}}})
	if err != nil {
		t.Fatal(err)
	}

	pin := inputs.Live.ProviderTranscripts[0]
	identity, err := recoveryFileIdentityDigest(test.ProviderJournalPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if test.Refusal == "" && identity != pin.JournalIdentityDigest {
		t.Fatalf("the fresh process found a different provider journal than the checkpoint pinned: %s", identity)
	}
	pin.JournalIdentityDigest = identity
	if test.RepinProvider {
		providerTrace, err := providerJournal.Trace()
		if err != nil {
			t.Fatal(err)
		}
		pin.JournalCount, pin.JournalHead = uint64(len(providerTrace.Records)), providerTrace.Records[len(providerTrace.Records)-1].Hash
		pin.ProjectionCount, pin.ProjectionDigest, pin.ProjectionHead = 0, "", ""
	}
	profile := broker.Profile{ID: workflowVerticalProfile, Model: providerbridge.ApprovedModel,
		Messages:     providerbridge.InitialPromptMessages(openrouter.Route{}, governedReplaySystem, governedReplayDeveloper),
		ToolManifest: broker.DeclaredToolManifest(), ToolCallIssuer: governedCallIssuer, ResponseIDIssuer: governedResponseIssuer}
	config := providerbridge.Config{WorkflowID: workflowVerticalID, ConversationID: workflowVerticalConversation,
		ProfileID: workflowVerticalProfile, SystemPrompt: governedReplaySystem, DeveloperPrompt: governedReplayDeveloper}
	transcript := GovernedRecoveryTranscript{Journal: providerJournal, JournalPath: test.ProviderJournalPath, IdentityDigest: identity,
		Pin: pin, Replay: GovernedReplayPins{Provider: providerbridge.RecoveryPins{Config: config,
			Model: providerbridge.ApprovedModel, Policy: policy}, Profile: profile,
			StorePolicyDigest: governedReplayStorePolicy, MetadataPolicyDigest: governedReplayMetaPolicy,
			BashOutputPaths: []string{workflowVerticalBashOutput}}}

	request := governedRecoveryRequest{WorkflowID: workflowVerticalID, Transcripts: []GovernedRecoveryTranscript{transcript},
		ProductionProfile: inputs.Profile, D09Template: inputs.Template}
	if test.CommandJournalPath != "" {
		commandJournal := open(test.CommandJournalPath)
		commandIdentity, err := recoveryFileIdentityDigest(test.CommandJournalPath, false)
		if err != nil {
			t.Fatal(err)
		}
		commandTrace, err := commandJournal.Trace()
		if err != nil {
			t.Fatal(err)
		}
		request.CommandHistories = []productionRecoveryCommandJournal{{Journal: commandJournal, Path: test.CommandJournalPath,
			Pin: productionHistoricalCommandPin{IdentityDigest: commandIdentity, Head: auditHeadHash(commandTrace.Records),
				RecordCount: uint64(len(commandTrace.Records))},
			Sessions: []productionRecoveryCommandSession{{RunID: replayDeniedBashRunID, SessionID: workflowVerticalSessionID,
				ConversationID: workflowVerticalConversation}}}}
	}
	recovered.command, _, err = openRecoveryCommandVerifier(request)
	if err != nil {
		return recovered, err
	}
	if recovered.command != nil && test.Refusal == "" {
		// The pre-replay steps of prepareGovernedRecovery that the retirement pins depend on.
		if recovered.historical, err = newGovernedHistoricalStoreVerifier(storeJournal, governedReplayStorePolicy,
			governedReplayMetaPolicy, recovered.command.VerifySettlement); err != nil {
			t.Fatal(err)
		}
		if err := recovered.historical.VerifyBundle(recovered.bundle); err != nil {
			t.Fatalf("historical Store verifier over the denied Bash effect: %v", err)
		}
		retirement := governedUnleasedCallRetirementVerifier{historical: recovered.historical, command: recovered.command}
		providerPins := &transcript.Replay.Provider
		providerPins.PreviousOwnerEpoch, providerPins.RetirementOwnerEpoch = inputs.Live.OwnerEpoch, inputs.Live.OwnerEpoch+1
		providerPins.CheckpointRecordCount, providerPins.CheckpointRecordHead = pin.JournalCount, pin.JournalHead
		providerPins.RetirementVerifier, providerPins.IdleRetirementVerifier = retirement, retirement
		providerPins.RequireHostCallReservations = true
	}
	recovered.transcripts, recovered.storeJ, recovered.store = []GovernedRecoveryTranscript{transcript}, storeJournal, managed.Store()
	recovered.projection, recovered.verifier, err = ReplayGovernedHistoryForRecovery(storeJournal, managed.Store(),
		recovered.transcripts, recoveryCommandVerifiers(recovered.command)...)
	return recovered, err
}

// requireReplayedDeniedBash checks what the accepted replay recognized: the denied Bash is settled-denied
// (no promotion, no generation advanced), the continuation was joined to the Store, and the state recovery
// would pin matches the checkpoint the live process left.
func requireReplayedDeniedBash(t *testing.T, inputs replayDeniedBashInputs, recovered replayedDeniedBash) {
	t.Helper()
	bundle, projection := recovered.bundle, recovered.projection
	if len(bundle.Operations) != 2 || !deniedAfterEffect(bundle.Operations[0]) || bundle.Operations[0].EffectID != inputs.DeniedEffectID ||
		bundle.Operations[1].Outcome != "success" || bundle.Operations[1].EffectID != inputs.ReadEffectID ||
		len(bundle.ApprovedDeltaLedger) != 0 || len(bundle.Generations) != 1 {
		t.Fatalf("persisted Store is not a refused Bash followed by a read: %+v", bundle.Operations)
	}
	if len(projection.Transcripts) != 1 {
		t.Fatalf("replay projected %d transcripts, want 1", len(projection.Transcripts))
	}
	provider := projection.Transcripts[0].Provider
	if provider.Disposition != providerbridge.DispositionAwaitingHostPrompt || provider.CapturedCallCount != 2 ||
		provider.DecisionCount != 2 || provider.ResultCount != 2 ||
		provider.ProviderGenerationID != bundle.Baseline.ID || provider.ProviderTreeDigest != bundle.Baseline.TreeDigest ||
		projection.Transcripts[0].StoreOperationCount != 2 || projection.Transcripts[0].StoreEffectCount != 2 ||
		projection.StoreTip != bundle.Baseline.ID || projection.StoreTree != bundle.Baseline.TreeDigest {
		t.Fatalf("replay did not join the continued history to the unpromoted Store tip: %+v", projection)
	}

	var deniedDecisions, releasedDecisions int
	for _, decision := range recovered.verifier.decisions {
		if decision.denied {
			deniedDecisions++
			if decision.entry.Outcome != "denied_after_effect" || decision.request.EffectID != inputs.DeniedEffectID ||
				decision.denyReason != sessionrepo.DenyReasonUncoveredPath || !decision.resultSeen ||
				decision.toGen != "" || decision.toTree != "" || decision.entry.OutputGeneration != "" {
				t.Fatalf("denied Bash was not recognized as settled-denied: %+v", decision)
			}
		} else if decision.resultSeen && decision.entry.Outcome == "success" && decision.toGen == bundle.Baseline.ID {
			releasedDecisions++
		}
	}
	if deniedDecisions != 1 || releasedDecisions != 1 {
		t.Fatalf("replayed decisions: %d denied, %d released read; want 1 and 1", deniedDecisions, releasedDecisions)
	}

	// Recovery next retires the idle lineage and strictly replays the whole history again before pinning it.
	recovered.verifier.resetForReadOnlyReplay()
	retirementPins := replayedProviderPins(recovered.transcripts[0].Replay.Provider, provider)
	if _, err := providerbridge.AppendIdleProviderLineageRetirement(recovered.transcripts[0].Journal,
		retirementPins, recovered.verifier); err != nil {
		t.Fatalf("retire the idle lineage after the denied Bash history: %v", err)
	}
	strict, err := ReplayGovernedHistory(recovered.storeJ, recovered.store, recovered.transcripts, recoveryCommandVerifiers(recovered.command)...)
	if err != nil {
		t.Fatalf("strict replay of the retired denied Bash history: %v", err)
	}
	observed, err := observedRecoveryPins(inputs.Live, "", strict, recovered.transcripts)
	if err != nil {
		t.Fatalf("pins observed from the replayed denied Bash history: %v", err)
	}
	if strict.Transcripts[0].Provider.Disposition != providerbridge.DispositionRetired ||
		observed.ProviderDisposition != workflowrecovery.DispositionRetired ||
		observed.StoreTipGenerationID != inputs.Live.StoreTipGenerationID || observed.StoreTipTreeDigest != inputs.Live.StoreTipTreeDigest ||
		observed.StoreTipGenerationID != inputs.Live.StoreBaselineGenerationID ||
		observed.StoreJournalHead != inputs.Live.StoreJournalHead || observed.StoreJournalCount != inputs.Live.StoreJournalCount ||
		observed.AdmissionJournalHead != inputs.Live.AdmissionJournalHead || observed.AdmissionJournalCount != inputs.Live.AdmissionJournalCount ||
		observed.PendingCallDisposition != inputs.Live.PendingCallDisposition {
		t.Fatalf("recovered pins differ from the live checkpoint:\nlive=%+v\nobserved=%+v", inputs.Live, observed)
	}
}
