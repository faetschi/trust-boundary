package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"tbound/supervisor/internal/calibration"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalreport"
)

// VarianceRepeats is the number of separate-session repeats per variance case
// (tbound-thesis.md section 7.5).
const VarianceRepeats = 2

// RerunHeadroomFor is the owner-decided ceiling, and the default, of the units
// that outage reruns may use beyond the planned units: 5% of the planned units,
// rounded up. It is computed from the manifest's own total (evalreport.PlannedUnits,
// D53 item 4); a pre-D48 manifest keeps 1,835 x 5 / 100 = 91.75, rounded up to 92 (1,935 with V: 97).
func RerunHeadroomFor(planned int) int { return (planned*5 + 99) / 100 }

const (
	planDomain  = "tbound-runner-plan/v2"
	orderDomain = "tbound-runner-order/v1"
)

// RunPurpose says what a run is for. The empty purpose is the study's run. A construct-witness run collects only the
// static P replays that corpus-check needs before registration (D41); it is never confirmatory.
type RunPurpose string

// PurposeConstructWitness is the non-confirmatory run that retains construct witnesses (docs/guides/construct-witnesses.md).
const PurposeConstructWitness RunPurpose = "construct-witness"

const PurposeScreening RunPurpose = "compliant-screening"

// ErrNonConfirmatoryPurpose means a construct-witness or compliant-screening run was asked for what only a confirmatory run has: its export,
// report or calibration summary. It holds no C outcomes, and none is synthesized as a failure.
var ErrNonConfirmatoryPurpose = errors.New("runner: a construct-witness or compliant-screening run is non-confirmatory and has no export, report or calibration summary")

// Config is the frozen protocol of one run. It is bound into the plan digest,
// so a journal cannot be resumed under a changed protocol.
type Config struct {
	CDH *CDHConfig `json:"cdh,omitempty"`
	// FreezeCommit is the digest-pinned repo freeze commit that is the
	// pre-registration (40 or 64 lowercase hex digits). It is required.
	FreezeCommit string `json:"freeze_commit"`
	// NET02Mandatory is true when pre-registration declares full VM-uplink
	// capture (EFF-NET-02) mandatory for H1. Only then does a missing NET-02
	// finding make observer coverage incomplete.
	NET02Mandatory bool `json:"net02_mandatory"`
	// AdaptiveCaps are the frozen adaptive caps used to judge each adaptive P run.
	AdaptiveCaps calibration.Caps `json:"adaptive_caps"`
	// EVQObligations lists the obligations every trial must report as passed.
	// If empty, a trial must report at least one obligation and all must pass.
	EVQObligations []string `json:"evq_obligations"`
	// UnitCap is the planned-unit ceiling. 0 means the manifest's
	// evalreport.PlannedUnits, and a larger value is refused.
	UnitCap int `json:"unit_cap"`
	// RerunHeadroom is the number of units beyond UnitCap that outage reruns may
	// use. The owner bounds it at RerunHeadroomFor the planned units (5%,
	// rounded up; 92 for a pre-D48 manifest). 0 means that default, and a larger or a negative value is refused
	// before anything is journaled. The normalized value is part of the plan
	// digest.
	RerunHeadroom int `json:"rerun_headroom"`
	// NoRerunHeadroom turns the headroom off: no unit beyond UnitCap is
	// admitted, so every rerun the plan has no slack for is refused. It needs
	// RerunHeadroom 0 and is bound into the plan digest.
	NoRerunHeadroom bool `json:"no_rerun_headroom"`
	// OnlyCases restricts the plan to these adversarial cases (a dry run, for
	// example the final-pilot cases of the G4 gate). The plan is then a Subset.
	OnlyCases []string `json:"only_cases"`
	// Purpose is empty for the study's run, which omits it from the digested bytes, so the study's digests do not change.
	// A construct-witness run plans only the static P replays of the admitted manifest.
	Purpose RunPurpose `json:"purpose,omitempty"`
	PlanV   bool       `json:"plan_v,omitempty"`
}

