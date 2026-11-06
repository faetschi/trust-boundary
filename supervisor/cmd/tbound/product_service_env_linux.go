//go:build linux

package main

import (
	"bytes"
	"errors"
	"os"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/productsession"
)

// holdLiveProviderJournal registers the open provider journal of a turn for read access and returns
// its release.
func (e *productTurnEnv) holdLiveProviderJournal(turnID string, journal *audit.Journal) func() error {
	e.liveMu.Lock()
	if e.liveProvider == nil {
		e.liveProvider = map[string]*audit.Journal{}
	}
	e.liveProvider[turnID] = journal
	e.liveMu.Unlock()
	return func() error {
		e.liveMu.Lock()
		delete(e.liveProvider, turnID)
		e.liveMu.Unlock()
		return nil
	}
}

// providerTrace is the verified provider journal of a linked turn. While this process holds the turn
// open it reads through that handle, since a second open would fail on the writer lock; once the
// turn is closed it reads the retained file.
func (e *productTurnEnv) providerTrace(turn productsession.Turn) (audit.Trace, error) {
	if turn.Link == nil {
		return audit.Trace{}, nil
	}
	e.liveMu.Lock()
	held := e.liveProvider[turn.ID]
	e.liveMu.Unlock()
	if held != nil {
		trace, err := held.Trace()
		if !errors.Is(err, audit.ErrClosed) {
			return trace, err
		}
	}
	// Observers never acquire a writer lock, including while a successor verifies this journal.
	return readProductJournalTrace(turn.Link.ProviderJournalPath)
}

func readProductJournalTrace(path string) (audit.Trace, error) {
	want, err := recoveryFileIdentityDigest(path, false)
	if err != nil {
		return audit.Trace{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return audit.Trace{}, err
	}
	actual, err := recoveryFileIdentityDigest(path, false)
	if err != nil || actual != want {
		return audit.Trace{}, errors.Join(ErrGovernedRecoveryUnavailable, err)
	}
	return audit.Verify(bytes.NewReader(data))
}

// pendingReviewTurn is a review turn this process holds open for its resolution.
func (e *productTurnEnv) pendingReviewTurn(turnID string) (productPreparedTurn, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	review := e.pending[turnID]
	if review == nil {
		return nil, false
	}
	return review.turn, true
}
