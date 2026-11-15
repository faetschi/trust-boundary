package evalcorpus

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The fixtures in testdata/d48 are select.py outputs over ledger v3 with fake beacons and all-pass decisions
// (gen_fixtures.py): test vectors with the registered sizes, not the registered draws.
func drawFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "d48", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func parsedDraw(t *testing.T, name string) Draw {
	t.Helper()
	d, err := ParseDraw(drawFixture(t, name))
	if err != nil {
		t.Fatalf("ParseDraw(%s): %v", name, err)
	}
	return d
}

// pilotDrawManifest is the synthetic pilot manifest whose adversarial roots are the pilot draw's members.
func pilotDrawManifest(t *testing.T) (Manifest, Quotas) {
	t.Helper()
	d := parsedDraw(t, "pilot-output.json")
	q, err := PilotQuotasFromDraw(d)
	if err != nil {
		t.Fatal(err)
	}
	m := synthManifest(q, synthSeed)
	m.Pins.DrawDigest = d.Digest
	next := map[ThreatClass]int{}
	for i := range m.Cases {
		if a := m.Cases[i].Adversarial; a != nil {
			a.RootScenarioID = d.Members[a.ThreatClass][next[a.ThreatClass]]
			next[a.ThreatClass]++
		}
	}
	return allocated(t, m, q), q
}

// thesisDrawManifest is a synthetic thesis manifest under the main-draw fixture: the main sample and the top-up as
// TM-A and TM-B cases, two derived TM-C cases, the 100 compliant tasks and three ambiguous cases.
func thesisDrawManifest(t *testing.T) (Manifest, Quotas) {
	t.Helper()
	d := parsedDraw(t, "draw-output.json")
	m := synthManifest(Quotas{Compliant: map[Tool]int{ToolRead: 25, ToolWrite: 25, ToolEdit: 25, ToolBash: 25}, Ambiguous: 3}, synthSeed)
	m.Pins.DrawDigest = d.Digest
	var adv []Case
	for _, class := range drawStrata {
		for part, ids := range map[SamplePart][]string{SampleMain: d.Members[class], SampleTopup: d.Topup[class]} {
			for _, id := range ids {
				c := synthAdversarial(class, len(adv)+1)
				c.Adversarial.RootScenarioID, c.Adversarial.SamplePart, c.MeaningRecord = id, part, goodEstablished()
				adv = append(adv, c)
			}
		}
	}
	for i := 1; i <= 2; i++ {
		c := synthAdversarial(ClassTMC, i)
		c.Adversarial.SamplePart, c.MeaningRecord = SampleTMC, goodDerived()
		adv = append(adv, c)
	}
	m.Cases = append(adv, m.Cases...)
	q, err := ThesisQuotasFromDraw(d, m)
	if err != nil {
		t.Fatal(err)
	}
	return allocated(t, m, q), q
}

// The ledger-v3 pilot split is TM-A 9, TM-B 2 (largest_remainder(11, 979, 286)), not the rehearsal's 7 and 4, and
// a manifest of exactly those cases is admitted from the pinned draw.
func TestLedgerV3PilotSplitIsAdmittedFromThePinnedDraw(t *testing.T) {
	m, q := pilotDrawManifest(t)
	if q.Adversarial[ClassTMA] != 9 || q.Adversarial[ClassTMB] != 2 || q.Adversarial[ClassTMC] != 0 {
		t.Fatalf("pilot quotas from the draw: %v, want TM-A 9 TM-B 2 TM-C 0", q.Adversarial)
	}
	wantClean(t, Validate(m, q))
	wantRule(t, Validate(m, PilotQuotas()), "pins.draw") // the rehearsal counts never stand in for the pinned draw
}

func TestManifestWhoseCasesDifferFromTheDrawIsRefused(t *testing.T) {
	m, q := pilotDrawManifest(t)
	short := m
	short.Cases = append([]Case(nil), m.Cases[1:]...) // one TM-A case fewer
	wantRule(t, validate(short, q), "count.adversarial.TM-A")
	wantRule(t, validate(short, q), "draw.member.TM-A")

	swapped := m
	swapped.Cases = append([]Case(nil), m.Cases...)
	a := *swapped.Cases[0].Adversarial
	a.RootScenarioID = "agentdojo:not:a:drawn:case"
	swapped.Cases[0].Adversarial = &a
	vs := validate(swapped, q)
	wantRule(t, vs, "draw.member.TM-A")
	wantNoRule(t, vs, "count.adversarial.TM-A") // right count, wrong case

	labelled := m
	labelled.Cases = append([]Case(nil), m.Cases...)
	b := *labelled.Cases[0].Adversarial
	b.SamplePart, b.ThreatClass = SampleMain, ClassTMA
	labelled.Cases[0].Adversarial, labelled.Cases[0].MeaningRecord = &b, goodEstablished()
	wantRule(t, validate(labelled, q), "draw.part") // a pilot case is never an H1 main case
}

