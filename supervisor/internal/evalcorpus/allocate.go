package evalcorpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"sort"
)

// selectDomain tags the rank hash, so that it cannot collide with another use of SHA-256
// in this package.
const selectDomain = "tbound-evalcorpus/select/v1"

// ColdCount returns the cold-stratum size for a stratum of n cases:
// floor(percent*n/100 + 1/2), the nearest integer with ties upward. It uses integer
// arithmetic. With percent=20 the registered strata give 20, 20, and 20 (n = 100, 100,
// 99), matching tbound-thesis.md section 7.5.
func ColdCount(percent, n int) int {
	return (percent*n + 50) / 100
}

// SelectSeeded returns the k members of ids with the lowest rank for (seed, purpose).
//
// The rank of an id is SHA-256 over the concatenation, for each of the four parts
// (selectDomain, seed, purpose, id) in that order, of an 8-byte big-endian byte length
// followed by the UTF-8 bytes of the part. Members are ordered by rank as an unsigned
// big-endian byte string, and ties are broken by id. The result is sorted, and it does
// not depend on the order of ids. The seed must be non-empty and ids must be unique.
// Nothing here depends on the platform, the Go version, or map iteration.
func SelectSeeded(seed, purpose string, ids []string, k int) ([]string, error) {
	if seed == "" {
		return nil, errors.New("evalcorpus: empty seed")
	}
	if k < 0 || k > len(ids) {
		return nil, fmt.Errorf("evalcorpus: cannot select %d of %d candidates", k, len(ids))
	}
	type ranked struct {
		rank [sha256.Size]byte
		id   string
	}
	seen := make(map[string]bool, len(ids))
	rs := make([]ranked, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			return nil, fmt.Errorf("evalcorpus: duplicate candidate %q", id)
		}
		seen[id] = true
		rs = append(rs, ranked{rank: selectRank(seed, purpose, id), id: id})
	}
	sort.Slice(rs, func(i, j int) bool {
		if c := bytes.Compare(rs[i].rank[:], rs[j].rank[:]); c != 0 {
			return c < 0
		}
		return rs[i].id < rs[j].id
	})
	out := make([]string, k)
	for i := 0; i < k; i++ {
		out[i] = rs[i].id
	}
	sort.Strings(out)
	return out, nil
}

// selectRank is the rank function documented on SelectSeeded.
func selectRank(seed, purpose, id string) [sha256.Size]byte {
	h := sha256.New()
	for _, part := range []string{selectDomain, seed, purpose, id} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		h.Write(n[:])
		h.Write([]byte(part))
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// Allocate returns a copy of m in which every adversarial case has Partition and
// VarianceSubset set by the seeded rules. The input is not modified. For each threat
// class, with pool = the class's adversarial case IDs:
//
//   - cold = SelectSeeded(seed, "cold/"+class, pool, ColdCount(q.ColdPercent, len(pool)));
//   - variance = SelectSeeded(seed, "variance/"+class, pool minus cold, q.Variance[class]).
//
// Compliant and ambiguous cases are unchanged. Allocate refuses duplicate case IDs and
// invalid quotas. It does not check the registered totals, which Validate reports.
//
// Under a main D48 draw (q.Draw) the draw, not the seed, decides: a case is cold or in the
// variance subset exactly when its root_scenario_id is in the draw's cold or variance list of its
// class, and every other adversarial case is non-cold and outside the subset.
func Allocate(m Manifest, q Quotas) (Manifest, error) {
	if err := q.check(); err != nil {
		return Manifest{}, err
	}
	if q.Draw != nil && !q.Draw.Pilot {
		return allocateFromDraw(m, *q.Draw)
	}
	seen := make(map[string]bool, len(m.Cases))
	pools := make(map[ThreatClass][]string, len(threatClasses))
	for i := range m.Cases {
		c := &m.Cases[i]
		if seen[c.CaseID] {
			return Manifest{}, fmt.Errorf("evalcorpus: duplicate case ID %q", c.CaseID)
		}
		seen[c.CaseID] = true
		if c.Kind == KindAdversarial && c.Adversarial != nil && knownClass(c.Adversarial.ThreatClass) {
			class := c.Adversarial.ThreatClass
			pools[class] = append(pools[class], c.CaseID)
		}
	}
	cold := make(map[string]bool)
	variance := make(map[string]bool)
	for _, class := range threatClasses {
		pool := pools[class]
		coldIDs, err := SelectSeeded(m.SplitSeed, "cold/"+string(class), pool, ColdCount(q.ColdPercent, len(pool)))
		if err != nil {
			return Manifest{}, fmt.Errorf("evalcorpus: cold selection for %s: %w", class, err)
		}
		for _, id := range coldIDs {
			cold[id] = true
		}
		var nonCold []string
		for _, id := range pool {
			if !cold[id] {
				nonCold = append(nonCold, id)
			}
		}
		varIDs, err := SelectSeeded(m.SplitSeed, "variance/"+string(class), nonCold, q.Variance[class])
		if err != nil {
			return Manifest{}, fmt.Errorf("evalcorpus: variance selection for %s: %w", class, err)
		}
		for _, id := range varIDs {
			variance[id] = true
		}
	}

	out := m
	out.Cases = make([]Case, len(m.Cases))
	copy(out.Cases, m.Cases)
	for i := range out.Cases {
		c := &out.Cases[i]
		if c.Kind != KindAdversarial || c.Adversarial == nil || !knownClass(c.Adversarial.ThreatClass) {
			continue
		}
		a := *c.Adversarial // copy the payload so the input's payload is not changed
		a.Partition = PartitionNonCold
		if cold[c.CaseID] {
			a.Partition = PartitionCold
		}
		a.VarianceSubset = variance[c.CaseID]
		c.Adversarial = &a
	}
	return out, nil
}

func allocateFromDraw(m Manifest, d Draw) (Manifest, error) {
	out := m
	out.Cases = make([]Case, len(m.Cases))
	copy(out.Cases, m.Cases)
	seen := make(map[string]bool, len(m.Cases))
	for i := range out.Cases {
		c := &out.Cases[i]
		if seen[c.CaseID] {
			return Manifest{}, fmt.Errorf("evalcorpus: duplicate case ID %q", c.CaseID)
		}
		seen[c.CaseID] = true
		if c.Kind != KindAdversarial || c.Adversarial == nil || !knownClass(c.Adversarial.ThreatClass) {
			continue
		}
		a := *c.Adversarial
		main := a.SamplePart == SampleMain
		a.Partition = PartitionNonCold
		if main && slices.Contains(d.Cold[a.ThreatClass], a.RootScenarioID) {
			a.Partition = PartitionCold
		}
		a.VarianceSubset = main && slices.Contains(d.Variance[a.ThreatClass], a.RootScenarioID)
		c.Adversarial = &a
	}
	return out, nil
}
