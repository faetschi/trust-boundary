//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/workflowrecovery"
)

// The catalog follows a workflow's checkpoint only at the initial and the empty-transcript bindings,
// while the ledger advances at every reservation, exchange and effect. A workflow that ran therefore
// settles at a digest the catalog has not seen, and its terminal record used to be refused as a stale
// checkpoint: pi_workflow_terminal was never written and every later request was refused as an
// unsettled prior. (The older terminal test marked the initial digest, so it never met this.)
func TestSettledWorkflowAdvancesTheCatalogToItsSettledCheckpointAndBecomesTerminal(t *testing.T) {
	root := privateGovernedTestRoot(t)
	ownerRoot := filepath.Join(root, "owner-store")
	if err := os.Mkdir(ownerRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(root, "workflow-catalog.jsonl")
	journal, err := audit.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	admissionHash := bareWorkflowDigest(digestBytes([]byte("settled-terminal-admission")))
	entry := catalogReplayEntry(root, ownerRoot, admissionHash)
	reserved, err := json.Marshal(piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: admissionHash, Entry: entry})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(audit.Event{Kind: "pi_workflow_admission_reserved",
		ID: workflowCatalogEventID("reserved", admissionHash), Data: reserved}); err != nil {
		t.Fatal(err)
	}
	catalog := reopenCatalogReplayJournal(t, journal, ownerRoot)
	initialDigest := bareWorkflowDigest(digestBytes([]byte("settled-terminal-initial")))
	transcriptDigest := bareWorkflowDigest(digestBytes([]byte("settled-terminal-transcript")))
	settledDigest := bareWorkflowDigest(digestBytes([]byte("settled-terminal-settled")))
	incarnation := workflowrecovery.OwnerIncarnation{
		Digest: bareWorkflowDigest(digestBytes([]byte("settled-terminal-incarnation"))), BootID: "settled-terminal-boot",
		PID: os.Getpid(), StartTimeTicks: 1, PIDNamespaceInode: 1, EffectiveUID: uint32(os.Geteuid()),
		CgroupIdentityDigest: bareWorkflowDigest(digestBytes([]byte("settled-terminal-cgroup"))),
	}
	store := workflowrecovery.OwnerStoreIdentity{
		Digest: bareWorkflowDigest(digestBytes([]byte("settled-terminal-store"))), Path: entry.OwnerDBPath,
	}
	if err := catalog.AuthorizeFreshWorkflow(context.Background(), catalogReplayWorkflowID, 1, initialDigest,
		incarnation, entry.Limits.Deadline, bareWorkflowDigest(digestBytes([]byte("settled-terminal-root"))), store); err != nil {
		t.Fatalf("fresh authorization refused: %v", err)
	}
	checkpoint, err := json.Marshal(piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema, AdmissionHash: admissionHash, Digest: initialDigest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(audit.Event{Kind: "pi_workflow_checkpoint_bound",
		ID: workflowCatalogEventID("checkpoint", admissionHash), Data: checkpoint}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = audit.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog = reopenCatalogReplayJournal(t, journal, ownerRoot)
	// What the service does while the workflow starts: the one advance to the empty-transcript checkpoint.
	if err := catalog.MarkCheckpoint(admissionHash, initialDigest, transcriptDigest); err != nil {
		t.Fatal(err)
	}

	proof := digestBytes([]byte("settled-terminal-proof"))
	const transaction = "no-publication:settled-terminal-release"
	if err := catalog.MarkTerminal(admissionHash, settledDigest, catalogReplayRunID, proof, catalogReplayProofSequence, transaction); !errors.Is(err, ErrPiWorkflowCatalog) {
		t.Fatalf("the bare terminal mark must still refuse a digest the catalog has not followed: %v", err)
	}
	if err := catalog.markSettledTerminal(admissionHash, settledDigest, catalogReplayRunID, proof, catalogReplayProofSequence, transaction); err != nil {
		t.Fatalf("a settled workflow whose checkpoint advanced past the catalog's was not made terminal: %v", err)
	}
	terminal, ok := catalog.admissionEntry(admissionHash)
	if !ok || !terminal.Terminal || terminal.LatestCheckpointDigest != settledDigest || terminal.TerminalRunID != catalogReplayRunID {
		t.Fatalf("terminal entry = %+v", terminal)
	}
	if err := catalog.markSettledTerminal(admissionHash, settledDigest, catalogReplayRunID, proof, catalogReplayProofSequence, transaction); err != nil {
		t.Fatalf("settling the same terminal tuple again was refused: %v", err)
	}
	for name, mutate := range map[string]func() error{
		"another checkpoint": func() error {
			return catalog.markSettledTerminal(admissionHash, bareWorkflowDigest(digestBytes([]byte("other"))), catalogReplayRunID, proof, catalogReplayProofSequence, transaction)
		},
		"another run": func() error {
			return catalog.markSettledTerminal(admissionHash, settledDigest, "other-run", proof, catalogReplayProofSequence, transaction)
		},
		"another transaction": func() error {
			return catalog.markSettledTerminal(admissionHash, settledDigest, catalogReplayRunID, proof, catalogReplayProofSequence, "other")
		},
		"unknown admission": func() error {
			return catalog.markSettledTerminal(bareWorkflowDigest(digestBytes([]byte("unknown"))), settledDigest, catalogReplayRunID, proof, catalogReplayProofSequence, transaction)
		},
	} {
		if err := mutate(); !errors.Is(err, ErrPiWorkflowCatalog) {
			t.Errorf("%s: a mismatched terminal was accepted: %v", name, err)
		}
	}

	// Cold replay of the journal (advance to the transcript checkpoint, advance to the settled
	// checkpoint, terminal) yields the same state.
	want := summarizeCatalogAdmission(t, catalog, admissionHash)
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	if got := runCatalogReplayFreshProcess(t, journalPath, ownerRoot, admissionHash); got != want {
		t.Fatalf("fresh-process replay state = %+v, want %+v", got, want)
	}
	if !want.Terminal || want.LatestCheckpointDigest != settledDigest {
		t.Fatalf("replayed state = %+v", want)
	}
}
