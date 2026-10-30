//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

const (
	bashRunWorkflowID     = "bash-run-workflow"
	bashRunConversationID = "bash-run-conversation"
	bashRunSessionID      = "bash-run-session"
	bashRunProviderID     = "synthetic-offline-profile-v1"
	bashRunRunID          = "run-bash-run"
)

var (
	bashRunStorePolicy    = "sha256:" + strings.Repeat("a", 64)
	bashRunMetadataPolicy = "sha256:" + strings.Repeat("b", 64)
	bashRunXattrPolicy    = "sha256:" + strings.Repeat("c", 64)
)

// productionBashRun is one governed Bash call driven through the production D09 wiring (a sealed
// receipt stands in for the Podman cell): the gate ALLOWs it, the command runs and settles on the cell,
// and the Store either promotes its delta or, under a scope that does not cover it, refuses it after the
// effect. The Store, its journals and the command journal stay on disk under root, so a test can reopen
// them as a fresh instance once release has closed this one.
type productionBashRun struct {
	t                  *testing.T
	root               string
	storePath          string
	storeJournalPath   string
	commandJournalPath string
	runner             *productionCommandRunner
	profile            piruntime.HostProfile
	template           piruntime.D09CellTemplate
	managed            *sessionlaunch.ManagedStore
	store              *sessionrepo.Store
	journals           []*audit.Journal
	prepared           *sessionlaunch.PreparedSession
	execErr            error
	bundle             sessionrepo.EvidenceBundle
	operation          sessionrepo.OperationEvidence
	// retained is the signed runner's own record of the command: its receipt and the transition (or
	// refused candidate) it was bound to.
	retained productionD09RunEvidence
}

func newBashRunProfile(t *testing.T) (piruntime.HostProfile, piruntime.D09CellTemplate) {
	t.Helper()
	profile, template, _ := productionProofFixture()
	template.CommandExecutable = "/bin/bash"
	template.CommandExecutableDigest = "sha256:" + strings.Repeat("6", 64)
	templateDigest, err := template.Digest()
	if err != nil {
		t.Fatal(err)
	}
	profile.ContainmentProfileDigest = templateDigest
	return profile, template
}

