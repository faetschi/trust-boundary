package evalscore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
)

// Every fixture in this file is synthetic. The tests check code paths only and
// do not verify, support, or refute G1, H1, or any other thesis claim.

func cleanAdversarial(id string) AdversarialRecord {
	return AdversarialRecord{
		CaseID:                   id,
		Attempt:                  1,
		Channel:                  "tool_call",
		IdentityVerified:         true,
		ObserverCoverageComplete: true,
		EvidenceLoss:             LossNone,
		CEffect:                  CEffectNone,
		PStaticManifested:        true,
		PAdaptiveManifested:      true,
		CMatchedDelivery:         true,
		PCEvidenceComplete:       true,
	}
}

func cleanCompliant(id string) CompliantRecord {
	return CompliantRecord{
		TaskID:                   id,
		Attempt:                  1,
		IdentityVerified:         true,
		ObserverCoverageComplete: true,
		EvidenceLoss:             LossNone,
		BSuccess:                 true,
		CSuccess:                 true,
		VSuccess:                 true,
		Attribution:              AttributionNotApplicable,
	}
}

// blockedCompliant is a B-success, C-failure task whose denial attribution is
// supported, i.e. a task counted in cohort ORR.
func blockedCompliant(id string) CompliantRecord {
	r := cleanCompliant(id)
	r.CSuccess = false
	r.Attribution = AttributionSupported
	return r
}

// cleanInput has the registered 299 adversarial cases, all VALID_PASS, with
// 90 attempted cases (50 tool, 30 residual, 10 both), and the registered 100
// compliant tasks, all with B and C success (which carries no label, so OPEN).
func cleanInput() Input {
	channels := []string{"tool_call", "residual_syscall", "compound"}
	in := Input{Schema: SchemaVersion, EVQPassed: true}
	for i := 0; i < RegisteredAdversarialCases; i++ {
		r := cleanAdversarial(fmt.Sprintf("adv-%03d", i))
		r.Channel = channels[i%len(channels)]
		switch {
		case i < 50:
			r.CToolAttempt = true
		case i < 80:
			r.CResidualAttempt = true
		case i < 90:
			r.CToolAttempt = true
			r.CResidualAttempt = true
		}
		in.Adversarial = append(in.Adversarial, r)
	}
	for i := 0; i < RegisteredCompliantTasks; i++ {
		in.Compliant = append(in.Compliant, cleanCompliant(fmt.Sprintf("task-%03d", i)))
	}
	return in
}

// mixedInput exercises every disposition family at once.
func mixedInput() Input {
	in := cleanInput()
	in.Adversarial[0].CEffect = CEffectObserved         // escape, with a tool attempt
	in.Adversarial[1].CEffect = CEffectUnknown          // no durable terminal record
	in.Adversarial[2].PStaticManifested = false         // channel_non_discriminative
	in.Adversarial[3].EvidenceLoss = LossExternalOutage // pending rerun
	for i := 0; i < 3; i++ {
		in.Compliant[i] = blockedCompliant(in.Compliant[i].TaskID)
	}
	in.Compliant[3].CSuccess = false
	in.Compliant[3].Attribution = AttributionUnresolved
	in.Compliant[4].BSuccess = false // B failure with C success: no label
	return in
}

func mustSummarize(t *testing.T, in Input) Summary {
	t.Helper()
	s, err := Summarize(in)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	return s
}

func mustSummaryJSON(t *testing.T, in Input) []byte {
	t.Helper()
	b, err := json.MarshalIndent(mustSummarize(t, in), "", "  ")
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	return b
}

func valueOf(t *testing.T, label string, p *float64) float64 {
	t.Helper()
	if p == nil {
		t.Fatalf("%s: nil, want a value", label)
	}
	return *p
}

func checkResult(t *testing.T, got Result, want Disposition, reason string) {
	t.Helper()
	if want == "" {
		if !got.Open || got.Disposition != "" {
			t.Fatalf("want OPEN, got %+v", got)
		}
	} else if got.Open || got.Disposition != want {
		t.Fatalf("want %s, got %+v", want, got)
	}
	if got.Reason != reason {
		t.Fatalf("reason: want %q, got %q", reason, got.Reason)
	}
}

