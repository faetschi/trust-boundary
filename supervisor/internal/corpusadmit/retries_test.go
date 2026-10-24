package corpusadmit

import (
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
)

// A pre-effect provider retry re-dispatches the provider turn, so it reserves one more host
// call. A budget of exactly 2N+1 host calls leaves no room for the retries the signed service
// config allows (max_pre_effect_retries), so the validator that sees that setting requires the
// margin.
func TestCheckTrialsChargesPreEffectRetries(t *testing.T) {
	c := scriptedCase(t, "a")
	c.Trial.MaxHostCalls = 3 // the script's own provider turns and effects
	ceilings := &Ceilings{MaxHostCalls: 5, MaxTokens: 1 << 20, DurationSeconds: 60, MaxPreEffectRetries: 2}
	if err := CheckTrials([]evalcorpus.Case{c}, ceilings); err == nil || !strings.Contains(err.Error(), "pre-effect retries") {
		t.Fatalf("a 3-host-call script with 2 retries allowed: %v", err)
	}
	c.Trial.MaxHostCalls = 5
	if err := CheckTrials([]evalcorpus.Case{c}, ceilings); err != nil {
		t.Fatalf("a script that covers its retries: %v", err)
	}
	// Without a retry allowance the same budget is enough.
	ceilings.MaxPreEffectRetries = 0
	c.Trial.MaxHostCalls = 3
	if err := CheckTrials([]evalcorpus.Case{c}, ceilings); err != nil {
		t.Fatalf("no retries: %v", err)
	}
	// A nil ceilings has no service setting to see: the script's own minimum applies.
	if err := CheckTrials([]evalcorpus.Case{c}, nil); err != nil {
		t.Fatalf("no ceilings: %v", err)
	}
}
