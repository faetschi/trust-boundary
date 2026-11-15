package corpusadmit

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
)

const thesisCap = 4096 // cmd/tbound productionProviderCompletionCap

func modelCeilings(tokens uint64) *Ceilings {
	return &Ceilings{MaxHostCalls: 17, MaxTokens: tokens, DurationSeconds: 1200, ModelTokensPerCall: thesisCap}
}

func kindCase(id string, kind evalcorpus.Kind, calls, tokens uint64) evalcorpus.Case {
	c := goodCase(id)
	c.Kind, c.Trial.MaxHostCalls, c.Trial.MaxTokens = kind, calls, tokens
	return c
}

// A trial whose role calls the model books the full completion cap per host call, so its token budget must
// cover host calls x cap, or the trial runs dry after a few turns.
func TestCheckTrialsRefusesAModelDrivenBudgetBelowHostCallsTimesTheCap(t *testing.T) {
	for _, kind := range []evalcorpus.Kind{evalcorpus.KindCompliant, evalcorpus.KindAmbiguous, evalcorpus.KindAdversarial} {
		short := kindCase("short-"+string(kind), kind, 7, 10000)
		err := CheckTrials([]evalcorpus.Case{short}, modelCeilings(69632))
		if err == nil || !strings.Contains(err.Error(), `"short-`+string(kind)+`"`) || !strings.Contains(err.Error(), "10000") ||
			!strings.Contains(err.Error(), "28672") {
			t.Errorf("%s: %v", kind, err)
		}
		if err := CheckTrials([]evalcorpus.Case{kindCase("ok", kind, 7, 28672)}, modelCeilings(69632)); err != nil {
			t.Errorf("%s: 28672 for 7 calls refused: %v", kind, err)
		}
	}
	if err := CheckTrials([]evalcorpus.Case{kindCase("big", evalcorpus.KindAdversarial, 17, 69632)}, modelCeilings(69632)); err != nil {
		t.Errorf("17 x 4096 refused: %v", err)
	}
}

// A case of no registered kind runs no model role: its small budget stays admissible, and so does every
// budget when the caller names no per-call cap (the offline digest tool).
func TestCheckTrialsLeavesBudgetsWithoutAModelRoleAlone(t *testing.T) {
	if err := CheckTrials([]evalcorpus.Case{kindCase("none", "", 7, 10000)}, modelCeilings(69632)); err != nil {
		t.Fatal(err)
	}
	if err := CheckTrials([]evalcorpus.Case{kindCase("c", evalcorpus.KindCompliant, 7, 10000)},
		&Ceilings{MaxHostCalls: 17, MaxTokens: 69632, DurationSeconds: 1200}); err != nil {
		t.Fatal(err)
	}
}

// Both committed pilot manifests (the T5 draft that corpus mode installs and the draft the tests and the panel read)
// are admitted under the ceilings of the signed pilot service config and the D50 per-call cap, as the corpus-mode
// service checks them (cmd/tbound loadProductionCorpus). Each case carries its registered budget: adversarial 17 host
// calls / 69632 tokens, compliant and ambiguous 11 / 45056 (5 tools at a provider call and an effect each, plus the
// final reply; owner decision 2026-11-14), 1200 s each.
func TestPilotManifestsAreAdmittedUnderTheSignedPilotConfig(t *testing.T) {
	raw, err := os.ReadFile("../../../infra/guest/serve-pi-service.corpus-pilot.json")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		MaxHostCalls        uint64 `json:"workflow_max_host_calls"`
		MaxTokens           uint64 `json:"workflow_max_tokens"`
		DurationSeconds     uint64 `json:"workflow_duration_seconds"`
		MaxPreEffectRetries uint64 `json:"workflow_max_pre_effect_retries"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	ceilings := &Ceilings{MaxHostCalls: config.MaxHostCalls, MaxTokens: config.MaxTokens, DurationSeconds: config.DurationSeconds,
		MaxPreEffectRetries: config.MaxPreEffectRetries, ModelTokensPerCall: ThesisModelTokensPerCall}
	if ceilings.MaxHostCalls == 0 || ceilings.MaxTokens == 0 || ceilings.DurationSeconds == 0 || ceilings.MaxPreEffectRetries == 0 {
		t.Fatalf("pilot config ceilings not read: %+v", ceilings)
	}
	for name, manifest := range map[string]evalcorpus.Manifest{"pilot draft": loadPilotManifest(t), "T5 draft": loadT5Manifest(t)} {
		if len(manifest.Cases) != 33 {
			t.Fatalf("%s: %d cases", name, len(manifest.Cases))
		}
		if err := CheckTrials(manifest.Cases, ceilings); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		for _, c := range manifest.Cases {
			want := evalcorpus.Trial{MaxHostCalls: 11, MaxTokens: 45056, DurationSeconds: 1200}
			if c.Kind == evalcorpus.KindAdversarial {
				want = evalcorpus.Trial{MaxHostCalls: 17, MaxTokens: 69632, DurationSeconds: 1200}
			}
			if got := c.Trial; got == nil {
				t.Errorf("%s: %s has no trial", name, c.CaseID)
			} else if got.MaxHostCalls != want.MaxHostCalls || got.MaxTokens != want.MaxTokens || got.DurationSeconds != want.DurationSeconds {
				t.Errorf("%s: %s budget %d / %d / %d, want %d / %d / %d", name, c.CaseID, got.MaxHostCalls, got.MaxTokens,
					got.DurationSeconds, want.MaxHostCalls, want.MaxTokens, want.DurationSeconds)
			}
		}
	}
}
