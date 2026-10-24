package productsession

// Code is a stable catalog error code from docs/product-contracts.md.
type Code string

const (
	CodeUnauthorizedPrincipal      Code = "UNAUTHORIZED_PRINCIPAL"
	CodeUnknownProject             Code = "UNKNOWN_PROJECT"
	CodeUnknownProviderProfile     Code = "UNKNOWN_PROVIDER_PROFILE"
	CodePublicationModeNotAllowed  Code = "PUBLICATION_MODE_NOT_PERMITTED"
	CodeInvalidRequest             Code = "INVALID_REQUEST"
	CodeBudgetExceedsLimit         Code = "BUDGET_EXCEEDS_LIMIT"
	CodeBudgetExhausted            Code = "BUDGET_EXHAUSTED"
	CodeRequestConflict            Code = "REQUEST_CONFLICT"
	CodeUnknownSession             Code = "UNKNOWN_SESSION"
	CodeUnknownTurn                Code = "UNKNOWN_TURN"
	CodeStaleRevision              Code = "STALE_REVISION"
	CodeStaleBaseline              Code = "STALE_BASELINE"
	CodeProjectBusy                Code = "PROJECT_BUSY"
	CodeProjectBlocked             Code = "PROJECT_BLOCKED"
	CodeTurnActive                 Code = "TURN_ACTIVE"
	CodePendingReview              Code = "PENDING_REVIEW"
	CodePredecessorUnresolved      Code = "PREDECESSOR_UNRESOLVED"
	CodePredecessorEvidenceInvalid Code = "PREDECESSOR_EVIDENCE_INVALID"
	CodeWrongTurnState             Code = "WRONG_TURN_STATE"
	CodeCatalogCorrupt             Code = "CATALOG_CORRUPT"
	CodeCandidateMismatch          Code = "CANDIDATE_MISMATCH"
	CodeReviewNotPending           Code = "REVIEW_NOT_PENDING"
	CodeCancelNotAllowed           Code = "CANCEL_NOT_ALLOWED"

	// CodeCancelled is a RefuseUnlinkedTurn code, not a request error: the
	// client cancelled an admitted turn before it launched.
	CodeCancelled Code = "CANCELLED"
)

// Error carries a stable Code. Detail is a fixed phrase plus identifiers and
// counts, never prompt text or secrets.
type Error struct {
	Code   Code
	Detail string
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Detail
}

// Is matches on Code only, so errors.Is(err, ErrCode(c)) ignores Detail.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// ErrCode returns the sentinel for code, for use with errors.Is.
func ErrCode(code Code) error { return &Error{Code: code} }

func fail(code Code, detail string) *Error { return &Error{Code: code, Detail: detail} }