var compliantVOrders = [6][3]Configuration{
	{CfgB, CfgC, CfgV}, {CfgB, CfgV, CfgC}, {CfgC, CfgB, CfgV},
	{CfgC, CfgV, CfgB}, {CfgV, CfgB, CfgC}, {CfgV, CfgC, CfgB},
}

var freezeRE = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// normalize fills the defaults and refuses values outside the bounds that planned, the
// manifest's planned-unit total, sets.
func (c Config) normalize(planned int) (Config, error) {
	if c.CDH != nil {
		if err := c.CDH.Validate(); err != nil {
			return c, err
		}
		copy := *c.CDH
		// Freeze the nested manifest too; caller mutation cannot change a planned treatment.
		raw, err := json.Marshal(copy.Manifest)
		if err != nil {
			return c, err
		}
		if err := json.Unmarshal(raw, &copy.Manifest); err != nil {
			return c, err
		}
		copy.LiveAdversarialCases = slices.Sorted(slices.Values(copy.LiveAdversarialCases))
		c.CDH = &copy
	}
	if !freezeRE.MatchString(c.FreezeCommit) {
		return c, errors.New("runner: FreezeCommit must be a 40 or 64 digit lowercase hex commit id")
	}
	if c.UnitCap == 0 {
		c.UnitCap = planned
	}
	if c.UnitCap < 1 || c.UnitCap > planned {
		return c, fmt.Errorf("runner: UnitCap %d outside 1..%d", c.UnitCap, planned)
	}
	switch {
	case c.NoRerunHeadroom && c.RerunHeadroom != 0:
		return c, errors.New("runner: NoRerunHeadroom needs RerunHeadroom 0")
	case c.NoRerunHeadroom:
	case c.RerunHeadroom == 0:
		c.RerunHeadroom = RerunHeadroomFor(planned)
	case c.RerunHeadroom < 0 || c.RerunHeadroom > RerunHeadroomFor(planned):
		return c, fmt.Errorf("runner: RerunHeadroom %d outside 1..%d", c.RerunHeadroom, RerunHeadroomFor(planned))
	}
	if c.PlanV && c.CDH != nil {
		return c, errors.New("runner: a C-DH run plans no compliant task, so PlanV is refused")
	}
	switch c.Purpose {
	case "":
	case PurposeConstructWitness:
		if c.PlanV {
			return c, errors.New("runner: a construct-witness run plans no compliant task, so PlanV is refused")
		}
		if c.CDH != nil {
			return c, errors.New("runner: a construct-witness run cannot be C-DH")
		}
		if len(c.OnlyCases) != 0 {
			return c, errors.New("runner: a construct-witness run plans every admitted adversarial case, so OnlyCases is refused")
		}
	case PurposeScreening:
		if c.CDH != nil {
			return c, errors.New("runner: a compliant-screening run cannot be C-DH")
		}
		if c.PlanV {
			return c, errors.New("runner: a compliant-screening run is Configuration B only, so PlanV is refused")
		}
	default:
		return c, fmt.Errorf("runner: unknown run purpose %q", c.Purpose)
	}
	c.EVQObligations = slices.Sorted(slices.Values(c.EVQObligations))
	c.OnlyCases = slices.Sorted(slices.Values(c.OnlyCases))
	if c.EVQObligations == nil {
		c.EVQObligations = []string{}
	}
	if c.OnlyCases == nil {
		c.OnlyCases = []string{}
	}
	return c, nil
}

// Schedule is the deterministic plan: the ordered steps and the digest that
// binds them, the manifest and the protocol.
type Schedule struct {
	Steps          []Step
	Digest         string
	ManifestDigest string
	Config         Config
	// Subset is true when Config.OnlyCases restricted the plan. A subset plan
	// is not the registered plan.
	Subset bool
	// PlannedUnits is the manifest's registered planned-unit total
	// (evalreport.PlannedUnits), which bounds UnitCap and the rerun headroom.
	// The plan digest binds it through the manifest digest.
	PlannedUnits int
	// RunBinding is the digest of the host's attestation and collector inputs of a run
	// (receipt pins, route report, EVQ results, PROC settings). It is not part of Digest,
	// so the registered plan digest stays the pre-registered one, but the journal header
	// records it and OpenJournal refuses a journal written under another binding. It is
	// empty when the caller binds nothing.
	RunBinding string

	index map[string]int // step ID to position
}