func TestTamperedOrUnpinnedDrawIsRefused(t *testing.T) {
	raw := drawFixture(t, "pilot-output.json")
	m, q := pilotDrawManifest(t)

	tampered := bytes.Replace(raw, []byte(`"status": "complete"`), []byte(`"status": "complete" `), 1)
	d, err := ParseDraw(tampered)
	if err != nil {
		t.Fatalf("a whitespace edit still parses: %v", err)
	}
	tq := q
	tq.Draw = &d
	wantRule(t, Validate(m, tq), "pins.draw") // the pinned digest names other bytes

	unpinned := m
	unpinned.Pins.DrawDigest = ""
	wantRule(t, Validate(unpinned, q), "pins.draw")
	malformed := m
	malformed.Pins.DrawDigest = "sha256:XYZ"
	wantRule(t, Validate(malformed, q), "digest.format")

	for name, edit := range map[string][2]string{
		"incomplete":            {"\n  \"status\": \"complete\"", "\n  \"status\": \"needs-adaptation\""},
		"test sizes":            {`"registered_parameters": true`, `"registered_parameters": false`},
		"other ledger":          {RegisteredLedgerSHA256, "0000000000000000000000000000000000000000000000000000000000000000"},
		"other script":          {RegisteredSelectSHA256, "1111111111111111111111111111111111111111111111111111111111111111"},
		"main schema on pilot":  {DrawSchemaPilot, DrawSchemaMain},
		"extra membership":      {`"memberships": {`, `"memberships": {"extra": [],`},
		"a twelfth pilot place": {`"TM-B": [`, `"TM-B": ["extra:case",`},
	} {
		changed := bytes.ReplaceAll(raw, []byte(edit[0]), []byte(edit[1]))
		if bytes.Equal(changed, raw) {
			t.Fatalf("%s: the edit did not apply", name)
		}
		if _, err := ParseDraw(changed); err == nil {
			t.Errorf("%s: ParseDraw accepted the edited draw", name)
		}
	}
	if _, err := PilotQuotasFromDraw(parsedDraw(t, "draw-output.json")); err == nil {
		t.Error("the pilot profile accepted the main draw")
	}
}

func TestMainDrawFixesSamplePartsColdAndVariance(t *testing.T) {
	d := parsedDraw(t, "draw-output.json")
	if len(d.Members[ClassTMA])+len(d.Members[ClassTMB]) != 299 || len(d.Variance[ClassTMA])+len(d.Variance[ClassTMB]) != 50 ||
		len(d.Cold[ClassTMA]) != ColdCount(20, len(d.Members[ClassTMA])) || len(d.Topup[ClassTMA])+len(d.Topup[ClassTMB]) == 0 {
		t.Fatalf("main draw fixture: %d+%d main, %d+%d variance, %d+%d top-up", len(d.Members[ClassTMA]), len(d.Members[ClassTMB]),
			len(d.Variance[ClassTMA]), len(d.Variance[ClassTMB]), len(d.Topup[ClassTMA]), len(d.Topup[ClassTMB]))
	}
	m, q := thesisDrawManifest(t)
	wantClean(t, Validate(m, q))
	if q.Variance[ClassTMC] != 0 || q.Adversarial[ClassTMC] != 2 || q.Ambiguous != 3 {
		t.Fatalf("thesis quotas: %+v", q)
	}

	mutate := func(rule string, edit func(a *Adversarial, c *Case) bool) {
		t.Helper()
		mm := m
		mm.Cases = append([]Case(nil), m.Cases...)
		for i := range mm.Cases {
			if a := mm.Cases[i].Adversarial; a != nil {
				cp := *a
				if edit(&cp, &mm.Cases[i]) {
					mm.Cases[i].Adversarial = &cp
					wantRule(t, validate(mm, q), rule)
					return
				}
			}
		}
		t.Fatalf("%s: no case to edit", rule)
	}
	mutate("draw.cold", func(a *Adversarial, _ *Case) bool {
		if a.SamplePart != SampleMain || a.Partition != PartitionCold {
			return false
		}
		a.Partition = PartitionNonCold
		return true
	})
	mutate("draw.variance", func(a *Adversarial, _ *Case) bool {
		if a.SamplePart != SampleTopup {
			return false
		}
		a.VarianceSubset = true
		return true
	})
	mutate("draw.member.TM-A", func(a *Adversarial, _ *Case) bool { // a top-up relabelled main is not a main member
		if a.SamplePart != SampleTopup || a.ThreatClass != ClassTMA {
			return false
		}
		a.SamplePart = SampleMain
		return true
	})
	mutate("draw.part", func(a *Adversarial, c *Case) bool { // a legacy (no sample part) case is not a thesis case
		if a.SamplePart != SampleMain {
			return false
		}
		a.SamplePart = ""
		return true
	})
	mutate("draw.part", func(a *Adversarial, c *Case) bool {
		if a.ThreatClass != ClassTMC {
			return false
		}
		a.SamplePart, c.MeaningRecord = "", nil
		return true
	})
	mutate("draw.cold", func(a *Adversarial, _ *Case) bool {
		if a.ThreatClass != ClassTMC {
			return false
		}
		a.Partition = PartitionCold
		return true
	})

	if _, err := ThesisQuotasFromDraw(parsedDraw(t, "pilot-output.json"), m); err == nil {
		t.Error("the thesis profile accepted the pilot draw")
	}
}

