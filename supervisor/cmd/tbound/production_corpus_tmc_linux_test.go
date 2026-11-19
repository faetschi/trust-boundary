//go:build linux

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/runner"
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

	// A C-DH trajectory is a C role too: refused for a fixture that C-DH would otherwise serve (live, no pivots).
	m, err := providerbridge.NewCDHLocalManifest("synthetic/local", "http://127.0.0.1:8080/v1", "sha256:"+strings.Repeat("a", 64), .8, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fixture, tmA := fixtures[0].CaseID, pilotCases[0].caseID
	corpus.cdh = &runner.CDHConfig{Manifest: m, LiveAdversarialCases: []string{fixture, tmA}}
	corpus.hintProviders = map[string]providerbridge.HintProvider{}
	for _, model := range m.Models {
		if corpus.hintProviders[model], err = providerbridge.NewCDHProvider(m, providerbridge.ChooserConfig{Model: model}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{fixture, tmA} {
		trial := corpus.trials[id]
		trial.Pivots = nil
		corpus.trials[id] = trial
	}
	role := corpusadmit.CDHRole(m.Models[0], "trajectory")
	if corpus.roleAllowed(fixture, role) || !corpus.roleAllowed(tmA, role) {
		t.Errorf("C-DH trajectory: TM-C fixture allowed %t, TM-A case allowed %t", corpus.roleAllowed(fixture, role), corpus.roleAllowed(tmA, role))
	}
}
