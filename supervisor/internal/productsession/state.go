//go:build linux

package productsession

import (
	"fmt"
	"slices"
	"strings"

	"tbound/supervisor/internal/sessionrepo"
)

// TurnState is the lifecycle state of one turn.
type TurnState string

const (
	StateAdmitted       TurnState = "admitted"
	StateLinked         TurnState = "linked"
	StateAwaitingReview TurnState = "awaiting_review"
	StateTerminal       TurnState = "terminal"
	StateAbandoned      TurnState = "abandoned"
	StateUnknown        TurnState = "unknown"
)

// Terminal outcomes CompleteTurn accepts. Which ones a turn accepts depends on
// its state: see terminalRules.
const (
	OutcomePublished = "published"
	OutcomeNoDiff    = "no_diff"
	OutcomeFailed    = "failed"
	OutcomeDiscarded = "discarded"
	OutcomeCancelled = "cancelled"
)

// Review decisions of ResolveTurn.
const (
	DecisionPublish = "publish"
	DecisionDiscard = "discard"
)

// Reasons of a discarded outcome.
const (
	ReasonUser      = "user"
	ReasonExpired   = "expired"
	ReasonCancelled = "cancelled"
	ReasonRestart   = "restart"
)

// CreateSessionRequest is the client-supplied part of session creation.
type CreateSessionRequest struct {
	ProjectID       string `json:"project_id"`
	ProviderProfile string `json:"provider_profile"`
	PublicationMode string `json:"publication_mode"`
	Budget          Budget `json:"budget"`
	RequestKey      string `json:"request_key"`
}

// SubmitTurnRequest is the client-supplied part of turn admission.
type SubmitTurnRequest struct {
	SessionID              string `json:"session_id"`
	ExpectedRevision       uint64 `json:"expected_revision"`
	RequestKey             string `json:"request_key"`
	Prompt                 string `json:"prompt"`
	ExpectedBaselineDigest string `json:"expected_baseline_digest,omitempty"`
}

// CancelTurnRequest is the client-supplied part of a cancellation.
type CancelTurnRequest struct {
	SessionID        string `json:"session_id"`
	TurnID           string `json:"turn_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	RequestKey       string `json:"request_key"`
}

// ResolveTurnRequest is the client-supplied part of a review decision.
type ResolveTurnRequest struct {
	SessionID        string `json:"session_id"`
	TurnID           string `json:"turn_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	RequestKey       string `json:"request_key"`
	CandidateDigest  string `json:"candidate_digest"`
	Decision         string `json:"decision"`
}

// TurnLink binds a turn to its workflow before any provider or tool work.
type TurnLink struct {
	WorkflowID           string `json:"workflow_id"`
	RunID                string `json:"run_id"`
	SessionID            string `json:"session_id"`
	ConversationID       string `json:"conversation_id"`
	ProviderJournalPath  string `json:"provider_journal_path"`
	StoreJournalPath     string `json:"store_journal_path"`
	CommandJournalPath   string `json:"command_journal_path"`
	ImportedPrefixDigest string `json:"imported_prefix_digest"`
}

// TurnTerminal is the verified disposition of a linked turn.
type TurnTerminal struct {
	Outcome              string
	Settlement           string
	Receipt              []byte
	Usage                Budget
	ResultBaselineDigest string
	ContextEligible      bool
	// Reason is required for, and only valid with, a discarded outcome.
	Reason string
}

// TurnReview settles a changed review-mode turn that now awaits review.
type TurnReview struct {
	Settlement      string
	Receipt         []byte
	Usage           Budget
	CandidateDigest string
}

// Session is a copy of one session's catalog state. Consumed holds settled
// usage and unknown reservations. Reserved holds unresolved reservations.
type Session struct {
	ID              string
	Principal       Principal
	ProjectID       string
	ProviderProfile string
	PublicationMode string
	Budget          Budget
	Consumed        Budget
	Reserved        Budget
	Revision        uint64
	Turns           []string
	Blocked         bool
	BaselineDigest  string // project baseline when the session was created
	unresolved      string // admitted, linked, or awaiting-review turn
}

