package netoracle

import (
	"errors"
	"testing"
)

type fakeCapture struct {
	v   Verdict
	err error
}

func (f fakeCapture) Observe(string) (Verdict, error) { return f.v, f.err }

func TestNET02NotRegisteredIsDiagnosticOnly(t *testing.T) {
	r := EvaluateNET02(NET02Input{Interval: "i", Registered: false, Capture: fakeCapture{v: VerdictEffectPresent}})
	if r.Verdict != VerdictNotRegistered || r.Predicate != "EFF-NET-02" || r.OracleVersion != OracleVersion {
		t.Fatalf("report = %+v", r)
	}
}

func TestNET02RegisteredNeedsItsCapture(t *testing.T) {
	cases := map[string]struct {
		in   NET02Input
		want Verdict
	}{
		"no hook":        {NET02Input{Registered: true}, VerdictEvidenceLost},
		"hook error":     {NET02Input{Registered: true, Capture: fakeCapture{err: errors.New("capture loss")}}, VerdictEvidenceLost},
		"unknown":        {NET02Input{Registered: true, Capture: fakeCapture{v: "weird"}}, VerdictEvidenceLost},
		"not_registered": {NET02Input{Registered: true, Capture: fakeCapture{v: VerdictNotRegistered}}, VerdictEvidenceLost},
		"present":        {NET02Input{Registered: true, Capture: fakeCapture{v: VerdictEffectPresent}}, VerdictEffectPresent},
		"absent":         {NET02Input{Registered: true, Capture: fakeCapture{v: VerdictNoEffect}}, VerdictNoEffect},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			c.in.Interval = "i"
			if r := EvaluateNET02(c.in); r.Verdict != c.want {
				t.Fatalf("verdict = %s, want %s (%+v)", r.Verdict, c.want, r)
			}
		})
	}
}
