//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/workflowrecovery"
)

const (
	catalogReplayRequestPathEnv = "TB_CATALOG_REPLAY_REQUEST_PATH"
	catalogReplayWorkflowID     = "workflow-catalog-replay"
	catalogReplayRunID          = "run-catalog-replay"
	catalogReplayProofSequence  = uint64(7)
)

// catalogReplaySummary is the admission state a reopened catalog must reproduce.
// The terminal tuple lives in json:"-" fields on the entry, so it is copied out.
type catalogReplaySummary struct {
	FreshAuthorizationUsed  bool
	IncarnationDigest       string
	InitialCheckpointDigest string
	CheckpointBound         bool
	LatestCheckpointDigest  string
	Terminal                bool
	TerminalRunID           string
	TerminalProofDigest     string
	TerminalProofSequence   uint64
	TerminalTransactionID   string
	OwnerDBPath             string
}

type catalogReplayFreshRequest struct {
	JournalPath   string
	OwnerRoot     string
	AdmissionHash string
	ResultPath    string
}

// TestCatalogOpenerKeepsFixedPathGate pins the production gate: the only opener
// refuses every catalog path but the fixed host path, so the replay seam is not
// a path override.
func TestCatalogOpenerKeepsFixedPathGate(t *testing.T) {
	path := filepath.Join(privateGovernedTestRoot(t), "workflow-catalog.jsonl")
	if _, err := openPiServiceWorkflowCatalog(path); !errors.Is(err, ErrPiWorkflowCatalog) {
		t.Fatalf("production opener accepted a non-fixed catalog path: %v", err)
	}
}

