//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workflowrecovery"
)

func TestCatalogOwnerDrainRequiresCleanupReceiptOrLiveScope(t *testing.T) {
	root := t.TempDir()
	digest := strings.Repeat("a", 64)
	cgroupRootDigest := "sha256:" + strings.Repeat("a", 64)
	bareDigest := strings.Repeat("b", 64)
	workflow := "workflow-test"
	entry := piServiceWorkflowEntry{
		AdmissionRecordHash: digest, WorkflowID: workflow, CheckpointBound: true,
	}
	incarnation := workflowrecovery.OwnerIncarnation{
		Digest: bareDigest, BootID: "different-boot", PID: 424242, StartTimeTicks: 1,
		PIDNamespaceInode: 1, EffectiveUID: uint32(os.Geteuid()), CgroupIdentityDigest: bareDigest,
	}
	owner := workflowrecovery.OwnerEpochIdentity{Epoch: 1, Incarnation: incarnation}
	catalog := &piServiceWorkflowCatalog{entries: map[string]piServiceWorkflowEntry{digest: entry}}
	verifier := catalogOwnerDrainVerifier{catalog: catalog, cgroupRoot: filepath.Clean(root),
		cgroupRootDigest: cgroupRootDigest, containment: piruntime.D09ContainmentProfile,
		settlement: piruntime.D09SettlementProfile}

	err := verifier.VerifyOwnerCellsDrained(context.Background(), workflow, []workflowrecovery.OwnerEpochIdentity{owner})
	if err == nil {
		t.Fatalf("absent owner scope without a durable cleanup receipt was accepted: %v", err)
	}
	if _, exists := catalog.ownerScope(workflow, owner.Epoch); exists {
		t.Fatal("failed drain verification appended a synthetic cleanup receipt")
	}

	key := workflowOwnerEpochKey(workflow, owner.Epoch)
	catalog.ownerScopes = map[string]piServiceWorkflowOwnerScope{key: {
		AdmissionHash: digest, WorkflowID: workflow, OwnerEpoch: owner.Epoch,
		Incarnation: incarnation, ScopeName: workflowOwnerScopeName(workflow, owner.Epoch),
		Drained: true, Unexposed: true,
	}}
	if err := verifier.VerifyOwnerCellsDrained(context.Background(), workflow, []workflowrecovery.OwnerEpochIdentity{owner}); err != nil {
		t.Fatalf("exact prior cleanup receipt was not accepted: %v", err)
	}
}

