//go:build linux

package main

import (
	"testing"

	"tbound/supervisor/internal/productsession"
)

// The client keeps copies of the catalog's wire vocabulary because productsession builds only on
// Linux. This pins every copy, and requires an exit status for every catalog code.
func TestChatWireVocabularyMatchesTheCatalog(t *testing.T) {
	for client, catalog := range map[string]string{
		chatStateAdmitted: string(productsession.StateAdmitted), chatStateLinked: string(productsession.StateLinked),
		chatStateAwaitingReview: string(productsession.StateAwaitingReview), chatStateTerminal: string(productsession.StateTerminal),
		chatStateAbandoned: string(productsession.StateAbandoned), chatStateUnknown: string(productsession.StateUnknown),
		chatModeReview: productsession.PublicationReview, chatModeAuto: productsession.PublicationAuto,
		chatDecisionPublish: productsession.DecisionPublish, chatDecisionDiscard: productsession.DecisionDiscard,
	} {
		if client != catalog {
			t.Errorf("client constant %q differs from the catalog's %q", client, catalog)
		}
	}
	for _, code := range []productsession.Code{
		productsession.CodeUnauthorizedPrincipal, productsession.CodeUnknownProject, productsession.CodeUnknownProviderProfile,
		productsession.CodePublicationModeNotAllowed, productsession.CodeInvalidRequest, productsession.CodeBudgetExceedsLimit,
		productsession.CodeBudgetExhausted, productsession.CodeRequestConflict, productsession.CodeUnknownSession,
		productsession.CodeUnknownTurn, productsession.CodeStaleRevision, productsession.CodeStaleBaseline,
		productsession.CodeProjectBusy, productsession.CodeProjectBlocked, productsession.CodeTurnActive,
		productsession.CodePendingReview, productsession.CodePredecessorUnresolved, productsession.CodePredecessorEvidenceInvalid,
		productsession.CodeWrongTurnState, productsession.CodeCatalogCorrupt, productsession.CodeCandidateMismatch,
		productsession.CodeReviewNotPending, productsession.CodeCancelNotAllowed, productsession.CodeCancelled,
	} {
		if _, ok := chatCodes[string(code)]; !ok {
			t.Errorf("catalog code %s has no client exit status", code)
		}
	}
}
