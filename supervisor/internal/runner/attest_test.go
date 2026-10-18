package runner

import (
	"context"
	"errors"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
)

var attestPins = ReceiptPins{
	ProfileHash:              "sha256:" + hexOf('1'),
	ContainmentProfileDigest: "sha256:" + hexOf('2'),
	FrozenSourceDigest:       "sha256:" + hexOf('3'),
}

func hexOf(c byte) string {
	b := make([]byte, 64)
	for i := range b {
		b[i] = c
	}
	return string(b)
}

// stubInner returns a fixed outcome and error and records its calls.
type stubInner struct {
	out     Outcome
	err     error
	prepErr error
	prepped int
	ran     int
}

func (s *stubInner) Prepare(context.Context, Step) error { s.prepped++; return s.prepErr }
func (s *stubInner) Execute(context.Context, Step) (Outcome, error) {
	s.ran++
	return s.out, s.err
}

func matching() *ReceiptFacts {
	return &ReceiptFacts{ProfileHash: attestPins.ProfileHash,
		ContainmentProfileDigest: attestPins.ContainmentProfileDigest, FrozenSourceDigest: attestPins.FrozenSourceDigest,
		ProviderMode: "model"}
}

// The expected digests newAttest gives for the known cases.
var (
	testScriptDigest = "sha256:" + hexOf('s')
	testPolicyC      = "sha256:" + hexOf('c')
	testPolicyP      = "sha256:" + hexOf('p')
	testAttacker     = "sha256:" + hexOf('a')
	testPivots       = "sha256:" + hexOf('v')
)

// matchingFor is the receipt a correct service returns for the step: a model run says only "model", a
// scripted replay carries its script digest and the admission policy digest of its configuration.
func matchingFor(s Step) *ReceiptFacts {
	r := matching()
	if s.Role == RoleStaticReplay || s.Role == RoleAdaptiveReplay {
		r.ProviderMode, r.ScriptDigest, r.AdmissionPolicyDigest = "scripted", testScriptDigest, testPolicyC
		if s.Configuration == CfgP {
			r.AdmissionPolicyDigest, r.Delta = testPolicyP, "net-sink/v1" // the registered p_delta of adv-p
		}
		if s.Role == RoleAdaptiveReplay {
			// the machine chose the turns: no script digest, the attacker run instead
			r.ScriptDigest, r.AttackerDigest, r.PivotsDigest = "", testAttacker, testPivots
			r.PivotsIssued, r.AttackerStop, r.AttackerStateDigest = 2, "success_pivot", hexOf('d')
		}
	}
	return r
}

func newAttest(inner Executor) Attest {
	return Attest{
		Inner: inner, Pins: attestPins, EVQ: []string{"EVQ-1", "EVQ-2"},
		EVQResults: EVQResults{{ID: "EVQ-1", Passed: true, EvidenceRef: "ref-1"}, {ID: "EVQ-2", Passed: true, EvidenceRef: "ref-2"}},
		Route:      RouteCoverage{Complete: true, Digest: "sha256:" + hexOf('r')},
		PDelta: func(id string) (string, bool) {
			return map[string]string{"adv-p": "net-sink/v1", "adv-none": ""}[id], id == "adv-p" || id == "adv-none"
		},
		ScriptDigest:   func(id string) (string, bool) { return testScriptDigest, id == "adv-p" || id == "adv-none" },
		AttackerDigest: testAttacker,
		PivotsDigest:   func(id string) (string, bool) { return testPivots, id == "adv-p" || id == "adv-none" },
		AdmissionPolicyDigest: func(id string, cfg Configuration) (string, bool) {
			if cfg == CfgP {
				return testPolicyP, id == "adv-p" || id == "adv-none"
			}
			return testPolicyC, id == "adv-p" || id == "adv-none"
		},
	}
}

func advStep(cfg Configuration, role Role) Step {
	return Step{CaseID: "adv-p", Kind: evalcorpus.KindAdversarial, Configuration: cfg, Role: role, Attempt: 1,
		Predicate: "EFF-WS-01", ThreatClass: evalcorpus.ClassTMA}
}

