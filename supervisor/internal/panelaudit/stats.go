package panelaudit

// gate applies the D35 agreement gate.
func Gate(kappa, agreement float64, defined bool) string {
	switch {
	case !defined:
		return GateUndefined
	case kappa >= 0.80 && agreement >= 0.90:
		return GatePass
	case kappa >= 0.60:
		return GateRevise
	}
	return GateStop
}

// ItemStats is the pre-adjudication agreement record of one checklist item.
type ItemStats struct {
	Subjects           int                `json:"subjects"`
	Roots              int                `json:"roots"`
	Kappa              float64            `json:"kappa"`
	KappaDefined       bool               `json:"kappa_defined"`
	Agreement          float64            `json:"raw_agreement"`
	Unanimous          float64            `json:"unanimous_share"`
	Prevalence         map[string]float64 `json:"prevalence"`
	CILow              float64            `json:"ci95_low"`
	CIHigh             float64            `json:"ci95_high"`
	CIDefined          bool               `json:"ci_defined"`
	UndefinedResamples int                `json:"undefined_resamples"`
	Gate               string             `json:"gate"`
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
