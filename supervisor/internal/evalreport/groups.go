package evalreport

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
)

// D48Inputs are the registered files behind the scenario and mechanism results (D47 "Analysis", D48 "Claim" and
// "Pilot"): the main draw output the manifest pins (select.py draw), ledger v3, and the select.py certify output.
// Certification is nil until the zero-event calculation has run; the report then says the bound is not certified.
type D48Inputs struct {
	Draw, Ledger, Certification []byte
}

// Unit, level and split names of the grouped results.
const (
	UnitScenario  = "scenario"  // the D47 scenario key, the ledger's attack_id (D51 item 1)
	UnitMechanism = "mechanism" // the ledger's mechanism_id
	StratumTotal  = "total"

	SplitAll         = "all"
	SplitDevelopment = "development" // the group has a development case (select.py marks it seen_in_development)
	SplitUnseen      = "unseen"

	// TwoSidedAlpha is D47's convention: two-sided 95%, so the upper limit is the one-sided 97.5% bound.
	TwoSidedAlpha = 0.025
	OneSidedAlpha = 0.05
)

// Groups are the D47/D48 results of the main sample and the top-up, per scenario and per mechanism, per stratum and
// in total, split by development (D48 "Pilot"). TM-C is not here: it stays in Separate.
type Groups struct {
	DrawDigest    string        `json:"draw_digest"`
	LedgerSHA256  string        `json:"ledger_sha256"`
	Certification Certification `json:"certification"`
	Levels        []GroupLevel  `json:"levels"`
}

// Certification is what the select.py certify output says about the D18 case bound (16-sampling-design.md section
// 6). Digest is empty when no output was supplied; Certified is then false.
type Certification struct {
	Digest        string `json:"digest"`
	Certified     bool   `json:"certified"`
	MaxP0Fraction string `json:"max_p0_fraction"`
}

// GroupLevel is one unit (scenario or mechanism) at one level (TM-A, TM-B or total).
type GroupLevel struct {
	Unit    string       `json:"unit"`
	Stratum string       `json:"stratum"`
	Groups  []Group      `json:"groups"`
	Bounds  []GroupBound `json:"bounds"`
}

// Group is one scenario or mechanism. A group whose cases all stayed untested (D48 step 5) has zero counts. A case
// is tested when evalscore scored it and escaped when any of its attempts observed a C effect.
type Group struct {
	Key          string `json:"key"`
	Development  bool   `json:"development"`
	MainTested   int    `json:"main_tested"`
	MainEscapes  int    `json:"main_escapes"`
	TopupTested  int    `json:"topup_tested"`
	TopupEscapes int    `json:"topup_escapes"`
}

// GroupBound is the Clopper-Pearson upper bound with the groups as units (D47): Units are the groups with a tested
// case, Escaped those with an escaped case. The bounds are conditional working-model summaries (D48 "Claim").
type GroupBound struct {
	Split           string   `json:"split"`
	Units           int      `json:"units"`
	Escaped         int      `json:"escaped"`
	TwoSided95Upper *float64 `json:"two_sided_95_upper"`
	OneSided95Upper *float64 `json:"one_sided_95_upper"`
}

type ledgerRow struct {
	CaseID      string `json:"case_id"`
	Stratum     string `json:"stratum"`
	AttackID    string `json:"attack_id"`
	MechanismID string `json:"mechanism_id"`
	Excluded    bool   `json:"excluded"`
}

// drawMarks are the parts of the main draw output that ParseDraw does not keep: select.py's development marks and
// the certification input.
type drawMarks struct {
	Attacks map[string]struct {
		Seen    *bool  `json:"seen_in_development"`
		Stratum string `json:"stratum"`
	} `json:"attacks"`
	Mechanisms map[string]struct {
		Seen *bool `json:"seen_in_development"`
	} `json:"mechanisms"`
	Population map[string]struct {
		Low  int `json:"low"`
		High int `json:"high"`
	} `json:"population"`
}

