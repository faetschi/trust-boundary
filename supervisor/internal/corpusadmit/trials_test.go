package corpusadmit

import (
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
)

func goodCase(id string) evalcorpus.Case {
	return evalcorpus.Case{CaseID: id, Trial: &evalcorpus.Trial{Prompt: "do " + id, MaxHostCalls: 4, MaxTokens: 1000, DurationSeconds: 60}}
}

func TestCheckTrialsAcceptsBoundedTrials(t *testing.T) {
	cases := []evalcorpus.Case{goodCase("a"), goodCase("b")}
	if err := CheckTrials(cases, nil); err != nil {
		t.Fatalf("no ceilings: %v", err)
	}
	if err := CheckTrials(cases, &Ceilings{MaxHostCalls: 4, MaxTokens: 1000, DurationSeconds: 60}); err != nil {
		t.Fatalf("ceilings equal to the budgets: %v", err)
	}
}

func TestCheckTrialsRefusals(t *testing.T) {
	long := strings.Repeat("x", TaskIDMaxBytes)
	mut := func(f func(*evalcorpus.Case)) []evalcorpus.Case {
		c := goodCase("a")
		f(&c)
		return []evalcorpus.Case{c}
	}
	ceil := &Ceilings{MaxHostCalls: 4, MaxTokens: 1000, DurationSeconds: 60}
	for name, tc := range map[string]struct {
		cases []evalcorpus.Case
		want  string
	}{
		"no trial":                     {mut(func(c *evalcorpus.Case) { c.Trial = nil }), "has no trial"},
		"blank prompt":                 {mut(func(c *evalcorpus.Case) { c.Trial.Prompt = "  " }), "bounded task identity or prompt"},
		"nul in prompt":                {mut(func(c *evalcorpus.Case) { c.Trial.Prompt = "a\x00b" }), "bounded task identity or prompt"},
		"padded id":                    {mut(func(c *evalcorpus.Case) { c.CaseID = " a" }), "bounded task identity or prompt"},
		"id too long for a request id": {mut(func(c *evalcorpus.Case) { c.CaseID = long }), "bounded task identity or prompt"},
		"host calls":                   {mut(func(c *evalcorpus.Case) { c.Trial.MaxHostCalls = 5 }), "host ceilings"},
		"tokens":                       {mut(func(c *evalcorpus.Case) { c.Trial.MaxTokens = 1001 }), "host ceilings"},
		"duration":                     {mut(func(c *evalcorpus.Case) { c.Trial.DurationSeconds = 61 }), "host ceilings"},
	} {
		err := CheckTrials(tc.cases, ceil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v, want it to contain %q", name, err, tc.want)
		}
	}
}

func TestAdmissionID(t *testing.T) {
	if got := AdmissionID("case-1", "variance-2", 2); got != "case-1/variance-2/2" {
		t.Fatalf("AdmissionID %q", got)
	}
}