// TestCatalogLoaderInitializesProductionOwnerRoot checks that the replay path
// starts from the production owner store root; only tests override it.
func TestCatalogLoaderInitializesProductionOwnerRoot(t *testing.T) {
	journal, err := audit.Open(filepath.Join(privateGovernedTestRoot(t), "workflow-catalog.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadPiServiceWorkflowCatalog(journal)
	if err != nil {
		t.Fatalf("replay of an empty catalog journal: %v", err)
	}
	defer func() { _ = catalog.Close() }()
	if catalog.ownerStoreRoot != piServiceOwnerStoreRoot || catalog.ownerRoot() != piServiceOwnerStoreRoot {
		t.Fatalf("loaded catalog owner root = %q, want %q", catalog.ownerStoreRoot, piServiceOwnerStoreRoot)
	}
}

// TestCatalogReplayReopensTerminalAdmissionInFreshProcess drives the durable catalog
// through reserve, fresh authorization, checkpoint bind, and terminal. It then
// reopens the journal cold in a child process and requires the same admission
// state. The checkpoint-bind record is written in the shape BindInitialCheckpoint
// appends: the real method takes a workflowrecovery.Receipt from a real owner
// ledger, which this test does not build. The terminal tuple is written only by
// the real MarkTerminal.
func TestCatalogReplayReopensTerminalAdmissionInFreshProcess(t *testing.T) {
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
	admissionHash := bareWorkflowDigest(digestBytes([]byte("catalog-replay-admission")))
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
	initialDigest := bareWorkflowDigest(digestBytes([]byte("catalog-replay-initial-checkpoint")))
	incarnation := workflowrecovery.OwnerIncarnation{
		Digest: bareWorkflowDigest(digestBytes([]byte("catalog-replay-incarnation"))), BootID: "catalog-replay-boot",
		PID: os.Getpid(), StartTimeTicks: 1, PIDNamespaceInode: 1, EffectiveUID: uint32(os.Geteuid()),
		CgroupIdentityDigest: bareWorkflowDigest(digestBytes([]byte("catalog-replay-cgroup"))),
	}
	store := workflowrecovery.OwnerStoreIdentity{
		Digest: bareWorkflowDigest(digestBytes([]byte("catalog-replay-store"))), Path: entry.OwnerDBPath,
	}
	rootIdentity := bareWorkflowDigest(digestBytes([]byte("catalog-replay-root")))
	if err := catalog.AuthorizeFreshWorkflow(context.Background(), catalogReplayWorkflowID, 1, initialDigest,
		incarnation, entry.Limits.Deadline, rootIdentity, store); err != nil {
		t.Fatalf("fresh authorization refused: %v", err)
	}
	checkpoint, err := json.Marshal(piServiceWorkflowEvent{SchemaVersion: workflowCatalogSchema,
		AdmissionHash: admissionHash, Digest: initialDigest})
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

	// Reopen from the journal, so the bound checkpoint comes from the durable
	// record and not from this run's in-memory state.
	journal, err = audit.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog = reopenCatalogReplayJournal(t, journal, ownerRoot)
	if err := catalog.MarkTerminal(admissionHash, initialDigest, catalogReplayRunID,
		digestBytes([]byte("catalog-replay-proof")), catalogReplayProofSequence, "no-publication:catalog-replay-release"); err != nil {
		t.Fatalf("terminal tuple refused: %v", err)
	}
	want := summarizeCatalogAdmission(t, catalog, admissionHash)
	if !want.Terminal || want.TerminalRunID != catalogReplayRunID || !want.CheckpointBound {
		t.Fatalf("terminal tuple was not recorded on the live catalog: %+v", want)
	}
	if err := catalog.Close(); err != nil {
		t.Fatal(err)
	}
	if got := runCatalogReplayFreshProcess(t, journalPath, ownerRoot, admissionHash); got != want {
		t.Fatalf("fresh-process replay state = %+v, want %+v", got, want)
	}
}

// TestCatalogReplayFreshProcessHelper is the child side of
// runCatalogReplayFreshProcess. It does nothing unless the parent sets the request path.
func TestCatalogReplayFreshProcessHelper(t *testing.T) {
	path := os.Getenv(catalogReplayRequestPathEnv)
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var request catalogReplayFreshRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	journal, err := audit.Open(request.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadPiServiceWorkflowCatalog(journal)
	if err != nil {
		t.Fatalf("fresh-process catalog replay refused: %v", err)
	}
	defer func() { _ = catalog.Close() }()
	catalog.ownerStoreRoot = request.OwnerRoot
	encoded, err := json.Marshal(summarizeCatalogAdmission(t, catalog, request.AdmissionHash))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.ResultPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

// catalogReplayEntry is the reserved admission. Every field is a well-formed
// production-shaped value, and the owner DB path sits under the test owner root.
func catalogReplayEntry(root, ownerRoot, admissionHash string) piServiceWorkflowEntry {
	return piServiceWorkflowEntry{
		SchemaVersion: workflowCatalogSchema, HostAdmissionID: "catalog-replay-host-admission",
		AdmissionRecordHash: admissionHash, AdmissionSequence: 1,
		PeerUID: maxInt(1, os.Geteuid()), PeerGID: maxInt(1, os.Getegid()), PeerPID: os.Getpid(),
		RequestID: "catalog-replay-request", TaskID: "catalog-replay-task",
		PayloadDigest:         digestBytes([]byte("catalog-replay-payload")),
		ServiceConfigDigest:   digestBytes([]byte("catalog-replay-config")),
		ServiceRegistrationID: digestBytes([]byte("catalog-replay-registration")),
		WorkflowID:            catalogReplayWorkflowID, RunID: catalogReplayRunID,
		SessionID: "session-catalog-replay", ConversationID: "conversation-catalog-replay",
		OwnerDBPath:         filepath.Join(ownerRoot, catalogReplayWorkflowID+".sqlite"),
		StoreRootPath:       filepath.Join(root, "store"),
		AdminRootPath:       filepath.Join(root, "admin"),
		StoreJournalPath:    filepath.Join(root, "store-audit.jsonl"),
		ProviderJournalPath: filepath.Join(root, "provider-audit.jsonl"),
		CommandJournalPath:  filepath.Join(root, "command-audit.jsonl"),
		TaskPrompt:          "Read the prepared fixture file.",
		TaskDigest:          digestBytes([]byte("catalog-replay-task-identity")),
		PromptDigest:        digestBytes([]byte("Read the prepared fixture file.")),
		Limits: workflowrecovery.Limits{MaxPreEffectRetries: 2, MaxCleanRestarts: 2, MaxHostCalls: 4, MaxTokens: 4096,
			Deadline: time.Now().UTC().Add(45 * time.Minute).Round(0)},
		ReservedAt: time.Now().UTC().Round(0),
	}
}

// reopenCatalogReplayJournal replays journal and points the owner-DB check at the
// test owner store. That is the only test-side change to the loaded catalog.
func reopenCatalogReplayJournal(t *testing.T, journal *audit.Journal, ownerRoot string) *piServiceWorkflowCatalog {
	t.Helper()
	catalog, err := loadPiServiceWorkflowCatalog(journal)
	if err != nil {
		t.Fatalf("replay catalog journal: %v", err)
	}
	catalog.ownerStoreRoot = ownerRoot
	return catalog
}

func summarizeCatalogAdmission(t *testing.T, catalog *piServiceWorkflowCatalog, admissionHash string) catalogReplaySummary {
	t.Helper()
	entry, ok := catalog.admissionEntry(admissionHash)
	if !ok {
		t.Fatalf("catalog has no admission %s", admissionHash)
	}
	return catalogReplaySummary{
		FreshAuthorizationUsed: entry.FreshAuthorizationUsed, IncarnationDigest: entry.OwnerIncarnation.Digest,
		InitialCheckpointDigest: entry.InitialCheckpointDigest, CheckpointBound: entry.CheckpointBound,
		LatestCheckpointDigest: entry.LatestCheckpointDigest, Terminal: entry.Terminal,
		TerminalRunID: entry.TerminalRunID, TerminalProofDigest: entry.TerminalProofDigest,
		TerminalProofSequence: entry.TerminalProofSequence, TerminalTransactionID: entry.TerminalTransactionID,
		OwnerDBPath: entry.OwnerDBPath,
	}
}

// runCatalogReplayFreshProcess reopens the journal in a child test binary, so no
// in-memory state from this run is reused, and returns the child's admission state.
func runCatalogReplayFreshProcess(t *testing.T, journalPath, ownerRoot, admissionHash string) catalogReplaySummary {
	t.Helper()
	dir := filepath.Dir(journalPath)
	requestPath, resultPath := filepath.Join(dir, "fresh-catalog-request.json"), filepath.Join(dir, "fresh-catalog-result.json")
	encoded, err := json.Marshal(catalogReplayFreshRequest{JournalPath: journalPath, OwnerRoot: ownerRoot,
		AdmissionHash: admissionHash, ResultPath: resultPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestCatalogReplayFreshProcessHelper$", "-test.count=1")
	command.Env = append(os.Environ(), catalogReplayRequestPathEnv+"="+requestPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fresh-process catalog replay failed: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	var summary catalogReplaySummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatal(err)
	}
	return summary
}