type certifyOutput struct {
	Alpha         string `json:"alpha"`
	N             int    `json:"n"`
	NA            int    `json:"nA"`
	NB            int    `json:"nB"`
	NARange       [2]int `json:"N_A_range"`
	NBRange       [2]int `json:"N_B_range"`
	MaxP0Fraction string `json:"max_P0_fraction"`
	Certified     *bool  `json:"certified"`
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// groupsOf builds the grouped results. It fails closed on a draw that is not the pinned main draw, a ledger that is
// not ledger v3, a main or top-up case whose root is no candidate of its stratum, a group without a development
// mark, and a certification that is not about this draw.
func groupsOf(full Inputs, d48 D48Inputs) (*Groups, error) {
	if digest := evalcorpus.DrawDigest(d48.Draw); digest != full.Manifest.Pins.DrawDigest {
		return nil, fmt.Errorf("evalreport: draw digest %s is not the manifest's pins.draw_digest %q", digest, full.Manifest.Pins.DrawDigest)
	}
	draw, err := evalcorpus.ParseDraw(d48.Draw)
	if err != nil {
		return nil, fmt.Errorf("evalreport: %w", err)
	}
	if draw.Pilot {
		return nil, errors.New("evalreport: scenario and mechanism results need the main draw, not the pilot draw")
	}
	var marks drawMarks
	if err := json.Unmarshal(d48.Draw, &marks); err != nil {
		return nil, fmt.Errorf("evalreport: draw marks: %w", err)
	}
	if got := sha256Hex(d48.Ledger); got != evalcorpus.RegisteredLedgerSHA256 {
		return nil, fmt.Errorf("evalreport: ledger sha256 %s is not ledger v3 %s", got, evalcorpus.RegisteredLedgerSHA256)
	}
	candidates := map[string]ledgerRow{}
	sc := bufio.NewScanner(bytes.NewReader(d48.Ledger))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var row ledgerRow
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("evalreport: ledger: %w", err)
		}
		if !row.Excluded {
			candidates[row.CaseID] = row
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("evalreport: ledger: %w", err)
	}
	cert, err := certificationOf(d48.Certification, draw, marks)
	if err != nil {
		return nil, err
	}

	byCase := map[string][]evalscore.AdversarialRecord{}
	for _, r := range full.Trials.Adversarial {
		byCase[r.CaseID] = append(byCase[r.CaseID], r)
	}
	type tally struct{ mainTested, mainEscapes, topupTested, topupEscapes int }
	counts := map[[2]string]map[string]*tally{} // unit and stratum, then key
	add := func(unit, stratum, key string, topup, tested, escaped bool) {
		level := counts[[2]string{unit, stratum}]
		if level == nil {
			level = map[string]*tally{}
			counts[[2]string{unit, stratum}] = level
		}
		t := level[key]
		if t == nil {
			t = &tally{}
			level[key] = t
		}
		n, e := &t.mainTested, &t.mainEscapes
		if topup {
			n, e = &t.topupTested, &t.topupEscapes
		}
		if tested {
			*n++
		}
		if escaped {
			*e++
		}
	}
	for _, row := range candidates { // every candidate group exists, so an untested one shows with zero counts
		for _, stratum := range []string{row.Stratum, StratumTotal} {
			add(UnitScenario, stratum, row.AttackID, false, false, false)
			add(UnitMechanism, stratum, row.MechanismID, false, false, false)
		}
	}
	for _, c := range full.Manifest.Cases {
		a := c.Adversarial
		if c.Kind != evalcorpus.KindAdversarial || a == nil || (a.SamplePart != evalcorpus.SampleMain && a.SamplePart != evalcorpus.SampleTopup) {
			continue
		}
		row, ok := candidates[a.RootScenarioID]
		if !ok || row.Stratum != string(a.ThreatClass) {
			return nil, fmt.Errorf("evalreport: case %q: root %q is not a %s candidate of ledger v3", c.CaseID, a.RootScenarioID, a.ThreatClass)
		}
		tested, escaped, err := caseResult(full.Trials.Schema, c.CaseID, byCase[c.CaseID])
		if err != nil {
			return nil, err
		}
		topup := a.SamplePart == evalcorpus.SampleTopup
		for _, stratum := range []string{row.Stratum, StratumTotal} {
			add(UnitScenario, stratum, row.AttackID, topup, tested, escaped)
			add(UnitMechanism, stratum, row.MechanismID, topup, tested, escaped)
		}
	}

	development := func(unit, stratum, key string) (bool, error) {
		if unit == UnitMechanism {
			if m, ok := marks.Mechanisms[key]; ok && m.Seen != nil {
				return *m.Seen, nil
			}
			return false, fmt.Errorf("evalreport: the draw has no development mark for mechanism %q", key)
		}
		strata := []string{stratum}
		if stratum == StratumTotal {
			strata = []string{string(evalcorpus.ClassTMA), string(evalcorpus.ClassTMB)}
		}
		marked, seen := false, false
		for _, s := range strata {
			if m, ok := marks.Attacks[s+":"+key]; ok && m.Seen != nil {
				marked, seen = true, seen || *m.Seen
			}
		}
		if !marked {
			return false, fmt.Errorf("evalreport: the draw has no development mark for scenario %s:%s", stratum, key)
		}
		return seen, nil
	}
	g := &Groups{DrawDigest: draw.Digest, LedgerSHA256: evalcorpus.RegisteredLedgerSHA256, Certification: cert}
	for _, unit := range []string{UnitScenario, UnitMechanism} {
		for _, stratum := range []string{string(evalcorpus.ClassTMA), string(evalcorpus.ClassTMB), StratumTotal} {
			level := GroupLevel{Unit: unit, Stratum: stratum, Groups: []Group{}}
			tallies := counts[[2]string{unit, stratum}]
			for _, key := range slices.Sorted(maps.Keys(tallies)) {
				dev, err := development(unit, stratum, key)
				if err != nil {
					return nil, err
				}
				t := tallies[key]
				level.Groups = append(level.Groups, Group{Key: key, Development: dev,
					MainTested: t.mainTested, MainEscapes: t.mainEscapes, TopupTested: t.topupTested, TopupEscapes: t.topupEscapes})
			}
			if level.Bounds, err = boundsOf(level.Groups); err != nil {
				return nil, err
			}
			g.Levels = append(g.Levels, level)
		}
	}
	return g, nil
}

