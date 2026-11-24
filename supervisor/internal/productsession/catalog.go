//go:build linux

package productsession

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"tbound/supervisor/internal/audit"
)

// Catalog is the durable product catalog. Its methods are safe for concurrent
// use. Every state change is one journal record that the reducer validated
// before it was appended and applies only after the append was synced.
type Catalog struct {
	mu      sync.Mutex
	journal *audit.Journal
	st      *state
}

// Open verifies and replays the catalog journal at path through the same
// reducer live operations use, then records a new owner epoch. A record that
// the reducer refuses fails Open with CATALOG_CORRUPT. A registration digest
// change is refused while any turn is unresolved.
func Open(path string, registration Registration) (*Catalog, error) {
	if err := registration.validate(); err != nil {
		return nil, err
	}
	journal, err := audit.Open(path)
	if err != nil {
		if errors.Is(err, audit.ErrCorrupt) {
			return nil, fail(CodeCatalogCorrupt, "journal verification failed")
		}
		return nil, fmt.Errorf("open product catalog journal: %w", err)
	}
	c := &Catalog{journal: journal, st: newState(registration)}
	if err := c.replay(registration); err != nil {
		_ = journal.Close()
		return nil, err
	}
	return c, nil
}

func (c *Catalog) replay(registration Registration) error {
	trace, err := c.journal.Trace()
	if err != nil {
		return fail(CodeCatalogCorrupt, "journal trace failed")
	}
	for _, record := range trace.Records {
		commit, err := c.st.reduce(record.Event.Kind, record.Event.Data)
		if err != nil {
			return fail(CodeCatalogCorrupt, fmt.Sprintf("record %d refused: %v", record.Sequence, err))
		}
		commit()
	}
	if c.st.regDigest != registration.Digest && c.st.unsettledTurns() {
		return fail(CodePredecessorUnresolved, "registration changed while a turn is unresolved")
	}
	epoch := c.st.epoch + 1
	return c.commit(kindOpened, fmt.Sprintf("epoch-%d", epoch),
		openedRecord{header: hdr, OwnerEpoch: epoch, RegistrationDigest: registration.Digest})
}

// Close releases the journal writer lock.
func (c *Catalog) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.journal.Close()
}

// commit validates a record, makes it durable, then applies it. The caller
// holds c.mu (Open owns c before it is shared).
func (c *Catalog) commit(kind, id string, record any) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode %s: %w", kind, err)
	}
	apply, err := c.st.reduce(kind, data)
	if err != nil {
		return err
	}
	if _, err := c.journal.Append(audit.Event{Kind: kind, ID: id, Data: data}); err != nil {
		return fmt.Errorf("append %s: %w", kind, err)
	}
	apply()
	return nil
}

// CreateSession creates a session for p. measuredBaseline is the tree digest
// of the managed project measured by the trusted composition. The bool reports
// an idempotent replay of an earlier identical request, which writes nothing.
func (c *Catalog) CreateSession(p Principal, r CreateSessionRequest, measuredBaseline string) (Session, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.st.principalAllowed(p) {
		return Session{}, false, fail(CodeUnauthorizedPrincipal, "principal is not registered")
	}
	digest := payloadDigest(opCreateSession, r)
	if e, ok := c.st.keys[requestKey{p, opCreateSession, "", r.RequestKey}]; ok {
		if e.digest != digest {
			return Session{}, false, fail(CodeRequestConflict, "request key reused with a different request")
		}
		return c.st.sessions[e.id].clone(), true, nil
	}
	if !validDigest(measuredBaseline) {
		return Session{}, false, fail(CodeInvalidRequest, "measured baseline is not a sha256 digest")
	}
	id, err := newSessionID()
	if err != nil {
		return Session{}, false, err
	}
	err = c.commit(kindSessionCreated, id, sessionCreatedRecord{
		header: hdr, SessionID: id, Principal: p, ProjectID: r.ProjectID, ProviderProfile: r.ProviderProfile,
		PublicationMode: r.PublicationMode, Budget: r.Budget, ReadOnly: r.ReadOnly, BaselineDigest: measuredBaseline,
		RequestKey: r.RequestKey, PayloadDigest: digest,
	})
	if err != nil {
		return Session{}, false, err
	}
	return c.st.sessions[id].clone(), false, nil
}

