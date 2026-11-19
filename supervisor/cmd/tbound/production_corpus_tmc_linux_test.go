//go:build linux

package main

import (
	"encoding/json"
	"os"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
)

// The corpus-mode service admits the B6 qualification carrier (PR #213: the T5 draft plus two derived TM-C
// development fixtures) under the pilot profile and serves those fixtures Configuration P only (D41). The T5 cases
// keep their C roles.
func TestPilotCorpusServesTMCFixturesConfigurationPOnly(t *testing.T) {
	raw, err := os.ReadFile("../../internal/corpusadmit/testdata/tmc/proc-qual-dev-01-tmc-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []evalcorpus.Case
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	corpus := loadPilotCorpus(t, fixtures...)
	for _, fixture := range fixtures {
		for _, role := range []string{corpusRolePStaticReplay, corpusRolePAdaptiveReplay} {
			if !corpus.roleServable(fixture.CaseID, role) {
				t.Errorf("%s/%s is not served", fixture.CaseID, role)
			}
		}
		for _, role := range []string{corpusRoleTrajectory, corpusRoleStaticReplay, corpusRoleAdaptiveReplay, corpusRoleVariance1} {
			if corpus.roleServable(fixture.CaseID, role) {
				t.Errorf("%s/%s (Configuration C) is served", fixture.CaseID, role)
			}
		}
	}
	if !corpus.roleServable(pilotCases[0].caseID, corpusRoleStaticReplay) {
		t.Errorf("%s lost its static replay", pilotCases[0].caseID)
	}
}
