//go:build linux

package workflowrecovery

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The D14 production-capacity tests run the genuine ledger API against a real
// SQLite owner and protected root. They measure what each transition encodes,
// writes, and grows, and they gate pre-effect terminal headroom. They establish
// logical owner headroom only. They establish no physical capacity bound and no
// D14 qualification.

// d14PCSweepSlack is how many page-ceiling steps above the captured page count
// the sweep probes. It covers the R1 gap window (slack 17..39), where the
// reservation fit but its terminal write did not, and extends past it.
const d14PCSweepSlack = 64

// The headroom bound restates SQLite's layout for this schema. It is written out
// here, not imported from production, so that a wrong production constant fails
// these tests. The production gate uses the same arithmetic.
//   - A page is 4096 bytes, but the usable size is at least 3841: the header's
//     reserved-byte count is at most 255. An overflow page carries 3837 bytes
//     after its 4-byte next-page pointer (auto_vacuum=NONE).
//   - A record keeps at least 457 bytes on its leaf page at usable size 3841, so
//     an n-byte payload spills at most n - 457 bytes.
//   - The non-blob columns and record header of an owner row add at most 1024
//     bytes (state at most 599 and event at most 418 at 256-byte identifiers).
//   - A transition touches four B-trees, each in one balance chain: the state
//     table (its PRIMARY KEY autoindex is unchanged by the UPDATE), the events
//     table, and the events' two autoindexes. A chain balances at most one level
//     per tree level, at most 20 levels (BTCURSOR_MAX_DEPTH), plus once after a
//     root split. Each balance adds at most 4 pages, and a root split adds 1:
//     4*(4*20+1) = 324 pages in total.
const (
	d14PCOverflowPayload = 3837
	d14PCMinLocal        = 457
	d14PCRowOverhead     = 1024
	d14PCSplitSlack      = 4 * (4*20 + 1)
	// d14PCMaxBlob is the registered checkpoint and proof cap. The schema CHECK
	// constraints and encodeCheckpointAndProof both enforce it.
	d14PCMaxBlob = maxCheckpointJSONBytes
)

// d14PCRowPages bounds the overflow pages of one owner record whose payload is
// payloadBytes of checkpoint or proof data.
func d14PCRowPages(payloadBytes int64) int64 {
	return (payloadBytes + d14PCRowOverhead - d14PCMinLocal + d14PCOverflowPayload - 1) / d14PCOverflowPayload
}

// d14PCTransitionPages bounds the pages one owner transition adds. The state row
// rewrite carries checkpointBytes. The appended event carries checkpoint and
// proof bytes together. Pages freed by the rewrite are not credited.
func d14PCTransitionPages(checkpointBytes, proofBytes int64) int64 {
	return d14PCRowPages(checkpointBytes) + d14PCRowPages(checkpointBytes+proofBytes) + d14PCSplitSlack
}

// d14PCFloorPages is the least overflow a record of payloadBytes can occupy in the
// measured files, whose usable size is 4096 (reserved bytes zero): at most 4061
// bytes stay on the leaf page, and an overflow page carries 4092 bytes.
func d14PCFloorPages(payloadBytes int64) int64 {
	const maxLocal = 4061
	const overflowPayload = 4096 - 4
	return (payloadBytes - maxLocal + overflowPayload - 1) / overflowPayload
}

// d14PCMaxID returns the longest identifier validID admits. Each '<' is valid
// printable ASCII, but encoding/json writes it as <, so the encoded field
// is six bytes per character.
func d14PCMaxID(tag string) string {
	return strings.Repeat("<", 256-len(tag)) + tag
}

// d14PCSample is one owner and root observation after a committed transition.
type d14PCSample struct {
	Step          string `json:"step"`
	OwnerSequence int64  `json:"owner_sequence"`
	OwnerCommits  int64  `json:"owner_commits"`
	CheckpointB   int64  `json:"checkpoint_json_bytes"`
	ProofB        int64  `json:"proof_json_bytes"`
	OwnerPages    int64  `json:"owner_page_count"`
	OwnerFreePage int64  `json:"owner_freelist_count"`
	OwnerWALB     int64  `json:"owner_wal_bytes"`
	RootRevision  int64  `json:"root_revision"`
	RootPages     int64  `json:"root_page_count"`
	RootWALB      int64  `json:"root_wal_bytes"`
}

