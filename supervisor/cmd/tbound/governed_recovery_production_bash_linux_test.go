//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflow"
)

// The production-bash scenario of the combined harness. The original process runs one Bash call through the
// production D09 runner (a sealed cell stands in for Podman); recovery prepares and activates through the production
// sequence with that history; publication is checked against the activated session.
const (
	combinedProductionScenario   = "production-bash"
	combinedResumedScenario      = "production-bash-resumed"
	combinedProductionCall       = "combined-production-bash-call"
	combinedResumedCall          = "combined-resumed-bash-call"
	combinedProductionStop       = "managed-live-root"
	combinedOriginalSession      = "combined-recovery-session-g1"
	combinedOriginalConversation = "combined-recovery-conversation-g1"
)

// combinedSessionIdentity names the session and conversation of recovery generation gen. Generation 1 is the original
// process; each later generation is a recovered session with its own identities.
func combinedSessionIdentity(gen uint64) (string, string) {
	if gen == 1 {
		return combinedOriginalSession, combinedOriginalConversation
	}
	return fmt.Sprintf("%s-session-g%d", combinedRecoveryWorkflow, gen), fmt.Sprintf("%s-conversation-g%d", combinedRecoveryWorkflow, gen)
}

// combinedPreviousSessions are the sessions whose command records a recovery of generation gen finds in the journal.
func combinedPreviousSessions(gen uint64) []productionRecoveryCommandSession {
	sessions := make([]productionRecoveryCommandSession, 0, gen-1)
	for previous := uint64(1); previous < gen; previous++ {
		session, conversation := combinedSessionIdentity(previous)
		sessions = append(sessions, productionRecoveryCommandSession{RunID: session, SessionID: session, ConversationID: conversation})
	}
	return sessions
}

// combinedResumedBashTurn is the scripted provider turn of a Bash call the recovered session makes after activation.
func combinedResumedBashTurn() providerbridge.DevelopmentProviderTurn {
	return providerbridge.DevelopmentProviderTurn{ResponseID: "combined-resumed-bash-response", CallID: combinedResumedCall,
		Tool: "bash", Arguments: json.RawMessage(`{"command":"printf 'bash-stage\\n' >> task.txt"}`)}
}

// combinedCommandRunner is what a combined-recovery phase hands the authority and the durable executor: the
// development runner or the production D09 runner.
type combinedCommandRunner interface {
	governedCommandRunner
	sessionrepo.CommandRunner
}

// combinedProductionBashTurn is the scripted provider turn of the original Bash call. The cell appends to task.txt,
// which the scenario's Bash output scope authorizes, so the call promotes.
func combinedProductionBashTurn() providerbridge.DevelopmentProviderTurn {
	return providerbridge.DevelopmentProviderTurn{ResponseID: "combined-production-bash-response", CallID: combinedProductionCall,
		Tool: "bash", Arguments: json.RawMessage(`{"command":"printf 'bash-stage\\n' >> task.txt"}`)}
}

// combinedProductionRunner is the production D09 runner of one phase: identities from that phase's session, and the
// same command journal the history is read from.
func combinedProductionRunner(t *testing.T, commandJournal *audit.Journal, profile piruntime.HostProfile,
	template piruntime.D09CellTemplate, session, conversation string) *productionCommandRunner {
	t.Helper()
	templateDigest, err := template.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return newProductionCommandRunner(func(leaseID string) (sessionrepo.CommandRunner, error) {
		cell := &sealedD09Cell{leaseID: leaseID, template: template, templateDigest: templateDigest}
		return newProductionD09BashRunnerWithInvoker(leaseID, cell, commandJournal, session, session,
			combinedRecoveryWorkflow, conversation, profile, template), nil
	})
}

