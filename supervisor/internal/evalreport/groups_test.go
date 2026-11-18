package evalreport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
)

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// groupInputs is a thesis corpus under the main-draw test vector (evalcorpus testdata/d48, select.py over ledger v3
// with all-pass decisions): the 299 main cases and 5 top-ups by their ledger roots, 100 compliant tasks, all clean,
// with the real ledger v3 and a select.py certify output for the vector's sizes.
func groupInputs(t *testing.T) (Inputs, evalcorpus.Draw) {
	t.Helper()
	raw := readTestFile(t, "../evalcorpus/testdata/d48/draw-output.json")
	d, err := evalcorpus.ParseDraw(raw)
	if err != nil {
		t.Fatal(err)
	}
	in := Inputs{
		Manifest: evalcorpus.Manifest{SchemaVersion: evalcorpus.SchemaVersion, SplitSeed: "synthetic-seed", Pins: evalcorpus.Pins{DrawDigest: d.Digest}},
		Trials:   evalscore.Input{Schema: evalscore.SchemaVersion, EVQPassed: true},
		Ledger:   okLedger(),
		D48: &D48Inputs{Draw: raw, Ledger: readTestFile(t, "../../../corpus/sampling/d48-ledger-v3.jsonl"),
			Certification: readTestFile(t, "testdata/certify-output.json")},
	}
	for _, class := range []evalcorpus.ThreatClass{evalcorpus.ClassTMA, evalcorpus.ClassTMB} {
		for _, part := range []evalcorpus.SamplePart{evalcorpus.SampleMain, evalcorpus.SampleTopup} {
			ids := d.Members[class]
			if part == evalcorpus.SampleTopup {
				ids = d.Topup[class]
			}
			for _, root := range ids {
				c := advCase("case-"+root, class, evalcorpus.PartitionNonCold)
				c.Adversarial.RootScenarioID, c.Adversarial.SamplePart = root, part
				in.Manifest.Cases = append(in.Manifest.Cases, c)
				in.Trials.Adversarial = append(in.Trials.Adversarial, cleanAdv(c.CaseID))
			}
		}
	}
	for i := 0; i < evalscore.RegisteredCompliantTasks; i++ {
		id := fmt.Sprintf("task-%03d", i)
		in.Manifest.Cases = append(in.Manifest.Cases, cmpCase(id, []evalcorpus.Tool{evalcorpus.ToolRead, evalcorpus.ToolWrite, evalcorpus.ToolEdit, evalcorpus.ToolBash}[i%4]))
		in.Trials.Compliant = append(in.Trials.Compliant, cleanCmp(id))
	}
	return in, d
}

// ledgerKeys reads the attack and mechanism of one ledger v3 row.
func ledgerKeys(t *testing.T, ledger []byte, caseID string) (attack, mechanism string) {
	t.Helper()
	for _, line := range bytes.Split(ledger, []byte("\n")) {
		var row struct {
			CaseID      string `json:"case_id"`
			AttackID    string `json:"attack_id"`
			MechanismID string `json:"mechanism_id"`
		}
		if json.Unmarshal(line, &row) == nil && row.CaseID == caseID {
			return row.AttackID, row.MechanismID
		}
	}
	t.Fatalf("no ledger row %q", caseID)
	return "", ""
}

func level(t *testing.T, g *Groups, unit, stratum string) GroupLevel {
	t.Helper()
	for _, l := range g.Levels {
		if l.Unit == unit && l.Stratum == stratum {
			return l
		}
	}
	t.Fatalf("no %s level %s", unit, stratum)
	return GroupLevel{}
}

func group(t *testing.T, l GroupLevel, key string) Group {
	t.Helper()
	for _, g := range l.Groups {
		if g.Key == key {
			return g
		}
	}
	t.Fatalf("%s %s: no group %q", l.Unit, l.Stratum, key)
	return Group{}
}

func bound(l GroupLevel, split string) GroupBound {
	for _, b := range l.Bounds {
		if b.Split == split {
			return b
		}
	}
	return GroupBound{}
}

func escape(in *Inputs, caseID string) {
	for i := range in.Trials.Adversarial {
		if in.Trials.Adversarial[i].CaseID == caseID {
			in.Trials.Adversarial[i].CEffect = evalscore.CEffectObserved
		}
	}
}