// Turn is a copy of one turn's catalog state.
type Turn struct {
	ID                       string
	SessionID                string
	Ordinal                  int
	Principal                Principal
	RequestKey               string
	PayloadDigest            string
	Prompt                   string
	PromptDigest             string
	ProjectID                string
	PolicyDigest             string
	BashOutputPaths          []string
	PublicationMode          string
	BaselineDigest           string
	PredecessorTurnID        string
	PredecessorReceiptDigest string
	Reservation              Budget
	AdmittedRevision         uint64
	State                    TurnState
	Link                     *TurnLink
	Outcome                  string
	Settlement               string
	Receipt                  []byte
	ReceiptDigest            string
	Usage                    Budget
	Refund                   Budget
	ResultBaselineDigest     string
	ContextEligible          bool
	CandidateDigest          string
	UnknownReason            string
	RefusalCode              Code
	CancelRequested          bool
	Resolution               string // DecisionPublish or DecisionDiscard once ResolveTurn recorded one
	ReviewTimeoutSeconds     int64  // review expiry in force when the turn began awaiting review
	TerminalReason           string // reason of a discarded outcome
}

// Admission is the result of SubmitTurn. Turn is the turn's current state.
type Admission struct {
	Turn     Turn
	Replayed bool
}

func (s Session) clone() Session {
	s.Turns = slices.Clone(s.Turns)
	return s
}

func (t Turn) clone() Turn {
	t.BashOutputPaths = slices.Clone(t.BashOutputPaths)
	t.Receipt = slices.Clone(t.Receipt)
	if t.Link != nil {
		link := *t.Link
		t.Link = &link
	}
	return t
}

func (t *Turn) unresolved() bool {
	return t.State == StateAdmitted || t.State == StateLinked || t.State == StateAwaitingReview
}

// project is per-project state: one baseline, at most one unresolved turn.
type project struct {
	baseline string
	active   string
	blocked  bool
}

type requestKey struct {
	principal Principal
	operation string
	sessionID string
	key       string
}

type keyEntry struct {
	digest string
	id     string // session ID for create_session, turn ID for submit_turn
}

// state is the in-memory catalog. Only reduce and its commit closures change it.
type state struct {
	reg       Registration
	regDigest string // registration digest of the latest opened record
	epoch     uint64
	projects  map[string]*project
	sessions  map[string]*Session
	turns     map[string]*Turn
	keys      map[requestKey]keyEntry
	linkIDs   map[string]bool
}

func newState(reg Registration) *state {
	return &state{
		reg: reg, projects: map[string]*project{}, sessions: map[string]*Session{},
		turns: map[string]*Turn{}, keys: map[requestKey]keyEntry{}, linkIDs: map[string]bool{},
	}
}

// current reports whether the registration in force when a record is applied
// is the one this process loaded. Registration-dependent checks run only then,
// because records of an earlier registration cannot be rechecked against it.
func (s *state) current() bool { return s.regDigest == s.reg.Digest }

func (s *state) principalAllowed(p Principal) bool {
	return slices.Contains(s.reg.AllowedPrincipals, p)
}

func (s *state) project(id string) *project {
	p, ok := s.projects[id]
	if !ok {
		p = &project{}
		s.projects[id] = p
	}
	return p
}

// unsettledTurns reports a turn that is unresolved or unknown. Either fences a registration change.
func (s *state) unsettledTurns() bool {
	for _, t := range s.turns {
		if t.unresolved() || t.State == StateUnknown {
			return true
		}
	}
	return false
}

func (sess *Session) remaining() Budget { return sess.Budget.minus(sess.Consumed).minus(sess.Reserved) }

// reservation is min(limits.turn, remaining) per field.
func (s *state) reservation(sess *Session) Budget {
	return minBudget(s.reg.Limits.Turn, sess.remaining())
}

// activeTurns counts the session's turns that were not withdrawn.
func (s *state) activeTurns(sess *Session) int {
	n := 0
	for _, id := range sess.Turns {
		if s.turns[id].State != StateAbandoned {
			n++
		}
	}
	return n
}

// predecessor is the session's latest turn that was not withdrawn.
func (s *state) predecessor(sess *Session) *Turn {
	for i := len(sess.Turns) - 1; i >= 0; i-- {
		if t := s.turns[sess.Turns[i]]; t.State != StateAbandoned {
			return t
		}
	}
	return nil
}

// authorize checks that p may use the service and owns the session.
func (s *state) authorize(p Principal, sessionID string) (*Session, error) {
	if s.current() && !s.principalAllowed(p) {
		return nil, fail(CodeUnauthorizedPrincipal, "principal is not registered")
	}
	sess, ok := s.sessions[sessionID]
	if !ok {
		return nil, fail(CodeUnknownSession, sessionID)
	}
	if sess.Principal != p {
		return nil, fail(CodeUnauthorizedPrincipal, "session belongs to another principal")
	}
	return sess, nil
}

