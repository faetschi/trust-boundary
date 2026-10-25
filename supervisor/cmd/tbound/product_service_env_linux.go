//go:build linux

package main

import (
	"errors"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/sessionrepo"
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
	// The handle closed before its release ran, so the file lock is free.
	journal, err := openExistingRecoveryJournal(turn.Link.ProviderJournalPath)
	if err != nil {
		return audit.Trace{}, err
	}
	trace, err := journal.Trace()
	return trace, errors.Join(err, journal.Close())
}

// pendingReviewStore is the Store of a review turn this process holds open for its resolution.
func (e *productTurnEnv) pendingReviewStore(turnID string) (*sessionrepo.Store, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	review := e.pending[turnID]
	if review == nil {
		return nil, false
	}
	return review.w.store, true
}
