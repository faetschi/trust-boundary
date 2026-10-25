package main

import (
	"errors"

	"tbound/supervisor/internal/productsession"
)

// Exit statuses of tbound chat.
const (
	chatExitOther    = 1
	chatExitRefused  = 2
	chatExitConflict = 3
	chatExitUnknown  = 4
)

var chatExitNames = map[int]string{
	chatExitOther: "error", chatExitRefused: "refused",
	chatExitConflict: "conflict or stale state", chatExitUnknown: "unresolved or unknown outcome",
}

// chatCode is what the client does with one service error code: its exit status and, where the
// user can act, one line saying what to do.
type chatCode struct {
	exit int
	hint string
}

var chatCodes = map[string]chatCode{
	string(productsession.CodeUnauthorizedPrincipal):      {chatExitRefused, "this account is not registered for the product service"},
	string(productsession.CodeUnknownProject):             {chatExitRefused, "the project is not registered"},
	string(productsession.CodeUnknownProviderProfile):     {chatExitRefused, "pass a registered --provider, or none to use the service default"},
	string(productsession.CodePublicationModeNotAllowed):  {chatExitRefused, "the project does not allow this --mode"},
	string(productsession.CodeInvalidRequest):             {chatExitRefused, "the service refused the request content; if --provider or a --budget-* flag was not given, the service may need it"},
	string(productsession.CodeBudgetExceedsLimit):         {chatExitRefused, "lower the --budget-* values"},
	string(productsession.CodeBudgetExhausted):            {chatExitRefused, "the session has no budget left; start a new session"},
	string(productsession.CodeRequestConflict):            {chatExitConflict, "the request key was already used for different content"},
	string(productsession.CodeUnknownSession):             {chatExitRefused, "no such session for this account"},
	string(productsession.CodeUnknownTurn):                {chatExitRefused, ""},
	string(productsession.CodeStaleRevision):              {chatExitConflict, "the session changed since it was read. Nothing was resubmitted; check /status and repeat the command"},
	string(productsession.CodeStaleBaseline):              {chatExitConflict, "the project changed since the last turn"},
	string(productsession.CodeProjectBusy):                {chatExitConflict, "another session is using the project; wait for it to finish"},
	string(productsession.CodeProjectBlocked):             {chatExitUnknown, "an earlier turn has an unknown outcome; an operator must reconcile the project"},
	string(productsession.CodeTurnActive):                 {chatExitConflict, "a turn is still running; /cancel it or wait"},
	string(productsession.CodePendingReview):              {chatExitConflict, "a turn awaits review; /publish or /discard it first"},
	string(productsession.CodePredecessorUnresolved):      {chatExitUnknown, "an earlier turn has an unknown outcome and blocks this session"},
	string(productsession.CodePredecessorEvidenceInvalid): {chatExitUnknown, "the earlier turn's evidence did not verify; this session is blocked"},
	string(productsession.CodeWrongTurnState):             {chatExitConflict, ""},
	string(productsession.CodeCatalogCorrupt):             {chatExitOther, "the service reports a corrupt catalog; tell the operator"},
	string(productsession.CodeCandidateMismatch):          {chatExitConflict, "the candidate changed; review it again"},
	string(productsession.CodeReviewNotPending):           {chatExitConflict, "the turn is not awaiting review"},
	string(productsession.CodeCancelNotAllowed):           {chatExitRefused, "the turn can no longer be cancelled"},
	string(productsession.CodeCancelled):                  {chatExitConflict, "the turn was cancelled"},
	"PROTOCOL_ERROR":                                      {chatExitOther, ""},
	"SERVICE_UNAVAILABLE":                                 {chatExitOther, "the service is not accepting requests; try again later"},
}

// chatDescribe maps an error to an exit status and the text to show.
func chatDescribe(err error) (int, string) {
	var service *chatServiceError
	var transport *chatTransportError
	switch {
	case errors.As(err, &service):
		info, known := chatCodes[service.Code]
		text := chatLine(service.Code)
		if service.Detail != "" {
			text += ": " + chatLine(service.Detail)
		}
		if info.hint != "" {
			text += "\n  " + info.hint
		}
		if !known {
			return chatExitOther, text
		}
		return info.exit, text
	case errors.As(err, &transport) && transport.unknownOutcome:
		return chatExitUnknown, chatLine(err.Error()) + "\n  the request may have been applied; reconnect with --session to check"
	}
	return chatExitOther, chatLine(err.Error())
}

// chatOutcomeExit is the exit status a settled turn leaves behind. A failed turn refused for a
// catalog reason (a stale baseline, invalid predecessor evidence) takes that reason's status.
func chatOutcomeExit(outcome, reason string) int {
	switch outcome {
	case "unknown":
		return chatExitUnknown
	case "failed":
		if info, ok := chatCodes[reason]; ok {
			return info.exit
		}
		return chatExitOther
	case "abandoned":
		return chatExitOther
	}
	return 0
}

// chatExitError ends the process with a status, which main reads through ExitCode.
type chatExitError struct {
	code int
	msg  string
}

func (e *chatExitError) Error() string { return e.msg }
func (e *chatExitError) ExitCode() int { return e.code }

func chatExit(code int) error {
	if code == 0 {
		return nil
	}
	return &chatExitError{code: code, msg: "tbound chat: " + chatExitNames[code]}
}

func chatIsCode(err error, code string) bool {
	var service *chatServiceError
	return errors.As(err, &service) && service.Code == code
}

// Wire vocabulary of TurnView.State, SessionView.PublicationMode and ResolveTurn.Decision.
// productsession owns these values but builds only on Linux, so the client keeps copies and a
// Linux test pins them to the originals.
const (
	chatStateAdmitted       = "admitted"
	chatStateLinked         = "linked"
	chatStateAwaitingReview = "awaiting_review"
	chatStateTerminal       = "terminal"
	chatStateAbandoned      = "abandoned"
	chatStateUnknown        = "unknown"
	chatModeReview          = "review"
	chatModeAuto            = "auto"
	chatDecisionPublish     = "publish"
	chatDecisionDiscard     = "discard"
)
