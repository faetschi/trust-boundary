package runner

import (
	"errors"
	"testing"

	"tbound/supervisor/internal/effectoracle"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/wsoracle"
)

// netProcKeys are the Outcome.Oracles keys the netoracle and procoracle packages supply.
var netProcKeys = []string{OracleNet01, OracleNet02, OracleProc01, OracleProc02, OracleProc03}

// adversarialHook applies f to the outcome of every adversarial step.
func adversarialHook(f func(o *Outcome)) func(Step, *Outcome) error {
	return func(s Step, o *Outcome) error {
		if s.Kind == evalcorpus.KindAdversarial {
			f(o)
		}
		return nil
	}
}

func TestNetFindingWithoutARefFailsExport(t *testing.T) {
	seen := 0
	err := exportWith(t, adversarialHook(func(o *Outcome) {
		if o.Oracles[OracleNet01] != "" {
			seen++
			delete(o.OracleRefs, OracleNet01)
		}
	}))
	if seen == 0 {
		t.Fatal("no NET-01 finding was exercised")
	}
	if !errors.Is(err, ErrOracleVersion) {
		t.Errorf("Export err = %v, want ErrOracleVersion", err)
	}
}

func TestNetAndProcFindingsNeedTheirOwnOracleVersion(t *testing.T) {
	for _, key := range []string{OracleNet01, OracleProc01, OracleProc03} {
		for name, bad := range map[string]OracleRef{
			"old version":        {Version: "procoracle/v0", Report: "r"},
			"wsoracle version":   {Version: wsoracle.OracleVersion, Report: "r"},
			"mixed":              {Version: "mixed", Report: "r"},
			"empty report":       {Version: expectedVersion(t, key), Report: ""},
			"empty version":      {Version: "", Report: "r"},
			"other instrument's": {Version: expectedVersion(t, otherInstrumentKey(key)), Report: "r"},
		} {
			seen := 0
			err := exportWith(t, adversarialHook(func(o *Outcome) {
				if o.Oracles[key] != "" {
					seen++
					o.OracleRefs[key] = bad
				}
			}))
			if seen == 0 {
				t.Fatalf("%s: no %s finding was exercised", name, key)
			}
			if !errors.Is(err, ErrOracleVersion) {
				t.Errorf("%s with %s: Export err = %v, want ErrOracleVersion", key, name, err)
			}
		}
	}
}

func TestEveryNetAndProcFindingKindNeedsARef(t *testing.T) {
	for _, f := range []Finding{FindingObserved, FindingNone, FindingIndeterminate} {
		err := exportWith(t, adversarialHook(func(o *Outcome) {
			if o.Oracles[OracleNet01] != "" {
				o.Oracles[OracleNet01] = f
				delete(o.OracleRefs, OracleNet01)
			}
		}))
		if !errors.Is(err, ErrOracleVersion) {
			t.Errorf("%s without a ref: Export err = %v, want ErrOracleVersion", f, err)
		}
	}
}

func TestNetAndProcFindingsWithValidRefsExport(t *testing.T) {
	checked := 0
	err := exportWith(t, adversarialHook(func(o *Outcome) {
		for _, k := range netProcKeys {
			if o.Oracles[k] == "" {
				continue
			}
			checked++
			if ref := o.OracleRefs[k]; ref.Report == "" || ref.Version != expectedVersion(t, k) {
				t.Errorf("%s: default ref %+v", k, ref)
			}
		}
	}))
	if checked == 0 {
		t.Fatal("no NET or PROC finding was checked")
	}
	if err != nil {
		t.Errorf("Export with valid refs: %v", err)
	}
}

func TestNetAndProcRefsAreVersionedPerKey(t *testing.T) {
	for k, want := range map[string]string{
		OracleWS01: wsoracle.OracleVersion, OracleWS: wsoracle.OracleVersion,
		OracleNet01: NetOracleVersion, OracleNet02: NetOracleVersion,
		OracleProc01: ProcOracleVersion, OracleProc02: ProcOracleVersion, OracleProc03: ProcOracleVersion,
		OracleTransient: effectoracle.OracleVersion,
	} {
		if got, ok := oracleVersionFor(k); !ok || got != want {
			t.Errorf("oracleVersionFor(%s) = %q, %v; want %q", k, got, ok, want)
		}
	}
}

func TestMandatoryNoneWithoutARefLeavesCoverageIncomplete(t *testing.T) {
	cfg := testConfig()
	cfg.NET02Mandatory = true
	for _, tc := range []struct {
		name  string
		class evalcorpus.ThreatClass
		key   string
	}{
		{"NET-01", evalcorpus.ClassTMA, OracleNet01},
		{"NET-02", evalcorpus.ClassTMB, OracleNet02},
		{"PROC-02", evalcorpus.ClassTMC, OracleProc02},
	} {
		for name, mut := range map[string]func(o *Outcome){
			"no ref":        func(o *Outcome) { delete(o.OracleRefs, tc.key) },
			"wrong version": func(o *Outcome) { o.OracleRefs[tc.key] = OracleRef{Version: wsoracle.OracleVersion, Report: "r"} },
			"empty report":  func(o *Outcome) { o.OracleRefs[tc.key] = OracleRef{Version: expectedVersion(t, tc.key)} },
		} {
			v := goodAdv(cfg, tc.class)
			if v.Trajectory.Oracles[tc.key] != FindingNone {
				t.Fatalf("%s: fixture has %q", tc.name, v.Trajectory.Oracles[tc.key])
			}
			mut(v.Trajectory)
			r := cfg.AssembleAdversarial(v).Record
			if r.ObserverCoverageComplete || r.CEffect != evalscore.CEffectUnknown || r.EvidenceLoss == evalscore.LossNone {
				t.Errorf("%s %s: none counted: coverage=%v c_effect=%q loss=%q", tc.name, name, r.ObserverCoverageComplete, r.CEffect, r.EvidenceLoss)
			}
		}
		// Control: the same none with its ref is complete.
		v := goodAdv(cfg, tc.class)
		if r := cfg.AssembleAdversarial(v).Record; !r.ObserverCoverageComplete || r.CEffect != evalscore.CEffectNone {
			t.Errorf("%s control: coverage=%v c_effect=%q", tc.name, r.ObserverCoverageComplete, r.CEffect)
		}
	}
}

func expectedVersion(t *testing.T, key string) string {
	t.Helper()
	v, ok := oracleVersionFor(key)
	if !ok {
		t.Fatalf("no expected version for %s", key)
	}
	return v
}

func otherInstrumentKey(key string) string {
	switch key {
	case OracleNet01, OracleNet02:
		return OracleProc01
	}
	return OracleNet01
}

// withObserverRefs gives every NET and PROC finding of o a valid reference, as the
// Record helpers of netoracle and procoracle do for a real report.
func withObserverRefs(o *Outcome) *Outcome {
	o.OracleRefs = map[string]OracleRef{}
	for _, k := range observerKeys {
		if o.Oracles[k] != "" {
			v, _ := oracleVersionFor(k)
			o.OracleRefs[k] = OracleRef{Version: v, Report: "synthetic-report-" + k}
		}
	}
	return o
}
