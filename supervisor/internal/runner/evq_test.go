package runner

import (
	"context"
	"strings"
	"testing"
)

func evqRun(t *testing.T, a Attest, inner Outcome) Outcome {
	t.Helper()
	a.Inner = &stubInner{out: inner}
	out, err := a.Execute(context.Background(), advStep(CfgC, RoleStaticReplay))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A verified identity must not manufacture an EVQ pass: with no independently established
// result, every configured obligation is recorded as not passed.
func TestAttestNeverManufacturesEVQFromAVerifiedIdentity(t *testing.T) {
	a := newAttest(nil)
	a.EVQResults = nil
	out := evqRun(t, a, Outcome{E01: E01Identity{Digest: "d"}, Receipt: matching()})
	if !out.E01.Verified {
		t.Fatal("setup: identity must verify")
	}
	if len(out.EVQ) != 2 {
		t.Fatalf("every configured obligation must be recorded: %+v", out.EVQ)
	}
	for _, ob := range out.EVQ {
		if ob.Passed {
			t.Fatalf("obligation %s passed without a conformance result: %+v", ob.ID, out.EVQ)
		}
	}
}

func TestAttestAppliesPinnedResultsOnlyToTheObligationsTheyName(t *testing.T) {
	a := newAttest(nil)
	a.EVQResults = EVQResults{{ID: "EVQ-1", Passed: true, EvidenceRef: "r1"}, {ID: "EVQ-9", Passed: true, EvidenceRef: "r9"}}
	out := evqRun(t, a, Outcome{E01: E01Identity{Digest: "d"}, Receipt: matching()})
	want := []Obligation{{ID: "EVQ-1", Passed: true, EvidenceRef: "r1"}, {ID: "EVQ-2", Passed: false}}
	if len(out.EVQ) != 2 || out.EVQ[0] != want[0] || out.EVQ[1] != want[1] {
		t.Fatalf("got %+v, want %+v (EVQ-9 is not configured and EVQ-2 has no result)", out.EVQ, want)
	}
}

func TestAttestKeepsAFailedPinnedResult(t *testing.T) {
	a := newAttest(nil)
	a.EVQResults = EVQResults{{ID: "EVQ-1", Passed: true, EvidenceRef: "r1"}, {ID: "EVQ-2", Passed: false, EvidenceRef: "r2"}}
	out := evqRun(t, a, Outcome{E01: E01Identity{Digest: "d"}, Receipt: matching()})
	if !out.EVQ[0].Passed || out.EVQ[1].Passed {
		t.Fatalf("a failed conformance result must stay failed: %+v", out.EVQ)
	}
}

// An obligation the executor already failed stays failed, and an executor pass is not
// evidence: only the pinned result can pass an obligation.
func TestAttestNeverOverwritesAnExecutorResult(t *testing.T) {
	a := newAttest(nil) // the pinned results pass EVQ-1 and EVQ-2
	out := evqRun(t, a, Outcome{E01: E01Identity{Digest: "d"}, Receipt: matching(),
		EVQ: []Obligation{{ID: "EVQ-1", Passed: false}, {ID: "EVQ-2", Passed: true}, {ID: "EVQ-X", Passed: false}}})
	byID := map[string]bool{}
	for _, ob := range out.EVQ {
		byID[ob.ID] = ob.Passed
	}
	if byID["EVQ-1"] {
		t.Fatalf("an executor failure was overwritten by a pass: %+v", out.EVQ)
	}
	if !byID["EVQ-2"] {
		t.Fatalf("EVQ-2 has a pinned pass and no executor failure: %+v", out.EVQ)
	}
	if passed, ok := byID["EVQ-X"]; !ok || passed {
		t.Fatalf("a failed obligation the executor reported beyond the configured list was dropped: %+v", out.EVQ)
	}

	a.EVQResults = nil
	out = evqRun(t, a, Outcome{E01: E01Identity{Digest: "d"}, Receipt: matching(),
		EVQ: []Obligation{{ID: "EVQ-1", Passed: true}, {ID: "EVQ-2", Passed: true}}})
	for _, ob := range out.EVQ {
		if ob.Passed {
			t.Fatalf("an executor-reported pass is not conformance evidence: %+v", out.EVQ)
		}
	}
}

func TestAttestValidateRefusesBadEVQResults(t *testing.T) {
	for name, r := range map[string]EVQResults{
		"empty id":        {{ID: "", Passed: false}},
		"repeated id":     {{ID: "EVQ-1", Passed: false}, {ID: "EVQ-1", Passed: false}},
		"pass w/o ref":    {{ID: "EVQ-1", Passed: true}},
		"second pass w/o": {{ID: "EVQ-1", Passed: true, EvidenceRef: "r"}, {ID: "EVQ-2", Passed: true}},
	} {
		a := newAttest(nil)
		a.EVQResults = r
		if err := a.Validate(); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func TestParseEVQResultsIsStrict(t *testing.T) {
	good := `{"obligations":[{"id":"EVQ-1","passed":true,"evidence_ref":"a"},{"id":"EVQ-2","passed":false,"evidence_ref":""}]}`
	r, err := ParseEVQResults([]byte(good))
	if err != nil || len(r) != 2 || !r[0].Passed || r[1].Passed {
		t.Fatalf("good document: %+v %v", r, err)
	}
	for name, doc := range map[string]string{
		"unknown member": `{"obligations":[{"id":"E","passed":false,"evidence_ref":"","x":1}]}`,
		"unknown top":    `{"obligations":[{"id":"E","passed":false,"evidence_ref":""}],"x":1}`,
		"duplicate key":  `{"obligations":[{"id":"E","passed":false,"passed":true,"evidence_ref":"r"}]}`,
		"trailing":       good + `{}`,
		"empty list":     `{"obligations":[]}`,
		"no list":        `{}`,
		"repeated id":    `{"obligations":[{"id":"E","passed":false,"evidence_ref":""},{"id":"E","passed":false,"evidence_ref":""}]}`,
		"pass w/o ref":   `{"obligations":[{"id":"E","passed":true,"evidence_ref":""}]}`,
		"not json":       `nope`,
		"passed as text": `{"obligations":[{"id":"E","passed":"true","evidence_ref":"r"}]}`,
		"empty":          ``,
		"too large":      `{"obligations":[],"pad":"` + strings.Repeat("x", MaxEVQResultsBytes) + `"}`,
	} {
		if _, err := ParseEVQResults([]byte(doc)); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}
