//go:build linux

package productsession

import (
	"encoding/json"
	"fmt"
	"time"

	"tbound/supervisor/internal/audit"
)

const kindTurnOperatorResolved = "product_turn_operator_resolved"
const OutcomeAbandoned = "abandoned"
const ReasonOperatorResolved = "operator_resolved"

// OperatorEvidence commits the existing-only evidence checked by the composition.
// Empty journals use SHA-256 of the empty bytes as their head commitment.
type OperatorEvidence struct {
	WorkflowID       string `json:"workflow_id"`
	BaselineDigest   string `json:"baseline_digest"`
	ScanDigest       string `json:"scan_digest"`
	StoreHead        string `json:"store_head"`
	ProviderHead     string `json:"provider_head"`
	CommandHead      string `json:"command_head"`
	CheckpointDigest string `json:"checkpoint_digest"`
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
	r := operatorResolvedRecord{header: hdr, TurnID: turnID, OperatorResolution: OperatorResolution{OperatorEvidence: evidence, Reason: reason, ResolvedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	r.EvidenceDigest = r.digest()
	data, _ := json.Marshal(r)
	if _, err := c.st.reduce(kindTurnOperatorResolved, data); err != nil {
		return Turn{}, err
	}
	if dryRun {
		return turn.clone(), nil
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
	if r.OperatorUID != 0 || timeErr != nil || at.IsZero() || at.Location() != time.UTC || t.Link == nil || r.WorkflowID != t.Link.WorkflowID ||
		r.BaselineDigest != t.BaselineDigest || r.ScanDigest != t.BaselineDigest || s.project(t.ProjectID).baseline != t.BaselineDigest || r.EvidenceDigest != r.digest() {
		return nil, fail(CodeInvalidRequest, "operator resolution identity, timestamp, baseline or evidence commitment is invalid")
	}
	if r.CommandHead != sha256Digest(nil) {
		return nil, fail(CodeInvalidRequest, "operator resolution requires an empty command journal")
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
		t.Receipt, t.ReceiptDigest, t.ContextEligible = nil, "", false
		t.ResultBaselineDigest = t.BaselineDigest
		resolution := r.OperatorResolution
		t.OperatorResolution = &resolution
	}, nil
}