func TestAttestFillsWhatGovernedCLeavesEmpty(t *testing.T) {
	a := newAttest(nil)
	for _, tc := range []struct {
		role    Role
		matched bool
	}{{RoleTrajectory, false}, {RoleStaticReplay, true}, {RoleAdaptiveReplay, true}, {RoleVarianceRepeat, false}} {
		step := advStep(CfgC, tc.role)
		a.Inner = &stubInner{out: Outcome{E01: E01Identity{Digest: "sha256:e01"}, Receipt: matchingFor(step)}}
		out, err := a.Execute(context.Background(), step)
		if err != nil {
			t.Fatalf("%s: %v", tc.role, err)
		}
		if out.Channel != "EFF-WS-01" || !out.E01.Verified || out.E01.Digest != "sha256:e01" {
			t.Fatalf("%s: channel %q e01 %+v", tc.role, out.Channel, out.E01)
		}
		if out.Route != a.Route {
			t.Fatalf("%s: route %+v", tc.role, out.Route)
		}
		if len(out.EVQ) != 2 || out.EVQ[0] != (Obligation{ID: "EVQ-1", Passed: true, EvidenceRef: "ref-1"}) || out.EVQ[1] != (Obligation{ID: "EVQ-2", Passed: true, EvidenceRef: "ref-2"}) {
			t.Fatalf("%s: evq %+v", tc.role, out.EVQ)
		}
		if out.MatchedDelivery != tc.matched {
			t.Fatalf("%s: matched delivery %t", tc.role, out.MatchedDelivery)
		}
	}
}

func TestAttestVerifiesOnlyOnEqualPins(t *testing.T) {
	for name, mutate := range map[string]func(*ReceiptFacts){
		"profile":     func(r *ReceiptFacts) { r.ProfileHash = "sha256:" + hexOf('9') },
		"containment": func(r *ReceiptFacts) { r.ContainmentProfileDigest = "" },
		"source":      func(r *ReceiptFacts) { r.FrozenSourceDigest += "0" },
	} {
		r := matchingFor(advStep(CfgC, RoleStaticReplay))
		mutate(r)
		in := &stubInner{out: Outcome{E01: E01Identity{Digest: "d"}, Receipt: r}}
		out, err := newAttest(in).Execute(context.Background(), advStep(CfgC, RoleStaticReplay))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out.E01.Verified || out.MatchedDelivery {
			t.Fatalf("%s: verified %t matched %t on a pin mismatch", name, out.E01.Verified, out.MatchedDelivery)
		}
		// Receipt identity establishes E01 only: EVQ follows the pinned results.
		if len(out.EVQ) != 2 || !out.EVQ[0].Passed || !out.EVQ[1].Passed {
			t.Fatalf("%s: an unverified identity changed the EVQ results: %+v", name, out.EVQ)
		}
	}
	// No receipt facts at all: nothing to compare, nothing verified.
	out, err := newAttest(&stubInner{out: Outcome{E01: E01Identity{Digest: "d"}}}).Execute(context.Background(), advStep(CfgC, RoleTrajectory))
	if err != nil || out.E01.Verified {
		t.Fatalf("no receipt: verified %t err %v", out.E01.Verified, err)
	}
}

func TestAttestRefusesACReceiptWithADelta(t *testing.T) {
	for _, delta := range []string{"admit-all/v1", "net-sink/v1", "no-reap/v1", "unknown"} {
		r := matchingFor(advStep(CfgC, RoleStaticReplay))
		r.Delta = delta
		out, err := newAttest(&stubInner{out: Outcome{Receipt: r, E01: E01Identity{Digest: "d"}}}).Execute(context.Background(), advStep(CfgC, RoleStaticReplay))
		if !errors.Is(err, ErrReceiptPolicy) {
			t.Fatalf("delta %q: error %v", delta, err)
		}
		if out.E01.Verified || out.MatchedDelivery || !hasOutcome(out) {
			t.Fatalf("delta %q: a refused receipt must keep the outcome unverified: %+v", delta, out)
		}
	}
	for _, delta := range []string{"", "none"} {
		r := matchingFor(advStep(CfgC, RoleStaticReplay))
		r.Delta = delta
		if _, err := newAttest(&stubInner{out: Outcome{Receipt: r, E01: E01Identity{Digest: "d"}}}).Execute(context.Background(), advStep(CfgC, RoleStaticReplay)); err != nil {
			t.Fatalf("delta %q: %v", delta, err)
		}
	}
}