// Units is the planned unit count, the denominator of the run.
func (s Schedule) Units() int { return len(s.Steps) }

// Lookup returns the step with the given ID.
func (s Schedule) Lookup(id string) (Step, bool) {
	i, ok := s.index[id]
	if !ok {
		return Step{}, false
	}
	return s.Steps[i], true
}

// setEffects records the forbidden effects of an adversarial case on a step. Predicate is the first;
// Effects and EffectJoin are set only with two or more, so a single-effect plan is unchanged.
func (s *Step) setEffects(a *evalcorpus.Adversarial) {
	if len(a.ForbiddenEffects) == 0 {
		return
	}
	s.Predicate = a.ForbiddenEffects[0]
	if len(a.ForbiddenEffects) >= 2 {
		s.Effects, s.EffectJoin = slices.Clone(a.ForbiddenEffects), a.EffectJoin
	}
}

// Plan builds the deterministic schedule from the source manifest and its
// seeded allocation (evalcorpus.Allocate output). The caller is expected to
// have run evalcorpus.Validate on the allocation; Plan checks only that the
// allocation is the same corpus as the manifest and has been allocated.
//
// Order: adversarial cases in seeded-rank order, each as the C trajectory then
// the static P/C replays then the adaptive P/C replays; then compliant tasks in
// seeded-rank order, each as B and C in a balanced pair order, or as V, B and C in the six seeded orders when PlanV is on; then the
// ambiguous cases as B and C; then the two variance repeats of each
// variance-subset case. Configuration A is never planned. A construct-witness
// plan is only the static P replay of each adversarial case.
func Plan(manifest, allocation evalcorpus.Manifest, cfg Config) (Schedule, error) {
	planned := evalreport.PlannedUnits(allocation, cfg.PlanV)
	cfg, err := cfg.normalize(planned)
	if err != nil {
		return Schedule{}, err
	}
	if err := sameCorpus(manifest, allocation); err != nil {
		return Schedule{}, err
	}
	md, err := evalcorpus.Digest(allocation)
	if err != nil {
		return Schedule{}, fmt.Errorf("runner: manifest digest: %w", err)
	}

	byID := make(map[string]evalcorpus.Case, len(allocation.Cases))
	var adv, cmp, amb []string
	for _, c := range allocation.Cases {
		if c.CaseID == "" || strings.Contains(c.CaseID, "|") {
			return Schedule{}, fmt.Errorf("runner: case id %q is empty or contains '|'", c.CaseID)
		}
		if _, dup := byID[c.CaseID]; dup {
			return Schedule{}, fmt.Errorf("runner: duplicate case id %q", c.CaseID)
		}
		byID[c.CaseID] = c
		switch c.Kind {
		case evalcorpus.KindAdversarial:
			if c.Adversarial == nil || c.Adversarial.Partition == "" {
				return Schedule{}, fmt.Errorf("runner: adversarial case %q is not allocated", c.CaseID)
			}
			if c.Adversarial.VarianceSubset && c.Adversarial.Partition == evalcorpus.PartitionCold {
				return Schedule{}, fmt.Errorf("runner: case %q is both cold and in the variance subset", c.CaseID)
			}
			adv = append(adv, c.CaseID)
		case evalcorpus.KindCompliant:
			if c.Compliant == nil {
				return Schedule{}, fmt.Errorf("runner: compliant case %q has no payload", c.CaseID)
			}
			cmp = append(cmp, c.CaseID)
		case evalcorpus.KindAmbiguous:
			amb = append(amb, c.CaseID)
		default:
			return Schedule{}, fmt.Errorf("runner: case %q has unknown kind %q", c.CaseID, c.Kind)
		}
	}

	witness := cfg.Purpose == PurposeConstructWitness
	screening := cfg.Purpose == PurposeScreening
	subset := len(cfg.OnlyCases) > 0 || witness || screening
	if screening && len(adv)+len(amb) != 0 {
		return Schedule{}, errors.New("runner: a compliant-screening run plans compliant tasks only, and the manifest holds other cases")
	}
	if len(cfg.OnlyCases) > 0 {
		kind := evalcorpus.KindAdversarial
		if screening {
			kind = evalcorpus.KindCompliant
		}
		only := make(map[string]bool, len(cfg.OnlyCases))
		for _, id := range cfg.OnlyCases {
			if c, ok := byID[id]; !ok || c.Kind != kind {
				return Schedule{}, fmt.Errorf("runner: OnlyCases names %q, which is not a %s case", id, kind)
			}
			only[id] = true
		}
		if screening {
			cmp = slices.DeleteFunc(cmp, func(id string) bool { return !only[id] })
		} else {
			adv = slices.DeleteFunc(adv, func(id string) bool { return !only[id] })
		}
	}
	if subset && !screening {
		cmp, amb = nil, nil
	}
	if screening && len(cmp) == 0 {
		return Schedule{}, errors.New("runner: a compliant-screening run plans no compliant task")
	}

	if cfg.CDH != nil {
		return planCDH(Schedule{ManifestDigest: md, Config: cfg, PlannedUnits: planned}, allocation)
	}
	seed := allocation.SplitSeed
	var steps []Step
	add := func(c evalcorpus.Case, cfgn Configuration, role Role, repeat int) {
		st := Step{
			Index: len(steps), CaseID: c.CaseID, Kind: c.Kind, Configuration: cfgn,
			Role: role, Repeat: repeat, Attempt: 1,
		}
		if c.Adversarial != nil {
			st.ThreatClass = c.Adversarial.ThreatClass
			st.setEffects(c.Adversarial)
			st.WitnessID = c.Adversarial.CalibrationWitnessID
		}
		steps = append(steps, st)
	}

	for _, id := range rankOrder(seed, "order/adversarial", adv) {
		c := byID[id]
		if witness {
			add(c, CfgP, RoleStaticReplay, 0)
			continue
		}
		add(c, CfgC, RoleTrajectory, 0)
		add(c, CfgP, RoleStaticReplay, 0)
		add(c, CfgC, RoleStaticReplay, 0)
		add(c, CfgP, RoleAdaptiveReplay, 0)
		add(c, CfgC, RoleAdaptiveReplay, 0)
	}
	for i, id := range rankOrder(seed, "order/compliant", cmp) {
		if screening {
			add(byID[id], CfgB, RoleCompliant, 0)
			continue
		}
		if cfg.PlanV {
			for _, cf := range compliantVOrders[i%len(compliantVOrders)] {
				add(byID[id], cf, RoleCompliant, 0)
			}
			continue
		}
		first, second := CfgB, CfgC
		if i%2 == 1 {
			first, second = CfgC, CfgB
		}
		add(byID[id], first, RoleCompliant, 0)
		add(byID[id], second, RoleCompliant, 0)
	}
	for i, id := range rankOrder(seed, "order/ambiguous", amb) {
		first, second := CfgB, CfgC
		if i%2 == 1 {
			first, second = CfgC, CfgB
		}
		add(byID[id], first, RoleAmbiguous, 0)
		add(byID[id], second, RoleAmbiguous, 0)
	}
	if !subset {
		var variance []string
		for _, id := range adv {
			if byID[id].Adversarial.VarianceSubset {
				variance = append(variance, id)
			}
		}
		for _, id := range rankOrder(seed, "order/variance", variance) {
			for r := 1; r <= VarianceRepeats; r++ {
				add(byID[id], CfgC, RoleVarianceRepeat, r)
			}
		}
	}
	if len(steps) > cfg.UnitCap {
		return Schedule{}, fmt.Errorf("%w: %d steps, cap %d", ErrOverPlanned, len(steps), cfg.UnitCap)
	}

	s := Schedule{Steps: steps, ManifestDigest: md, Config: cfg, Subset: subset, PlannedUnits: planned, index: make(map[string]int, len(steps))}
	for i, st := range steps {
		if _, dup := s.index[st.ID()]; dup {
			return Schedule{}, fmt.Errorf("runner: duplicate step id %q", st.ID())
		}
		s.index[st.ID()] = i
	}
	if err := s.validatePurposeSteps(); err != nil {
		return Schedule{}, err
	}
	if s.Digest, err = planDigest(s); err != nil {
		return Schedule{}, err
	}
	return s, nil
}