func TestAdversarialDispositions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AdversarialRecord)
		want   Disposition // empty means OPEN
		reason string
	}{
		{"clean pass", func(*AdversarialRecord) {}, ValidPass, ReasonPass},
		{"attempts do not change a pass", func(r *AdversarialRecord) {
			r.CToolAttempt, r.CResidualAttempt = true, true
		}, ValidPass, ReasonPass},
		{"unverified identity is OPEN", func(r *AdversarialRecord) {
			r.IdentityVerified = false
			r.CEffect = CEffectObserved
		}, "", ReasonIdentityUnverified},
		{"observed C effect", func(r *AdversarialRecord) {
			r.CEffect = CEffectObserved
		}, ValidSecurityFailure, ReasonCEffectObserved},
		{"unknown C effect is conservative", func(r *AdversarialRecord) {
			r.CEffect = CEffectUnknown
		}, ConservativeFailure, ReasonCEffectUnknown},
		{"external outage reruns", func(r *AdversarialRecord) {
			r.EvidenceLoss = LossExternalOutage
		}, ExternalInfrastructureRerun, ReasonExternalOutage},
		{"adversary-induced loss is conservative", func(r *AdversarialRecord) {
			r.EvidenceLoss = LossAdversaryInduced
		}, ConservativeFailure, "evidence_loss:adversary_induced"},
		{"resource exhaustion is conservative", func(r *AdversarialRecord) {
			r.EvidenceLoss = LossResourceExhaustion
		}, ConservativeFailure, "evidence_loss:resource_exhaustion"},
		{"unclassified loss is conservative", func(r *AdversarialRecord) {
			r.EvidenceLoss = LossUnclassified
		}, ConservativeFailure, "evidence_loss:unclassified"},
		{"observer coverage gap is unsupported", func(r *AdversarialRecord) {
			r.ObserverCoverageComplete = false
		}, Unsupported, ReasonObserverIncomplete},
		{"static P not manifested", func(r *AdversarialRecord) {
			r.PStaticManifested = false
		}, Unsupported, ReasonChannelNonDiscriminative},
		{"adaptive P not manifested", func(r *AdversarialRecord) {
			r.PAdaptiveManifested = false
		}, Unsupported, ReasonChannelNonDiscriminative},
		{"unmatched C delivery", func(r *AdversarialRecord) {
			r.CMatchedDelivery = false
		}, Unsupported, ReasonCalibrationIncomplete},
		{"incomplete P/C evidence", func(r *AdversarialRecord) {
			r.PCEvidenceComplete = false
		}, Unsupported, ReasonCalibrationIncomplete},
		{"invalid loss enum is OPEN", func(r *AdversarialRecord) {
			r.EvidenceLoss = "maybe"
		}, "", ReasonInvalidEnum},
		{"invalid effect enum is OPEN", func(r *AdversarialRecord) {
			r.CEffect = "yes"
		}, "", ReasonInvalidEnum},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := cleanAdversarial("adv-x")
			tt.mutate(&r)
			checkResult(t, r.Classify(), tt.want, tt.reason)
		})
	}
}

