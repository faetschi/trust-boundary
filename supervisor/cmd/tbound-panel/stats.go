package main

import "tbound/supervisor/internal/panelaudit"

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
	agreementDefinition = "raw agreement = Fleiss' observed agreement P-bar: the mean, over subjects, of the share of agreeing rater pairs (equal to Gwet's Pa for a constant rater count)"
	bootstrapMethod     = "nonparametric cluster bootstrap: resample source roots with replacement (all subjects of a drawn root enter together), recompute Gwet's AC1 and Fleiss' kappa on the same resamples, percentile 95% intervals (linear interpolation); resamples with undefined kappa are counted and excluded from the kappa interval"
	gateRule            = "Gwet's AC1 >= 0.80 and raw agreement >= 0.90: pass; AC1 0.60 to 0.79 (or AC1 >= 0.80 with agreement < 0.90): revise/relabel; AC1 < 0.60: stop; undefined AC1: no pass claim; any case x model x phase without a valid answer: every gate incomplete-no-pass. Fleiss' kappa, prevalence and the bootstrap intervals are reported for every item and not gated; kappa collapses at high prevalence"
)

type ItemStats = panelaudit.ItemStats

var gate = panelaudit.Gate