// combinedBashCall runs one scripted Bash call of generation gen through the production runner and records the call's
// proposal-call origin in origin-g<gen>.json. A later recovery compares its terminal origin against that file.
func combinedBashCall(t *testing.T, root, callID string, gen uint64, conversation *providerbridge.Conversation,
	durable *DurableExecutor, policy gate.Policy) {
	t.Helper()
	ctx := context.Background()
	step := &workflowVerticalFixture{ctx: ctx, conversation: conversation, policy: policy}
	proposal, decision := ledgerDenyTurn(t, step, callID, gate.Allow)
	executor, err := providerbridge.NewExecutor(conversation, durable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(ctx, proposal, decision); err != nil {
		t.Fatalf("generation %d Bash call %s: %v", gen, callID, err)
	}
	proposalID, err := durableTrustedIdentity("proposal", proposal, decision)
	if err != nil {
		t.Fatal(err)
	}
	combinedRecoveryWriteJSON(t, filepath.Join(root, fmt.Sprintf("origin-g%d.json", gen)), delta.OperationIdentity{
		Kind: delta.OperationProposalCall, ProposalID: proposalID, CallIssuer: governedCallIssuer, CallID: callID})
}

// combinedRecoveryCommandHistory opens the scenario's command journal once and pins its records as recovery reads
// them. The journal is the one the production runner appends to, and the one the frozen history reads.
func combinedRecoveryCommandHistory(t *testing.T, path string, sessions []productionRecoveryCommandSession) (*audit.Journal,
	[]productionRecoveryCommandJournal) {
	t.Helper()
	journal, err := openExistingRecoveryJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	identity, err := recoveryFileIdentityDigest(path, false)
	if err != nil {
		t.Fatal(err)
	}
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	return journal, []productionRecoveryCommandJournal{{Journal: journal, Path: path,
		Pin: productionHistoricalCommandPin{IdentityDigest: identity, Head: auditHeadHash(trace.Records),
			RecordCount: uint64(len(trace.Records))},
		Sessions: sessions}}
}

// combinedPinnedProfileDigest is the profile digest the checkpoint pins: the production profile for a production
// scenario, the provider profile otherwise.
func combinedPinnedProfileDigest(template governedRecoveryRequest) string {
	if template.ProductionProfile.Mode == piruntime.ModeProduction {
		return template.ProductionProfile.Digest
	}
	return template.ProviderProfileDigest
}

// combinedRecoveryPublish runs the publication checks of the activated session: CheckPublicationPrerequisites with the
// session's own authority, its prepared source and the production runner. It stops at the managed live-root permit,
// the first check this harness does not satisfy. Every check before that stop, including the Bash checks of each
// historical operation, must have passed.
func combinedRecoveryPublish(t *testing.T, root string, session *governedRecoverySession, runner *productionCommandRunner,
	profile piruntime.HostProfile, template piruntime.D09CellTemplate, sessionID, conversationID string) string {
	t.Helper()
	bundle, err := session.Store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	proofJournal, err := audit.Open(filepath.Join(root, "launch-proof-"+sessionID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proofJournal.Close() })
	baseline := session.Store.BaselineGeneration()
	sourceBinding := "sha256:" + strings.Repeat("d", 64)
	_, _, proof := productionProofFixture()
	proof.TemplateDigest, proof.LauncherProcessPID = profile.ContainmentProfileDigest, 4242
	proof.ImageDigest, proof.ImageRef = template.ExpectedImageDigest, "registry.invalid/tbound@"+template.ExpectedImageDigest
	lifecycle := settledWorkerLifecycle(g1SettledWorker, true, nil)
	lifecycle.markTurnCompleted()
	if err := journalProductionLaunchProof(proofJournal, lifecycle, sessionID, sessionID, combinedRecoveryWorkflow,
		conversationID, baseline.ID(), sourceBinding, profile, template, 4242, proof); err != nil {
		t.Fatal(err)
	}
	trust := &productionTrust{authority: session.Authority, storeJournal: proofJournal, baseline: baseline,
		profile: profile, template: template, sessionID: sessionID, workflowID: combinedRecoveryWorkflow,
		conversationID: conversationID, sourceGenerationID: baseline.ID(), sourceBindingDigest: sourceBinding,
		policyDigest: governedReplayStorePolicy, metadataPolicyDigest: governedReplayMetaPolicy,
		lifecycle: lifecycle, runner: runner, runID: sessionID}
	err = trust.CheckPublicationPrerequisites(context.Background(), workflow.EvidenceRequest{Bundle: bundle})
	if !errors.Is(err, ErrManagedLiveRootUnavailable) {
		t.Fatalf("publication Bash checks refused before the live-root permit: %v", err)
	}
	return combinedProductionStop
}

// combinedProductionProfile is the production profile and D09 template of the production-bash scenario. Their provider
// profile, runtime and image pins are the combined harness's own constants, which the production profile check of
// recovery compares against the request. The containment digest follows the template.
func combinedProductionProfile(t *testing.T) (piruntime.HostProfile, piruntime.D09CellTemplate) {
	t.Helper()
	profile, template := newBashRunProfile(t)
	profile.ProviderProfile = combinedRecoveryProfile
	profile.ProviderProfileDigest = digestBytes([]byte("combined-recovery-provider-profile/v1"))
	profile.PiRuntimeBundleDigest = digestBytes([]byte("combined-recovery-runtime/v1"))
	template.ExpectedImageDigest = digestBytes([]byte("combined-recovery-image/v1"))
	templateDigest, err := template.Digest()
	if err != nil {
		t.Fatal(err)
	}
	profile.ContainmentProfileDigest = templateDigest
	return profile, template
}

// combinedRecoveryTerminalOrigin resolves the terminal lease of the activated session's history. The expected origin
// is the proposal-call identity the original process computed for the Bash call.
func combinedRecoveryTerminalOrigin(t *testing.T, root string, session *governedRecoverySession, gen uint64) {
	t.Helper()
	var want delta.OperationIdentity
	data, err := os.ReadFile(filepath.Join(root, fmt.Sprintf("origin-g%d.json", gen)))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	bundle, err := session.Store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	terminal := bundle.ApprovedDeltaLedger[len(bundle.ApprovedDeltaLedger)-1]
	origin, err := session.Authority.ResolveTerminalLeaseOrigin(context.Background(), bundle, terminal)
	if err != nil {
		t.Fatalf("terminal origin of the historical Bash lease: %v", err)
	}
	if origin != want {
		t.Fatalf("terminal origin = %+v, want %+v", origin, want)
	}
}

// combinedRecoveryCommandRefusals changes the activated session's command journal after activation and checks that
// both operation verification and terminal-origin resolution refuse, each with the frozen-prefix refusal. A valid
// append is the one change that must still verify. The file is restored after each change.
func combinedRecoveryCommandRefusals(t *testing.T, root string, session *governedRecoverySession,
	runner *productionCommandRunner, journal *audit.Journal) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(root, "command-audit.jsonl")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := session.Store.EvidenceBundle()
	if err != nil {
		t.Fatal(err)
	}
	bash, err := retainedBashOperationOf(bundle)
	if err != nil {
		t.Fatal(err)
	}
	terminal := bundle.ApprovedDeltaLedger[len(bundle.ApprovedDeltaLedger)-1]
	verifies := func(step string) {
		t.Helper()
		if err := runner.VerifyProductionBashEvidence(ctx, bash); err != nil {
			t.Fatalf("%s: operation verification refused: %v", step, err)
		}
		if _, err := session.Authority.ResolveTerminalLeaseOrigin(ctx, bundle, terminal); err != nil {
			t.Fatalf("%s: terminal origin refused: %v", step, err)
		}
	}
	refuses := func(step string) {
		t.Helper()
		if err := runner.VerifyProductionBashEvidence(ctx, bash); err == nil || !strings.Contains(err.Error(), "frozen-prefix") {
			t.Fatalf("%s: operation verification = %v, want the frozen-prefix refusal", step, err)
		}
		if _, err := session.Authority.ResolveTerminalLeaseOrigin(ctx, bundle, terminal); err == nil ||
			!strings.Contains(err.Error(), "frozen-prefix") {
			t.Fatalf("%s: terminal origin = %v, want the frozen-prefix refusal", step, err)
		}
	}
	verifies("activated history")
	if _, err := journal.Append(audit.Event{Kind: "fresh_command_progress", ID: "fresh-command-progress", Data: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	verifies("valid append after activation")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	rewritten := combinedRecoveryRewriteSettled(t, original)
	if err := os.WriteFile(path, rewritten, 0o600); err != nil {
		t.Fatal(err)
	}
	refuses("frozen record rewritten into a valid journal")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	refuses("journal truncated below the frozen count")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	verifies("journal restored")
}

// combinedRecoveryRewriteSettled returns a valid journal equal to data except that its settled Bash record names a
// different provider call. Every record is re-appended, so the hash chain is valid and only the content differs.
func combinedRecoveryRewriteSettled(t *testing.T, data []byte) []byte {
	t.Helper()
	trace, err := audit.Verify(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rewritten-command-audit.jsonl")
	journal, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := false
	for _, record := range trace.Records {
		event := audit.Event{Kind: record.Event.Kind, ID: record.Event.ID, Data: append([]byte(nil), record.Event.Data...)}
		if event.Kind == productionD09SettledKind && !rewritten {
			var payload productionD09CommandSettledEvent
			if err := unmarshalGovernedJSON(event.Data, &payload); err != nil {
				t.Fatal(err)
			}
			payload.ProviderCall.CallID = "forged-call"
			event.Data = mustJSON(payload)
			rewritten = true
		}
		if _, err := journal.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	if !rewritten {
		t.Fatal("the command journal holds no settled Bash record to rewrite")
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// retainedBashOperationOf returns the bundle's Bash operation. The scenario holds exactly one.
func retainedBashOperationOf(bundle sessionrepo.EvidenceBundle) (sessionrepo.OperationEvidence, error) {
	for _, operation := range bundle.Operations {
		if operation.Tool == "bash" {
			return operation, nil
		}
	}
	return sessionrepo.OperationEvidence{}, errors.New("the bundle holds no Bash operation")
}

// TestGovernedRecoveryCombinedPublishesAHistoricalBashTerminalLease recovers a history whose last approved transition
// is the original process's production Bash call. Recovery reaches the frozen history only through prepare and Activate.
func TestGovernedRecoveryCombinedPublishesAHistoricalBashTerminalLease(t *testing.T) {
	root := privateGovernedTestRoot(t)
	combinedRecoveryRunChild(t, root, combinedProductionScenario, "original", "o", combinedRecoveryCrashExit)
	resumed := combinedRecoveryRunChild(t, root, combinedProductionScenario, "recover", "r1", 0)
	if resumed.PrepareError != "" || resumed.ActivateError != "" || !resumed.Activated || resumed.Publication != combinedProductionStop {
		t.Fatalf("recovery of the historical Bash terminal: prepare=%q activate=%q publication=%q",
			resumed.PrepareError, resumed.ActivateError, resumed.Publication)
	}
}

// TestGovernedRecoveryCombinedPublishesAcrossAnotherRecovery runs a Bash call in the recovered session, which appends
// its settled record to the same command journal and Store, and then recovers again. The second recovery must route both
// records to their pinned sessions, and publication must check both historical Bash operations.
func TestGovernedRecoveryCombinedPublishesAcrossAnotherRecovery(t *testing.T) {
	root := privateGovernedTestRoot(t)
	combinedRecoveryRunChild(t, root, combinedResumedScenario, "original", "o", combinedRecoveryCrashExit)
	first := combinedRecoveryRunChild(t, root, combinedResumedScenario, "recover-bash", "r1", 0)
	if first.PrepareError != "" || first.ActivateError != "" || !first.Activated || first.Publication != combinedProductionStop {
		t.Fatalf("first recovery with a resumed Bash call: prepare=%q activate=%q publication=%q",
			first.PrepareError, first.ActivateError, first.Publication)
	}
	second := combinedRecoveryRunChild(t, root, combinedResumedScenario, "recover-again", "r2", 0)
	if second.PrepareError != "" || second.ActivateError != "" || !second.Activated || second.Publication != combinedProductionStop {
		t.Fatalf("second recovery after the resumed Bash call: prepare=%q activate=%q publication=%q",
			second.PrepareError, second.ActivateError, second.Publication)
	}
}

// TestGovernedRecoveryCombinedPublishesAHistoricalBashBeforeAResumedWrite recovers the same history and then promotes a
// resumed write. The write is the terminal transition, and the historical Bash operation must still pass its checks.
func TestGovernedRecoveryCombinedPublishesAHistoricalBashBeforeAResumedWrite(t *testing.T) {
	root := privateGovernedTestRoot(t)
	combinedRecoveryRunChild(t, root, combinedProductionScenario, "original", "o", combinedRecoveryCrashExit)
	resumed := combinedRecoveryRunChild(t, root, combinedProductionScenario, "recover-write", "r1", 0)
	if resumed.PrepareError != "" || resumed.ActivateError != "" || !resumed.Activated || resumed.Publication != combinedProductionStop {
		t.Fatalf("recovery before a resumed write: prepare=%q activate=%q publication=%q",
			resumed.PrepareError, resumed.ActivateError, resumed.Publication)
	}
}
