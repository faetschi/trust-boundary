//go:build linux

package productsession

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"tbound/supervisor/internal/audit"
)

const kindTurnOperatorResolved = "product_turn_operator_resolved"
const OutcomeAbandoned = "abandoned"
const ReasonOperatorResolved = "operator_resolved"

// A turn publishes once, so a resolution closes at most a handful of pending records.
const maxPublicationClosed = 4

// OperatorEvidence commits the existing-only evidence checked by the composition.
// Empty journals use SHA-256 of the empty bytes as their head commitment.
// CommandsSettled marks a nonempty command journal whose every command the Store
// matched to a verified settlement.
// PublicationClosed lists the pending publication records of the workflow that Settle
// closed as unapplied. PublicationStagingLeft lists the absolute paths of unbound publication staging directories
// that closing left in place for the operator to remove.
// PublishedBaseline is set only when the publication fully applied before the process died: the project then holds
// exactly the candidate tree, which the resolution records as the turn's result baseline (outcome published). An older
// binary rejects the record, as it does the other fields added after format 2.
// Confirm, Settle and Close are transient: the composition uses them to keep the evidence
// handles open, to re-attest the project tree at append time and, for a real
// resolution only, to close durable evidence directly before the append. They are
// never part of the record.
type OperatorEvidence struct {
	WorkflowID             string       `json:"workflow_id"`
	BaselineDigest         string       `json:"baseline_digest"`
	ScanDigest             string       `json:"scan_digest"`
	StoreHead              string       `json:"store_head"`
	ProviderHead           string       `json:"provider_head"`
	CommandOwnerHead       string       `json:"command_owner_head,omitempty"`
	CommandsSettled        bool         `json:"commands_settled,omitempty"`
	PublicationClosed      []string     `json:"publication_closed,omitempty"`
	PublicationStagingLeft []string     `json:"publication_staging_left,omitempty"`
	PublishedBaseline      string       `json:"published_baseline,omitempty"`
	CommandHead            string       `json:"command_head"`
	CheckpointDigest       string       `json:"checkpoint_digest"`
	Confirm                func() error `json:"-"`
	Settle                 func() error `json:"-"`
	Close                  func() error `json:"-"`
}

type OperatorResolution struct {
	OperatorEvidence
	OperatorUID uint32 `json:"operator_uid"`
	Reason      string `json:"reason"`
	ResolvedAt  string `json:"resolved_at"`
}

type operatorResolvedRecord struct {
	header
	TurnID string `json:"turn_id"`
	OperatorResolution
	EvidenceDigest string `json:"evidence_digest"`
}

func (r operatorResolvedRecord) digest() string {
	r.EvidenceDigest = ""
	data, _ := json.Marshal(r)
	return sha256Digest(append([]byte("tbound-product-operator-resolution/v1\n"), data...))
}

func ValidateOperatorReason(reason string) error {
	if len(reason) > maxReasonBytes || !validPrompt(reason) {
		return fail(CodeInvalidRequest, "--reason must be nonempty plain text of at most 256 UTF-8 bytes")
	}
	return nil
}