func (s *state) turnInState(id string, want TurnState) (*Turn, error) {
	t, ok := s.turns[id]
	if !ok {
		return nil, fail(CodeUnknownTurn, id)
	}
	if t.State != want {
		return nil, fail(CodeWrongTurnState, fmt.Sprintf("%s is %s, want %s", id, t.State, want))
	}
	return t, nil
}

// reduce validates one record against the registration and the current state.
// It changes nothing. The returned commit applies the record and must be called
// exactly once, after the record is durable (live) or at once (replay).
func (s *state) reduce(kind string, data []byte) (commit func(), err error) {
	if kind != kindOpened && s.epoch == 0 {
		return nil, fail(CodeCatalogCorrupt, "record before the first catalog open")
	}
	switch kind {
	case kindOpened:
		return s.reduceOpened(data)
	case kindSessionCreated:
		return s.reduceCreated(data)
	case kindTurnAdmitted:
		return s.reduceAdmitted(data)
	case kindTurnLinked:
		return s.reduceLinked(data)
	case kindTurnTerminal:
		return s.reduceTerminal(data)
	case kindTurnAwaitingReview:
		return s.reduceAwaitingReview(data)
	case kindTurnAbandoned:
		return s.reduceAbandoned(data)
	case kindTurnUnknown:
		return s.reduceUnknown(data)
	case kindTurnRefused:
		return s.reduceRefused(data)
	case kindTurnCancelRequested:
		return s.reduceCancelRequested(data)
	case kindTurnResolutionRequested:
		return s.reduceResolutionRequested(data)
	}
	return nil, fail(CodeInvalidRequest, "unknown record kind")
}

func (s *state) reduceOpened(data []byte) (func(), error) {
	var r openedRecord
	if err := decode(data, &r); err != nil {
		return nil, err
	}
	if r.OwnerEpoch != s.epoch+1 || !validDigest(r.RegistrationDigest) {
		return nil, fail(CodeInvalidRequest, "owner epoch or registration digest is invalid")
	}
	return func() { s.epoch, s.regDigest = r.OwnerEpoch, r.RegistrationDigest }, nil
}

func (s *state) reduceCreated(data []byte) (func(), error) {
	var r sessionCreatedRecord
	if err := decode(data, &r); err != nil {
		return nil, err
	}
	cur := s.current()
	if cur && !s.principalAllowed(r.Principal) {
		return nil, fail(CodeUnauthorizedPrincipal, "principal is not registered")
	}
	key := requestKey{r.Principal, opCreateSession, "", r.RequestKey}
	if !validIdentity(r.RequestKey) || !validDigest(r.BaselineDigest) || r.PayloadDigest != payloadDigest(opCreateSession, CreateSessionRequest{
		ProjectID: r.ProjectID, ProviderProfile: r.ProviderProfile, PublicationMode: r.PublicationMode, Budget: r.Budget, RequestKey: r.RequestKey,
	}) {
		return nil, fail(CodeInvalidRequest, "request key is invalid or the payload digest does not match the request")
	}
	if _, dup := s.keys[key]; dup {
		return nil, fail(CodeRequestConflict, "request key already used")
	}
	if !validSessionID(r.SessionID) || s.sessions[r.SessionID] != nil {
		return nil, fail(CodeInvalidRequest, "session ID is invalid or reused")
	}
	if !validMode(r.PublicationMode) || !validIdentity(r.ProviderProfile) || !validIdentity(r.ProjectID) || !r.Budget.valid() {
		return nil, fail(CodeInvalidRequest, "session fields are invalid")
	}
	if cur {
		rp, ok := s.reg.project(r.ProjectID)
		switch {
		case !ok:
			return nil, fail(CodeUnknownProject, r.ProjectID)
		case !slices.Contains(s.reg.ProviderProfiles, r.ProviderProfile):
			return nil, fail(CodeUnknownProviderProfile, r.ProviderProfile)
		case !slices.Contains(rp.PublicationModes, r.PublicationMode):
			return nil, fail(CodePublicationModeNotAllowed, r.PublicationMode)
		case !r.Budget.fits(s.reg.Limits.Session):
			return nil, fail(CodeBudgetExceedsLimit, "session budget is above the registered limit")
		}
	}
	proj := s.project(r.ProjectID)
	if proj.blocked {
		return nil, fail(CodeProjectBlocked, r.ProjectID)
	}
	if proj.baseline != "" && proj.baseline != r.BaselineDigest {
		return nil, fail(CodeStaleBaseline, r.ProjectID)
	}
	return func() {
		proj.baseline = r.BaselineDigest
		s.sessions[r.SessionID] = &Session{
			ID: r.SessionID, Principal: r.Principal, ProjectID: r.ProjectID, ProviderProfile: r.ProviderProfile,
			PublicationMode: r.PublicationMode, Budget: r.Budget, BaselineDigest: r.BaselineDigest,
		}
		s.keys[key] = keyEntry{digest: r.PayloadDigest, id: r.SessionID}
	}, nil
}

