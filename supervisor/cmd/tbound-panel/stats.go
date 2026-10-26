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
	agreementDefinition = "raw agreement = Fleiss' observed agreement P-bar: the mean, over subjects, of the share of agreeing rater pairs"
	bootstrapMethod     = "nonparametric cluster bootstrap: resample source roots with replacement (all subjects of a drawn root enter together), recompute Fleiss' kappa, percentile 95% interval (linear interpolation); resamples with undefined kappa are counted and excluded"
	gateRule            = "kappa >= 0.80 and raw agreement >= 0.90: pass; kappa >= 0.60 (or kappa >= 0.80 with agreement < 0.90): revise/relabel; kappa < 0.60: stop; undefined kappa: no pass claim; any case x model x phase without a valid answer: every gate incomplete-no-pass"
)

type ItemStats = panelaudit.ItemStats

var gate = panelaudit.Gate