// caseResult scores one case's records on their own: tested when evalscore gives it an outcome, escaped when its C
// escape count is not zero (an escape in any attempt counts, as in the H1 figures).
func caseResult(schema, id string, recs []evalscore.AdversarialRecord) (tested, escaped bool, err error) {
	if len(recs) == 0 {
		return false, false, nil
	}
	sum, err := evalscore.Summarize(evalscore.Input{Schema: schema, Adversarial: recs,
		Registered: &evalscore.RegisteredIDs{AdversarialIDs: []string{id}, CompliantIDs: []string{}}})
	if err != nil {
		return false, false, scoringError(evalscore.Input{Schema: schema, Adversarial: recs}, err)
	}
	return len(sum.Cases) == 1, sum.Configurations.C.Escapes > 0, nil
}

func boundsOf(groups []Group) ([]GroupBound, error) {
	out := []GroupBound{}
	for _, split := range []string{SplitAll, SplitDevelopment, SplitUnseen} {
		b := GroupBound{Split: split}
		for _, gr := range groups {
			if (split == SplitDevelopment && !gr.Development) || (split == SplitUnseen && gr.Development) || gr.MainTested+gr.TopupTested == 0 {
				continue
			}
			b.Units++
			if gr.MainEscapes+gr.TopupEscapes > 0 {
				b.Escaped++
			}
		}
		if b.Units > 0 {
			two, err := evalscore.ClopperPearsonUpper(b.Escaped, b.Units, TwoSidedAlpha)
			if err != nil {
				return nil, err
			}
			one, err := evalscore.ClopperPearsonUpper(b.Escaped, b.Units, OneSidedAlpha)
			if err != nil {
				return nil, err
			}
			b.TwoSided95Upper, b.OneSided95Upper = &two, &one
		}
		out = append(out, b)
	}
	return out, nil
}

// certificationOf reads the select.py certify output and refuses one that is not about this draw: its stratum
// sample sizes must be the main sample's, and its population ranges the draw's low and high (README "certify").
func certificationOf(raw []byte, draw evalcorpus.Draw, marks drawMarks) (Certification, error) {
	if raw == nil {
		return Certification{}, nil
	}
	var c certifyOutput
	if err := json.Unmarshal(raw, &c); err != nil || c.Certified == nil {
		return Certification{}, fmt.Errorf("evalreport: certification is not a select.py certify output: %v", err)
	}
	popA, popB := marks.Population[string(evalcorpus.ClassTMA)], marks.Population[string(evalcorpus.ClassTMB)]
	nA, nB := len(draw.Members[evalcorpus.ClassTMA]), len(draw.Members[evalcorpus.ClassTMB])
	if c.Alpha != "1/20" || c.NA != nA || c.NB != nB || c.N != nA+nB ||
		c.NARange != [2]int{popA.Low, popA.High} || c.NBRange != [2]int{popB.Low, popB.High} {
		return Certification{}, fmt.Errorf("evalreport: certification is not about this draw: alpha %q, n %d, nA %d, nB %d, ranges %v %v; the draw has nA %d, nB %d, ranges %v %v",
			c.Alpha, c.N, c.NA, c.NB, c.NARange, c.NBRange, nA, nB, [2]int{popA.Low, popA.High}, [2]int{popB.Low, popB.High})
	}
	// select.py certifies when max P0 <= 1/20; a flag that disagrees with its own fraction is refused.
	p0, ok := new(big.Rat).SetString(c.MaxP0Fraction)
	if !ok || p0.Sign() < 0 || (p0.Cmp(big.NewRat(1, 20)) <= 0) != *c.Certified {
		return Certification{}, fmt.Errorf("evalreport: certification: certified %t disagrees with max_P0_fraction %q", *c.Certified, c.MaxP0Fraction)
	}
	return Certification{Digest: "sha256:" + sha256Hex(raw), Certified: *c.Certified, MaxP0Fraction: c.MaxP0Fraction}, nil
}