// OperatorResolve holds the catalog writer lock from replay through evidence checking
// and append. It never records an owner epoch, so dry-run writes no catalog records.
// The trusted composition authenticates root and establishes worker exclusion.
func OperatorResolve(path string, registration Registration, turnID, reason string, dryRun bool,
	check func(Turn) (OperatorEvidence, error)) (Turn, error) {
	if err := ValidateOperatorReason(reason); err != nil {
		return Turn{}, err
	}
	if err := registration.validate(); err != nil {
		return Turn{}, err
	}
	j, err := audit.OpenExisting(path)
	if err != nil {
		return Turn{}, fmt.Errorf("catalog writer exclusion: %w", err)
	}
	defer j.Close()
	c := &Catalog{journal: j, st: newState(registration)}
	trace, err := j.Trace()
	if err != nil {
		return Turn{}, err
	}
	for _, record := range trace.Records {
		apply, err := c.st.reduce(record.Event.Kind, record.Event.Data)
		if err != nil {
			return Turn{}, fail(CodeCatalogCorrupt, fmt.Sprintf("record %d refused: %v", record.Sequence, err))
		}
		apply()
	}
	if c.st.regDigest != registration.Digest {
		return Turn{}, fail(CodeInvalidRequest, "registration differs from the catalog")
	}
	turn, err := c.st.turnInState(turnID, StateUnknown)
	if err != nil {
		return Turn{}, err
	}
	evidence, err := check(turn.clone())
	if err != nil {
		return Turn{}, err
	}
	if evidence.Close != nil {
		// The composition keeps its journals and project directory open until
		// the durable append below, so its evidence cannot go stale in between.
		defer evidence.Close()
	}
	r := operatorResolvedRecord{header: hdr, TurnID: turnID, OperatorResolution: OperatorResolution{OperatorEvidence: evidence, Reason: reason, ResolvedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	r.EvidenceDigest = r.digest()
	data, _ := json.Marshal(r)
	if _, err := c.st.reduce(kindTurnOperatorResolved, data); err != nil {
		return Turn{}, err
	}
	if evidence.Confirm != nil {
		// Re-measure the project inside the still-open evidence window, directly
		// before the record becomes durable.
		if err := evidence.Confirm(); err != nil {
			return Turn{}, err
		}
	}
	if dryRun {
		return turn.clone(), nil
	}
	if evidence.Settle != nil {
		if err := evidence.Settle(); err != nil {
			return Turn{}, err
		}
	}
	if err := c.commit(kindTurnOperatorResolved, turnID, r); err != nil {
		return Turn{}, err
	}
	return c.st.turns[turnID].clone(), nil
}

func (s *state) reduceOperatorResolved(data []byte) (func(), error) {
	var r operatorResolvedRecord
	if err := decode(data, &r); err != nil {
		return nil, err
	}
	t, err := s.turnInState(r.TurnID, StateUnknown)
	if err != nil {
		return nil, err
	}
	if err := ValidateOperatorReason(r.Reason); err != nil {
		return nil, err
	}
	at, timeErr := time.Parse(time.RFC3339Nano, r.ResolvedAt)
	published := r.PublishedBaseline != ""
	wantScan := t.BaselineDigest
	if published { // the project holds the candidate, which must differ from the admitted baseline
		wantScan = r.PublishedBaseline
	}
	if r.OperatorUID != 0 || timeErr != nil || at.IsZero() || at.Location() != time.UTC || t.Link == nil || r.WorkflowID != t.Link.WorkflowID ||
		r.BaselineDigest != t.BaselineDigest || r.ScanDigest != wantScan || published && (!validDigest(wantScan) || wantScan == t.BaselineDigest) || s.project(t.ProjectID).baseline != t.BaselineDigest || r.EvidenceDigest != r.digest() {
		return nil, fail(CodeInvalidRequest, "operator resolution identity, timestamp, baseline or evidence commitment is invalid")
	}
	if r.CommandOwnerHead != "" && !validDigest(r.CommandOwnerHead) {
		return nil, fail(CodeInvalidRequest, "invalid drained command owner head")
	}
	if r.CommandHead != sha256Digest(nil) && r.CommandOwnerHead == "" && !r.CommandsSettled {
		return nil, fail(CodeInvalidRequest, "operator resolution requires an empty or fully settled command journal")
	}
	if len(r.PublicationClosed) > maxPublicationClosed {
		return nil, fail(CodeInvalidRequest, "operator resolution closes too many publication records")
	}
	for _, id := range r.PublicationClosed {
		if id == "" || len(id) > 128 {
			return nil, fail(CodeInvalidRequest, "invalid closed publication record ID")
		}
	}
	if len(r.PublicationStagingLeft) > maxPublicationClosed {
		return nil, fail(CodeInvalidRequest, "operator resolution leaves too many staging directories")
	}
	for _, path := range r.PublicationStagingLeft {
		if !filepath.IsAbs(path) || len(path) > 4096 {
			return nil, fail(CodeInvalidRequest, "invalid left staging directory path")
		}
	}
	for _, digest := range []string{r.BaselineDigest, r.ScanDigest, r.StoreHead, r.ProviderHead, r.CommandHead, r.CheckpointDigest} {
		if !validDigest(digest) {
			return nil, fail(CodeInvalidRequest, "operator evidence digest is invalid")
		}
	}
	return func() {
		sess := s.sessions[t.SessionID]
		// Unknown review turns were already settled with a refund; charge it back.
		sess.Consumed = sess.Consumed.plus(t.Refund)
		sess.Revision++
		sess.Blocked = false
		s.project(t.ProjectID).blocked = false
		t.State, t.Outcome, t.TerminalReason = StateTerminal, OutcomeAbandoned, ReasonOperatorResolved
		t.Usage, t.Refund = t.Reservation, Budget{}
		// Retain a prior review receipt for the append-only observation history.
		// Never context-eligible: no final answer or receipt of the dead process is retained to replay.
		t.ContextEligible = false
		t.ResultBaselineDigest = t.BaselineDigest
		if published {
			t.Outcome, t.ResultBaselineDigest = OutcomePublished, r.PublishedBaseline
			s.project(t.ProjectID).baseline = r.PublishedBaseline
		}
		resolution := r.OperatorResolution
		t.OperatorResolution = &resolution
	}, nil
}