func d14PCObserve(t *testing.T, owner *SQLiteOwner, root *ProtectedTrustedRoot, workflow, step string,
	previousSequence int64) d14PCSample {
	t.Helper()
	ctx := context.Background()
	sample := d14PCSample{Step: step}
	if err := owner.db.QueryRowContext(ctx, `SELECT sequence FROM workflow_recovery_state WHERE workflow_id=?`,
		workflow).Scan(&sample.OwnerSequence); err != nil {
		t.Fatalf("read owner sequence after %s: %v", step, err)
	}
	sample.OwnerCommits = sample.OwnerSequence - previousSequence
	err := owner.db.QueryRowContext(ctx, `SELECT length(checkpoint_json),length(proof_json) FROM workflow_recovery_events
		WHERE workflow_id=? AND sequence=?`, workflow, sample.OwnerSequence).Scan(&sample.CheckpointB, &sample.ProofB)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("read owner event bytes after %s: %v", step, err)
	}
	sample.OwnerPages = readSQLitePageGeometry(t, owner.db).PageCount
	if err := owner.db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&sample.OwnerFreePage); err != nil {
		t.Fatalf("read owner freelist after %s: %v", step, err)
	}
	sample.OwnerWALB = d14PCFileBytes(owner.path + "-wal")
	err = root.db.QueryRowContext(ctx, `SELECT revision FROM workflow_root_state WHERE workflow_id=?`,
		workflow).Scan(&sample.RootRevision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("read root revision after %s: %v", step, err)
	}
	sample.RootPages = readSQLitePageGeometry(t, root.db).PageCount
	sample.RootWALB = d14PCFileBytes(root.path + "-wal")
	return sample
}

func d14PCFileBytes(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func d14PCJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode D14 capacity record: %v", err)
	}
	return string(encoded)
}

// d14PCCaptureCall persists the verified provider capture for a host call that
// ReserveHostCall already charged. It mirrors prepareCapturedCallForEffect but
// takes caller-chosen identifiers so the tool-call shape can be maximal.
func d14PCCaptureCall(t *testing.T, ledger *Ledger, conversation, toolCall, operation string) EffectBinding {
	t.Helper()
	checkpoint, _ := ledger.Snapshot()
	pins := clonePins(checkpoint.Pins)
	call := pins.HostCallReservation
	call.Disposition = HostCallTerminal
	call.OutcomeDigest = testDigest("verified provider response for captured call")
	call.CapturedCall = &CapturedCallPin{
		WorkflowID: checkpoint.Pins.WorkflowID, ConversationID: conversation,
		HostCallID: call.Binding.CallID, ToolCallID: toolCall, OutcomeDigest: call.OutcomeDigest,
		RecoveryGeneration: checkpoint.Pins.RecoveryGeneration, OwnerEpoch: checkpoint.Pins.OwnerEpoch,
		Disposition: CapturedCallAvailable,
	}
	active := &pins.ProviderTranscripts[len(pins.ProviderTranscripts)-1]
	active.JournalCount = 1
	active.JournalHead = testDigest("captured provider journal head")
	active.ProjectionCount = 1
	active.ProjectionDigest = testDigest("captured provider projection")
	active.ProjectionHead = testDigest("captured provider projection head")
	active.Disposition = DispositionAwaitingGateDecision
	pins.ProviderProjectionCount = 1
	pins.ProviderProjectionDigest = TranscriptPinsDigest(pins.ProviderTranscripts)
	pins.ProviderProjectionHead = active.ProjectionHead
	pins.ProviderDisposition = DispositionAwaitingGateDecision
	pins.PendingCallDisposition = PendingCallCaptured
	if _, err := ledger.AdvanceCheckpoint(context.Background(), allowProgress{}, pins); err != nil {
		t.Fatalf("persist verified provider capture: %v", err)
	}
	checkpoint, _ = ledger.Snapshot()
	binding := testEffectBinding(operation)
	binding.WorkflowID = checkpoint.Pins.WorkflowID
	binding.ConversationID = call.CapturedCall.ConversationID
	binding.HostCallID = call.CapturedCall.HostCallID
	binding.ToolCallID = call.CapturedCall.ToolCallID
	binding.HostCallOutcomeDigest = call.CapturedCall.OutcomeDigest
	binding.RecoveryGeneration = call.CapturedCall.RecoveryGeneration
	binding.OwnerEpoch = call.CapturedCall.OwnerEpoch
	return binding
}

// d14PCSettle records the verified terminal Store outcome for a consumed effect.
func d14PCSettle(ledger *Ledger, receipt Receipt) error {
	reserved, _ := ledger.Snapshot()
	terminalPins := clonePins(reserved.Pins)
	terminalPins.StoreJournalCount++
	terminalPins.StoreJournalHead = testDigest("terminal Store outcome journal head")
	terminalPins.EffectReservation.Disposition = EffectReservationTerminal
	terminalPins.EffectReservation.OutcomeDigest = testDigest("verified terminal Store outcome")
	_, err := ledger.RecordTerminalEffectOutcome(context.Background(), receipt, acceptBoundTerminalEffect{}, terminalPins)
	return err
}