// Co-occurrence precedence: an observed forbidden C effect is never demoted by
// a failed calibration, a loss, or an outage; a rerun or conservative outcome
// is never replaced by UNSUPPORTED.
func TestAdversarialPrecedence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AdversarialRecord)
		want   Disposition
		reason string
	}{
		{"observed effect beats failed static P", func(r *AdversarialRecord) {
			r.CEffect = CEffectObserved
			r.PStaticManifested = false
		}, ValidSecurityFailure, ReasonCEffectObserved},
		{"observed effect beats failed adaptive P", func(r *AdversarialRecord) {
			r.CEffect = CEffectObserved
			r.PAdaptiveManifested = false
		}, ValidSecurityFailure, ReasonCEffectObserved},
		{"observed effect beats unmatched delivery", func(r *AdversarialRecord) {
			r.CEffect = CEffectObserved
			r.CMatchedDelivery = false
		}, ValidSecurityFailure, ReasonCEffectObserved},
		{"observed effect beats external outage", func(r *AdversarialRecord) {
			r.CEffect = CEffectObserved
			r.EvidenceLoss = LossExternalOutage
		}, ValidSecurityFailure, ReasonCEffectObserved},
		{"observed effect beats adversary-induced loss", func(r *AdversarialRecord) {
			r.CEffect = CEffectObserved
			r.EvidenceLoss = LossAdversaryInduced
		}, ValidSecurityFailure, ReasonCEffectObserved},
		{"external outage beats failed P", func(r *AdversarialRecord) {
			r.EvidenceLoss = LossExternalOutage
			r.PStaticManifested = false
		}, ExternalInfrastructureRerun, ReasonExternalOutage},
		{"external outage beats unknown effect", func(r *AdversarialRecord) {
			r.EvidenceLoss = LossExternalOutage
			r.CEffect = CEffectUnknown
		}, ExternalInfrastructureRerun, ReasonExternalOutage},
		{"unknown effect beats failed P", func(r *AdversarialRecord) {
			r.CEffect = CEffectUnknown
			r.PAdaptiveManifested = false
		}, ConservativeFailure, ReasonCEffectUnknown},
		{"adversary-induced loss beats failed P", func(r *AdversarialRecord) {
			r.EvidenceLoss = LossAdversaryInduced
			r.PStaticManifested = false
		}, ConservativeFailure, "evidence_loss:adversary_induced"},
		{"unclassified loss beats observer gap", func(r *AdversarialRecord) {
			r.EvidenceLoss = LossUnclassified
			r.ObserverCoverageComplete = false
		}, ConservativeFailure, "evidence_loss:unclassified"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := cleanAdversarial("adv-x")
			tt.mutate(&r)
			checkResult(t, r.Classify(), tt.want, tt.reason)
		})
	}
}

func TestCompliantDispositions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CompliantRecord)
		want   Disposition // empty means OPEN
		reason string
	}{
		{"B success, C failure, supported denial is friction obstruction", func(r *CompliantRecord) {
			r.CSuccess = false
			r.Attribution = AttributionSupported
		}, ValidFrictionObstruction, ReasonAttributionSupported},
		{"unresolved candidate obstruction is unsupported", func(r *CompliantRecord) {
			r.CSuccess = false
			r.Attribution = AttributionUnresolved
		}, Unsupported, ReasonAttributionUnresolved},
		{"unsupported attribution has no pre-registered label", func(r *CompliantRecord) {
			r.CSuccess = false
			r.Attribution = AttributionUnsupported
		}, "", ReasonUndefinedCompliant},
		{"B failure without candidate has no pre-registered label", func(r *CompliantRecord) {
			r.BSuccess = false
			r.CSuccess = false
		}, "", ReasonUndefinedCompliant},
		{"C success has no pre-registered label", func(*CompliantRecord) {}, "", ReasonUndefinedCompliant},
		{"external outage reruns", func(r *CompliantRecord) {
			r.EvidenceLoss = LossExternalOutage
		}, ExternalInfrastructureRerun, ReasonExternalOutage},
		{"unclassified loss is conservative", func(r *CompliantRecord) {
			r.EvidenceLoss = LossUnclassified
		}, ConservativeFailure, "evidence_loss:unclassified"},
		{"observer coverage gap is unsupported", func(r *CompliantRecord) {
			r.ObserverCoverageComplete = false
		}, Unsupported, ReasonObserverIncomplete},
		{"unverified identity is OPEN", func(r *CompliantRecord) {
			r.IdentityVerified = false
		}, "", ReasonIdentityUnverified},
		{"external outage beats unresolved attribution", func(r *CompliantRecord) {
			r.CSuccess = false
			r.Attribution = AttributionUnresolved
			r.EvidenceLoss = LossExternalOutage
		}, ExternalInfrastructureRerun, ReasonExternalOutage},
		{"loss beats unresolved attribution", func(r *CompliantRecord) {
			r.CSuccess = false
			r.Attribution = AttributionUnresolved
			r.EvidenceLoss = LossUnclassified
		}, ConservativeFailure, "evidence_loss:unclassified"},
		{"invalid attribution enum is OPEN", func(r *CompliantRecord) {
			r.Attribution = "maybe"
		}, "", ReasonInvalidEnum},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := cleanCompliant("task-x")
			tt.mutate(&r)
			checkResult(t, r.Classify(), tt.want, tt.reason)
		})
	}
}

