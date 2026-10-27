//go:build linux

package productsession

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"tbound/supervisor/internal/audit"
)

// UnsettledTurns replays the catalog journal at path through the reducer, under registration, and returns
// copies of the turns that fence a registration change (see Open): the unresolved ones and the ones with an
// unknown outcome, ordered by turn ID. It appends nothing, so a dry run can use it. It holds the journal's
// writer lock while it reads, so it fails while the service has the catalog open.
func UnsettledTurns(path string, registration Registration) ([]Turn, error) {
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
	defer journal.Close()
	trace, err := journal.Trace()
	if err != nil {
		return nil, fail(CodeCatalogCorrupt, "journal trace failed")
	}
	st := newState(registration)
	for _, record := range trace.Records {
		commit, err := st.reduce(record.Event.Kind, record.Event.Data)
		if err != nil {
			return nil, fail(CodeCatalogCorrupt, fmt.Sprintf("record %d refused: %v", record.Sequence, err))
		}
		commit()
	}
	var turns []Turn
	for _, t := range st.turns {
		if t.unresolved() || t.State == StateUnknown {
			turns = append(turns, t.clone())
		}
	}
	slices.SortFunc(turns, func(a, b Turn) int { return strings.Compare(a.ID, b.ID) })
	return turns, nil
}