// validatePurposeSteps refuses a construct-witness schedule with any step but a static P replay, and a
// compliant-screening schedule with any step but a Configuration B compliant step.
func (s Schedule) validatePurposeSteps() error {
	for _, st := range s.Steps {
		switch {
		case s.Config.Purpose == PurposeConstructWitness && (st.Configuration != CfgP || st.Role != RoleStaticReplay):
			return fmt.Errorf("runner: a construct-witness run holds only static P replays, not %s/%s", st.Configuration, st.Role)
		case s.Config.Purpose == PurposeScreening && (st.Configuration != CfgB || st.Role != RoleCompliant):
			return fmt.Errorf("runner: a compliant-screening run holds only Configuration B compliant steps, not %s/%s", st.Configuration, st.Role)
		}
	}
	return nil
}

// sameCorpus checks that allocation is manifest with partitions assigned: same
// header, same cases in the same order, and no contradiction of any partition
// the manifest already carries.
func sameCorpus(m, a evalcorpus.Manifest) error {
	if m.SchemaVersion != a.SchemaVersion || m.SplitSeed != a.SplitSeed || m.Pins != a.Pins || len(m.Cases) != len(a.Cases) {
		return errors.New("runner: allocation does not match the manifest header or size")
	}
	for i := range m.Cases {
		x, y := m.Cases[i], a.Cases[i]
		if x.CaseID != y.CaseID || x.Kind != y.Kind {
			return fmt.Errorf("runner: allocation case %d is %q/%s, manifest has %q/%s", i, y.CaseID, y.Kind, x.CaseID, x.Kind)
		}
		if x.Adversarial != nil && y.Adversarial != nil {
			if p := x.Adversarial.Partition; p != "" && p != y.Adversarial.Partition {
				return fmt.Errorf("runner: allocation changes the partition of %q", x.CaseID)
			}
			if x.Adversarial.ThreatClass != y.Adversarial.ThreatClass {
				return fmt.Errorf("runner: allocation changes the threat class of %q", x.CaseID)
			}
		}
	}
	return nil
}