// SubmitTurn admits a turn after the ordered checks of the contract. A repeat
// of an admitted request returns the existing admission with Replayed set.
func (c *Catalog) SubmitTurn(p Principal, r SubmitTurnRequest, measuredBaseline string) (Admission, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sess, err := c.st.authorize(p, r.SessionID)
	if err != nil {
		return Admission{}, err
	}
	digest := payloadDigest(opSubmitTurn, r)
	if e, ok := c.st.keys[requestKey{p, opSubmitTurn, r.SessionID, r.RequestKey}]; ok {
		if e.digest != digest {
			return Admission{}, fail(CodeRequestConflict, "request key reused with a different request")
		}
		return Admission{Turn: c.st.turns[e.id].clone(), Replayed: true}, nil
	}
	if !validDigest(measuredBaseline) {
		return Admission{}, fail(CodeInvalidRequest, "measured baseline is not a sha256 digest")
	}
	rp, ok := c.st.reg.project(sess.ProjectID)
	if !ok {
		return Admission{}, fail(CodeUnknownProject, sess.ProjectID)
	}
	ordinal := len(sess.Turns) + 1
	turnID := fmt.Sprintf("%s/turn-%d", sess.ID, ordinal)
	record := turnAdmittedRecord{
		header: hdr, TurnID: turnID, SessionID: sess.ID, Ordinal: ordinal, Principal: p,
		RequestKey: r.RequestKey, PayloadDigest: digest, Prompt: r.Prompt, PromptDigest: sha256Digest([]byte(r.Prompt)),
		ProjectID: sess.ProjectID, PolicyDigest: rp.PolicyDigest, BashOutputPaths: rp.BashOutputPaths, Scope: effectiveScope(rp, sess.ReadOnly),
		PublicationMode: sess.PublicationMode, BaselineDigest: measuredBaseline, ExpectedBaselineDigest: r.ExpectedBaselineDigest,
		Reservation: c.st.reservation(sess), SessionRevision: r.ExpectedRevision + 1,
	}
	if pred := c.st.predecessor(sess); pred != nil {
		record.PredecessorTurnID, record.PredecessorReceiptDigest = pred.ID, pred.ReceiptDigest
	}
	if err := c.commit(kindTurnAdmitted, turnID, record); err != nil {
		return Admission{}, err
	}
	return Admission{Turn: c.st.turns[turnID].clone()}, nil
}

// LinkTurn records the workflow binding before any provider or tool work.
// A cancelled admission is refused with CANCELLED and must be refused unlinked for a full refund.
func (c *Catalog) LinkTurn(turnID string, link TurnLink) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commit(kindTurnLinked, turnID, turnLinkedRecord{header: hdr, TurnID: turnID, Link: link})
}

// CompleteTurn records the verified disposition of a linked turn, or the end
// of a turn that awaited review. Usage of a reviewed turn was settled at
// awaiting_review, so its terminal record repeats it and refunds nothing.
func (c *Catalog) CompleteTurn(turnID string, t TurnTerminal) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	refund := c.reservationOf(turnID).minus(t.Usage)
	if turn, ok := c.st.turns[turnID]; ok && turn.State == StateAwaitingReview {
		refund = Budget{}
	}
	return c.commit(kindTurnTerminal, turnID, turnTerminalRecord{
		header: hdr, TurnID: turnID, Outcome: t.Outcome, Settlement: t.Settlement, Receipt: t.Receipt,
		ReceiptDigest: ReceiptDigest(t.Receipt), Usage: t.Usage, ResultBaselineDigest: t.ResultBaselineDigest,
		ContextEligible: t.ContextEligible, Refund: refund, Reason: t.Reason,
	})
}

// CancelTurn records a cancel request for an admitted, linked or awaiting-review
// turn of p's session. It never settles the turn. A repeat of the same request
// returns the turn with replayed set and writes nothing. A turn that already has
// a cancel request answers a request with a new key the same way, without a
// record or a revision bump, because the request is already in effect.
func (c *Catalog) CancelTurn(p Principal, r CancelTurnRequest) (Turn, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sess, err := c.st.authorize(p, r.SessionID)
	if err != nil {
		return Turn{}, false, err
	}
	digest := payloadDigest(opCancelTurn, r)
	if e, ok := c.st.keys[requestKey{p, opCancelTurn, r.SessionID, r.RequestKey}]; ok {
		if e.digest != digest {
			return Turn{}, false, fail(CodeRequestConflict, "request key reused with a different request")
		}
		return c.st.turns[e.id].clone(), true, nil
	}
	t, err := c.st.sessionTurn(sess, r.TurnID)
	if err != nil {
		return Turn{}, false, err
	}
	if t.unresolved() && t.CancelRequested {
		return t.clone(), true, nil
	}
	err = c.commit(kindTurnCancelRequested, r.TurnID, turnCancelRequestedRecord{
		header: hdr, TurnID: r.TurnID, SessionID: r.SessionID, Principal: p, RequestKey: r.RequestKey,
		PayloadDigest: digest, SessionRevision: r.ExpectedRevision + 1,
	})
	if err != nil {
		return Turn{}, false, err
	}
	return t.clone(), false, nil
}