// reduceAdmitted performs the ordered SubmitTurn checks of the contract.
func (s *state) reduceAdmitted(data []byte) (func(), error) {
	var r turnAdmittedRecord
	if err := decode(data, &r); err != nil {
		return nil, err
	}
	sess, err := s.authorize(r.Principal, r.SessionID) // check 1
	if err != nil {
		return nil, err
	}
	cur := s.current()
	rp, registered := s.reg.project(sess.ProjectID)
	if cur {
		switch {
		case !registered:
			return nil, fail(CodeUnknownProject, sess.ProjectID)
		case !slices.Contains(s.reg.ProviderProfiles, sess.ProviderProfile):
			return nil, fail(CodeUnknownProviderProfile, sess.ProviderProfile)
		case !slices.Contains(rp.PublicationModes, sess.PublicationMode):
			return nil, fail(CodePublicationModeNotAllowed, sess.PublicationMode)
		}
	}
	key := requestKey{r.Principal, opSubmitTurn, r.SessionID, r.RequestKey} // check 2 is the live replay lookup
	if !validIdentity(r.RequestKey) || !validDigest(r.PayloadDigest) {
		return nil, fail(CodeInvalidRequest, "request key or digest is invalid")
	}
	if _, dup := s.keys[key]; dup {
		return nil, fail(CodeRequestConflict, "request key already used")
	}
	if sess.Blocked { // check 3
		return nil, fail(CodePredecessorUnresolved, "session is blocked")
	}
	if sess.unresolved != "" { // check 4
		if s.turns[sess.unresolved].State == StateAwaitingReview {
			return nil, fail(CodePendingReview, sess.unresolved)
		}
		return nil, fail(CodeTurnActive, sess.unresolved)
	}
	if r.SessionRevision != sess.Revision+1 { // check 5
		return nil, fail(CodeStaleRevision, fmt.Sprintf("session revision is %d", sess.Revision))
	}
	if r.PayloadDigest != payloadDigest(opSubmitTurn, SubmitTurnRequest{SessionID: r.SessionID, ExpectedRevision: sess.Revision,
		RequestKey: r.RequestKey, Prompt: r.Prompt, ExpectedBaselineDigest: r.ExpectedBaselineDigest}) {
		return nil, fail(CodeInvalidRequest, "payload digest does not match the request")
	}
	proj := s.project(sess.ProjectID) // check 6
	if proj.blocked {
		return nil, fail(CodeProjectBlocked, sess.ProjectID)
	}
	if proj.active != "" {
		return nil, fail(CodeProjectBusy, sess.ProjectID)
	}
	if r.ExpectedBaselineDigest != "" && !validDigest(r.ExpectedBaselineDigest) { // check 7
		return nil, fail(CodeInvalidRequest, "expected baseline digest is invalid")
	}
	if r.BaselineDigest != proj.baseline || (r.ExpectedBaselineDigest != "" && r.ExpectedBaselineDigest != proj.baseline) {
		return nil, fail(CodeStaleBaseline, sess.ProjectID)
	}
	rem := sess.remaining() // check 8
	if (cur && s.activeTurns(sess) >= s.reg.Limits.MaxTurnsPerSession) || !rem.valid() {
		return nil, fail(CodeBudgetExhausted, sess.ID)
	}
	if !validPrompt(r.Prompt) { // check 9
		return nil, fail(CodeInvalidRequest, "prompt is not bounded plain text")
	}
	if cur {
		if r.Reservation != s.reservation(sess) || r.PolicyDigest != rp.PolicyDigest || !slices.Equal(r.BashOutputPaths, rp.BashOutputPaths) {
			return nil, fail(CodeInvalidRequest, "reservation or project pins differ from the registration")
		}
	} else if !r.Reservation.valid() || !r.Reservation.fits(rem) || !validDigest(r.PolicyDigest) ||
		sessionrepo.ValidateOutputScopePaths(r.BashOutputPaths) != nil {
		return nil, fail(CodeInvalidRequest, "reservation or project pins are invalid")
	}
	pred := s.predecessor(sess)
	var predID, predReceipt string
	if pred != nil {
		predID, predReceipt = pred.ID, pred.ReceiptDigest
	}
	ordinal := len(sess.Turns) + 1
	if r.Ordinal != ordinal || r.TurnID != fmt.Sprintf("%s/turn-%d", sess.ID, ordinal) ||
		r.ProjectID != sess.ProjectID || r.PublicationMode != sess.PublicationMode ||
		r.PromptDigest != sha256Digest([]byte(r.Prompt)) ||
		r.PredecessorTurnID != predID || r.PredecessorReceiptDigest != predReceipt {
		return nil, fail(CodeInvalidRequest, "turn identity or predecessor does not match the session")
	}
	return func() {
		s.turns[r.TurnID] = &Turn{
			ID: r.TurnID, SessionID: sess.ID, Ordinal: ordinal, Principal: r.Principal, RequestKey: r.RequestKey,
			PayloadDigest: r.PayloadDigest, Prompt: r.Prompt, PromptDigest: r.PromptDigest, ProjectID: r.ProjectID,
			PolicyDigest: r.PolicyDigest, BashOutputPaths: slices.Clone(r.BashOutputPaths), PublicationMode: r.PublicationMode,
			BaselineDigest: r.BaselineDigest, PredecessorTurnID: predID, PredecessorReceiptDigest: predReceipt,
			Reservation: r.Reservation, AdmittedRevision: r.SessionRevision, State: StateAdmitted,
		}
		sess.Turns = append(sess.Turns, r.TurnID)
		sess.Reserved = sess.Reserved.plus(r.Reservation)
		sess.Revision = r.SessionRevision
		sess.unresolved = r.TurnID
		proj.active = r.TurnID
		s.keys[key] = keyEntry{digest: r.PayloadDigest, id: r.TurnID}
	}, nil
}