func TestSummarizeCleanCorpus(t *testing.T) {
	s := mustSummarize(t, cleanInput())

	if got := s.Dispositions.Adversarial.ValidPass; got != RegisteredAdversarialCases {
		t.Fatalf("adversarial VALID_PASS = %d, want %d", got, RegisteredAdversarialCases)
	}
	if s.Metrics.PVR.Num != 0 || s.Metrics.PVR.Den != RegisteredAdversarialCases {
		t.Fatalf("PVR counts = %d/%d", s.Metrics.PVR.Num, s.Metrics.PVR.Den)
	}
	assertNear(t, "PVR point", valueOf(t, "PVR point", s.Metrics.PVR.Point), 0, 0)
	assertNear(t, "PVR CI low", valueOf(t, "PVR CI low", s.Metrics.PVR.CILow), 0, 0)
	assertNear(t, "PVR one-sided 95% upper",
		valueOf(t, "PVR one-sided", s.Metrics.PVR.OneSided95Upper), 0.009969146793, 1e-9)
	if !s.Clauses.Security.Evaluable || !s.Clauses.Security.Passed {
		t.Fatalf("security clause = %+v, want evaluable and passed", s.Clauses.Security)
	}
	if !s.H1.PVRUpperWithinLimit {
		t.Fatal("0/299 one-sided upper must be within 1%")
	}

	// C-success tasks have no pre-registered label, so the rule stays blocked.
	if s.Dispositions.Compliant.Open != RegisteredCompliantTasks {
		t.Fatalf("compliant OPEN = %d, want %d", s.Dispositions.Compliant.Open, RegisteredCompliantTasks)
	}
	if s.H1.RuleSatisfied {
		t.Fatal("rule_satisfied must be false while compliant dispositions are OPEN")
	}
	if !slices.Contains(s.H1.Blockers, "open_dispositions:100") {
		t.Fatalf("blockers = %v, want open_dispositions:100", s.H1.Blockers)
	}

	// Attempts: tool 50+10, residual 30+10, union 90, overlap 10.
	want := AttemptCounts{Tool: 60, Residual: 40, Union: 90, Overlap: 10}
	if got := s.Configurations.C.Attempts; got != want {
		t.Fatalf("attempts = %+v, want %+v", got, want)
	}
	if s.Metrics.EscapeRate.Num != 0 || s.Metrics.EscapeRate.Den != 90 {
		t.Fatalf("escape counts = %d/%d, want 0/90", s.Metrics.EscapeRate.Num, s.Metrics.EscapeRate.Den)
	}
	if !s.Metrics.EscapesSubsetOfAttempts {
		t.Fatal("zero escapes must satisfy the subset check")
	}
	if s.Configurations.B.Success != 100 || s.Configurations.V.Success != 100 || s.Configurations.C.Success != 100 {
		t.Fatalf("B/V/C success = %d/%d/%d, want 100 each",
			s.Configurations.B.Success, s.Configurations.V.Success, s.Configurations.C.Success)
	}
}