// One main escape and one top-up escape show under their scenario and mechanism, per stratum and in total; the
// top-up escape stays out of the H1 figures, and every group is split by development as the draw marks it.
func TestGroupsCountEscapesPerScenarioAndMechanism(t *testing.T) {
	in, d := groupInputs(t)
	mainRoot, topupRoot := d.Members[evalcorpus.ClassTMA][0], d.Topup[evalcorpus.ClassTMB][0]
	escape(&in, "case-"+mainRoot)
	escape(&in, "case-"+topupRoot)
	r := mustBuild(t, in)
	if r.Groups == nil {
		t.Fatal("no groups")
	}
	if r.Configurations.C.Escapes != 1 || r.Metrics.PVR.Num != 1 {
		t.Fatalf("H1 figures: %d escapes, PVR %d; want the main escape only", r.Configurations.C.Escapes, r.Metrics.PVR.Num)
	}

	mainAttack, mainMech := ledgerKeys(t, in.D48.Ledger, mainRoot)
	topAttack, topMech := ledgerKeys(t, in.D48.Ledger, topupRoot)
	for _, tc := range []struct {
		unit, stratum, key string
		mainEsc, topEsc    int
	}{
		{UnitScenario, "TM-A", mainAttack, 1, 0}, {UnitMechanism, "TM-A", mainMech, 1, 0},
		{UnitScenario, "TM-B", topAttack, 0, 1}, {UnitMechanism, "TM-B", topMech, 0, 1},
		{UnitScenario, StratumTotal, mainAttack, 1, 0}, {UnitScenario, StratumTotal, topAttack, 0, 1},
	} {
		g := group(t, level(t, r.Groups, tc.unit, tc.stratum), tc.key)
		if g.MainEscapes < tc.mainEsc || g.TopupEscapes < tc.topEsc || g.MainTested+g.TopupTested == 0 {
			t.Errorf("%s %s %s: %+v, want main escapes >= %d and top-up escapes >= %d", tc.unit, tc.stratum, tc.key, g, tc.mainEsc, tc.topEsc)
		}
	}

	var marks drawMarks
	if err := json.Unmarshal(in.D48.Draw, &marks); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, l := range r.Groups.Levels {
		all, dev, unseen := bound(l, SplitAll), bound(l, SplitDevelopment), bound(l, SplitUnseen)
		if dev.Units+unseen.Units != all.Units || dev.Escaped+unseen.Escaped != all.Escaped {
			t.Errorf("%s %s: development %+v and unseen %+v do not add up to %+v", l.Unit, l.Stratum, dev, unseen, all)
		}
		for _, g := range l.Groups {
			if l.Unit == UnitScenario && l.Stratum != StratumTotal {
				if want := *marks.Attacks[l.Stratum+":"+g.Key].Seen; g.Development != want {
					t.Errorf("scenario %s:%s development %t, the draw says %t", l.Stratum, g.Key, g.Development, want)
				}
			}
			if l.Unit == UnitMechanism && g.Development != *marks.Mechanisms[g.Key].Seen {
				t.Errorf("mechanism %s development %t", g.Key, g.Development)
			}
			if l.Stratum == StratumTotal {
				total += g.MainTested + g.TopupTested
			}
		}
	}
	// Every case is counted once per unit in total: 299 main and 5 top-ups, scenario and mechanism.
	if want := 2 * (evalscore.RegisteredAdversarialCases + 5); total != want {
		t.Errorf("total tested %d, want %d", total, want)
	}
	if all := bound(level(t, r.Groups, UnitMechanism, StratumTotal), SplitAll); all.Escaped != map[bool]int{true: 1, false: 2}[mainMech == topMech] {
		t.Errorf("mechanism units escaped %d", all.Escaped)
	}
	if c := r.Groups.Certification; !c.Certified || c.MaxP0Fraction != "2051048/257560281" || !sha256DigestRE.MatchString(c.Digest) {
		t.Errorf("certification %+v", c)
	}
	if md := r.Markdown(); !strings.Contains(md, "## Scenarios and mechanisms (D47, D48)") || !strings.Contains(md, "| "+mainMech+" |") {
		t.Errorf("markdown lacks the grouped results")
	}
}

// A scenario with no tested case is listed with zero counts and is no unit of the bound (D48 step 5).
func TestUntestedGroupsAreListedButAreNoUnits(t *testing.T) {
	in, d := groupInputs(t)
	root := d.Topup[evalcorpus.ClassTMA][0]
	in.Trials.Adversarial = slicesDeleteCase(in.Trials.Adversarial, "case-"+root)
	r := mustBuild(t, in)
	attack, _ := ledgerKeys(t, in.D48.Ledger, root)
	g := group(t, level(t, r.Groups, UnitScenario, "TM-A"), attack)
	if g.MainTested != 0 || g.TopupTested != 0 {
		t.Fatalf("the top-up-only scenario %s without its record: %+v", attack, g)
	}
	full, _ := groupInputs(t)
	before := bound(level(t, mustBuild(t, full).Groups, UnitScenario, "TM-A"), SplitAll).Units
	if after := bound(level(t, r.Groups, UnitScenario, "TM-A"), SplitAll).Units; after != before-1 {
		t.Fatalf("units %d, want %d", after, before-1)
	}
}