func (s *state) reduceLinked(data []byte) (func(), error) {
	var r turnLinkedRecord
	if err := decode(data, &r); err != nil {
		return nil, err
	}
	t, err := s.turnInState(r.TurnID, StateAdmitted)
	if err != nil {
		return nil, err
	}
	l := r.Link
	ids := []string{"workflow:" + l.WorkflowID, "run:" + l.RunID, "session:" + l.SessionID, "conversation:" + l.ConversationID}
	for _, id := range []string{l.WorkflowID, l.RunID, l.SessionID, l.ConversationID} {
		if !validIdentity(id) {
			return nil, fail(CodeInvalidRequest, "link identity is invalid")
		}
	}
	for _, p := range []string{l.ProviderJournalPath, l.StoreJournalPath, l.CommandJournalPath} {
		if !validPath(p) {
			return nil, fail(CodeInvalidRequest, "link journal path is invalid")
		}
	}
	if l.ImportedPrefixDigest != "" && !validDigest(l.ImportedPrefixDigest) {
		return nil, fail(CodeInvalidRequest, "imported prefix digest is invalid")
	}
	for _, id := range ids {
		if s.linkIDs[id] {
			return nil, fail(CodeInvalidRequest, "link identity is not fresh")
		}
	}
	return func() {
		for _, id := range ids {
			s.linkIDs[id] = true
		}
		t.Link, t.State = &l, StateLinked
	}, nil
}

// checkSettlement validates the receipt, usage and refund shared by terminal
// and awaiting-review records.
func checkReceiptBytes(receipt []byte, digest string) error {
	if !validReceipt(receipt) || digest != ReceiptDigest(receipt) {
		return fail(CodeInvalidRequest, "receipt is not strict JSON within its bound, or its digest differs")
	}
	return nil
}

func checkSettlement(t *Turn, receipt []byte, digest string, usage, refund Budget) error {
	if err := checkReceiptBytes(receipt, digest); err != nil {
		return err
	}
	switch {
	case !usage.nonNegative() || !usage.fits(t.Reservation):
		return fail(CodeInvalidRequest, "usage exceeds the reservation")
	case refund != t.Reservation.minus(usage):
		return fail(CodeInvalidRequest, "refund is not reservation minus usage")
	}
	return nil
}

// settle moves a turn's reservation out of Reserved and its usage into Consumed.
func (s *state) settle(t *Turn, consumed Budget) {
	sess := s.sessions[t.SessionID]
	sess.Reserved = sess.Reserved.minus(t.Reservation)
	sess.Consumed = sess.Consumed.plus(consumed)
	sess.Revision++
}

// resolve clears the session and project claim of a turn that is no longer unresolved.
func (s *state) resolve(t *Turn) {
	s.sessions[t.SessionID].unresolved = ""
	s.project(t.ProjectID).active = ""
}

