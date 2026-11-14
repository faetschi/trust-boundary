package evalcorpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// The D48 draw outputs of corpus/sampling/select.py (corpus/sampling/README.md, "Output"). The digests are the
// files the pilot commitment binds (corpus/sampling/pilot-commitment-v1.template.json): ledger v3 (D51 item 26)
// and the frozen selection script (D48 "Fixed before the draw"). A draw over another ledger or script is refused.
const (
	DrawSchemaPilot = "tbound-d48-pilot/v1"
	DrawSchemaMain  = "tbound-d48-draw/v1"

	RegisteredLedgerSHA256 = "277fc72c16bbfc27bc90723202c190a57b44987950e93f266209b3118ce79004"
	RegisteredSelectSHA256 = "085c771f571062fdcaf17a7f166631d18f89fad85bf0a25af986ff6408d8a940"

	// PilotPlaces is the research pilot's adversarial size (D47; 16-sampling-design.md section 5 step 0),
	// MainSampleSize the H1 main sample (D47, D48) and VarianceSize the variance subset (section 5 step 6).
	PilotPlaces    = 11
	MainSampleSize = 299
	VarianceSize   = 50

	// LedgerV3CandidatesTMA and TMB are ledger v3's candidates per stratum (D51 item 26), the weights of the pilot split.
	LedgerV3CandidatesTMA = 979
	LedgerV3CandidatesTMB = 286

	// MaxDrawBytes bounds a draw output: it lists every examined candidate of the 1,265 in ledger v3.
	MaxDrawBytes = 16 << 20
)