func TestPiServiceWorkflowCatalogTerminalTupleIsExactAndIdempotent(t *testing.T) {
	root := privateGovernedTestRoot(t)
	journal, err := audit.Open(filepath.Join(root, "workflow-catalog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	admissionHash := strings.Repeat("a", 64)
	checkpointDigest := strings.Repeat("b", 64)
	proofDigest := "sha256:" + strings.Repeat("c", 64)
	entry := piServiceWorkflowEntry{AdmissionRecordHash: admissionHash, WorkflowID: "workflow-terminal-tuple",
		CheckpointBound: true, LatestCheckpointDigest: checkpointDigest}
	catalog := &piServiceWorkflowCatalog{journal: journal,
		entries:           map[string]piServiceWorkflowEntry{admissionHash: entry},
		byHostAdmissionID: make(map[string]string), ownerScopes: make(map[string]piServiceWorkflowOwnerScope)}
	mark := func(checkpoint, runID, proof string, sequence uint64, transaction string) error {
		return catalog.MarkTerminal(admissionHash, checkpoint, runID, proof, sequence, transaction)
	}
	if err := mark(strings.Repeat("d", 64), "run-terminal-tuple", proofDigest, 3, "transaction-terminal-tuple"); !errors.Is(err, ErrPiWorkflowCatalog) {
		t.Fatalf("stale checkpoint was accepted as terminal: %v", err)
	}
	if err := mark(checkpointDigest, "run-terminal-tuple", proofDigest, 3, "transaction-terminal-tuple"); err != nil {
		t.Fatalf("matching terminal tuple was refused: %v", err)
	}
	if err := mark(checkpointDigest, "run-terminal-tuple", proofDigest, 3, "transaction-terminal-tuple"); err != nil {
		t.Fatalf("exact terminal tuple was not idempotent: %v", err)
	}
	for name, mutate := range map[string]func() error{
		"run ID": func() error { return mark(checkpointDigest, "other-run", proofDigest, 3, "transaction-terminal-tuple") },
		"proof digest": func() error {
			return mark(checkpointDigest, "run-terminal-tuple", "sha256:"+strings.Repeat("e", 64), 3, "transaction-terminal-tuple")
		},
		"proof sequence": func() error {
			return mark(checkpointDigest, "run-terminal-tuple", proofDigest, 4, "transaction-terminal-tuple")
		},
		"publication transaction": func() error {
			return mark(checkpointDigest, "run-terminal-tuple", proofDigest, 3, "other-transaction")
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := mutate(); !errors.Is(err, ErrPiWorkflowCatalog) {
				t.Fatalf("mismatched terminal tuple was accepted: %v", err)
			}
		})
	}
	terminal, ok := catalog.entries[admissionHash]
	trace, err := journal.Trace()
	if err != nil || !ok || !terminal.Terminal || terminal.LatestCheckpointDigest != checkpointDigest ||
		terminal.TerminalRunID != "run-terminal-tuple" || terminal.TerminalProofDigest != proofDigest ||
		terminal.TerminalProofSequence != 3 || terminal.TerminalTransactionID != "transaction-terminal-tuple" ||
		len(trace.Records) != 1 {
		t.Fatalf("terminal tuple was not durably recorded exactly once: entry=%+v records=%d err=%v", terminal, len(trace.Records), err)
	}
}

func TestSettledServiceProfileRequiresExactLandlockABI(t *testing.T) {
	profile, template, _ := productionProofFixture()
	profile.CgroupRootDigest = "sha256:" + strings.Repeat("9", 64)
	for _, test := range []struct {
		name string
		abi  int
	}{{"below", 7}, {"exact", 8}, {"future", 9}} {
		t.Run(test.name, func(t *testing.T) {
			abi := test.abi
			template.LandlockABI = abi
			err := validateSettledServiceProfile(profile, template)
			if abi == 8 && err != nil {
				t.Fatalf("the pinned Landlock ABI was refused: %v", err)
			}
			if abi != 8 && !errors.Is(err, ErrPiWorkflowCatalog) {
				t.Fatalf("unsupported Landlock ABI %d was accepted: %v", abi, err)
			}
		})
	}
}

func TestSettledServiceRequestPinsMatchBareRootIdentity(t *testing.T) {
	profile, template, _ := productionProofFixture()
	bareRoot := strings.Repeat("c", 64)
	entry := piServiceWorkflowEntry{WorkflowID: "workflow-settled", RunID: "run-settled", SessionID: "session-settled",
		ConversationID: "conversation-settled", RootIdentityDigest: bareRoot,
		TaskDigest: "sha256:" + strings.Repeat("d", 64), PromptDigest: "sha256:" + strings.Repeat("e", 64)}
	pins := workflowrecovery.Pins{SessionID: entry.SessionID, RootIdentityDigest: bareRoot,
		TaskDigest: bareWorkflowDigest(entry.TaskDigest), PromptDigest: bareWorkflowDigest(entry.PromptDigest),
		ProfileDigest: bareWorkflowDigest(profile.Digest), ImageDigest: bareWorkflowDigest(template.ExpectedImageDigest),
		OwnerEpoch: 1, RecoveryGeneration: 1, StoreJournalCount: 1, StoreJournalHead: strings.Repeat("f", 64),
		ProviderDisposition: workflowrecovery.DispositionAwaitingProviderExchange}
	view := workflowrecovery.SettledCheckpointView{Checkpoint: workflowrecovery.Checkpoint{Pins: pins}}
	// An admitted bare root identity must pass the pin comparison and reach the disposition gate, which refuses the unresolved exchange.
	err := (*piServiceWorkflowCatalog)(nil).verifyCompletedServiceRequestPins(context.Background(), piServiceRequestState{}, entry,
		productionPiReceipt{}, view, piruntime.D09ManagedLiveRootRegistration{}, managedLiveRootDescriptorIdentity{},
		workflow.RootIdentity{}, catalogOwnerDrainVerifier{}, template, profile)
	if !errors.Is(err, workflowrecovery.ErrPendingUnknown) {
		t.Fatalf("admitted bare root identity did not reach the settled disposition gate: %v", err)
	}
}

func TestSettledServiceDispositionRejectsUnresolvedPins(t *testing.T) {
	const currentConversation = "conversation-settled-g1"
	settled := func() workflowrecovery.Pins {
		return workflowrecovery.Pins{
			ProviderDisposition: workflowrecovery.DispositionAwaitingHostPrompt,
			ProviderTranscripts: []workflowrecovery.TranscriptPin{{ConversationID: currentConversation,
				Disposition: workflowrecovery.DispositionAwaitingHostPrompt}},
			PendingCallDisposition: workflowrecovery.PendingCallNone,
			EffectReservation:      &workflowrecovery.EffectReservationPin{Disposition: workflowrecovery.EffectReservationTerminal},
			HostCallReservation:    &workflowrecovery.HostCallReservationPin{Disposition: workflowrecovery.HostCallTerminal},
		}
	}
	if err := validateSettledServiceDisposition(settled(), currentConversation); err != nil {
		t.Fatalf("fully settled current conversation was refused: %v", err)
	}
	retired := settled()
	retired.ProviderDisposition = workflowrecovery.DispositionRetired
	retired.ProviderTranscripts[0].Disposition = workflowrecovery.DispositionRetired
	retired.PendingCallDisposition = workflowrecovery.PendingCallRetired
	retired.EffectReservation.Disposition = workflowrecovery.EffectReservationRetired
	retired.HostCallReservation.Disposition = workflowrecovery.HostCallRetired
	if err := validateSettledServiceDisposition(retired, currentConversation); err != nil {
		t.Fatalf("fully retired pins were refused: %v", err)
	}
	closedHistory := settled()
	closedHistory.ProviderTranscripts = []workflowrecovery.TranscriptPin{
		{ConversationID: "conversation-previous-g1", Disposition: workflowrecovery.DispositionRetired},
		{ConversationID: currentConversation, Disposition: workflowrecovery.DispositionAwaitingHostPrompt},
	}
	if err := validateSettledServiceDisposition(closedHistory, currentConversation); err != nil {
		t.Fatalf("a quiescent latest conversation after closed history was refused: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*workflowrecovery.Pins)
	}{
		{"provider awaiting exchange", func(p *workflowrecovery.Pins) {
			p.ProviderDisposition = workflowrecovery.DispositionAwaitingProviderExchange
		}},
		{"provider awaiting gate", func(p *workflowrecovery.Pins) {
			p.ProviderDisposition = workflowrecovery.DispositionAwaitingGateDecision
		}},
		{"provider pending result", func(p *workflowrecovery.Pins) {
			p.ProviderDisposition = workflowrecovery.DispositionPendingProviderResult
		}},
		{"provider unknown", func(p *workflowrecovery.Pins) { p.ProviderDisposition = workflowrecovery.DispositionUnknown }},
		{"pending call in flight", func(p *workflowrecovery.Pins) { p.PendingCallDisposition = workflowrecovery.PendingCallInFlight }},
		{"pending call captured", func(p *workflowrecovery.Pins) { p.PendingCallDisposition = workflowrecovery.PendingCallCaptured }},
		{"pending call unknown", func(p *workflowrecovery.Pins) { p.PendingCallDisposition = workflowrecovery.PendingCallUnknown }},
		{"effect reserved", func(p *workflowrecovery.Pins) {
			p.EffectReservation.Disposition = workflowrecovery.EffectReservationReserved
		}},
		{"effect unknown", func(p *workflowrecovery.Pins) {
			p.EffectReservation.Disposition = workflowrecovery.EffectReservationUnknown
		}},
		{"host call reserved", func(p *workflowrecovery.Pins) { p.HostCallReservation.Disposition = workflowrecovery.HostCallReserved }},
		{"host call unknown", func(p *workflowrecovery.Pins) { p.HostCallReservation.Disposition = workflowrecovery.HostCallUnknown }},
		{"transcript pending", func(p *workflowrecovery.Pins) {
			p.ProviderTranscripts[0].Disposition = workflowrecovery.DispositionPendingProviderResult
		}},
		{"transcript unknown", func(p *workflowrecovery.Pins) {
			p.ProviderTranscripts[0].Disposition = workflowrecovery.DispositionUnknown
		}},
		{"transcript missing terminal lineage", func(p *workflowrecovery.Pins) {
			p.ProviderTranscripts[0].Disposition = workflowrecovery.DispositionNone
			p.ProviderDisposition = workflowrecovery.DispositionNone
		}},
		{"non-final transcript awaiting host prompt", func(p *workflowrecovery.Pins) {
			p.ProviderTranscripts = []workflowrecovery.TranscriptPin{
				{ConversationID: "conversation-previous", Disposition: workflowrecovery.DispositionAwaitingHostPrompt},
				{ConversationID: currentConversation, Disposition: workflowrecovery.DispositionAwaitingHostPrompt},
			}
		}},
		{"provider disposition does not match latest transcript", func(p *workflowrecovery.Pins) {
			p.ProviderDisposition = workflowrecovery.DispositionRetired
		}},
		{"latest transcript does not match current generation", func(p *workflowrecovery.Pins) {
			p.ProviderTranscripts[0].ConversationID = "conversation-old-generation"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pins := settled()
			test.mutate(&pins)
			if err := validateSettledServiceDisposition(pins, currentConversation); !errors.Is(err, workflowrecovery.ErrPendingUnknown) {
				t.Fatalf("unresolved pins were admitted: %v", err)
			}
		})
	}
}