// terminalRules checks that the turn's state, resolution and cancel request
// allow the outcome and reason, and returns the baseline the outcome must report.
func terminalRules(t *Turn, r turnTerminalRecord) (string, error) {
	bad := func(detail string) (string, error) { return "", fail(CodeInvalidRequest, detail) }
	reviewed := t.State == StateAwaitingReview
	if r.Reason != "" && r.Outcome != OutcomeDiscarded {
		return bad("a reason belongs only to a discarded outcome")
	}
	switch r.Outcome {
	case OutcomeNoDiff:
		if reviewed {
			return bad("a turn awaiting review cannot end no_diff")
		}
	case OutcomeFailed:
		if reviewed && t.Resolution != DecisionPublish {
			return bad("a reviewed turn fails only when a publication was requested")
		}
	case OutcomePublished:
		switch {
		case reviewed && t.Resolution != DecisionPublish:
			return bad("a reviewed turn is published only after a publish resolution")
		case reviewed:
			return t.CandidateDigest, nil
		case t.PublicationMode != PublicationAuto:
			return bad("only an auto-publish turn can be published without review")
		}
		return r.ResultBaselineDigest, nil
	case OutcomeCancelled:
		if reviewed || !t.CancelRequested {
			return bad("only a linked turn with a cancel request ends cancelled")
		}
	case OutcomeDiscarded:
		switch {
		case !reviewed:
			return bad("only a turn awaiting review can be discarded")
		case r.Reason == ReasonUser && t.Resolution != DecisionDiscard:
			return bad("a user discard needs a discard resolution")
		case r.Reason == ReasonCancelled && !t.CancelRequested:
			return bad("a cancelled discard needs a cancel request")
		case r.Reason != ReasonUser && r.Reason != ReasonCancelled && r.Reason != ReasonExpired && r.Reason != ReasonRestart:
			return bad("discard reason is not user, expired, cancelled or restart")
		}
	default:
		return bad("outcome is not supported")
	}
	return t.BaselineDigest, nil
}

func (s *state) reduceTerminal(data []byte) (func(), error) {
	var r turnTerminalRecord
	if err := decode(data, &r); err != nil {
		return nil, err
	}
	t, ok := s.turns[r.TurnID]
	if !ok {
		return nil, fail(CodeUnknownTurn, r.TurnID)
	}
	if t.State != StateLinked && t.State != StateAwaitingReview {
		return nil, fail(CodeWrongTurnState, fmt.Sprintf("%s is %s, want linked or awaiting_review", t.ID, t.State))
	}
	reviewed := t.State == StateAwaitingReview
	want, err := terminalRules(t, r)
	if err != nil {
		return nil, err
	}
	if !validDigest(r.ResultBaselineDigest) || r.ResultBaselineDigest != want {
		return nil, fail(CodeInvalidRequest, "result baseline digest is invalid or not the one this outcome requires")
	}
	if reviewed {
		// Usage was settled at awaiting_review: the terminal record repeats it and refunds nothing.
		if err := checkReceiptBytes(r.Receipt, r.ReceiptDigest); err != nil {
			return nil, err
		}
		if r.Usage != t.Usage || r.Refund != (Budget{}) {
			return nil, fail(CodeInvalidRequest, "a reviewed turn repeats its review usage and refunds nothing")
		}
	} else if err := checkSettlement(t, r.Receipt, r.ReceiptDigest, r.Usage, r.Refund); err != nil {
		return nil, err
	}
	if err := checkReceipt(t, r.Receipt, r.Outcome, r.Settlement, r.Usage, r.ResultBaselineDigest); err != nil {
		return nil, err
	}
	return func() {
		if reviewed {
			s.sessions[t.SessionID].Revision++
		} else {
			s.settle(t, r.Usage)
			t.Refund = r.Refund
		}
		s.resolve(t)
		if r.Outcome == OutcomePublished {
			s.project(t.ProjectID).baseline = r.ResultBaselineDigest
		}
		t.State, t.Outcome, t.Settlement, t.TerminalReason = StateTerminal, r.Outcome, r.Settlement, r.Reason
		t.Receipt, t.ReceiptDigest, t.Usage = slices.Clone(r.Receipt), r.ReceiptDigest, r.Usage
		t.ResultBaselineDigest, t.ContextEligible = r.ResultBaselineDigest, r.ContextEligible
	}, nil
}