// The test vectors are select.py over the files the pilot commitment binds: if the frozen ledger or script moves,
// the registered digests here must move with it.
func TestRegisteredDrawDigestsAreTheFrozenFiles(t *testing.T) {
	for path, want := range map[string]string{
		"d48-ledger-v3.jsonl": RegisteredLedgerSHA256,
		"select.py":           RegisteredSelectSHA256,
	} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "corpus", "sampling", path))
		if err != nil {
			t.Fatal(err)
		}
		if got := DrawDigest(raw); got != "sha256:"+want {
			t.Errorf("%s digests to %s, registered %s", path, got, want)
		}
	}
}

func TestDerivedOriginWithoutADerivedRecordIsRefused(t *testing.T) {
	m, q := derivedManifest(t)
	c := findCase(t, &m, derivedCaseID)
	mr := *goodEstablished()
	mr.CaseOrigin = OriginDerived
	c.MeaningRecord = &mr
	c.Adversarial.SamplePart = ""
	vs := validate(m, q)
	wantRule(t, vs, "meaning.derived")
	for _, v := range vs {
		if v.Rule == "meaning.derived" && !bytes.Contains([]byte(v.Detail), []byte(fmt.Sprintf("%q", derivedCaseID))) {
			t.Errorf("meaning.derived names another case: %s", v.Detail)
		}
	}
}

// The main draw's cold and variance lists are sets inside the main sample, and the variance places follow the
// registered split (16-sampling-design.md section 5 step 6); a draw that breaks either is refused.
func TestMainDrawWithBrokenPartitionsIsRefused(t *testing.T) {
	d := parsedDraw(t, "draw-output.json")
	if a, b := largestRemainder(VarianceSize, len(d.Members[ClassTMA])-len(d.Cold[ClassTMA]), len(d.Members[ClassTMB])-len(d.Cold[ClassTMB])); a != len(d.Variance[ClassTMA]) || b != len(d.Variance[ClassTMB]) {
		t.Fatalf("variance split %d/%d, fixture %d/%d", a, b, len(d.Variance[ClassTMA]), len(d.Variance[ClassTMB]))
	}
	if a, b := largestRemainder(PilotPlaces, LedgerV3CandidatesTMA, LedgerV3CandidatesTMB); a != 9 || b != 2 {
		t.Fatalf("ledger-v3 pilot split %d/%d, want 9/2", a, b)
	}
	clone := func(m map[ThreatClass][]string) map[ThreatClass][]string {
		out := map[ThreatClass][]string{}
		for k, v := range m {
			out[k] = append([]string(nil), v...)
		}
		return out
	}
	for name, edit := range map[string]func(d *Draw){
		"repeated cold id": func(d *Draw) { d.Cold[ClassTMA][1] = d.Cold[ClassTMA][0] },
		"repeated variance id": func(d *Draw) {
			d.Variance[ClassTMA][1] = d.Variance[ClassTMA][0]
		},
		"variance split moved": func(d *Draw) {
			for _, id := range d.Members[ClassTMA] {
				if !slices.Contains(d.Cold[ClassTMA], id) && !slices.Contains(d.Variance[ClassTMA], id) {
					d.Variance[ClassTMA] = append(d.Variance[ClassTMA], id)
					d.Variance[ClassTMB] = d.Variance[ClassTMB][1:]
					return
				}
			}
		},
	} {
		bad := d
		bad.Cold, bad.Variance = clone(d.Cold), clone(d.Variance)
		edit(&bad)
		if err := bad.check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := d.check(); err != nil {
		t.Fatalf("the fixture itself: %v", err)
	}
	pilot := parsedDraw(t, "pilot-output.json")
	pilot.Members = clone(pilot.Members)
	pilot.Members[ClassTMA] = append(pilot.Members[ClassTMA], pilot.Members[ClassTMB][0])
	pilot.Members[ClassTMB] = pilot.Members[ClassTMB][1:]
	if err := pilot.check(); err == nil {
		t.Error("a 10/1 pilot split was accepted")
	}
}
