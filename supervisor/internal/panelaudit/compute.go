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
// must be one of them). Every subject has the same rater count.
func ComputeItem(subjects []Subject, cats []string, seed uint64, resamples int) ItemStats {
	var st ItemStats
	st.Subjects = len(subjects)
	if len(subjects) == 0 {
		st.Gate = GateUndefined
		return st
	}
	var prev []float64
	st.Kappa, st.Agreement, prev, st.KappaDefined = Fleiss(CountMatrix(subjects, cats))
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
	var ks []float64
	for b := 0; b < resamples; b++ {
		var sample []Subject
		for range roots {
			sample = append(sample, byRoot[roots[rng.IntN(len(roots))]]...)
		}
		if k, _, _, ok := Fleiss(CountMatrix(sample, cats)); ok {
			ks = append(ks, k)
		} else {
			st.UndefinedResamples++
		}
	}
	if len(ks) > 1 {
		sort.Float64s(ks)
		st.CILow, st.CIHigh, st.CIDefined = Quantile(ks, 0.025), Quantile(ks, 0.975), true
	}
	st.Gate = Gate(st.Kappa, st.Agreement, st.KappaDefined)
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