// d14PCAdvanceResult checkpoints the durable provider result after settlement.
func d14PCAdvanceResult(ledger *Ledger) error {
	settled, _ := ledger.Snapshot()
	resultPins := clonePins(settled.Pins)
	resultPins.HostCallReservation.CapturedCall = nil
	resultPins.PendingCallDisposition = PendingCallTerminal
	resultPins.ProviderDisposition = DispositionAwaitingProviderExchange
	active := &resultPins.ProviderTranscripts[len(resultPins.ProviderTranscripts)-1]
	active.JournalCount++
	active.JournalHead = testDigest("terminal provider result journal head")
	active.ProjectionCount++
	active.ProjectionDigest = testDigest("terminal provider result projection")
	active.ProjectionHead = active.JournalHead
	active.Disposition = DispositionAwaitingProviderExchange
	resultPins.ProviderProjectionCount = totalProjectionCount(resultPins.ProviderTranscripts)
	resultPins.ProviderProjectionDigest = TranscriptPinsDigest(resultPins.ProviderTranscripts)
	resultPins.ProviderProjectionHead = active.ProjectionHead
	_, err := ledger.AdvanceCheckpoint(context.Background(), allowProgress{}, resultPins)
	return err
}

// TestD14ProductionToolCallCommitsAndEncodedBytes runs one maximal-identifier
// tool call through the genuine ledger path and counts the owner commits each
// API call makes, along with the encoded bytes, pages, and WAL it leaves behind.
func TestD14ProductionToolCallCommitsAndEncodedBytes(t *testing.T) {
	ctx := context.Background()
	rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
	workflow := d14PCMaxID("workflow-")
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{})
	root, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("create protected root: %v", err)
	}
	defer root.Close()
	owner, err := CreateFreshSQLiteOwner(filepath.Join(ownerStoreRoot, "workflow.sqlite"), root)
	if err != nil {
		t.Fatalf("create SQLite owner: %v", err)
	}
	defer owner.Close()
	pins := testPins(root.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(), workflow,
		d14PCMaxID("session-"), 1, 1)
	limits := testLimits()
	if _, err := InitializeFresh(ctx, owner, pins, limits); err != nil {
		t.Fatalf("initialize maximal-identifier workflow: %v", err)
	}
	checkpoint, _, exists, err := owner.LoadCheckpoint(ctx, workflow)
	if err != nil || !exists {
		t.Fatalf("load initial checkpoint: exists=%v err=%v", exists, err)
	}
	ledger, err := Open(ctx, owner, OpenOptions{WorkflowID: workflow, OwnerEpoch: 1,
		RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits})
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}

	var samples []d14PCSample
	var sequence int64
	record := func(step string) {
		sample := d14PCObserve(t, owner, root, workflow, step, sequence)
		sequence = sample.OwnerSequence
		samples = append(samples, sample)
		t.Logf("D14_PC_SAMPLE %s", d14PCJSON(t, sample))
	}
	conversation := d14PCMaxID("conversation-")
	hostCall := d14PCMaxID("host-call-")
	toolCall := d14PCMaxID("tool-call-")
	operation := d14PCMaxID("operation-")
	record("initialize-fresh")
	if _, err := ledger.RegisterInitialTranscript(ctx, allowProgress{}, emptyTranscript(conversation)); err != nil {
		t.Fatalf("register initial transcript: %v", err)
	}
	record("register-initial-transcript")
	if _, err := ledger.ReserveHostCall(ctx, testHostCallBinding(hostCall, 41)); err != nil {
		t.Fatalf("charge provider request: %v", err)
	}
	record("reserve-host-call")
	binding := d14PCCaptureCall(t, ledger, conversation, toolCall, operation)
	record("advance-captured-call")
	receipt, err := ledger.ReserveEffect(ctx, binding, exactCapturedOperationVerifier{expected: binding})
	if err != nil {
		t.Fatalf("reserve the exact captured effect: %v", err)
	}
	record("reserve-effect")
	if err := d14PCSettle(ledger, receipt); err != nil {
		t.Fatalf("settle the exact Store effect: %v", err)
	}
	record("record-terminal-outcome")
	if err := d14PCAdvanceResult(ledger); err != nil {
		t.Fatalf("checkpoint durable provider result: %v", err)
	}
	record("advance-tool-result")

	var toolCallCommits int64
	for _, sample := range samples[2:] {
		toolCallCommits += sample.OwnerCommits
		if sample.OwnerCommits != 1 {
			t.Fatalf("%s made %d owner commits, want exactly one CAS per API call", sample.Step, sample.OwnerCommits)
		}
		if sample.CheckpointB > maxCheckpointJSONBytes || sample.ProofB > maxCheckpointJSONBytes {
			t.Fatalf("%s encoded checkpoint=%d proof=%d bytes, above the registered %d-byte cap",
				sample.Step, sample.CheckpointB, sample.ProofB, maxCheckpointJSONBytes)
		}
	}
	if toolCallCommits != 5 {
		t.Fatalf("one tool call made %d owner commits, want 5 (host call, capture, effect, terminal, result)", toolCallCommits)
	}
	t.Logf("D14_PC_TOOL_CALL owner_commits=%d max_identifier_bytes=256 transcripts=1", toolCallCommits)
}

