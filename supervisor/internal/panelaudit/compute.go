package panelaudit

import (
	"math"
	"math/rand/v2"
	"sort"
)

// Subject is one case rated on one item by every panel model.
type Subject struct {
	Root    string
	Ratings []string
}

// Fleiss returns Fleiss' kappa, observed agreement P-bar and category prevalence for a
// subjects x categories count matrix with a constant number of raters per subject. ok is
// false when kappa is undefined (expected agreement 1, or fewer than two raters).
func Fleiss(counts [][]int) (kappa, agreement float64, prevalence []float64, ok bool) {
	if len(counts) == 0 {
		return 0, 0, nil, false
	}
	n := 0
	for _, c := range counts[0] {
		n += c
	}
	prevalence = make([]float64, len(counts[0]))
	if n < 2 {
		return 0, 0, prevalence, false
	}
	var pbar float64
	for _, row := range counts {
		sq := 0
		for j, c := range row {
			sq += c * c
			prevalence[j] += float64(c)
		}
		pbar += float64(sq-n) / float64(n*(n-1))
	}
	N := float64(len(counts))
	pbar /= N
	var pe float64
	for j := range prevalence {
		prevalence[j] /= N * float64(n)
		pe += prevalence[j] * prevalence[j]
	}
	if 1-pe < 1e-12 {
		return 0, pbar, prevalence, false
	}
	return (pbar - pe) / (1 - pe), pbar, prevalence, true
}

// AC1 returns Gwet's first-order agreement coefficient for a subjects x categories count
// matrix, over the subjects with at least two ratings (Gwet, K. L. (2008), Computing
// inter-rater reliability and its variance in the presence of high agreement, British Journal
// of Mathematical and Statistical Psychology 61, 29-48). Pa is the mean over subjects of
// sum_k r_ik (r_ik-1) / (r_i (r_i-1)); pi_k the mean of r_ik / r_i; the chance term is
// Pe = sum_k pi_k (1-pi_k) / (q-1) with q the number of possible categories, observed or not.
// With a single possible category nothing can disagree and Pe is 0. ok is false when no
// subject has a pair of ratings.
func AC1(counts [][]int) (ac1 float64, ok bool) {
	if len(counts) == 0 {
		return 0, false
	}
	q := len(counts[0])
	pi := make([]float64, q)
	var pa float64
	n := 0
	for _, row := range counts {
		r := 0
		for _, c := range row {
			r += c
		}
		if r < 2 {
			continue
		}
		n++
		for k, c := range row {
			pa += float64(c*(c-1)) / float64(r*(r-1))
			pi[k] += float64(c) / float64(r)
		}
	}
	if n == 0 {
		return 0, false
	}
	pa /= float64(n)
	var pe float64
	if q > 1 {
		for _, p := range pi {
			p /= float64(n)
			pe += p * (1 - p)
		}
		pe /= float64(q - 1)
	}
	return (pa - pe) / (1 - pe), true
}

// CountMatrix is the subjects x categories count matrix of the ratings.
func CountMatrix(subjects []Subject, cats []string) [][]int {
	m := make([][]int, len(subjects))
	for i, s := range subjects {
		m[i] = make([]int, len(cats))
		for _, r := range s.Ratings {
			for j, c := range cats {
				if r == c {
					m[i][j]++
				}
			}
		}
	}
	return m
}

// ComputeItem computes the statistics of one item over the given categories (every rating
// must be one of them). Every subject has the same rater count. The kappa and AC1 intervals
// come from the same root-level bootstrap resamples; resamples=0 skips them.
func ComputeItem(subjects []Subject, cats []string, seed uint64, resamples int) ItemStats {
	var st ItemStats
	st.Subjects = len(subjects)
	if len(subjects) == 0 {
		st.Gate = GateUndefined
		return st
	}
	counts := CountMatrix(subjects, cats)
	var prev []float64
	st.Kappa, st.Agreement, prev, st.KappaDefined = Fleiss(counts)
	st.AC1, st.AC1Defined = AC1(counts)
	st.Prevalence = map[string]float64{}
	for j, c := range cats {
		st.Prevalence[c] = prev[j]
	}
	unanimous := 0
	byRoot := map[string][]Subject{}
	for _, s := range subjects {
		same := true
		for _, r := range s.Ratings {
			same = same && r == s.Ratings[0]
		}
		if same {
			unanimous++
		}
		byRoot[s.Root] = append(byRoot[s.Root], s)
	}
	st.Unanimous = float64(unanimous) / float64(len(subjects))
	roots := make([]string, 0, len(byRoot))
	for r := range byRoot {
		roots = append(roots, r)
	}
	sort.Strings(roots)
	st.Roots = len(roots)
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	var ks, as []float64
	for b := 0; b < resamples; b++ {
		var sample []Subject
		for range roots {
			sample = append(sample, byRoot[roots[rng.IntN(len(roots))]]...)
		}
		m := CountMatrix(sample, cats)
		if k, _, _, ok := Fleiss(m); ok {
			ks = append(ks, k)
		} else {
			st.KappaUndefinedResamples++
		}
		if a, ok := AC1(m); ok {
			as = append(as, a)
		}
	}
	if len(ks) > 1 {
		sort.Float64s(ks)
		st.KappaCILow, st.KappaCIHigh, st.KappaCIDefined = Quantile(ks, 0.025), Quantile(ks, 0.975), true
	}
	if len(as) > 1 {
		sort.Float64s(as)
		st.AC1CILow, st.AC1CIHigh, st.AC1CIDefined = Quantile(as, 0.025), Quantile(as, 0.975), true
	}
	st.Gate = Gate(st.AC1, st.Agreement, st.AC1Defined)
	return st
}

// Quantile is the linear-interpolation (type 7) quantile of sorted values.
func Quantile(sorted []float64, q float64) float64 {
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	if lo+1 >= len(sorted) {
		return sorted[lo]
	}
	return sorted[lo] + (pos-float64(lo))*(sorted[lo+1]-sorted[lo])
}