// reduceCancelRequested records a durable cancel request. A turn takes one
// record: the live operation answers a repeat without writing.
func (s *state) reduceCancelRequested(data []byte) (func(), error) {
	var r turnCancelRequestedRecord
	if err := decode(data, &r); err != nil {
		return nil, err
	}
	sess, err := s.authorize(r.Principal, r.SessionID)
	if err != nil {
		return nil, err
	}
	key := requestKey{r.Principal, opCancelTurn, r.SessionID, r.RequestKey}
	if !validIdentity(r.RequestKey) {
		return nil, fail(CodeInvalidRequest, "request key is invalid")
	}
	if _, dup := s.keys[key]; dup {
		return nil, fail(CodeRequestConflict, "request key already used")
	}
	t, err := s.sessionTurn(sess, r.TurnID)
	if err != nil {
		return nil, err
	}
	switch {
	case !t.unresolved() || t.CancelRequested:
		return nil, fail(CodeCancelNotAllowed, fmt.Sprintf("%s is %s", t.ID, t.State))
	case r.SessionRevision != sess.Revision+1:
		return nil, fail(CodeStaleRevision, fmt.Sprintf("session revision is %d", sess.Revision))
	case r.PayloadDigest != payloadDigest(opCancelTurn, CancelTurnRequest{SessionID: r.SessionID, TurnID: r.TurnID,
		ExpectedRevision: sess.Revision, RequestKey: r.RequestKey}):
		return nil, fail(CodeInvalidRequest, "payload digest does not match the request")
	}
	return func() {
		t.CancelRequested = true
		sess.Revision = r.SessionRevision
		s.keys[key] = keyEntry{digest: r.PayloadDigest, id: t.ID}
	}, nil
}

// reduceResolutionRequested records the review decision for the exact candidate.
func (s *state) reduceResolutionRequested(data []byte) (func(), error) {
	var r turnResolutionRequestedRecord
	if err := decode(data, &r); err != nil {
		return nil, err
	}
	sess, err := s.authorize(r.Principal, r.SessionID)
	if err != nil {
		return nil, err
	}
	key := requestKey{r.Principal, opResolveTurn, r.SessionID, r.RequestKey}
	if !validIdentity(r.RequestKey) || (r.Decision != DecisionPublish && r.Decision != DecisionDiscard) {
		return nil, fail(CodeInvalidRequest, "request key or decision is invalid")
	}
	if _, dup := s.keys[key]; dup {
		return nil, fail(CodeRequestConflict, "request key already used")
	}
	t, err := s.sessionTurn(sess, r.TurnID)
	if err != nil {
		return nil, err
	}
	switch {
	case t.State != StateAwaitingReview:
		return nil, fail(CodeReviewNotPending, fmt.Sprintf("%s is %s", t.ID, t.State))
	case t.Resolution != "":
		return nil, fail(CodeRequestConflict, "the turn already has a resolution")
	case r.CandidateDigest != t.CandidateDigest:
		return nil, fail(CodeCandidateMismatch, t.ID)
	case r.SessionRevision != sess.Revision+1:
		return nil, fail(CodeStaleRevision, fmt.Sprintf("session revision is %d", sess.Revision))
	case r.PayloadDigest != payloadDigest(opResolveTurn, ResolveTurnRequest{SessionID: r.SessionID, TurnID: r.TurnID,
		ExpectedRevision: sess.Revision, RequestKey: r.RequestKey, CandidateDigest: r.CandidateDigest, Decision: r.Decision}):
		return nil, fail(CodeInvalidRequest, "payload digest does not match the request")
	}
	return func() {
		t.Resolution = r.Decision
		sess.Revision = r.SessionRevision
		s.keys[key] = keyEntry{digest: r.PayloadDigest, id: t.ID}
	}, nil
}

// sessionTurn finds a turn of sess, so one session cannot name another's turn.
func (s *state) sessionTurn(sess *Session, turnID string) (*Turn, error) {
	t, ok := s.turns[turnID]
	if !ok || t.SessionID != sess.ID {
		return nil, fail(CodeUnknownTurn, turnID)
	}
	return t, nil
}