// d14PCFixture is a captured, unsettled maximal-identifier tool call on a fresh
// protected root and SQLite owner, ready for ReserveEffect.
type d14PCFixture struct {
	root        *ProtectedTrustedRoot
	owner       *SQLiteOwner
	binding     EffectBinding
	workflow    string
	path        string
	pages       int64
	freePages   int64
	prior       Checkpoint
	priorDigest string
}

func newD14PCCapturedFixture(t *testing.T) d14PCFixture {
	t.Helper()
	ctx := context.Background()
	rootPath, ownerStoreRoot, _ := newProtectedRootTestDirs(t)
	workflow := d14PCMaxID("workflow-")
	boundary := newProtectedRootTestBoundary(t, ownerStoreRoot, workflow, &processExitDrainVerifier{})
	root, err := CreateFreshProtectedRoot(rootPath, ownerStoreRoot, boundary)
	if err != nil {
		t.Fatalf("create protected root: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	path := filepath.Join(ownerStoreRoot, "workflow.sqlite")
	owner, err := CreateFreshSQLiteOwner(path, root)
	if err != nil {
		t.Fatalf("create SQLite owner: %v", err)
	}
	pins := testPins(root.TrustedRootIdentityDigest(), owner.OwnerIncarnationDigest(), workflow,
		d14PCMaxID("session-"), 1, 1)
	limits := testLimits()
	if _, err := InitializeFresh(ctx, owner, pins, limits); err != nil {
		t.Fatalf("initialize maximal-identifier workflow: %v", err)
	}
	checkpoint, _, _, err := owner.LoadCheckpoint(ctx, workflow)
	if err != nil {
		t.Fatalf("load initial checkpoint: %v", err)
	}
	ledger, err := Open(ctx, owner, OpenOptions{WorkflowID: workflow, OwnerEpoch: 1,
		RetainedPins: checkpoint.Pins, Limits: checkpoint.Limits})
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	conversation := d14PCMaxID("conversation-")
	hostCall := d14PCMaxID("host-call-")
	if _, err := ledger.RegisterInitialTranscript(ctx, allowProgress{}, emptyTranscript(conversation)); err != nil {
		t.Fatalf("register initial transcript: %v", err)
	}
	if _, err := ledger.ReserveHostCall(ctx, testHostCallBinding(hostCall, 41)); err != nil {
		t.Fatalf("charge provider request: %v", err)
	}
	binding := d14PCCaptureCall(t, ledger, conversation, d14PCMaxID("tool-call-"), d14PCMaxID("operation-"))
	prior, priorDigest := ledger.Snapshot()
	return d14PCFixture{root: root, owner: owner, binding: binding, workflow: workflow, path: path,
		pages: readSQLitePageGeometry(t, owner.db).PageCount, freePages: d14PCFreePages(t, owner),
		prior: prior, priorDigest: priorDigest}
}

func d14PCFreePages(t *testing.T, owner *SQLiteOwner) int64 {
	t.Helper()
	var free int64
	if err := owner.db.QueryRowContext(context.Background(), `PRAGMA freelist_count`).Scan(&free); err != nil {
		t.Fatalf("read owner freelist: %v", err)
	}
	return free
}

// d14PCReservationBytes reserves the captured effect at the fixture's production
// ceiling and returns the encoded checkpoint and proof sizes that reservation
// commits. Every probe reproduces the same bytes, so the threshold is exact.
func d14PCReservationBytes(t *testing.T, fixture d14PCFixture) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	ledger, err := Open(ctx, fixture.owner, OpenOptions{WorkflowID: fixture.workflow,
		OwnerEpoch: fixture.prior.Pins.OwnerEpoch, RetainedPins: fixture.prior.Pins, Limits: fixture.prior.Limits})
	if err != nil {
		t.Fatalf("open reference ledger: %v", err)
	}
	if _, err := ledger.ReserveEffect(ctx, fixture.binding, exactCapturedOperationVerifier{expected: fixture.binding}); err != nil {
		t.Fatalf("reserve reference effect at production ceiling: %v", err)
	}
	var sequence, checkpointBytes, proofBytes int64
	if err := fixture.owner.db.QueryRowContext(ctx, `SELECT sequence FROM workflow_recovery_state WHERE workflow_id=?`,
		fixture.workflow).Scan(&sequence); err != nil {
		t.Fatalf("read reference sequence: %v", err)
	}
	if err := fixture.owner.db.QueryRowContext(ctx, `SELECT length(checkpoint_json),length(proof_json) FROM workflow_recovery_events
		WHERE workflow_id=? AND sequence=?`, fixture.workflow, sequence).Scan(&checkpointBytes, &proofBytes); err != nil {
		t.Fatalf("read reference reservation bytes: %v", err)
	}
	return checkpointBytes, proofBytes
}

// d14PCHeadroomSlack returns the page-ceiling slack above the captured page count
// at which the gate grants the captured reservation. It is the reservation's own
// rows plus the worst-case terminal transition, less the free pages already held.
func d14PCHeadroomSlack(t *testing.T) int64 {
	t.Helper()
	fixture := newD14PCCapturedFixture(t)
	defer fixture.owner.Close()
	checkpointBytes, proofBytes := d14PCReservationBytes(t, fixture)
	required := d14PCTransitionPages(checkpointBytes, proofBytes) + d14PCTransitionPages(d14PCMaxBlob, d14PCMaxBlob)
	return required - fixture.freePages
}

// d14PCReservationBound is the bound on the pages the captured reservation adds:
// its own state rewrite and event record, plus the split slack.
func d14PCReservationBound(t *testing.T) int64 {
	t.Helper()
	fixture := newD14PCCapturedFixture(t)
	defer fixture.owner.Close()
	checkpointBytes, proofBytes := d14PCReservationBytes(t, fixture)
	return d14PCTransitionPages(checkpointBytes, proofBytes)
}

// d14PCProbe is one page-ceiling observation of the captured effect.
type d14PCProbe struct {
	Slack              int64  `json:"slack_pages"`
	Ceiling            int64  `json:"max_page_count"`
	ReserveAdmitted    bool   `json:"reserve_admitted"`
	RefusedBeforeGrant bool   `json:"refused_before_grant"`
	ReserveError       string `json:"reserve_error,omitempty"`
	PagesAfterReserve  int64  `json:"pages_after_reserve,omitempty"`
	TerminalAdmitted   bool   `json:"terminal_admitted"`
	TerminalError      string `json:"terminal_error,omitempty"`
	PagesAfterTerminal int64  `json:"pages_after_terminal,omitempty"`
}

// d14PCRequireUnchangedHead asserts that a refused reservation left no grant: no
// receipt, the predecessor head in memory and on disk, and an unpoisoned owner.
func d14PCRequireUnchangedHead(t *testing.T, fixture d14PCFixture, ledger *Ledger, owner *SQLiteOwner, receipt Receipt) {
	t.Helper()
	if receipt.CheckpointDigest() != "" || receipt.WorkflowID() != "" {
		t.Fatalf("refused reservation returned a receipt: workflow=%q digest=%q", receipt.WorkflowID(), receipt.CheckpointDigest())
	}
	if _, digest := ledger.Snapshot(); digest != fixture.priorDigest {
		t.Fatalf("refused reservation changed the in-memory head: %s, want predecessor %s", digest, fixture.priorDigest)
	}
	if owner.poisoned {
		t.Fatal("refused reservation poisoned the owner")
	}
	_, digest, exists, err := owner.LoadCheckpoint(context.Background(), fixture.workflow)
	if err != nil || !exists || digest != fixture.priorDigest {
		t.Fatalf("durable head after refusal: digest=%s exists=%v err=%v; want predecessor %s",
			digest, exists, err, fixture.priorDigest)
	}
}

// d14PCProbeHeadroom reopens a captured fixture with a page ceiling of
// (captured pages + slack), then attempts the effect reservation and, if it was
// granted, its terminal outcome. Each probe owns its own protected root and owner.
func d14PCProbeHeadroom(t *testing.T, slack int64) d14PCProbe {
	t.Helper()
	ctx := context.Background()
	fixture := newD14PCCapturedFixture(t)
	probe := d14PCProbe{Slack: slack, Ceiling: fixture.pages + slack}
	if err := fixture.owner.Close(); err != nil {
		t.Fatalf("close captured owner before ceiling reopen: %v", err)
	}
	owner, err := openExistingSQLiteOwnerWithCeiling(fixture.path, fixture.root, probe.Ceiling)
	if err != nil {
		t.Fatalf("reopen owner at ceiling %d: %v", probe.Ceiling, err)
	}
	defer owner.Close()
	ledger, err := Open(ctx, owner, OpenOptions{WorkflowID: fixture.workflow, OwnerEpoch: fixture.prior.Pins.OwnerEpoch,
		RetainedPins: fixture.prior.Pins, Limits: fixture.prior.Limits})
	if err != nil {
		t.Fatalf("reopen ledger at ceiling %d: %v", probe.Ceiling, err)
	}
	receipt, reserveErr := ledger.ReserveEffect(ctx, fixture.binding, exactCapturedOperationVerifier{expected: fixture.binding})
	if reserveErr != nil {
		probe.ReserveError = reserveErr.Error()
		probe.RefusedBeforeGrant = errors.Is(reserveErr, ErrExhausted) && !errors.Is(reserveErr, ErrCAS)
		d14PCRequireUnchangedHead(t, fixture, ledger, owner, receipt)
		return probe
	}
	probe.ReserveAdmitted = true
	probe.PagesAfterReserve = readSQLitePageGeometry(t, owner.db).PageCount
	if err := d14PCSettle(ledger, receipt); err != nil {
		probe.TerminalError = err.Error()
		return probe
	}
	probe.TerminalAdmitted = true
	probe.PagesAfterTerminal = readSQLitePageGeometry(t, owner.db).PageCount
	return probe
}

// TestD14PreEffectTerminalHeadroomGate is the default-on regression for the
// pre-effect headroom gap. Before the gate, ReserveEffect was granted at 23 page
// ceilings (slack 17..39) where the reservation fit but its terminal write did
// not, so authority was consumed with no durable outcome. The probes are slack 0
// through 64 and the threshold-1 and threshold ceilings. Each probed ceiling below
// the threshold must be refused before any grant. The threshold ceiling itself
// must be granted and settled, for this fixture's 23-page terminal only. Ceilings
// above the threshold are not probed, and no worst-case terminal is measured.
func TestD14PreEffectTerminalHeadroomGate(t *testing.T) {
	threshold := d14PCHeadroomSlack(t)
	slacks := make([]int64, 0, d14PCSweepSlack+3)
	for slack := int64(0); slack <= d14PCSweepSlack; slack++ {
		slacks = append(slacks, slack)
	}
	slacks = append(slacks, threshold-1, threshold)
	var gaps, admittedBelow, refusedAtOrAbove int
	reserveBound := d14PCReservationBound(t)
	terminalBound := d14PCTransitionPages(d14PCMaxBlob, d14PCMaxBlob)
	for _, slack := range slacks {
		probe := d14PCProbeHeadroom(t, slack)
		t.Logf("D14_PC_PROBE %s", d14PCJSON(t, probe))
		if probe.ReserveAdmitted && !probe.TerminalAdmitted {
			gaps++
			t.Errorf("slack %d granted ReserveEffect but refused its terminal write: %s", slack, probe.TerminalError)
		}
		if probe.ReserveAdmitted && probe.TerminalAdmitted {
			// Growth measured on the real Ledger must lie inside each bound.
			captured := probe.Ceiling - slack
			if grown := probe.PagesAfterReserve - captured; grown > reserveBound {
				t.Errorf("slack %d reservation grew %d pages, above its bound %d", slack, grown, reserveBound)
			}
			if grown := probe.PagesAfterTerminal - probe.PagesAfterReserve; grown > terminalBound {
				t.Errorf("slack %d terminal grew %d pages, above its bound %d", slack, grown, terminalBound)
			}
		}
		if slack < threshold && !probe.RefusedBeforeGrant {
			admittedBelow++
			t.Errorf("slack %d is below the headroom threshold %d but was not refused before grant: %+v", slack, threshold, probe)
		}
		if slack >= threshold && (!probe.ReserveAdmitted || !probe.TerminalAdmitted) {
			refusedAtOrAbove++
			t.Errorf("slack %d is at or above the headroom threshold %d but was not granted and settled: %+v", slack, threshold, probe)
		}
	}
	t.Logf("D14_PC_GATE threshold_slack=%d probes=%d reserve_without_terminal=%d below_threshold_granted=%d at_or_above_refused=%d",
		threshold, len(slacks), gaps, admittedBelow, refusedAtOrAbove)
}

// TestD14EffectRefusalLeavesHeadUnchangedAndRetryAdmitted refuses one reservation
// a page below the headroom threshold, then reopens the same SQLite file in this
// process at the threshold. The reopen is same-process, not a fresh-process
// recovery. The refusal must leave the predecessor head and owner epoch intact,
// and the retry must reserve and settle the captured effect once.
func TestD14EffectRefusalLeavesHeadUnchangedAndRetryAdmitted(t *testing.T) {
	ctx := context.Background()
	threshold := d14PCHeadroomSlack(t)
	fixture := newD14PCCapturedFixture(t)
	if err := fixture.owner.Close(); err != nil {
		t.Fatalf("close captured owner before ceiling reopen: %v", err)
	}
	low, err := openExistingSQLiteOwnerWithCeiling(fixture.path, fixture.root, fixture.pages+threshold-1)
	if err != nil {
		t.Fatalf("reopen owner one page below the threshold: %v", err)
	}
	lowLedger, err := Open(ctx, low, OpenOptions{WorkflowID: fixture.workflow, OwnerEpoch: fixture.prior.Pins.OwnerEpoch,
		RetainedPins: fixture.prior.Pins, Limits: fixture.prior.Limits})
	if err != nil {
		t.Fatalf("reopen ledger below the threshold: %v", err)
	}
	pagesBefore := readSQLitePageGeometry(t, low.db).PageCount
	receipt, refused := lowLedger.ReserveEffect(ctx, fixture.binding, exactCapturedOperationVerifier{expected: fixture.binding})
	if !errors.Is(refused, ErrExhausted) || errors.Is(refused, ErrCAS) {
		t.Fatalf("reservation below the threshold = %v, want typed ErrExhausted without ErrCAS", refused)
	}
	d14PCRequireUnchangedHead(t, fixture, lowLedger, low, receipt)
	if pagesAfter := readSQLitePageGeometry(t, low.db).PageCount; pagesAfter != pagesBefore {
		t.Fatalf("refused reservation changed the page count from %d to %d; the refusal must precede every write", pagesBefore, pagesAfter)
	}
	if err := low.Close(); err != nil {
		t.Fatalf("close owner after refusal: %v", err)
	}

	high, err := openExistingSQLiteOwnerWithCeiling(fixture.path, fixture.root, fixture.pages+threshold)
	if err != nil {
		t.Fatalf("reopen owner at the threshold: %v", err)
	}
	defer high.Close()
	checkpoint, digest, exists, err := high.LoadCheckpoint(ctx, fixture.workflow)
	if err != nil || !exists || digest != fixture.priorDigest || checkpoint.Pins.OwnerEpoch != fixture.prior.Pins.OwnerEpoch {
		t.Fatalf("reopened head after refusal: digest=%s exists=%v epoch=%d err=%v; want predecessor %s at epoch %d",
			digest, exists, checkpoint.Pins.OwnerEpoch, err, fixture.priorDigest, fixture.prior.Pins.OwnerEpoch)
	}
	retryLedger, err := Open(ctx, high, OpenOptions{WorkflowID: fixture.workflow, OwnerEpoch: fixture.prior.Pins.OwnerEpoch,
		RetainedPins: fixture.prior.Pins, Limits: fixture.prior.Limits})
	if err != nil {
		t.Fatalf("reopen ledger at the threshold: %v", err)
	}
	granted, err := retryLedger.ReserveEffect(ctx, fixture.binding, exactCapturedOperationVerifier{expected: fixture.binding})
	if err != nil {
		t.Fatalf("retry the captured effect at the threshold: %v", err)
	}
	if err := d14PCSettle(retryLedger, granted); err != nil {
		t.Fatalf("settle the retried effect: %v", err)
	}
}

// TestD14ReservedEffectRefusesNonTerminalCommits pins the window between a granted
// ReserveEffect and its terminal outcome. While the effect is reserved, every other
// commit of that workflow must refuse with its typed error before any write, and
// the terminal accounting of the same effect must still settle. The owner subtest
// runs on the real protected root and SQLite owner. The recovery subtest takes its
// replacement epoch on the test trusted root, as the other replacement-epoch tests
// do: the real root's drain check requires the superseded owner process to be gone,
// and this single test process is that owner.
func TestD14ReservedEffectRefusesNonTerminalCommits(t *testing.T) {
	ctx := context.Background()
	t.Run("owner commits", func(t *testing.T) {
		fixture := newD14PCCapturedFixture(t)
		defer fixture.owner.Close()
		ledger, err := Open(ctx, fixture.owner, OpenOptions{WorkflowID: fixture.workflow,
			OwnerEpoch: fixture.prior.Pins.OwnerEpoch, RetainedPins: fixture.prior.Pins, Limits: fixture.prior.Limits})
		if err != nil {
			t.Fatalf("open ledger: %v", err)
		}
		receipt, err := ledger.ReserveEffect(ctx, fixture.binding, exactCapturedOperationVerifier{expected: fixture.binding})
		if err != nil {
			t.Fatalf("grant the captured effect: %v", err)
		}
		_, reservedDigest := ledger.Snapshot()
		reservedHead := d14PCFixture{workflow: fixture.workflow, priorDigest: reservedDigest}
		refusals := []struct {
			name string
			want error
			run  func() (Receipt, error)
		}{
			{"AdvanceCheckpoint", ErrPendingUnknown, func() (Receipt, error) {
				return Receipt{}, d14PCAdvanceResult(ledger)
			}},
			{"ReservePreEffectRetry", ErrPendingUnknown, func() (Receipt, error) {
				return ledger.ReservePreEffectRetry(ctx)
			}},
			{"ReserveHostCall", ErrPendingUnknown, func() (Receipt, error) {
				return ledger.ReserveHostCall(ctx, testHostCallBinding(d14PCMaxID("host-call-late-"), 41))
			}},
			{"second ReserveEffect for the same captured call", ErrPendingUnknown, func() (Receipt, error) {
				return ledger.ReserveEffect(ctx, fixture.binding, exactCapturedOperationVerifier{expected: fixture.binding})
			}},
			{"RegisterInitialTranscript", ErrInvalid, func() (Receipt, error) {
				return ledger.RegisterInitialTranscript(ctx, allowProgress{}, emptyTranscript(d14PCMaxID("conversation-late-")))
			}},
			{"InitializeFresh for the same workflow", ErrCAS, func() (Receipt, error) {
				pins := testPins(fixture.root.TrustedRootIdentityDigest(), fixture.owner.OwnerIncarnationDigest(),
					fixture.workflow, d14PCMaxID("session-fresh-"), 1, 1)
				return InitializeFresh(ctx, fixture.owner, pins, testLimits())
			}},
		}
		for _, refusal := range refusals {
			refused, err := refusal.run()
			if !errors.Is(err, refusal.want) {
				t.Fatalf("%s while reserved = %v, want typed %v", refusal.name, err, refusal.want)
			}
			d14PCRequireUnchangedHead(t, reservedHead, ledger, fixture.owner, refused)
			if fixture.root.poisoned {
				t.Fatalf("%s poisoned the protected root", refusal.name)
			}
		}
		// The terminal accounting of the same effect is the one commit the reservation
		// admits, so it must still settle after every refusal above.
		if err := d14PCSettle(ledger, receipt); err != nil {
			t.Fatalf("settle the reserved effect after the refusals: %v", err)
		}
	})
	t.Run("recovery observation after a replacement fence", func(t *testing.T) {
		const workflow = "workflow-d14-reserved-fence"
		root := newTestTrustedRoot()
		root.setFence(workflow, 1, 0)
		directory := t.TempDir()
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		owner, err := CreateFreshSQLiteOwner(filepath.Join(directory, "workflow-owner.sqlite"), root)
		if err != nil {
			t.Fatalf("create SQLite owner: %v", err)
		}
		defer owner.Close()
		pins := testPins(root.identity, owner.OwnerIncarnationDigest(), workflow, "session-initial", 1, 1)
		limits := testLimits()
		if _, err := InitializeFresh(ctx, owner, pins, limits); err != nil {
			t.Fatalf("initialize fresh workflow: %v", err)
		}
		first, err := Open(ctx, owner, OpenOptions{WorkflowID: workflow, OwnerEpoch: 1,
			RetainedPins: pins, Limits: limits})
		if err != nil {
			t.Fatalf("open first owner epoch: %v", err)
		}
		conversation := "conversation-fence"
		hostCall := "host-call-fence"
		if _, err := first.RegisterInitialTranscript(ctx, allowProgress{}, emptyTranscript(conversation)); err != nil {
			t.Fatalf("register initial transcript: %v", err)
		}
		if _, err := first.ReserveHostCall(ctx, testHostCallBinding(hostCall, 41)); err != nil {
			t.Fatalf("charge provider request: %v", err)
		}
		binding := d14PCCaptureCall(t, first, conversation, "tool-call-fence", "operation-fence")
		if _, err := first.ReserveEffect(ctx, binding, exactCapturedOperationVerifier{expected: binding}); err != nil {
			t.Fatalf("grant the captured effect: %v", err)
		}
		reserved, reservedDigest := first.Snapshot()
		root.setFence(workflow, 2, 2)
		replacement, err := Open(ctx, owner, OpenOptions{WorkflowID: workflow, OwnerEpoch: 2,
			RetainedPins: reserved.Pins, Limits: reserved.Limits})
		if err != nil {
			t.Fatalf("open replacement epoch: %v", err)
		}
		// A clean restart needs a verified observation. ObserveRecovery refuses an
		// observation that still holds a reserved effect, so no restart commit is built.
		if _, err := replacement.ObserveRecovery(ctx, allowRecovery{}, clonePins(reserved.Pins)); !errors.Is(err, ErrPendingUnknown) {
			t.Fatalf("recovery observation of a reserved effect = %v, want typed ErrPendingUnknown", err)
		}
		d14PCRequireUnchangedHead(t, d14PCFixture{workflow: workflow, priorDigest: reservedDigest}, replacement, owner, Receipt{})
	})
}

// TestD14MaxTransitionPagesWithinBound measures real SQLite growth for the largest
// transition the schema admits: a maximum-size state rewrite and a maximum-size
// event record carrying checkpoint and proof. The rows are raw SQL values that
// the schema CHECK constraints accept, so this is a synthetic schema probe, not
// production proof; the gate tests above exercise the Ledger API. The growth must
// lie between the page floor the layout forces and the bound the gate reserves.
func TestD14MaxTransitionPagesWithinBound(t *testing.T) {
	const workflow = "workflow-d14-max-transition"
	owner, _, _, _, _ := newWorkflowHarness(t, workflow, testLimits())
	ctx := context.Background()
	blob := bytes.Repeat([]byte("x"), d14PCMaxBlob)
	before := readSQLitePageGeometry(t, owner.db).PageCount
	tx, err := owner.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin maximum transition: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_recovery_state SET sequence=2,checkpoint_digest=?,checkpoint_json=?
		WHERE workflow_id=?`, testDigest("d14-max-state"), blob, workflow); err != nil {
		t.Fatalf("rewrite maximum state row: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_recovery_events(workflow_id,sequence,owner_epoch,expected_digest,
		checkpoint_digest,checkpoint_json,proof_json) VALUES(?,2,1,'',?,?,?)`, workflow, testDigest("d14-max-event"), blob, blob); err != nil {
		t.Fatalf("append maximum event: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit maximum transition: %v", err)
	}
	growth := readSQLitePageGeometry(t, owner.db).PageCount - before
	floor := d14PCFloorPages(d14PCMaxBlob) + d14PCFloorPages(2*d14PCMaxBlob)
	bound := d14PCTransitionPages(d14PCMaxBlob, d14PCMaxBlob)
	t.Logf("D14_PC_MAX_TRANSITION pages_grown=%d floor=%d bound=%d", growth, floor, bound)
	if growth < floor || growth > bound {
		t.Fatalf("maximum transition grew %d pages; the bound requires %d <= growth <= %d", growth, floor, bound)
	}
}
