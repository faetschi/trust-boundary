// Package publicationevents derives the live-root events that publication's
// application of a ledger makes. It is a leaf package so both the runner's
// transient judge and the publication package's observer test can use the one
// rule without importing each other.
package publicationevents

import (
	"tbound/supervisor/internal/effectoracle"
	"tbound/supervisor/internal/wsoracle"
)

// Event is one live-root event the observer sees when publication applies a ledger change.
type Event struct {
	Path  string
	Kind  string
	IsDir bool
}

// Sequence is the exact events, in order, that publication's application of the ledger makes in the
// watched live root: nothing else, and each operation once. Publication applies every object by rename from or to its
// private artifact directory, a sibling of the live root that is not watched (internal/publication; checked against a
// real publish by TestPublishRenamesAreTheOnlyLiveRootEvents), so every rename has one side outside the watched tree:
//
//   - create (no before, a file after): moved_to on the path;
//   - directory create (no before, a directory after): moved_to on the path, a directory;
//   - overwrite (a file before and after, swapped with RENAME_EXCHANGE): moved_to on the path, then moved_from on the
//     path (the displaced file);
//   - delete (a file before, nothing after): moved_from on the path.
//
// Entries and the changes within them are in the order they were applied. ok is false when a change is none of these
// operations (a directory removed, a type change): the events it would make are not known.
func Sequence(ledger []wsoracle.LedgerEntry) (seq []Event, ok bool) {
	for _, e := range ledger {
		for _, ch := range e.Changes {
			isFile := func(en *wsoracle.Entry) bool { return en != nil && en.Type == wsoracle.TypeFile }
			switch {
			case ch.Before == nil && isFile(ch.After):
				seq = append(seq, Event{ch.Path, effectoracle.KindMovedTo, false})
			case ch.Before == nil && ch.After != nil && ch.After.Type == wsoracle.TypeDir:
				seq = append(seq, Event{ch.Path, effectoracle.KindMovedTo, true})
			case isFile(ch.Before) && isFile(ch.After):
				seq = append(seq, Event{ch.Path, effectoracle.KindMovedTo, false}, Event{ch.Path, effectoracle.KindMovedFrom, false})
			case isFile(ch.Before) && ch.After == nil:
				seq = append(seq, Event{ch.Path, effectoracle.KindMovedFrom, false})
			default:
				return nil, false
			}
		}
	}
	return seq, true
}
