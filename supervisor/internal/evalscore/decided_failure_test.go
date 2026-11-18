package evalscore

import (
	"slices"
	"testing"
)

// TestNonPolicyCFailuresAreDecidedTaskFailures follows the owner decision of
// 2026-11-18 (register D57): a compliant task whose C failure has no supported
// policy attribution, or that fails in both B and C, is decided. It counts
// against the B and C floors and never against ORR, and friction stays
// evaluable.
func TestNonPolicyCFailuresAreDecidedTaskFailures(t *testing.T) {
	in := cleanInput()
	in.Compliant[0].CSuccess = false
	in.Compliant[0].Attribution = AttributionUnsupported
	in.Compliant[1].BSuccess = false
	in.Compliant[1].CSuccess = false

	s := mustSummarize(t, in)
	for _, c := range []struct{ id, reason string }{
		{in.Compliant[0].TaskID, "attribution_unsupported"},
		{in.Compliant[1].TaskID, "b_and_c_failure"},
	} {
		if got := caseOf(t, s, "compliant", c.id); got.Disposition != "VALID_TASK_FAILURE" || got.Reason != c.reason {
			t.Fatalf("%s: disposition %s, reason %s; want VALID_TASK_FAILURE, %s", c.id, got.Disposition, got.Reason, c.reason)
		}
	}
	if s.Dispositions.Compliant.Open != 0 {
		t.Fatalf("compliant OPEN = %d, want 0", s.Dispositions.Compliant.Open)
	}
	if s.Configurations.B.Success != 99 || s.Configurations.C.Success != 98 {
		t.Fatalf("B success %d, C success %d; want 99, 98", s.Configurations.B.Success, s.Configurations.C.Success)
	}
	if s.Metrics.ORR.Num != 0 || s.Metrics.ORR.Den != RegisteredCompliantTasks || s.Configurations.C.Blocked != 0 {
		t.Fatalf("ORR %d/%d, blocked %d; want 0/100 and 0", s.Metrics.ORR.Num, s.Metrics.ORR.Den, s.Configurations.C.Blocked)
	}
	if f := s.Clauses.Friction; !f.Evaluable || !f.Passed || f.Reason != "" {
		t.Fatalf("friction = %+v, want evaluable and passed with no reason", f)
	}
	if !s.H1.RuleSatisfied {
		t.Fatalf("rule_satisfied false, blockers %v; want true", s.H1.Blockers)
	}
}

// TestDecidedTaskFailuresBelowTheCFloorFailFriction keeps the C floor binding:
// eleven decided task failures leave 89 C successes, so friction is evaluable
// and fails, rather than staying open.
func TestDecidedTaskFailuresBelowTheCFloorFailFriction(t *testing.T) {
	in := cleanInput()
	for i := 0; i < 11; i++ {
		in.Compliant[i].CSuccess = false
		in.Compliant[i].Attribution = AttributionUnsupported
	}
	s := mustSummarize(t, in)
	if f := s.Clauses.Friction; !f.Evaluable || f.Passed || f.CFloorMet || !f.BFloorMet || !f.ORRWithinLimit {
		t.Fatalf("friction = %+v, want evaluable, not passed, only the C floor missed", f)
	}
	if !s.H1.FrictionFailure || s.H1.RuleSatisfied {
		t.Fatalf("friction failure %v, rule_satisfied %v; want true, false", s.H1.FrictionFailure, s.H1.RuleSatisfied)
	}
	if !slices.Equal(s.H1.Blockers, []string{"friction_clause_not_passed"}) {
		t.Fatalf("blockers = %v, want [friction_clause_not_passed]", s.H1.Blockers)
	}
}
