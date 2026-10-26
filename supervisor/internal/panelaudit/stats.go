package panelaudit

// Gate applies the D35 agreement gate to Gwet's AC1 and the raw agreement: AC1 >= 0.80 with
// agreement >= 0.90 passes, AC1 0.60 to 0.79 is revise (so is AC1 >= 0.80 under agreement 0.90),
// anything lower is stop. Fleiss' kappa is reported, never gated: it collapses at high prevalence.
func Gate(ac1, agreement float64, defined bool) string {
	switch {
	case !defined:
		return GateUndefined
	case ac1 >= 0.80 && agreement >= 0.90:
		return GatePass
	case ac1 >= 0.60:
		return GateRevise
	}
	return GateStop
}

// ItemStats is the pre-adjudication agreement record of one checklist item.
type ItemStats struct {
	Subjects     int     `json:"subjects"`
	Roots        int     `json:"roots"`
	AC1          float64 `json:"ac1"`
	AC1Defined   bool    `json:"ac1_defined"`
	AC1CILow     float64 `json:"ac1_ci95_low"`
	AC1CIHigh    float64 `json:"ac1_ci95_high"`
	AC1CIDefined bool    `json:"ac1_ci_defined"`
	// Fleiss' kappa and its interval are reported for comparison and are not gated.
	Kappa                   float64            `json:"kappa"`
	KappaDefined            bool               `json:"kappa_defined"`
	KappaCILow              float64            `json:"kappa_ci95_low"`
	KappaCIHigh             float64            `json:"kappa_ci95_high"`
	KappaCIDefined          bool               `json:"kappa_ci_defined"`
	KappaUndefinedResamples int                `json:"kappa_undefined_resamples"`
	Agreement               float64            `json:"raw_agreement"`
	Unanimous               float64            `json:"unanimous_share"`
	Prevalence              map[string]float64 `json:"prevalence"`
	Gate                    string             `json:"gate"`
}

type Stats struct {
	Models        []string             `json:"models"`
	Schema        string               `json:"schema"`
	ContentDigest string               `json:"content_digest"`
	DryRun        bool                 `json:"dry_run"`
	LiveRun       bool                 `json:"live_run"`
	Provider      string               `json:"provider"`
	Cases         int                  `json:"cases"`
	CompleteCases int                  `json:"complete_cases"`
	Incomplete    []string             `json:"incomplete_cases"`
	Missing       []map[string]any     `json:"missing_pairs"`
	Items         map[string]ItemStats `json:"items"`
}
