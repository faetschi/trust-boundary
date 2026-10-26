package panelaudit

import (
	"math"
	"reflect"
	"testing"
)

// Fleiss (1971) worked example as reproduced in most textbooks: 10 subjects, 14 raters,
// 5 categories, P-bar = 0.378, P-bar-e = 0.213, kappa = 0.210.
func TestFleissKappaTextbook(t *testing.T) {
	counts := [][]int{
		{0, 0, 0, 0, 14}, {0, 2, 6, 4, 2}, {0, 0, 3, 5, 6}, {0, 3, 9, 2, 0}, {2, 2, 8, 1, 1},
		{7, 7, 0, 0, 0}, {3, 2, 6, 3, 0}, {2, 5, 3, 2, 2}, {6, 5, 2, 1, 0}, {0, 2, 2, 3, 7},
	}
	k, agree, prev, ok := Fleiss(counts)
	if !ok {
		t.Fatal("kappa undefined")
	}
	if math.Abs(k-0.210) > 0.0005 || math.Abs(agree-0.378) > 0.0005 {
		t.Fatalf("kappa %.4f agreement %.4f", k, agree)
	}
	if math.Abs(prev[0]-0.143) > 0.0005 || math.Abs(prev[2]-0.279) > 0.0005 {
		t.Fatalf("prevalence %v", prev)
	}
}

func TestFleissKappaPerfectAndUndefined(t *testing.T) {
	if k, a, _, ok := Fleiss([][]int{{3, 0, 0}, {0, 3, 0}, {0, 0, 3}}); !ok || k != 1 || a != 1 {
		t.Fatalf("perfect: %v %v %v", k, a, ok)
	}
	// Every rating in one category: expected agreement is 1, kappa is undefined.
	if _, a, _, ok := Fleiss([][]int{{3, 0, 0}, {3, 0, 0}}); ok || a != 1 {
		t.Fatalf("all-pass must be undefined with agreement 1, got ok=%v a=%v", ok, a)
	}
}

func TestGate(t *testing.T) {
	cases := []struct {
		k, a    float64
		defined bool
		want    string
	}{
		{0.85, 0.95, true, GatePass},
		{0.85, 0.85, true, GateRevise}, // agreement below 90% cannot pass
		{0.70, 0.95, true, GateRevise},
		{0.59, 0.95, true, GateStop},
		{0, 1, false, GateUndefined},
	}
	for _, c := range cases {
		if got := Gate(c.k, c.a, c.defined); got != c.want {
			t.Errorf("Gate(%v,%v,%v)=%s want %s", c.k, c.a, c.defined, got, c.want)
		}
	}
}

func TestItemStatsBootstrapIsSeededAndGroupsRoots(t *testing.T) {
	// Six subjects over four roots; rater disagreement on two subjects.
	subjects := []Subject{
		{Root: "r1", Ratings: []string{"pass", "pass", "pass"}},
		{Root: "r1", Ratings: []string{"fail", "fail", "fail"}},
		{Root: "r2", Ratings: []string{"pass", "fail", "pass"}},
		{Root: "r3", Ratings: []string{"unclear", "pass", "pass"}},
		{Root: "r4", Ratings: []string{"fail", "fail", "fail"}},
		{Root: "r4", Ratings: []string{"pass", "pass", "pass"}},
	}
	a := ComputeItem(subjects, []string{"pass", "fail", "unclear"}, 7, 500)
	b := ComputeItem(subjects, []string{"pass", "fail", "unclear"}, 7, 500)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("bootstrap not deterministic: %+v vs %+v", a, b)
	}
	if a.Subjects != 6 || a.Roots != 4 || !a.KappaDefined || a.CILow > a.Kappa || a.CIHigh < a.Kappa || a.CILow >= a.CIHigh {
		t.Fatalf("unexpected stats %+v", a)
	}
	if math.Abs(a.Unanimous-4.0/6) > 1e-9 {
		t.Fatalf("unanimous %v", a.Unanimous)
	}
}
