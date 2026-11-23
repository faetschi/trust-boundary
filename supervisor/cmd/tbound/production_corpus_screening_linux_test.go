//go:build linux

package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/piruntime"
)

var screeningTools = []evalcorpus.Tool{evalcorpus.ToolRead, evalcorpus.ToolWrite, evalcorpus.ToolEdit, evalcorpus.ToolBash}

func screeningTestManifest(t *testing.T, perTool int) evalcorpus.Manifest {
	t.Helper()
	m := evalcorpus.Manifest{SchemaVersion: evalcorpus.SchemaVersion, SplitSeed: "screening-test-seed",
		Pins: evalcorpus.Pins{EnvironmentDigest: corpusTestDigest("env"), ModelDigest: corpusTestDigest("model"),
			ProfileDigest: corpusTestDigest("profile"), PolicyDigest: corpusTestDigest("policy")}}
	for _, tool := range screeningTools {
		for i := 1; i <= perTool; i++ {
			id := fmt.Sprintf("cmp-%s-%02d.s1", tool, i)
			m.Cases = append(m.Cases, evalcorpus.Case{CaseID: id, Kind: evalcorpus.KindCompliant, Signoff: corpusTestSignoff(),
				Compliant: &evalcorpus.Compliant{PrimaryTool: tool, TaskGoal: "goal", ToolMapping: "m", OracleID: "task-oracle",
					OracleVersion: "0", WorkspaceFixtureDigest: corpusTestDigest("cmp/" + id),
					AcceptanceTest:    evalcorpus.Ref{Ref: "acc-" + id, Digest: corpusTestDigest("acc/" + id)},
					ReferenceSolution: evalcorpus.ReferenceSolution{Ref: "sol-" + id, Digest: corpusTestDigest("sol/" + id), ForbiddenEffects: []string{}}},
				Trial: &evalcorpus.Trial{Prompt: "Host-approved prompt of " + id, MaxHostCalls: 9, MaxTokens: 36864, DurationSeconds: 600}})
		}
	}
	return sealConstructReview(t, m)
}

func screeningConfig(t *testing.T, m evalcorpus.Manifest, route string) piServiceConfig {
	t.Helper()
	config := installCorpusManifestWithRealQuotas(t, sealConstructReview(t, m), piruntime.D09ManagedPiCorpusQuotaProfileScreening)
	config.ProviderRoute = route
	return config
}

func TestScreeningProfileIsResearchRouteOnly(t *testing.T) {
	m := screeningTestManifest(t, 25)
	research := screeningConfig(t, m, piruntime.D09ManagedPiProviderRouteResearch)
	if err := validateProductionPiServiceConfig(research); err != nil {
		t.Fatalf("screening on the research route refused: %v", err)
	}
	if _, err := loadProductionCorpus(research); err != nil {
		t.Fatalf("screening corpus on the research route refused: %v", err)
	}
	withAllowance := research
	withAllowance.AllowPilotOnResearch = true
	if err := validateProductionPiServiceConfig(withAllowance); !errors.Is(err, ErrPiServiceUnavailable) {
		t.Fatalf("screening with the pilot allowance accepted: %v", err)
	}
	engineering := screeningConfig(t, m, piruntime.D09ManagedPiProviderRouteEngineering)
	if err := validateProductionPiServiceConfig(engineering); !errors.Is(err, ErrPiServiceUnavailable) {
		t.Fatalf("screening on the engineering route accepted: %v", err)
	}
	if _, err := loadProductionCorpus(engineering); !errors.Is(err, ErrPiServiceCorpus) || !strings.Contains(err.Error(), "research route") {
		t.Fatalf("screening corpus on the engineering route loaded: %v", err)
	}
}

func TestScreeningCorpusValidation(t *testing.T) {
	research := piruntime.D09ManagedPiProviderRouteResearch
	other := corpusTestManifest(t)
	cases := map[string]func(*evalcorpus.Manifest){
		"adversarial case": func(m *evalcorpus.Manifest) { m.Cases = append(m.Cases, other.Cases[0]) },
		"ambiguous case": func(m *evalcorpus.Manifest) {
			for _, c := range other.Cases {
				if c.Kind == evalcorpus.KindAmbiguous {
					m.Cases = append(m.Cases, c)
					return
				}
			}
		},
		"24 of one tool": func(m *evalcorpus.Manifest) {
			for i, c := range m.Cases {
				if c.Compliant.PrimaryTool == evalcorpus.ToolBash {
					m.Cases = append(m.Cases[:i], m.Cases[i+1:]...)
					return
				}
			}
		},
		"draw pin":     func(m *evalcorpus.Manifest) { m.Pins.DrawDigest = corpusTestDigest("draw") },
		"attacker pin": func(m *evalcorpus.Manifest) { m.Pins.AttackerDigest = corpusTestDigest("attacker") },
	}
	for name, change := range cases {
		m := screeningTestManifest(t, 25)
		change(&m)
		if _, err := loadProductionCorpus(screeningConfig(t, m, research)); !errors.Is(err, ErrPiServiceCorpus) {
			t.Errorf("%s: loaded: %v", name, err)
		}
	}
	more, err := loadProductionCorpus(screeningConfig(t, screeningTestManifest(t, 26), research))
	if err != nil || len(more.trials) != 104 {
		t.Fatalf("26 per tool refused: %v", err)
	}
}

func TestScreeningAllowsBCompliantOnly(t *testing.T) {
	var none *productionCorpus
	if none.roleAllowed("any", corpusRoleBCompliant) {
		t.Fatal("a nil corpus allowed b-compliant")
	}
	m := screeningTestManifest(t, 25)
	corpus, err := loadProductionCorpus(screeningConfig(t, m, piruntime.D09ManagedPiProviderRouteResearch))
	if err != nil {
		t.Fatal(err)
	}
	id := m.Cases[0].CaseID
	if !corpus.roleAllowed(id, corpusRoleBCompliant) {
		t.Fatal("b-compliant refused under screening")
	}
	for _, role := range []string{corpusRoleCompliant, corpusRoleAmbiguous, corpusRoleBAmbiguous, corpusRoleTrajectory,
		corpusadmit.CDHRole("model/x", "compliant"), corpusadmit.CDHRole("model/x", "trajectory")} {
		if corpus.roleAllowed(id, role) {
			t.Errorf("role %q allowed under screening", role)
		}
	}
}