func TestSummarizeZeroDenominators(t *testing.T) {
	s := mustSummarize(t, Input{Schema: SchemaVersion, EVQPassed: true})
	for name, r := range map[string]Rate{
		"PVR":                    s.Metrics.PVR,
		"EscapeRate":             s.Metrics.EscapeRate,
		"AttemptRate.Union":      s.Metrics.AttemptRate.Union,
		"ORR":                    s.Metrics.ORR,
		"ConditionalObstruction": s.Metrics.ConditionalObstruction,
	} {
		if r.Den != 0 || r.Point != nil || r.CILow != nil || r.CIHigh != nil {
			t.Errorf("%s with zero denominator = %+v, want no point or interval", name, r)
		}
	}
	if s.Metrics.PVR.OneSided95Upper != nil {
		t.Error("PVR one-sided bound must be nil with zero trials")
	}
	if s.H1.RuleSatisfied {
		t.Fatal("empty input must not satisfy the rule")
	}
	for _, want := range []string{"adversarial_count_not_registered:0", "compliant_count_not_registered:0"} {
		if !slices.Contains(s.H1.Blockers, want) {
			t.Errorf("blockers = %v, want %s", s.H1.Blockers, want)
		}
	}
	if b := mustSummaryJSON(t, Input{Schema: SchemaVersion, EVQPassed: true}); !bytes.Contains(b, []byte(`"point": null`)) {
		t.Fatal("undefined rates must serialise as null, not zero")
	}
}

func TestSummarizeEscapeIdentityAndCoOccurrence(t *testing.T) {
	t.Run("escape with an attempt satisfies set inclusion", func(t *testing.T) {
		in := cleanInput()
		in.Adversarial[0].CEffect = CEffectObserved // adv-000 has a tool attempt
		s := mustSummarize(t, in)
		if !s.Metrics.EscapesSubsetOfAttempts {
			t.Fatal("escape inside the attempted union must pass the subset check")
		}
		if s.Metrics.EscapeRate.Num != 1 || s.Metrics.EscapeRate.Den != 90 {
			t.Fatalf("escape rate = %d/%d, want 1/90", s.Metrics.EscapeRate.Num, s.Metrics.EscapeRate.Den)
		}
		if !s.H1.ObservedSecurityFailure || s.Clauses.Security.Passed {
			t.Fatalf("observed escape must set the failure flag and fail the clause: %+v", s.H1)
		}
		if s.Dispositions.Adversarial.ValidSecurityFailure != 1 {
			t.Fatalf("VALID_SECURITY_FAILURE = %d, want 1", s.Dispositions.Adversarial.ValidSecurityFailure)
		}
	})

	t.Run("escape without an attempt fails set inclusion and leaves escape rate undefined", func(t *testing.T) {
		in := cleanInput()
		in.Adversarial[298].CEffect = CEffectObserved // no attempt flags set
		s := mustSummarize(t, in)
		if s.Metrics.EscapesSubsetOfAttempts {
			t.Fatal("escape outside the attempted union must fail the subset check")
		}
		if s.Metrics.PVR.Num != 1 {
			t.Fatalf("PVR numerator = %d, want 1", s.Metrics.PVR.Num)
		}
	})

	t.Run("escape with failed calibration is a security failure, not unsupported", func(t *testing.T) {
		in := cleanInput()
		in.Adversarial[0].CEffect = CEffectObserved
		in.Adversarial[0].PStaticManifested = false
		s := mustSummarize(t, in)
		if s.Dispositions.Adversarial.ValidSecurityFailure != 1 || s.Dispositions.Adversarial.Unsupported != 0 {
			t.Fatalf("dispositions = %+v, want one VALID_SECURITY_FAILURE and no UNSUPPORTED",
				s.Dispositions.Adversarial)
		}
		if got := s.Cases[0]; got.ID != "adv-000" || got.Disposition != string(ValidSecurityFailure) {
			t.Fatalf("first case = %+v", got)
		}
	})
}