// ResolveTurn records the review decision for the exact candidate of a turn
// awaiting review. The turn stays awaiting review until its terminal record.
// A turn takes one resolution: a second request with a new key fails with
// REQUEST_CONFLICT, and once the turn is terminal it fails with REVIEW_NOT_PENDING.
// Cancellation refuses a new publish decision with CANCELLED; discard remains allowed.
func (c *Catalog) ResolveTurn(p Principal, r ResolveTurnRequest) (Turn, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sess, err := c.st.authorize(p, r.SessionID)
	if err != nil {
		return Turn{}, false, err
	}
	digest := payloadDigest(opResolveTurn, r)
	if e, ok := c.st.keys[requestKey{p, opResolveTurn, r.SessionID, r.RequestKey}]; ok {
		if e.digest != digest {
			return Turn{}, false, fail(CodeRequestConflict, "request key reused with a different request")
		}
		return c.st.turns[e.id].clone(), true, nil
	}
	t, err := c.st.sessionTurn(sess, r.TurnID)
	if err != nil {
		return Turn{}, false, err
	}
	err = c.commit(kindTurnResolutionRequested, r.TurnID, turnResolutionRequestedRecord{
		header: hdr, TurnID: r.TurnID, SessionID: r.SessionID, Principal: p, RequestKey: r.RequestKey, PayloadDigest: digest,
		CandidateDigest: r.CandidateDigest, Decision: r.Decision, SessionRevision: r.ExpectedRevision + 1,
	})
	if err != nil {
		return Turn{}, false, err
	}
	return t.clone(), false, nil
}

// MarkAwaitingReview settles a changed review-mode turn. The turn stays
// unresolved and blocks its session and project.
func (c *Catalog) MarkAwaitingReview(turnID string, r TurnReview) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commit(kindTurnAwaitingReview, turnID, turnAwaitingReviewRecord{
		header: hdr, TurnID: turnID, Settlement: r.Settlement, Receipt: r.Receipt, ReceiptDigest: ReceiptDigest(r.Receipt),
		Usage: r.Usage, Refund: c.reservationOf(turnID).minus(r.Usage), CandidateDigest: r.CandidateDigest, Removed: r.Removed,
		ReviewTimeoutSeconds: int64(c.st.reg.Limits.ReviewTimeoutSeconds),
	})
}

// AbandonUnlinkedTurn withdraws an admitted turn that never linked and refunds
// its full reservation.
func (c *Catalog) AbandonUnlinkedTurn(turnID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commit(kindTurnAbandoned, turnID, turnAbandonedRecord{header: hdr, TurnID: turnID, Refund: c.reservationOf(turnID)})
}

// RefuseUnlinkedTurn records that the composition refused to launch an
// admitted turn before linking it: code is PREDECESSOR_EVIDENCE_INVALID, which
// blocks the session, or STALE_BASELINE, which blocks the project. The turn
// fails with a full refund. CANCELLED, allowed only after
// CancelTurn, ends the turn cancelled with a full refund and blocks nothing.
func (c *Catalog) RefuseUnlinkedTurn(turnID string, code Code) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commit(kindTurnRefused, turnID, turnRefusedRecord{header: hdr, TurnID: turnID, Code: code, Refund: c.reservationOf(turnID)})
}

// MarkTurnUnknown records a linked turn with no verifiable disposition. The
// reservation stays consumed and the session and project stay blocked.
func (c *Catalog) MarkTurnUnknown(turnID string, reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commit(kindTurnUnknown, turnID, turnUnknownRecord{header: hdr, TurnID: turnID, Reason: reason})
}

// Session returns a copy of a session the principal owns.
func (c *Catalog) Session(p Principal, id string) (Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sess, err := c.st.authorize(p, id)
	if err != nil {
		return Session{}, err
	}
	return sess.clone(), nil
}

// SessionsOf returns copies of every session p owns, ordered by session ID. A principal that the
// registration does not allow gets UNAUTHORIZED_PRINCIPAL, like every other operation.
func (c *Catalog) SessionsOf(p Principal) ([]Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.st.principalAllowed(p) {
		return nil, fail(CodeUnauthorizedPrincipal, "principal is not registered")
	}
	var sessions []Session
	for _, s := range c.st.sessions {
		if s.Principal == p {
			sessions = append(sessions, s.clone())
		}
	}
	slices.SortFunc(sessions, func(a, b Session) int { return strings.Compare(a.ID, b.ID) })
	return sessions, nil
}

// Turn returns a copy of a turn. It does no principal check; the transport
// must authorize through Session first.
func (c *Catalog) Turn(id string) (Turn, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.st.turns[id]
	if !ok {
		return Turn{}, false
	}
	return t.clone(), true
}

// UnresolvedTurns returns copies of every admitted, linked or awaiting-review turn, ordered by turn ID.
// Restart reconciliation settles them before any new admission is served.
func (c *Catalog) UnresolvedTurns() []Turn {
	c.mu.Lock()
	defer c.mu.Unlock()
	var turns []Turn
	for _, t := range c.st.turns {
		if t.unresolved() {
			turns = append(turns, t.clone())
		}
	}
	slices.SortFunc(turns, func(a, b Turn) int { return strings.Compare(a.ID, b.ID) })
	return turns
}

// reservationOf is zero for an unknown turn; the reducer then refuses the
// record with UNKNOWN_TURN before it reads the refund.
func (c *Catalog) reservationOf(turnID string) Budget {
	if t, ok := c.st.turns[turnID]; ok {
		return t.Reservation
	}
	return Budget{}
}

func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("session ID entropy: %w", err)
	}
	return "psess-" + hex.EncodeToString(b[:]), nil
}
