package corpusadmit

import (
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

// The T5 draft still carries the pre-D50 budgets, 7/10000 and 17/20000, and is refused until it is rebuilt.
func TestT5DraftIsRefusedUnderTheThesisCap(t *testing.T) {
	draft := loadT5Manifest(t)
	err := CheckTrials(draft.Cases, modelCeilings(69632))
	if err == nil || !strings.Contains(err.Error(), "model-driven") {
		t.Fatalf("T5 draft: %v", err)
	}
	failing := map[evalcorpus.Kind]int{}
	for _, c := range draft.Cases {
		if CheckTrials([]evalcorpus.Case{c}, modelCeilings(69632)) != nil {
			failing[c.Kind]++
		}
	}
	t.Logf("T5 cases refused by kind: %v of %d", failing, len(draft.Cases))
	if failing[evalcorpus.KindCompliant] != 20 || failing[evalcorpus.KindAmbiguous] != 2 || failing[evalcorpus.KindAdversarial] != 11 {
		t.Fatalf("unexpected refusal counts %v", failing)
	}
}
