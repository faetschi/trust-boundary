package panelaudit

import (
	"fmt"
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

// Gwet, K. L. (2008), Computing inter-rater reliability and its variance in the presence of
// high agreement, British Journal of Mathematical and Statistical Psychology 61, 29-48.
// Multi-rater AC1: Pa = mean_i sum_k r_ik (r_ik-1) / (r_i (r_i-1)); pi_k = mean_i r_ik / r_i;
// Pe = sum_k pi_k (1-pi_k) / (q-1); AC1 = (Pa-Pe)/(1-Pe), over subjects with r_i >= 2.

// ratingRows builds a count matrix of n identical rows.
func ratingRows(n int, row []int) [][]int {
	var out [][]int
	for i := 0; i < n; i++ {
		out = append(out, row)
	}
	return out
}

// 410 unanimous passes and 9 single dissents (3 raters, q = 3 categories, only pass and fail used).
// Hand computation: Pa = (410 + 9*(2/6)) / 419 = 413/419. pi_pass = (410 + 9*2/3)/419 = 416/419,
// pi_fail = 9*(1/3)/419 = 3/419, pi_unclear = 0. Pe = (416*3 + 3*416)/419^2 / 2 = 1248/175561.
// AC1 = (413/419 - 1248/175561) / (1 - 1248/175561) = 171799/174313 = 0.985578.
// Fleiss kappa collapses on the same data: (173047 - 173065)/(175561 - 173065) = -18/2496 = -0.007212.
func TestAC1HighPrevalenceExample(t *testing.T) {
	counts := append(ratingRows(410, []int{3, 0, 0}), ratingRows(9, []int{2, 1, 0})...)
	ac1, ok := AC1(counts)
	if !ok || math.Abs(ac1-171799.0/174313.0) > 1e-12 {
		t.Fatalf("AC1 %v ok=%v; want 171799/174313", ac1, ok)
	}
	kappa, agreement, _, kok := Fleiss(counts)
	if !kok || math.Abs(kappa+18.0/2496.0) > 1e-12 || math.Abs(agreement-413.0/419.0) > 1e-12 {
		t.Fatalf("kappa %v agreement %v ok=%v; want -18/2496 and 413/419", kappa, agreement, kok)
	}
	if got := Gate(ac1, agreement, true); got != GatePass {
		t.Fatalf("98.6%% agreement with AC1 %.3f gates %s, want pass", ac1, got)
	}
}

// All raters pick the same category everywhere: Pe = 0 and AC1 = 1, where kappa is undefined.
func TestAC1AllUnanimousIsOne(t *testing.T) {
	counts := ratingRows(30, []int{3, 0, 0})
	if ac1, ok := AC1(counts); !ok || ac1 != 1 {
		t.Fatalf("AC1 %v ok=%v; want 1", ac1, ok)
	}
	if _, _, _, ok := Fleiss(counts); ok {
		t.Fatal("kappa must stay undefined for all-pass")
	}
}

// The two-rater table behind Gwet's kappa paradox (hand-computed here, not copied from the paper):
// 100 subjects, yes/yes 90, yes/no 5, no/yes 3, no/no 2. Pa = 0.92; both raters' marginals give
// pi_yes = (0.95 + 0.93)/2 = 0.94 and Pe = 2*0.94*0.06 = 0.1128, so AC1 = 0.8072/0.8872 = 1009/1109.
// Cohen/Fleiss kappa on the same table is about 0.29.
func TestAC1TwoRaterTable(t *testing.T) {
	counts := append(append(ratingRows(90, []int{2, 0}), ratingRows(8, []int{1, 1})...), ratingRows(2, []int{0, 2})...)
	ac1, ok := AC1(counts)
	if !ok || math.Abs(ac1-1009.0/1109.0) > 1e-12 {
		t.Fatalf("AC1 %v ok=%v; want 1009/1109", ac1, ok)
	}
}

// Subjects with fewer than two ratings do not enter; with none left AC1 is undefined.
func TestAC1SkipsSubjectsWithoutAPair(t *testing.T) {
	if ac1, ok := AC1([][]int{{3, 0, 0}, {1, 0, 0}}); !ok || ac1 != 1 {
		t.Fatalf("AC1 %v ok=%v; want 1 from the one subject with a pair", ac1, ok)
	}
	if _, ok := AC1([][]int{{1, 0, 0}}); ok {
		t.Fatal("AC1 defined without any pair of ratings")
	}
	if _, ok := AC1(nil); ok {
		t.Fatal("AC1 defined without subjects")
	}
}

// The gate follows AC1 and raw agreement: a kappa below 0.60 no longer stops a high-agreement item.
func TestItemStatsGateFollowsAC1(t *testing.T) {
	cats := []string{"pass", "fail", "unclear"}
	var subjects []Subject
	for i := 0; i < 410; i++ {
		subjects = append(subjects, Subject{Root: fmt.Sprintf("r%03d", i), Ratings: []string{"pass", "pass", "pass"}})
	}
	for i := 410; i < 419; i++ {
		subjects = append(subjects, Subject{Root: fmt.Sprintf("r%03d", i), Ratings: []string{"pass", "pass", "fail"}})
	}
	st := ComputeItem(subjects, cats, 7, 200)
	if st.Gate != GatePass || math.Abs(st.AC1-171799.0/174313.0) > 1e-12 || !st.AC1Defined || !st.KappaDefined || st.Kappa > 0 {
		t.Fatalf("stats %+v; want a pass with AC1 0.9856 and kappa about -0.007 still reported", st)
	}
	if !st.AC1CIDefined || st.AC1CILow > st.AC1 || st.AC1CIHigh < st.AC1 {
		t.Fatalf("AC1 interval %v..%v does not bracket AC1 %v", st.AC1CILow, st.AC1CIHigh, st.AC1)
	}
	unanimous := ComputeItem(subjects[:410], cats, 7, 200)
	if unanimous.Gate != GatePass || unanimous.AC1 != 1 || unanimous.KappaDefined {
		t.Fatalf("all-pass stats %+v; want pass, AC1 1, kappa undefined", unanimous)
	}
}

// AC1 and kappa intervals come from the same root-level resamples: grouping roots keeps both
// deterministic for one seed.
func TestAC1BootstrapIsSeededAndGroupsRoots(t *testing.T) {
	subjects := []Subject{
		{Root: "r1", Ratings: []string{"pass", "pass", "pass"}},
		{Root: "r1", Ratings: []string{"fail", "fail", "fail"}},
		{Root: "r2", Ratings: []string{"pass", "fail", "pass"}},
		{Root: "r3", Ratings: []string{"unclear", "pass", "pass"}},
		{Root: "r4", Ratings: []string{"fail", "fail", "fail"}},
		{Root: "r4", Ratings: []string{"pass", "pass", "pass"}},
	}
	cats := []string{"pass", "fail", "unclear"}
	a, b := ComputeItem(subjects, cats, 7, 500), ComputeItem(subjects, cats, 7, 500)
	if !reflect.DeepEqual(a, b) || !a.AC1CIDefined || a.AC1CILow >= a.AC1CIHigh || a.AC1CILow > a.AC1 || a.AC1CIHigh < a.AC1 {
		t.Fatalf("AC1 interval not seeded or not bracketing: %+v vs %+v", a, b)
	}
}