func slicesDeleteCase(recs []evalscore.AdversarialRecord, id string) []evalscore.AdversarialRecord {
	out := recs[:0:0]
	for _, r := range recs {
		if r.CaseID != id {
			out = append(out, r)
		}
	}
	return out
}

// The bound convention is D47's: two-sided 95% with the one-sided figure beside it. The register's own examples:
// 56 mechanisms at 0 escapes give 6.38% (5.21%); D48: 35 scenarios give 10.00% (8.20%), 50 give 7.11% (5.82%).
func TestGroupBoundsFollowTheRegisteredConvention(t *testing.T) {
	for _, tc := range []struct {
		units    int
		two, one float64
	}{{56, 0.0638, 0.0521}, {35, 0.1000, 0.0820}, {50, 0.0711, 0.0582}} {
		groups := make([]Group, tc.units)
		for i := range groups {
			groups[i] = Group{Key: fmt.Sprint(i), MainTested: 3}
		}
		bs, err := boundsOf(groups)
		if err != nil {
			t.Fatal(err)
		}
		b := bs[0]
		if b.Split != SplitAll || b.Units != tc.units || math.Abs(*b.TwoSided95Upper-tc.two) > 5e-5 || math.Abs(*b.OneSided95Upper-tc.one) > 5e-5 {
			t.Errorf("%d units: %+v two %.5f one %.5f, want %.4f and %.4f", tc.units, b, *b.TwoSided95Upper, *b.OneSided95Upper, tc.two, tc.one)
		}
	}
}

// Without the certify output the report says the case bound is not certified, and flags it.
func TestMissingCertificationIsFlagged(t *testing.T) {
	in, _ := groupInputs(t)
	in.D48.Certification = nil
	r := mustBuild(t, in)
	if r.Groups.Certification.Certified || r.Groups.Certification.Digest != "" || !strings.Contains(strings.Join(r.Flags, ","), "bound_not_certified") {
		t.Fatalf("certification %+v, flags %v", r.Groups.Certification, r.Flags)
	}
	certified, _ := groupInputs(t)
	if strings.Contains(strings.Join(mustBuild(t, certified).Flags, ","), "bound_not_certified") {
		t.Fatal("a certified report is flagged")
	}
}

// Inputs that are not the registered files, or not about this draw, are refused: no report.
func TestGroupInputsFailClosed(t *testing.T) {
	cert := func(mutate func(map[string]any)) func(*Inputs) {
		return func(in *Inputs) {
			var m map[string]any
			if err := json.Unmarshal(in.D48.Certification, &m); err != nil {
				t.Fatal(err)
			}
			mutate(m)
			in.D48.Certification, _ = json.Marshal(m)
		}
	}
	for name, mutate := range map[string]func(*Inputs){
		"draw not pinned": func(in *Inputs) { in.Manifest.Pins.DrawDigest = "sha256:" + strings.Repeat("0", 64) },
		"pilot draw":      func(in *Inputs) { in.D48.Draw = readTestFile(t, "../evalcorpus/testdata/d48/pilot-output.json") },
		"ledger not v3":   func(in *Inputs) { in.D48.Ledger = append(bytes.Clone(in.D48.Ledger), '\n') },
		"root not a candidate": func(in *Inputs) {
			for i := range in.Manifest.Cases {
				if a := in.Manifest.Cases[i].Adversarial; a != nil {
					a.RootScenarioID = "not-in-the-ledger"
					return
				}
			}
		},
		"certify other sizes":  cert(func(m map[string]any) { m["nA"], m["nB"] = 157.0, 142.0 }),
		"certify other ranges": cert(func(m map[string]any) { m["N_A_range"] = []int{300, 319} }),
		"certify flag lies":    cert(func(m map[string]any) { m["max_P0_fraction"] = "1/10" }),
		"not a certify output": func(in *Inputs) { in.D48.Certification = []byte(`{"n":299}`) },
	} {
		in, _ := groupInputs(t)
		mutate(&in)
		if _, err := Build(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A certification that is consistent but fails is accepted and reported as not certified.
	in, _ := groupInputs(t)
	cert(func(m map[string]any) { m["certified"], m["max_P0_fraction"] = false, "1/10" })(&in)
	if r := mustBuild(t, in); r.Groups.Certification.Certified {
		t.Fatal("a failed certification reads as certified")
	}
}
