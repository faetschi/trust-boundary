package panelaudit

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"tbound/supervisor/internal/evalcorpus"
)

const AuditPurpose = "tbound-panel/unanimous-audit"
const AuditSampleSize = 60
const RandomSampleReason = "random unanimous sample"

type Selection struct {
	Seed          string   `json:"seed"`
	Purpose       string   `json:"purpose"`
	Method        string   `json:"method"`
	SampleSize    int      `json:"sample_size"`
	EligibleRoots []string `json:"eligible_roots"`
	SelectedRoots []string `json:"selected_roots"`
}

func Root(c evalcorpus.Case) string {
	if c.MeaningRecord != nil && c.MeaningRecord.RootID != "" {
		return c.MeaningRecord.RootID
	}
	if c.Adversarial != nil && c.Adversarial.RootScenarioID != "" {
		return c.Adversarial.RootScenarioID
	}
	return c.CaseID
}

// SelectAudit keeps every member of a root out of the random pool if any member needs deep review.
func SelectAudit(cases []evalcorpus.Case, reasons map[string][]string, seed string, size int) (Selection, error) {
	unanimous := map[string]bool{}
	for _, c := range cases {
		rt := Root(c)
		if _, seen := unanimous[rt]; !seen {
			unanimous[rt] = true
		}
		unanimous[rt] = unanimous[rt] && c.Kind != evalcorpus.KindAmbiguous && len(reasons[c.CaseID]) == 0
	}
	var eligible []string
	for rt, ok := range unanimous {
		if ok {
			eligible = append(eligible, rt)
		}
	}
	sort.Strings(eligible)
	selected, err := evalcorpus.SelectSeeded(seed, AuditPurpose, eligible, min(size, len(eligible)))
	return Selection{seed, AuditPurpose, "evalcorpus.SelectSeeded", size, eligible, selected}, err
}

func verifySelection(m evalcorpus.Manifest, audit Audit, receipt Receipt) error {
	sel := audit.Selection
	if _, err := strconv.ParseUint(sel.Seed, 10, 64); err != nil || sel.Seed == "0" || sel.Seed != receipt.Seeds.AuditSample {
		return fmt.Errorf("panel audit selection seed %q differs from receipt or is invalid", sel.Seed)
	}
	if sel.SampleSize != AuditSampleSize || sel.Purpose != AuditPurpose || sel.Method != "evalcorpus.SelectSeeded" {
		return fmt.Errorf("panel audit selection must use %s and sample size %d", AuditPurpose, AuditSampleSize)
	}
	reasons := map[string][]string{}
	sampled := map[string]bool{}
	for _, c := range audit.Cases {
		if len(c.Reasons) == 1 && c.Reasons[0] == RandomSampleReason {
			sampled[c.CaseID] = true
		} else {
			if len(c.Reasons) == 0 {
				return fmt.Errorf("panel audit selection case %q has no reasons", c.CaseID)
			}
			reasons[c.CaseID] = c.Reasons
		}
	}
	expected, err := SelectAudit(m.Cases, reasons, sel.Seed, AuditSampleSize)
	if err != nil {
		return fmt.Errorf("panel audit selection: %w", err)
	}
	if !slices.Equal(sel.EligibleRoots, expected.EligibleRoots) || !slices.Equal(sel.SelectedRoots, expected.SelectedRoots) {
		return fmt.Errorf("panel audit selection eligible or selected roots differ from seeded selection")
	}
	for _, c := range m.Cases {
		want := len(reasons[c.CaseID]) == 0 && slices.Contains(expected.SelectedRoots, Root(c))
		if sampled[c.CaseID] != want {
			return fmt.Errorf("panel audit selection sample membership differs for case %q", c.CaseID)
		}
	}
	return nil
}