func TestAttestPReceiptDeltaMustEqualTheManifest(t *testing.T) {
	run := func(a Attest, caseID, delta string) (Outcome, error) {
		st := advStep(CfgP, RoleStaticReplay)
		st.CaseID = caseID
		r := matchingFor(st)
		r.Delta = delta
		a.Inner = &stubInner{out: Outcome{Receipt: r, E01: E01Identity{Digest: "d"}}}
		return a.Execute(context.Background(), st)
	}
	a := newAttest(nil)
	if out, err := run(a, "adv-p", "net-sink/v1"); err != nil || !out.E01.Verified || !out.MatchedDelivery {
		t.Fatalf("equal delta: %+v %v", out, err)
	}
	if _, err := run(a, "adv-p", "no-reap/v1"); !errors.Is(err, ErrReceiptPolicy) {
		t.Fatalf("different delta: %v", err)
	}
	if _, err := run(a, "adv-p", ""); !errors.Is(err, ErrReceiptPolicy) {
		t.Fatalf("missing delta where the manifest has one: %v", err)
	}
	// The manifest's empty p_delta is "none" and equals an empty or none receipt delta.
	if _, err := run(a, "adv-none", "none"); err != nil {
		t.Fatalf("none vs empty: %v", err)
	}
	if _, err := run(a, "adv-none", "admit-all/v1"); !errors.Is(err, ErrReceiptPolicy) {
		t.Fatalf("receipt delta where the manifest has none: %v", err)
	}
	// An unknown case, or no manifest source at all, fails closed.
	if _, err := run(a, "adv-x", "none"); !errors.Is(err, ErrReceiptPolicy) {
		t.Fatalf("unknown case: %v", err)
	}
	a.PDelta = nil
	if _, err := run(a, "adv-p", "net-sink/v1"); !errors.Is(err, ErrReceiptPolicy) {
		t.Fatalf("no p_delta source: %v", err)
	}
}

func TestAttestLeavesASetupFailureAlone(t *testing.T) {
	boom := errors.New("boom")
	in := &stubInner{err: boom}
	out, err := newAttest(in).Execute(context.Background(), advStep(CfgC, RoleTrajectory))
	if !errors.Is(err, boom) || hasOutcome(out) {
		t.Fatalf("a setup failure must stay an empty outcome with its error: %+v %v", out, err)
	}
}

func TestAttestKeepsAPartialOutcomeAndItsError(t *testing.T) {
	boom := errors.New("boom")
	in := &stubInner{out: Outcome{Receipt: matching(), E01: E01Identity{Digest: "d"}}, err: boom}
	out, err := newAttest(in).Execute(context.Background(), advStep(CfgC, RoleTrajectory))
	if !errors.Is(err, boom) || !out.E01.Verified || out.Channel != "EFF-WS-01" {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestAttestNonAdversarialStepHasNoChannel(t *testing.T) {
	in := &stubInner{out: Outcome{Receipt: matching(), E01: E01Identity{Digest: "d"}}}
	st := Step{CaseID: "cmp-1", Kind: evalcorpus.KindCompliant, Configuration: CfgC, Role: RoleCompliant, Attempt: 1}
	out, err := newAttest(in).Execute(context.Background(), st)
	if err != nil || out.Channel != "" || out.MatchedDelivery || !out.E01.Verified {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestAttestPrepare(t *testing.T) {
	in := &stubInner{}
	a := newAttest(in)
	if err := a.Prepare(context.Background(), advStep(CfgC, RoleTrajectory)); err != nil || in.prepped != 1 {
		t.Fatalf("prepare %v (%d)", err, in.prepped)
	}
	in.prepErr = ErrNotWired
	if err := a.Prepare(context.Background(), advStep(CfgC, RoleTrajectory)); !errors.Is(err, ErrNotWired) {
		t.Fatalf("inner refusal must pass through: %v", err)
	}
	for name, bad := range map[string]Attest{
		"no pins":       {Inner: in, EVQ: []string{"E"}},
		"no obligation": {Inner: in, Pins: attestPins},
		"no inner":      {Pins: attestPins, EVQ: []string{"E"}},
	} {
		err := bad.Prepare(context.Background(), advStep(CfgC, RoleTrajectory))
		if name == "no inner" {
			if !errors.Is(err, ErrNotWired) {
				t.Fatalf("%s: %v", name, err)
			}
			continue
		}
		if !errors.Is(err, ErrSetupRefused) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// A predicate-less adversarial step has no channel: refused in Prepare, not scored later.
	st := advStep(CfgC, RoleTrajectory)
	st.Predicate = ""
	in.prepErr = nil
	if err := a.Prepare(context.Background(), st); !errors.Is(err, ErrSetupRefused) {
		t.Fatalf("no predicate: %v", err)
	}
}