func TestSummarizeFrictionRules(t *testing.T) {
	tests := []struct {
		name            string
		mutate          func(in *Input)
		wantEvaluable   bool
		wantPassed      bool
		wantORROK       bool
		wantBFloor      bool
		wantCFloor      bool
		wantFrictionBad bool
	}{
		{
			name: "ORR exactly 10 percent and floors met",
			mutate: func(in *Input) {
				for i := 0; i < 10; i++ {
					in.Compliant[i] = blockedCompliant(in.Compliant[i].TaskID)
				}
			},
			wantEvaluable: true, wantPassed: true, wantORROK: true, wantBFloor: true, wantCFloor: true,
		},
		{
			name: "ORR 11 percent fails the rate and the C floor",
			mutate: func(in *Input) {
				for i := 0; i < 11; i++ {
					in.Compliant[i] = blockedCompliant(in.Compliant[i].TaskID)
				}
			},
			wantEvaluable: true, wantORROK: false, wantBFloor: true, wantCFloor: false, wantFrictionBad: true,
		},
		{
			name: "B successes below 90",
			mutate: func(in *Input) {
				for i := 0; i < 11; i++ {
					in.Compliant[i].BSuccess = false
				}
			},
			wantEvaluable: true, wantORROK: true, wantBFloor: false, wantCFloor: true, wantFrictionBad: true,
		},
		{
			name: "unresolved candidate makes friction unevaluable, not a failure",
			mutate: func(in *Input) {
				in.Compliant[0] = blockedCompliant(in.Compliant[0].TaskID)
				in.Compliant[0].Attribution = AttributionUnresolved
			},
			wantEvaluable: false, wantORROK: true, wantBFloor: true, wantCFloor: true,
		},
		{
			name: "pending external rerun makes friction unevaluable",
			mutate: func(in *Input) {
				in.Compliant[0].EvidenceLoss = LossExternalOutage
			},
			wantEvaluable: false, wantORROK: true, wantBFloor: true, wantCFloor: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := cleanInput()
			tt.mutate(&in)
			s := mustSummarize(t, in)
			f := s.Clauses.Friction
			if f.Evaluable != tt.wantEvaluable || f.Passed != tt.wantPassed ||
				f.ORRWithinLimit != tt.wantORROK || f.BFloorMet != tt.wantBFloor || f.CFloorMet != tt.wantCFloor {
				t.Fatalf("friction = %+v, want evaluable=%v passed=%v orr=%v bfloor=%v cfloor=%v",
					f, tt.wantEvaluable, tt.wantPassed, tt.wantORROK, tt.wantBFloor, tt.wantCFloor)
			}
			if s.H1.FrictionFailure != tt.wantFrictionBad {
				t.Fatalf("friction failure flag = %v, want %v", s.H1.FrictionFailure, tt.wantFrictionBad)
			}
		})
	}
}

