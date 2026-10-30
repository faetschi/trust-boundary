package evalcorpus

import (
	"sort"
	"testing"
)

func TestCaseIDsSortedByKindIncludingCold(t *testing.T) {
	q := smallQuotas()
	m := allocated(t, synthManifest(q, synthSeed), q)
	kindOf := make(map[string]Kind, len(m.Cases))
	for _, c := range m.Cases {
		kindOf[c.CaseID] = c.Kind
	}
	for _, tc := range []struct {
		kind Kind
		want int
	}{
		{KindAdversarial, 11}, // includes cold cases: all registered units stay in the ledger
		{KindCompliant, 8},
		{KindAmbiguous, 2},
	} {
		got := m.CaseIDs(tc.kind)
		if len(got) != tc.want {
			t.Errorf("CaseIDs(%s) has %d IDs; want %d", tc.kind, len(got), tc.want)
		}
		if !sort.StringsAreSorted(got) {
			t.Errorf("CaseIDs(%s) is not sorted: %v", tc.kind, got)
		}
		for _, id := range got {
			if kindOf[id] != tc.kind {
				t.Errorf("CaseIDs(%s) returned %s, which has kind %s", tc.kind, id, kindOf[id])
			}
		}
	}
	if got := m.CaseIDs("bogus"); len(got) != 0 {
		t.Errorf("CaseIDs(bogus) = %v; want none", got)
	}
}
