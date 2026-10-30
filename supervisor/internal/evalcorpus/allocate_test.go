package evalcorpus

import (
	"fmt"
	"strings"
	"testing"
)

func TestColdCountIsNearestWithTiesUp(t *testing.T) {
	cases := []struct{ percent, n, want int }{
		{20, 100, 20}, {20, 99, 20}, {20, 5, 1}, {20, 4, 1}, {20, 3, 1}, {20, 2, 0}, {20, 0, 0},
		{50, 1, 1}, {50, 3, 2}, {0, 100, 0}, {100, 7, 7},
	}
	for _, tc := range cases {
		if got := ColdCount(tc.percent, tc.n); got != tc.want {
			t.Errorf("ColdCount(%d, %d) = %d; want %d", tc.percent, tc.n, got, tc.want)
		}
	}
}

// goldenIDs returns synthetic TM-A IDs 001 through 010 in reverse order, so the tests
// also check that the result does not depend on input order.
func goldenIDs() []string {
	ids := make([]string, 0, 10)
	for i := 10; i >= 1; i-- {
		ids = append(ids, fmt.Sprintf("SYNTH-ADV-TM-A-%03d", i))
	}
	return ids
}

// The expected selections were computed outside this package, with python3 hashlib over
// the documented encoding (see the rank rule in allocate.go). They pin the rule across
// runs, platforms, and Go versions.
func TestSelectSeededGolden(t *testing.T) {
	cases := []struct {
		purpose string
		want    []string
	}{
		{"cold/TM-A", []string{"SYNTH-ADV-TM-A-001", "SYNTH-ADV-TM-A-008", "SYNTH-ADV-TM-A-009"}},
		{"variance/TM-A", []string{"SYNTH-ADV-TM-A-003", "SYNTH-ADV-TM-A-004", "SYNTH-ADV-TM-A-009"}},
	}
	for _, tc := range cases {
		got, err := SelectSeeded("golden-seed-v1", tc.purpose, goldenIDs(), 3)
		if err != nil {
			t.Fatalf("SelectSeeded(%s): %v", tc.purpose, err)
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("SelectSeeded(%s) = %v; want %v", tc.purpose, got, tc.want)
		}
	}
}

func TestSelectSeededRejectsBadInput(t *testing.T) {
	ids := goldenIDs()
	if _, err := SelectSeeded("", "cold/TM-A", ids, 3); err == nil {
		t.Error("empty seed accepted")
	}
	if _, err := SelectSeeded("golden-seed-v1", "cold/TM-A", ids, 11); err == nil {
		t.Error("k larger than the pool accepted")
	}
	if _, err := SelectSeeded("golden-seed-v1", "cold/TM-A", ids, -1); err == nil {
		t.Error("negative k accepted")
	}
	if _, err := SelectSeeded("golden-seed-v1", "cold/TM-A", append(ids, ids[0]), 3); err == nil {
		t.Error("duplicate candidate accepted")
	}
	got, err := SelectSeeded("golden-seed-v1", "cold/TM-A", ids, 0)
	if err != nil || len(got) != 0 {
		t.Errorf("k=0: got %v, err %v; want an empty selection", got, err)
	}
}

func coldSet(m Manifest) string {
	var out []string
	for i := range m.Cases {
		if a := m.Cases[i].Adversarial; a != nil && a.Partition == PartitionCold {
			out = append(out, m.Cases[i].CaseID)
		}
	}
	return strings.Join(out, ",")
}

func TestAllocateIsDeterministicValidAndNonMutating(t *testing.T) {
	q := smallQuotas()
	base := synthManifest(q, synthSeed)
	before, err := Digest(base)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	a := allocated(t, base, q)
	b := allocated(t, base, q)
	after, err := Digest(base)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if before != after {
		t.Error("Allocate changed its input manifest")
	}
	da, _ := Digest(a)
	db, _ := Digest(b)
	if da != db {
		t.Error("the same seed and quotas produced different allocations")
	}
	wantClean(t, Validate(a, q))
	for _, class := range []ThreatClass{ClassTMA, ClassTMB, ClassTMC} {
		cold := idsWhere(a, class, func(x *Adversarial) bool { return x.Partition == PartitionCold })
		if len(cold) != 1 {
			t.Errorf("%s cold count = %d; want 1", class, len(cold))
		}
		member := idsWhere(a, class, func(x *Adversarial) bool { return x.VarianceSubset })
		if len(member) != 1 {
			t.Errorf("%s variance count = %d; want 1", class, len(member))
		}
		for _, id := range member {
			if findCase(t, &a, id).Adversarial.Partition != PartitionNonCold {
				t.Errorf("variance member %s is not non-cold", id)
			}
		}
	}
}

func TestAllocateSeedChangesColdPartition(t *testing.T) {
	q := Quotas{
		Adversarial: map[ThreatClass]int{ClassTMA: 20, ClassTMB: 20, ClassTMC: 20},
		Compliant:   map[Tool]int{ToolRead: 0, ToolWrite: 0, ToolEdit: 0, ToolBash: 0},
		Ambiguous:   0,
		Variance:    map[ThreatClass]int{ClassTMA: 3, ClassTMB: 3, ClassTMC: 3},
		ColdPercent: 20,
	}
	a := allocated(t, synthManifest(q, "seed-a"), q)
	b := allocated(t, synthManifest(q, "seed-b"), q)
	if coldSet(a) == coldSet(b) {
		t.Error("two different seeds produced the same cold partition")
	}
}