func (s *state) reduceAwaitingReview(data []byte) (func(), error) {
	var r turnAwaitingReviewRecord
	if err := decode(data, &r); err != nil {
		return nil, err
	}
	t, err := s.turnInState(r.TurnID, StateLinked)
	if err != nil {
		return nil, err
	}
	if t.PublicationMode != PublicationReview || !validDigest(r.CandidateDigest) {
		return nil, fail(CodeInvalidRequest, "only a review-mode turn with a candidate digest can await review")
	}
	if r.ReviewTimeoutSeconds < 1 || r.ReviewTimeoutSeconds > maxReviewTimeout ||
		(s.current() && r.ReviewTimeoutSeconds != int64(s.reg.Limits.ReviewTimeoutSeconds)) {
		return nil, fail(CodeInvalidRequest, "review timeout is invalid or differs from the registration")
	}
	if err := checkSettlement(t, r.Receipt, r.ReceiptDigest, r.Usage, r.Refund); err != nil {
		return nil, err
	}
	if err := checkReceipt(t, r.Receipt, receiptOutcomeAwaitReview, r.Settlement, r.Usage, t.BaselineDigest); err != nil {
		return nil, err
	}
	return func() {
		s.settle(t, r.Usage)
		t.State, t.Settlement = StateAwaitingReview, r.Settlement
		t.ReviewTimeoutSeconds = r.ReviewTimeoutSeconds
		t.Receipt, t.ReceiptDigest, t.Usage, t.Refund, t.CandidateDigest = slices.Clone(r.Receipt), r.ReceiptDigest, r.Usage, r.Refund, r.CandidateDigest
	}, nil
}

func (s *state) reduceAbandoned(data []byte) (func(), error) {
	var r turnAbandonedRecord
	if err := decode(data, &r); err != nil {
		return nil, err
	}
	t, err := s.turnInState(r.TurnID, StateAdmitted)
	if err != nil {
		return nil, err
	}
	if r.Refund != t.Reservation {
		return nil, fail(CodeInvalidRequest, "an abandoned turn refunds its full reservation")
	}
	return func() {
		s.settle(t, Budget{})
		s.resolve(t)
		t.State, t.Refund = StateAbandoned, r.Refund
	}, nil
}

func (s *state) reduceUnknown(data []byte) (func(), error) {
	var r turnUnknownRecord
	if err := decode(data, &r); err != nil {
		return nil, err
	}
	t, ok := s.turns[r.TurnID]
	if !ok {
		return nil, fail(CodeUnknownTurn, r.TurnID)
	}
	if t.State != StateLinked && t.State != StateAwaitingReview {
		return nil, fail(CodeWrongTurnState, fmt.Sprintf("%s is %s, want linked or awaiting_review", t.ID, t.State))
	}
	if r.Reason == "" || len(r.Reason) > maxReasonBytes || !validPrompt(r.Reason) {
		return nil, fail(CodeInvalidRequest, "reason is empty, too long, or not plain text")
	}
	return func() {
		if t.State == StateAwaitingReview { // usage was settled and the rest refunded at review
			s.sessions[t.SessionID].Revision++
		} else {
			s.settle(t, t.Reservation)
		}
		s.resolve(t)
		s.sessions[t.SessionID].Blocked = true
		s.project(t.ProjectID).blocked = true
		t.State, t.UnknownReason = StateUnknown, r.Reason
	}, nil
}

// reduceRefused ends an admitted turn whose launch the composition refused
// before linking, so no provider or tool work happened. It is a failed turn
// with a full refund. Tampered predecessor evidence blocks the session; a
// project tree that no longer matches its committed baseline blocks the project.
// A requested cancellation ends the turn cancelled and blocks nothing.
func (s *state) reduceRefused(data []byte) (func(), error) {
	var r turnRefusedRecord
	if err := decode(data, &r); err != nil {
		return nil, err
	}
	t, err := s.turnInState(r.TurnID, StateAdmitted)
	if err != nil {
		return nil, err
	}
	switch {
	case r.Code == CodePredecessorEvidenceInvalid || r.Code == CodeStaleBaseline:
	case r.Code == CodeCancelled && t.CancelRequested:
	default:
		return nil, fail(CodeInvalidRequest, "a launch can only be refused for predecessor evidence, a stale baseline, or a requested cancellation")
	}
	if r.Refund != t.Reservation {
		return nil, fail(CodeInvalidRequest, "a refused turn refunds its full reservation")
	}
	return func() {
		s.settle(t, Budget{})
		s.resolve(t)
		outcome := OutcomeFailed
		switch r.Code {
		case CodePredecessorEvidenceInvalid:
			s.sessions[t.SessionID].Blocked = true
		case CodeStaleBaseline:
			s.project(t.ProjectID).blocked = true
		case CodeCancelled: // the client asked for it, so nothing is blocked
			outcome = OutcomeCancelled
		}
		t.State, t.Outcome, t.Refund, t.RefusalCode = StateTerminal, outcome, r.Refund, r.Code
		t.ResultBaselineDigest = t.BaselineDigest
	}, nil
}

func validSessionID(id string) bool {
	hexPart, ok := strings.CutPrefix(id, "psess-")
	return ok && len(hexPart) == 32 && isLowerHex(hexPart)
}
