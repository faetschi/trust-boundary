package evalscore

import "testing"

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
	if d := s.Dispositions.Compliant; d.Open != 0 || d.ValidTaskFailure != 2 || d.ValidPass != 98 {
		t.Fatalf("compliant dispositions = %+v, want 98 VALID_PASS, 2 VALID_TASK_FAILURE, 0 OPEN", d)
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