var drawIDRE = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`) // 16-sampling-design.md section 3 item 3

// drawStrata are the two sampled strata. TM-C is a separate derived set and is never drawn (D48 "TM-C").
var drawStrata = []ThreatClass{ClassTMA, ClassTMB}

// Draw is a complete D48 draw read from select.py's output. Members holds the ledger case ids per stratum: the
// pilot places of a pilot draw, the main sample of a main draw. Cold, Variance and Topup are set on a main draw
// only. A manifest case names its ledger case by root_scenario_id (adaptation rule section 4).
type Draw struct {
	Digest   string // "sha256:" + hex SHA-256 of the output bytes, the value pins.draw_digest must hold
	Pilot    bool
	Members  map[ThreatClass][]string
	Cold     map[ThreatClass][]string
	Variance map[ThreatClass][]string
	Topup    map[ThreatClass][]string
}

// DrawDigest is the pins.draw_digest value of a draw output's bytes.
func DrawDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type drawFile struct {
	Schema               string          `json:"schema"`
	Status               string          `json:"status"`
	RegisteredParameters *bool           `json:"registered_parameters"`
	Memberships          json.RawMessage `json:"memberships"`
	Script               struct {
		SHA256  string `json:"sha256"`
		Matches bool   `json:"matches"`
	} `json:"script"`
	Inputs struct {
		Ledger struct {
			SHA256 string `json:"sha256"`
		} `json:"ledger"`
		Commitment struct {
			Kind string `json:"kind"`
		} `json:"commitment"`
	} `json:"inputs"`
}

type strata struct {
	TMA []string `json:"TM-A"`
	TMB []string `json:"TM-B"`
}

func (s strata) byClass() map[ThreatClass][]string {
	return map[ThreatClass][]string{ClassTMA: s.TMA, ClassTMB: s.TMB}
}

// ParseDraw reads a select.py pilot or main output and refuses any that is not a complete draw under the registered
// parameters, over ledger v3 with the frozen script, or whose memberships break the draw's own rules (cold and
// variance inside the main sample, top-ups outside it, the registered sizes). It checks the file, not the beacon:
// select.py verified the round and randomness, and the manifest digest the owner signs binds the file.
func ParseDraw(data []byte) (Draw, error) {
	if len(data) > MaxDrawBytes {
		return Draw{}, fmt.Errorf("draw output: more than %d bytes", MaxDrawBytes)
	}
	var f drawFile
	if err := json.Unmarshal(data, &f); err != nil {
		return Draw{}, fmt.Errorf("draw output: %w", err)
	}
	d := Draw{Digest: DrawDigest(data)}
	switch {
	case f.Schema == DrawSchemaPilot && f.Inputs.Commitment.Kind == "pilot":
		d.Pilot = true
	case f.Schema == DrawSchemaMain && f.Inputs.Commitment.Kind == "main":
	default:
		return Draw{}, fmt.Errorf("draw output: schema %q with a %q commitment is not a D48 pilot or main draw", f.Schema, f.Inputs.Commitment.Kind)
	}
	switch {
	case f.Status != "complete":
		return Draw{}, fmt.Errorf("draw output: status %q; only a complete draw fixes membership", f.Status)
	case f.RegisteredParameters == nil || !*f.RegisteredParameters:
		return Draw{}, errors.New("draw output: not run with the registered sizes (registered_parameters is not true)")
	case f.Inputs.Ledger.SHA256 != RegisteredLedgerSHA256:
		return Draw{}, fmt.Errorf("draw output: ledger %q is not ledger v3 %s", f.Inputs.Ledger.SHA256, RegisteredLedgerSHA256)
	case f.Script.SHA256 != RegisteredSelectSHA256 || !f.Script.Matches:
		return Draw{}, fmt.Errorf("draw output: script %q is not the frozen select.py %s", f.Script.SHA256, RegisteredSelectSHA256)
	}
	dec := json.NewDecoder(bytes.NewReader(f.Memberships))
	dec.DisallowUnknownFields()
	if d.Pilot {
		var m struct {
			Pilot *strata `json:"pilot"`
		}
		if err := dec.Decode(&m); err != nil || m.Pilot == nil {
			return Draw{}, fmt.Errorf("draw output: memberships is not a pilot membership: %v", err)
		}
		d.Members = m.Pilot.byClass()
		return d, d.check()
	}
	var m struct {
		Main     *strata           `json:"main"`
		Cold     *strata           `json:"cold"`
		Variance *strata           `json:"variance"`
		Topup    map[string]string `json:"topup"`
		Untested []string          `json:"untested"`
	}
	if err := dec.Decode(&m); err != nil || m.Main == nil || m.Cold == nil || m.Variance == nil || m.Topup == nil {
		return Draw{}, fmt.Errorf("draw output: memberships is not a main-draw membership: %v", err)
	}
	d.Members, d.Cold, d.Variance = m.Main.byClass(), m.Cold.byClass(), m.Variance.byClass()
	d.Topup = map[ThreatClass][]string{}
	for _, group := range slices.Sorted(maps.Keys(m.Topup)) {
		class, attack, ok := strings.Cut(group, ":")
		if !ok || !slices.Contains(drawStrata, ThreatClass(class)) || !drawIDRE.MatchString(attack) {
			return Draw{}, fmt.Errorf("draw output: top-up group %q is not <stratum>:<attack_id>", group)
		}
		d.Topup[ThreatClass(class)] = append(d.Topup[ThreatClass(class)], m.Topup[group])
	}
	return d, d.check()
}

// check applies the registered sizes and the membership rules of 16-sampling-design.md section 5.
func (d Draw) check() error {
	seen := map[string]bool{}
	ids := func(what string, lists map[ThreatClass][]string) error {
		for _, class := range drawStrata {
			for _, id := range lists[class] {
				if !drawIDRE.MatchString(id) {
					return fmt.Errorf("draw output: %s id %q is not [A-Za-z0-9._:-]+", what, id)
				}
				if seen[id] {
					return fmt.Errorf("draw output: case %q is drawn twice", id)
				}
				seen[id] = true
			}
		}
		return nil
	}
	if err := ids("member", d.Members); err != nil {
		return err
	}
	if err := ids("top-up", d.Topup); err != nil {
		return err
	}
	if d.Pilot {
		// 16-sampling-design.md section 5 step 0 over the uncapped ledger-v3 candidates (D51 item 26).
		if a, b := largestRemainder(PilotPlaces, LedgerV3CandidatesTMA, LedgerV3CandidatesTMB); len(d.Members[ClassTMA]) != a || len(d.Members[ClassTMB]) != b {
			return fmt.Errorf("draw output: pilot places TM-A %d, TM-B %d; ledger v3 gives %d and %d", len(d.Members[ClassTMA]), len(d.Members[ClassTMB]), a, b)
		}
		return nil
	}
	if total := len(d.Members[ClassTMA]) + len(d.Members[ClassTMB]); total != MainSampleSize {
		return fmt.Errorf("draw output: a main sample of %d, registered %d", total, MainSampleSize)
	}
	nonCold := map[ThreatClass]int{}
	for _, class := range drawStrata {
		main, cold := d.Members[class], d.Cold[class]
		if len(cold) != ColdCount(20, len(main)) {
			return fmt.Errorf("draw output: %d cold %s cases; floor(0.20*%d+0.5) is %d", len(cold), class, len(main), ColdCount(20, len(main)))
		}
		for i, id := range cold {
			if !slices.Contains(main, id) || slices.Contains(cold[:i], id) {
				return fmt.Errorf("draw output: cold case %q is not a distinct %s main case", id, class)
			}
		}
		for i, id := range d.Variance[class] {
			if !slices.Contains(main, id) || slices.Contains(cold, id) || slices.Contains(d.Variance[class][:i], id) {
				return fmt.Errorf("draw output: variance case %q is not a distinct non-cold %s main case", id, class)
			}
		}
		nonCold[class] = len(main) - len(cold)
	}
	// Section 5 step 6: 50 places in proportion to the non-cold main cases, by largest remainder.
	if a, b := largestRemainder(VarianceSize, nonCold[ClassTMA], nonCold[ClassTMB]); len(d.Variance[ClassTMA]) != a || len(d.Variance[ClassTMB]) != b {
		return fmt.Errorf("draw output: variance places TM-A %d, TM-B %d; the registered split is %d and %d", len(d.Variance[ClassTMA]), len(d.Variance[ClassTMB]), a, b)
	}
	return nil
}

// largestRemainder is select.py's split of total in proportion to two weights: the floors, then a leftover place
// to the larger remainder, a tie to TM-B.
func largestRemainder(total, a, b int) (int, int) {
	if a+b == 0 {
		return 0, 0
	}
	na, ra := total*a/(a+b), total*a%(a+b)
	nb, rb := total*b/(a+b), total*b%(a+b)
	if na+nb < total {
		if ra > rb {
			na++
		} else {
			nb++
		}
	}
	return na, nb
}

// PilotQuotasFromDraw returns the research pilot's quotas: PilotQuotas with the adversarial split of the pilot
// draw (on ledger v3, TM-A 9 and TM-B 2: largest_remainder(11, 979, 286)) in place of the rehearsal's 7 and 4.
func PilotQuotasFromDraw(d Draw) (Quotas, error) {
	if !d.Pilot {
		return Quotas{}, errors.New("evalcorpus: the pilot profile needs the pilot draw, not the main draw")
	}
	q := PilotQuotas()
	for _, class := range drawStrata {
		q.Adversarial[class] = len(d.Members[class])
	}
	q.Draw = &d
	return q, nil
}

// ThesisQuotasFromDraw returns the thesis corpus quotas under D48. TM-A and TM-B are the main sample plus the
// top-up as drawn, and their variance places are the drawn ones; TM-C has no variance place and no cold case
// (16-sampling-design.md section 5 step 6). The compliant set stays 25 per governed tool (D48 "Compliant cases
// (100) ... do not change"). TM-C and the ambiguous stratum have no fixed size: TM-C holds as many derived cases
// as have strong constructions (D48 "TM-C") and the ambiguous stratum the established cases that pass (D48,
// D51 item 25), so their counts are the manifest's own.
func ThesisQuotasFromDraw(d Draw, m Manifest) (Quotas, error) {
	if d.Pilot {
		return Quotas{}, errors.New("evalcorpus: the thesis profile needs the main draw, not the pilot draw")
	}
	q := Quotas{
		Adversarial: map[ThreatClass]int{ClassTMC: 0},
		Compliant:   map[Tool]int{ToolRead: 25, ToolWrite: 25, ToolEdit: 25, ToolBash: 25},
		Variance:    map[ThreatClass]int{ClassTMC: 0},
		ColdPercent: 20,
		Draw:        &d,
	}
	for _, class := range drawStrata {
		q.Adversarial[class] = len(d.Members[class]) + len(d.Topup[class])
		q.Variance[class] = len(d.Variance[class])
	}
	for _, c := range m.Cases {
		switch {
		case c.Kind == KindAmbiguous:
			q.Ambiguous++
		case c.Kind == KindAdversarial && c.Adversarial != nil && c.Adversarial.ThreatClass == ClassTMC:
			q.Adversarial[ClassTMC]++
		}
	}
	return q, nil
}

// drawRules checks a manifest against its pinned D48 draw (16-sampling-design.md section 5), matching cases to
// ledger cases by root_scenario_id (adaptation rule section 4). Under a pilot draw the TM-A and TM-B cases are
// exactly the pilot places and carry no sample part: they are development cases, never H1 cases (D48 "Pilot").
// Under a main draw the sample_part main cases are exactly the main sample and the topup cases exactly the
// top-up; a main case is cold or in the variance subset exactly when the draw says so; top-up and TM-C cases are
// in neither subset (section 5 step 6); and every TM-C case is sample_part tmc. A manifest that pins a draw is
// refused without it, and a draw other than the pinned one is refused.
func (v *validator) drawRules(m Manifest, q Quotas) {
	d := q.Draw
	switch {
	case d == nil && m.Pins.DrawDigest != "":
		v.add("pins.draw", "pins.draw_digest is set but the manifest is validated without its D48 draw")
		return
	case d == nil:
		return
	case m.Pins.DrawDigest != d.Digest:
		v.add("pins.draw", "pins.draw_digest %q is not the digest %s of the draw supplied", m.Pins.DrawDigest, d.Digest)
		return
	}
	declared := map[SamplePart]map[ThreatClass][]string{}
	for i := range m.Cases {
		c := &m.Cases[i]
		a := c.Adversarial
		if c.Kind != KindAdversarial || a == nil {
			continue
		}
		label := fmt.Sprintf("case %q", c.CaseID)
		if a.ThreatClass == ClassTMC {
			if !d.Pilot && a.SamplePart != SampleTMC {
				v.add("draw.part", "%s: a TM-C case of the thesis corpus needs sample_part tmc", label)
			}
			if !d.Pilot && (a.Partition != PartitionNonCold || a.VarianceSubset) {
				v.add("draw.cold", "%s: a TM-C case is neither cold nor in the variance subset", label)
			}
			continue
		}
		if !slices.Contains(drawStrata, a.ThreatClass) {
			continue // field.invalid
		}
		switch {
		case d.Pilot && a.SamplePart != "":
			v.add("draw.part", "%s: a pilot case is a development case and carries no sample_part", label)
		case !d.Pilot && a.SamplePart != SampleMain && a.SamplePart != SampleTopup:
			v.add("draw.part", "%s: a %s case of the thesis corpus needs sample_part main or topup", label, a.ThreatClass)
		}
		if declared[a.SamplePart] == nil {
			declared[a.SamplePart] = map[ThreatClass][]string{}
		}
		declared[a.SamplePart][a.ThreatClass] = append(declared[a.SamplePart][a.ThreatClass], a.RootScenarioID)
		if d.Pilot {
			continue
		}
		main := a.SamplePart == SampleMain
		if cold := main && slices.Contains(d.Cold[a.ThreatClass], a.RootScenarioID); (a.Partition == PartitionCold) != cold {
			v.add("draw.cold", "%s: partition %q, but the draw puts it %s", label, a.Partition, map[bool]string{true: "cold", false: "non_cold"}[cold])
		}
		if variance := main && slices.Contains(d.Variance[a.ThreatClass], a.RootScenarioID); a.VarianceSubset != variance {
			v.add("draw.variance", "%s: variance_subset %t, but the draw says %t", label, a.VarianceSubset, variance)
		}
	}
	compare := func(part SamplePart, drawn map[ThreatClass][]string) {
		for _, class := range drawStrata {
			if got := declared[part][class]; !sameSet(got, drawn[class]) {
				v.add("draw.member."+string(class), "the %s cases with sample_part %q are not the draw's: %d declared, %d drawn, first difference %q",
					class, part, len(got), len(drawn[class]), firstDifference(got, drawn[class]))
			}
		}
	}
	if d.Pilot {
		compare("", d.Members)
		return
	}
	compare(SampleMain, d.Members)
	compare(SampleTopup, d.Topup)
}

// firstDifference returns the smallest id that is in exactly one of a and b.
func firstDifference(a, b []string) string {
	var diff []string
	for _, id := range a {
		if !slices.Contains(b, id) {
			diff = append(diff, id)
		}
	}
	for _, id := range b {
		if !slices.Contains(a, id) {
			diff = append(diff, id)
		}
	}
	if len(diff) == 0 {
		return ""
	}
	return slices.Min(diff)
}
