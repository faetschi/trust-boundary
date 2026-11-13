//go:build linux

package main

import (
	"testing"

	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/workflowrecovery"
)

// D50: the thesis path asks for 4,096 completion tokens per call, a product turn keeps 1,024, and both are bounded
// by what is left of the workflow token limit.
func TestProviderTokenUpperBoundSplitsThesisAndProductCaps(t *testing.T) {
	for _, tc := range []struct {
		maxTokens, thesis, product uint64
	}{
		{69632, 4096, 1024},
		{4096, 4096, 1024},
		{2048, 2048, 1024},
		{500, 500, 500},
	} {
		limits := workflowrecovery.Limits{MaxTokens: tc.maxTokens}
		if got := productionProviderTokenUpperBound(limits); got != tc.thesis {
			t.Errorf("thesis bound for %d tokens = %d, want %d", tc.maxTokens, got, tc.thesis)
		}
		if got := productProviderTokenUpperBound(limits); got != tc.product {
			t.Errorf("product bound for %d tokens = %d, want %d", tc.maxTokens, got, tc.product)
		}
	}
	if productionProviderCompletionCap != 4096 || productProviderCompletionCap != 1024 {
		t.Fatal("completion caps drifted")
	}
}

// The thesis route carries the Reasoning mark and is still a valid fixed route; the unmarked route stays the default.
func TestThesisProviderRouteCarriesTheReasoningMarkAndAuthorityKeepsIt(t *testing.T) {
	var a SessionOperationAuthority
	a.BindProviderRoute(productionProviderRoute{})
	route := a.currentProviderRoute()
	if !route.Reasoning || route.Validate() != nil || route.WithReasoning(false) != openrouter.EngineeringRoute() {
		t.Fatalf("thesis route = %+v", route)
	}
}

// The corpus admission applies the thesis cap: a model-driven trial needs max_host_calls x 4096 tokens.
func TestCorpusAdmissionRefusesAModelTrialBelowTheThesisCap(t *testing.T) {
	ceilings := &corpusadmit.Ceilings{MaxHostCalls: 17, MaxTokens: 69632, DurationSeconds: 1200, ModelTokensPerCall: productionProviderCompletionCap}
	c := evalcorpus.Case{CaseID: "a", Kind: evalcorpus.KindCompliant,
		Trial: &evalcorpus.Trial{Prompt: "do a", MaxHostCalls: 7, MaxTokens: 10000, DurationSeconds: 600}}
	if err := corpusadmit.CheckTrials([]evalcorpus.Case{c}, ceilings); err == nil {
		t.Fatal("7 calls / 10000 tokens admitted under the 4096 cap")
	}
	c.Trial.MaxTokens = 7 * productionProviderCompletionCap
	if err := corpusadmit.CheckTrials([]evalcorpus.Case{c}, ceilings); err != nil {
		t.Fatal(err)
	}
}