// runProductionBash drives one Bash call whose cell appends to task.txt under outputPaths as the
// declared Bash output scope: [] refuses the write after the effect, ["task.txt"] promotes it.
func runProductionBash(t *testing.T, outputPaths []string) *productionBashRun {
	t.Helper()
	root := privateGovernedTestRoot(t)
	run := &productionBashRun{t: t, root: root, storePath: filepath.Join(root, "store"),
		storeJournalPath: filepath.Join(root, "store.jsonl"), commandJournalPath: filepath.Join(root, "command.jsonl")}
	gatePolicy, err := gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "bash", Effect: gate.EffectAllow}}})
	if err != nil {
		t.Fatal(err)
	}
	openJournal := func(path string) *audit.Journal {
		journal, err := audit.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		run.journals = append(run.journals, journal)
		return journal
	}
	storeJournal, providerJournal, commandJournal := openJournal(run.storeJournalPath),
		openJournal(filepath.Join(root, "provider.jsonl")), openJournal(run.commandJournalPath)
	t.Cleanup(run.release)

	profile, template := newBashRunProfile(t)
	templateDigest, err := template.Digest()
	if err != nil {
		t.Fatal(err)
	}
	run.profile, run.template = profile, template
	run.runner = newProductionCommandRunner(func(leaseID string) (sessionrepo.CommandRunner, error) {
		cell := &sealedD09Cell{leaseID: leaseID, template: template, templateDigest: templateDigest}
		return newProductionD09BashRunnerWithInvoker(leaseID, cell, commandJournal, bashRunRunID, bashRunSessionID,
			bashRunWorkflowID, bashRunConversationID, profile, template), nil
	})
	authority, err := NewSessionOperationAuthority(storeJournal, providerJournal, bashRunWorkflowID, bashRunConversationID,
		bashRunProviderID, gatePolicy, bashRunStorePolicy, bashRunMetadataPolicy, run.runner, outputPaths)
	if err != nil {
		t.Fatal(err)
	}
	authority.requireEvidencedBashContainment = true

	sourcePath, adminPath := filepath.Join(root, "source"), filepath.Join(root, "admin")
	for _, path := range []string{run.storePath, sourcePath, adminPath} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "task.txt"), []byte("baseline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	storeRoot, err := os.Open(run.storePath)
	if err != nil {
		t.Fatal(err)
	}
	run.managed, err = authority.CreateStore(storeRoot, workspace.DefaultLimits(), workspace.XattrVisibilityAttestation{
		ProfileDigest: bashRunXattrPolicy, Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}, nil)
	_ = storeRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	run.store = run.managed.Store()
	sourceRoot, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	g0, err := run.store.Seed(sourceRoot, sessionrepo.RootAttestation{Quiescent: true})
	_ = sourceRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.BindBaseline(run.store, g0); err != nil {
		t.Fatal(err)
	}
	adminRoot, err := os.Open(adminPath)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sessionlaunch.OpenAdminParent(adminRoot)
	_ = adminRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	git, gitProfile := pinGovernedTestGit(t)
	t.Cleanup(func() { _ = git.Close() })
	prepared, err := sessionlaunch.Prepare(context.Background(), run.managed, g0, admin, git, gitProfile)
	if err != nil {
		t.Fatal(err)
	}
	run.prepared = prepared
	if err := authority.BindPreparedSession(prepared); err != nil {
		t.Fatal(err)
	}
	durable, err := NewDurableExecutor(DurableExecutorConfig{Store: run.store, Tip: g0, CallIssuer: governedCallIssuer,
		PolicyDigest: bashRunStorePolicy, MetadataPolicyDigest: bashRunMetadataPolicy,
		BashOutputPaths: outputPaths, CommandRunner: run.runner})
	if err != nil {
		t.Fatal(err)
	}

	const responseID, callID = "bash-run-response", "bash-run-call"
	arguments := json.RawMessage(`{"command":"printf 'staged\\n' >> task.txt"}`)
	argumentDigest, err := protocol.CanonicalArgumentsDigest("bash", arguments)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := protocol.NewStream(governedCallIssuer, governedResponseIssuer)
	if err != nil {
		t.Fatal(err)
	}
	if captured := stream.Capture(protocol.TrustedCapture{
		ResponseID: correlation.Identifier{Issuer: governedResponseIssuer, Opaque: responseID},
		ToolCallID: correlation.Identifier{Issuer: governedCallIssuer, Opaque: callID},
		ToolName:   "bash", RawArguments: append([]byte(nil), arguments...),
		CanonicalizationProfile: protocol.CanonicalizationProfile, CanonicalArgumentsDigest: argumentDigest,
		Sequence: 1, Generation: g0.ID(),
	}); !captured.Accepted || captured.StreamClosed {
		t.Fatalf("capture: %+v", captured)
	}
	ordinal := 0
	appendEvent := func(kind, id string, data any) {
		t.Helper()
		encoded, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		ordinal++
		if _, err := providerJournal.Append(audit.Event{Kind: kind, ID: fmt.Sprintf("%s:%s:%d", bashRunConversationID, id, ordinal), Data: encoded}); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent("provider_user_admitted", "user", providerPromptEvidence{WorkflowID: bashRunWorkflowID,
		ConversationID: bashRunConversationID, TaskID: "bash-run-task", Generation: g0.ID(), Prompt: "synthetic bash-run fixture"})
	providerURL, err := url.Parse(openrouter.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	appendEvent("provider_exchange", "exchange", providerExchangeEvidence{WorkflowID: bashRunWorkflowID, Turn: 1,
		Request: providerRequestEvidence{ProfileID: bashRunProviderID, Method: "POST", URL: openrouter.Endpoint, Host: providerURL.Host,
			ContentLength: int64(len("synthetic-offline-request")), Headers: map[string][]string{"content-type": {"application/json"}},
			Body: []byte("synthetic-offline-request")},
		StatusCode: 200, Response: []byte("synthetic-offline-response"), ResponseIDIssuer: governedResponseIssuer,
		ToolCallIDIssuer: governedCallIssuer, ResponseID: responseID, Model: providerbridge.ApprovedModel,
		ToolCall: &struct {
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}{ID: callID, Name: "bash", Arguments: append(json.RawMessage(nil), arguments...)}})
	proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: callID, Tool: "bash", Arguments: arguments}
	encodedProposal, err := protocol.MarshalProposal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	decision := gate.Evaluate(proposal, stream.Propose(encodedProposal), gatePolicy)
	if decision.Verdict != gate.Allow || decision.ResponseID == nil {
		t.Fatalf("gate: %+v", decision)
	}
	appendEvent("provider_gate_decision", "decision", decision)
	_, run.execErr = durable.Execute(context.Background(), proposal, decision)

	if run.bundle, err = run.store.EvidenceBundle(); err != nil {
		t.Fatal(err)
	}
	if len(run.bundle.Operations) != 1 {
		t.Fatalf("want one Bash operation: %+v", run.bundle.Operations)
	}
	run.operation = run.bundle.Operations[0]
	lease, ok := run.runner.evidenceRunners[run.operation.ViewID].(*productionD09BashRunner)
	if !ok {
		t.Fatalf("no signed runner retained for view %q", run.operation.ViewID)
	}
	run.retained = lease.evidence[run.operation.ViewID]
	return run
}

// release closes the Store and every journal so that a test can reopen them as a fresh instance.
func (r *productionBashRun) release() {
	if r.prepared != nil {
		_ = r.prepared.Close()
	}
	if r.managed != nil {
		_ = r.managed.Close()
	}
	for _, journal := range r.journals {
		_ = journal.Close()
	}
}

// reopen opens the persisted Store read-only (as recovery's preview does) and its command journal in a
// fresh instance and returns what recovery reads from them.
func (r *productionBashRun) reopen() (sessionrepo.EvidenceBundle, *audit.Journal, *audit.Journal) {
	r.t.Helper()
	r.release()
	storeJournal, err := openExistingRecoveryJournal(r.storeJournalPath)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = storeJournal.Close() })
	root, err := openExistingPrivateRecoveryDirectory(r.storePath)
	if err != nil {
		r.t.Fatal(err)
	}
	defer root.Close()
	managed, err := sessionlaunch.OpenRecoveryPreview(root, sessionrepo.Options{Journal: storeJournal,
		PolicyDigest: bashRunStorePolicy, MetadataPolicyDigest: bashRunMetadataPolicy, Limits: workspace.DefaultLimits(),
		XattrVisibility: workspace.XattrVisibilityAttestation{ProfileDigest: bashRunXattrPolicy,
			Authority: workspace.XattrAuthorityBoundedNoHiddenTrusted}})
	if err != nil {
		r.t.Fatalf("recovery preview of the persisted Store: %v", err)
	}
	defer managed.Close()
	bundle, err := managed.Store().EvidenceBundle()
	if err != nil {
		r.t.Fatal(err)
	}
	commandJournal, err := openExistingRecoveryJournal(r.commandJournalPath)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = commandJournal.Close() })
	return bundle, storeJournal, commandJournal
}

// historicalVerifier is the fresh process's command-settlement verifier over journal.
func (r *productionBashRun) historicalVerifier(journal *audit.Journal) (*productionHistoricalBashVerifier, error) {
	r.t.Helper()
	trace, err := journal.Trace()
	if err != nil {
		r.t.Fatal(err)
	}
	return newProductionHistoricalBashVerifier(journal, productionHistoricalCommandPin{IdentityDigest: "fresh-command-journal",
		Head: auditHeadHash(trace.Records), RecordCount: uint64(len(trace.Records))},
		r.profile, r.template, []productionRecoveryCommandSession{{RunID: bashRunRunID, SessionID: bashRunSessionID,
			ConversationID: bashRunConversationID}}, bashRunWorkflowID)
}