// rankOrder orders ids by SHA-256 over length-prefixed (domain, seed, purpose,
// id), ties by id. It is independent of the input order.
func rankOrder(seed, purpose string, ids []string) []string {
	type ranked struct {
		rank [sha256.Size]byte
		id   string
	}
	rs := make([]ranked, len(ids))
	for i, id := range ids {
		h := sha256.New()
		for _, part := range []string{orderDomain, seed, purpose, id} {
			var n [8]byte
			binary.BigEndian.PutUint64(n[:], uint64(len(part)))
			h.Write(n[:])
			h.Write([]byte(part))
		}
		copy(rs[i].rank[:], h.Sum(nil))
		rs[i].id = id
	}
	sort.Slice(rs, func(i, j int) bool {
		if c := bytes.Compare(rs[i].rank[:], rs[j].rank[:]); c != 0 {
			return c < 0
		}
		return rs[i].id < rs[j].id
	})
	out := make([]string, len(rs))
	for i := range rs {
		out[i] = rs[i].id
	}
	return out
}

func planDigest(s Schedule) (string, error) {
	doc := struct {
		Domain         string `json:"domain"`
		ManifestDigest string `json:"manifest_digest"`
		Config         Config `json:"config"`
		Steps          []Step `json:"steps"`
	}{planDomain, s.ManifestDigest, s.Config, s.Steps}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("runner: plan digest: %w", err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
