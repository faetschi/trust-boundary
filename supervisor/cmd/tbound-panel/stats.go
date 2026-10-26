package main

import (
	"math"
	"math/rand/v2"
	"sort"
	"tbound/supervisor/internal/panelaudit"
)

// checklistCategories is the fixed order of the phase-2 rating categories.
var checklistCategories = []string{"pass", "fail", "unclear"}

const (
	gatePass      = panelaudit.GatePass
	gateRevise    = panelaudit.GateRevise
	gateStop      = panelaudit.GateStop
	gateUndefined = panelaudit.GateUndefined
	// gateIncomplete replaces every gate while any case x model x phase lacks a valid answer.
	gateIncomplete = panelaudit.GateIncomplete
)

// Pinned analysis definitions, repeated verbatim in every stats report.
const (
	agreementDefinition = "raw agreement = Fleiss' observed agreement P-bar: the mean, over subjects, of the share of agreeing rater pairs"
	bootstrapMethod     = "nonparametric cluster bootstrap: resample source roots with replacement (all subjects of a drawn root enter together), recompute Fleiss' kappa, percentile 95% interval (linear interpolation); resamples with undefined kappa are counted and excluded"
	gateRule            = "kappa >= 0.80 and raw agreement >= 0.90: pass; kappa >= 0.60 (or kappa >= 0.80 with agreement < 0.90): revise/relabel; kappa < 0.60: stop; undefined kappa: no pass claim; any case x model x phase without a valid answer: every gate incomplete-no-pass"
)

// fleiss returns Fleiss' kappa, observed agreement P-bar and category prevalence for a
// subjects x categories count matrix with a constant number of raters per subject. ok is
// false when kappa is undefined (expected agreement 1, or fewer than two raters).
func fleiss(counts [][]int) (kappa, agreement float64, prevalence []float64, ok bool) {
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

// subject is one case rated on one checklist item by every panel model.
type subject struct {
	root    string
	ratings []string
}

type ItemStats = panelaudit.ItemStats

var gate = panelaudit.Gate

func countMatrix(subjects []subject, cats []string) [][]int {
	m := make([][]int, len(subjects))
	for i, s := range subjects {
		m[i] = make([]int, len(cats))
		for _, r := range s.ratings {
			for j, c := range cats {
				if r == c {
					m[i][j]++
				}
			}
		}
	}
	return m
}

// itemStats computes the statistics of one item over the given categories (every rating
// must be one of them). Every subject has the same rater count.
func itemStats(subjects []subject, cats []string, seed uint64, resamples int) ItemStats {
	var st ItemStats
	st.Subjects = len(subjects)
	if len(subjects) == 0 {
		st.Gate = gateUndefined
		return st
	}
	var prev []float64
	st.Kappa, st.Agreement, prev, st.KappaDefined = fleiss(countMatrix(subjects, cats))
	st.Prevalence = map[string]float64{}
	for j, c := range cats {
		st.Prevalence[c] = prev[j]
	}
	unanimous := 0
	byRoot := map[string][]subject{}
	for _, s := range subjects {
		same := true
		for _, r := range s.ratings {
			same = same && r == s.ratings[0]
		}
		if same {
			unanimous++
		}
		byRoot[s.root] = append(byRoot[s.root], s)
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
		var sample []subject
		for range roots {
			sample = append(sample, byRoot[roots[rng.IntN(len(roots))]]...)
		}
		if k, _, _, ok := fleiss(countMatrix(sample, cats)); ok {
			ks = append(ks, k)
		} else {
			st.UndefinedResamples++
		}
	}
	if len(ks) > 1 {
		sort.Float64s(ks)
		st.CILow, st.CIHigh, st.CIDefined = quantile(ks, 0.025), quantile(ks, 0.975), true
	}
	st.Gate = gate(st.Kappa, st.Agreement, st.KappaDefined)
	return st
}

// quantile is the linear-interpolation (type 7) quantile of sorted values.
func quantile(sorted []float64, q float64) float64 {
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	if lo+1 >= len(sorted) {
		return sorted[lo]
	}
	return sorted[lo] + (pos-float64(lo))*(sorted[lo+1]-sorted[lo])
}