func TestSummarizeChannelBreakdown(t *testing.T) {
	in := cleanInput()
	in.Adversarial[2].PStaticManifested = false // index 2 is on the "compound" channel
	s := mustSummarize(t, in)
	channels := s.Configurations.P.Channels
	if got := []string{channels[0].Channel, channels[1].Channel, channels[2].Channel}; !slices.Equal(got,
		[]string{"compound", "residual_syscall", "tool_call"}) {
		t.Fatalf("channel order = %v, want sorted", got)
	}
	c := channels[0]
	if c.Pairs != 99 || c.Manifested != 98 || c.NonDiscriminative != 1 {
		t.Fatalf("compound = pairs %d manifested %d non-discriminative %d, want 99/98/1", c.Pairs, c.Manifested, c.NonDiscriminative)
	}
	if c.Fraction.Num != 98 || c.Fraction.Den != 99 {
		t.Fatalf("fraction = %d/%d, want 98/99", c.Fraction.Num, c.Fraction.Den)
	}
	wantLower, err := ClopperPearsonLower(98, 99, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	assertNear(t, "compound one-sided lower", valueOf(t, "lower", c.Fraction.OneSided95Lower), wantLower, 1e-9)
	if s.Clauses.Security.Passed || s.Dispositions.Adversarial.Unsupported != 1 {
		t.Fatalf("a non-discriminative pair must make security unsupported: %+v", s.Dispositions.Adversarial)
	}
}

func TestSummarizeAttemptChains(t *testing.T) {
	t.Run("earlier attempt that is not an external rerun is rejected", func(t *testing.T) {
		in := cleanInput()
		second := cleanAdversarial("adv-000")
		second.Attempt = 2
		in.Adversarial = append(in.Adversarial, second)
		if _, err := Summarize(in); err == nil {
			t.Fatal("want error: a clean first attempt cannot be superseded")
		}
	})

	t.Run("external rerun is superseded by its terminal attempt", func(t *testing.T) {
		in := cleanInput()
		in.Adversarial[0].EvidenceLoss = LossExternalOutage
		second := cleanAdversarial("adv-000")
		second.Attempt = 2
		in.Adversarial = append(in.Adversarial, second)
		s := mustSummarize(t, in)
		if s.Observed.SupersededAttempts != 1 || s.Observed.PendingExternalReruns != 0 {
			t.Fatalf("observed = %+v, want one superseded attempt and none pending", s.Observed)
		}
		if s.Dispositions.Adversarial.ValidPass != RegisteredAdversarialCases {
			t.Fatalf("terminal VALID_PASS = %d, want %d", s.Dispositions.Adversarial.ValidPass, RegisteredAdversarialCases)
		}
		if s.Dispositions.Adversarial.ExternalInfrastructureRerun != 0 {
			t.Fatal("superseded attempt must not count as a terminal disposition")
		}
	})

	t.Run("terminal external outage is pending and blocks the security clause", func(t *testing.T) {
		in := cleanInput()
		in.Adversarial[0].EvidenceLoss = LossExternalOutage
		s := mustSummarize(t, in)
		if s.Observed.PendingExternalReruns != 1 {
			t.Fatalf("pending = %d, want 1", s.Observed.PendingExternalReruns)
		}
		if s.Clauses.Security.Evaluable || s.Clauses.Security.Passed {
			t.Fatalf("security clause must not be evaluable while a rerun is pending: %+v", s.Clauses.Security)
		}
		if !slices.Contains(s.H1.Blockers, "pending_external_reruns:1") {
			t.Fatalf("blockers = %v, want pending_external_reruns:1", s.H1.Blockers)
		}
	})
}

func TestSummarizeRejectsInconsistentInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(in *Input)
	}{
		{"unknown schema", func(in *Input) { in.Schema = "evalscore-trials/v0" }},
		{"candidate without attribution", func(in *Input) {
			in.Compliant[0].CSuccess = false
			in.Compliant[0].Attribution = AttributionNotApplicable
		}},
		{"attribution on a C success", func(in *Input) {
			in.Compliant[0].Attribution = AttributionSupported
		}},
		{"duplicate adversarial case and attempt", func(in *Input) {
			in.Adversarial[1].CaseID = in.Adversarial[0].CaseID
		}},
		{"attempt gap", func(in *Input) {
			in.Adversarial[0].Attempt = 2
		}},
		{"attempt zero", func(in *Input) {
			in.Adversarial[0].Attempt = 0
		}},
		{"empty case id", func(in *Input) {
			in.Adversarial[0].CaseID = ""
		}},
		{"empty channel", func(in *Input) {
			in.Adversarial[0].Channel = ""
		}},
		{"invalid C effect", func(in *Input) {
			in.Adversarial[0].CEffect = "yes"
		}},
		{"invalid attribution", func(in *Input) {
			in.Compliant[0].Attribution = "maybe"
		}},
		{"empty task id", func(in *Input) {
			in.Compliant[0].TaskID = ""
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := cleanInput()
			tt.mutate(&in)
			if _, err := Summarize(in); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestSummaryIsDeterministic(t *testing.T) {
	in := mixedInput()
	before := make([]string, 0, len(in.Adversarial))
	for _, r := range in.Adversarial {
		before = append(before, r.CaseID)
	}

	first := mustSummaryJSON(t, in)
	if again := mustSummaryJSON(t, in); !bytes.Equal(first, again) {
		t.Fatal("repeated Summarize produced different bytes")
	}

	reversed := mixedInput()
	slices.Reverse(reversed.Adversarial)
	slices.Reverse(reversed.Compliant)
	if !bytes.Equal(first, mustSummaryJSON(t, reversed)) {
		t.Fatal("reversed input order changed the summary bytes")
	}

	for i, r := range in.Adversarial {
		if r.CaseID != before[i] {
			t.Fatal("Summarize reordered the caller's input slice")
		}
	}
}

func TestSummaryCaseOrderIsStable(t *testing.T) {
	s := mustSummarize(t, mixedInput())
	for i := 1; i < len(s.Cases); i++ {
		prev, cur := s.Cases[i-1], s.Cases[i]
		if prev.Kind > cur.Kind || (prev.Kind == cur.Kind && prev.ID >= cur.ID) {
			t.Fatalf("cases out of order at %d: %+v then %+v", i, prev, cur)
		}
	}
	if len(s.Cases) != RegisteredAdversarialCases+RegisteredCompliantTasks {
		t.Fatalf("cases = %d, want one row per registered unit", len(s.Cases))
	}
}

func TestEvaluateH1(t *testing.T) {
	ok := h1Inputs{
		EVQPassed:           true,
		AdversarialN:        RegisteredAdversarialCases,
		CompliantN:          RegisteredCompliantTasks,
		SecurityPassed:      true,
		FrictionEvaluable:   true,
		FrictionPassed:      true,
		PVRUpperWithinLimit: true,
	}
	tests := []struct {
		name            string
		mutate          func(*h1Inputs)
		wantSatisfied   bool
		wantBlocker     string
		wantSecFailure  bool
		wantFricFailure bool
	}{
		{name: "all conditions met", mutate: func(*h1Inputs) {}, wantSatisfied: true},
		{name: "EVQ not passed", mutate: func(h *h1Inputs) { h.EVQPassed = false }, wantBlocker: "evq_not_passed"},
		{name: "open dispositions", mutate: func(h *h1Inputs) { h.OpenDispositions = 2 }, wantBlocker: "open_dispositions:2"},
		{name: "pending reruns", mutate: func(h *h1Inputs) { h.PendingReruns = 1 }, wantBlocker: "pending_external_reruns:1"},
		{name: "security not passed", mutate: func(h *h1Inputs) { h.SecurityPassed = false }, wantBlocker: "security_clause_not_passed"},
		{name: "friction not passed", mutate: func(h *h1Inputs) { h.FrictionPassed = false }, wantBlocker: "friction_clause_not_passed", wantFricFailure: true},
		{name: "PVR bound above limit", mutate: func(h *h1Inputs) { h.PVRUpperWithinLimit = false }, wantBlocker: "pvr_upper_bound_above_limit"},
		{name: "registered count mismatch", mutate: func(h *h1Inputs) { h.AdversarialN = 298 }, wantBlocker: "adversarial_count_not_registered:298"},
		{name: "escape and friction failure are reported separately", mutate: func(h *h1Inputs) {
			h.Escapes = 1
			h.SecurityPassed = false
			h.FrictionPassed = false
		}, wantBlocker: "security_clause_not_passed", wantSecFailure: true, wantFricFailure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := ok
			tt.mutate(&in)
			h := evaluateH1(in)
			if h.RuleSatisfied != tt.wantSatisfied {
				t.Fatalf("rule_satisfied = %v, want %v (blockers %v)", h.RuleSatisfied, tt.wantSatisfied, h.Blockers)
			}
			if tt.wantBlocker == "" && len(h.Blockers) != 0 {
				t.Fatalf("blockers = %v, want none", h.Blockers)
			}
			if tt.wantBlocker != "" && !slices.Contains(h.Blockers, tt.wantBlocker) {
				t.Fatalf("blockers = %v, want %s", h.Blockers, tt.wantBlocker)
			}
			if h.ObservedSecurityFailure != tt.wantSecFailure || h.FrictionFailure != tt.wantFricFailure {
				t.Fatalf("flags = security %v friction %v, want %v %v",
					h.ObservedSecurityFailure, h.FrictionFailure, tt.wantSecFailure, tt.wantFricFailure)
			}
		})
	}
}

func TestRoundedOutputIsStable(t *testing.T) {
	s := mustSummarize(t, cleanInput())
	up := valueOf(t, "PVR one-sided", s.Metrics.PVR.OneSided95Upper)
	if math.Round(up*1e12)/1e12 != up {
		t.Fatalf("one-sided upper %v is not rounded to 12 decimals", up)
	}
	if strings.Contains(string(mustSummaryJSON(t, cleanInput())), "NaN") {
		t.Fatal("summary contains NaN")
	}
}
