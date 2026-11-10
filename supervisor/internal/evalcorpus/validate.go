package evalcorpus

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// threatClasses and governedTools fix the registered strata and their report order.
var (
	threatClasses = []ThreatClass{ClassTMA, ClassTMB, ClassTMC}
	governedTools = []Tool{ToolRead, ToolWrite, ToolEdit, ToolBash}
)

// Quotas are the registered counts. ThesisQuotas returns the pre-registered values, and
// tests substitute small counts with the same keys. Every map must have exactly the
// keys TM-A, TM-B, TM-C (Adversarial and Variance) or read, write, edit, bash (Compliant).
type Quotas struct {
	Adversarial map[ThreatClass]int // adversarial case units per threat class
	Compliant   map[Tool]int        // compliant tasks per primary governed tool
	Ambiguous   int                 // ambiguous cases
	Variance    map[ThreatClass]int // variance-subset members per class (non-cold only)
	ColdPercent int                 // cold share of each class, in percent (nearest, ties up)
}

// Violation is one failed rule. Rule is a stable code, and Detail names the case or field.
type Violation struct {
	Rule   string
	Detail string
}

// ThesisQuotas returns the pre-registered counts of tbound-thesis.md section 7.5: 299
// adversarial units (TM-A 100, TM-B 100, TM-C 99), 100 compliant tasks (25 per governed
// tool), 20 ambiguous cases, a 50-member variance subset (17, 17, 16 from the non-cold
// units), and a 20% cold share per threat class.
//
// shortcut: quotas still the pre-D48 fixed counts; stage 2 derives them from the pinned D48 draw before the thesis corpus is admitted
func ThesisQuotas() Quotas {
	return Quotas{
		Adversarial: map[ThreatClass]int{ClassTMA: 100, ClassTMB: 100, ClassTMC: 99},
		Compliant:   map[Tool]int{ToolRead: 25, ToolWrite: 25, ToolEdit: 25, ToolBash: 25},
		Ambiguous:   20,
		Variance:    map[ThreatClass]int{ClassTMA: 17, ClassTMB: 17, ClassTMC: 16},
		ColdPercent: 20,
	}
}

// PilotQuotas returns the explicit counts of a pilot or G4 dry-run corpus: 11 adversarial
// units (TM-A 7, TM-B 4, TM-C 0), 20 compliant tasks (5 per governed tool), 2 ambiguous
// cases, a 2-member variance subset (1 each for TM-A and TM-B) and the same 20% cold
// share. The adversarial units are established benchmark roots only (D37 amendment
// 2026-10-24); TM-C is entirely derived and therefore absent from the established-only
// pilot. They are not the pre-registered quotas. Validate applies every structural rule
// under them, so a pilot corpus is checked as strictly as the thesis corpus except for
// its counts, and a result obtained from it must never be reported as a thesis result.
func PilotQuotas() Quotas {
	return Quotas{
		Adversarial: map[ThreatClass]int{ClassTMA: 7, ClassTMB: 4, ClassTMC: 0},
		Compliant:   map[Tool]int{ToolRead: 5, ToolWrite: 5, ToolEdit: 5, ToolBash: 5},
		Ambiguous:   2,
		Variance:    map[ThreatClass]int{ClassTMA: 1, ClassTMB: 1, ClassTMC: 0},
		ColdPercent: 20,
	}
}

// Validate checks m against q and returns every violation it finds, in manifest order
// and then in a fixed rule order. An empty result means that every rule holds for the
// manifest as supplied. It does not show that the manifest is the registered corpus,
// and it does not show anything about any claim.
//
// Rules, by code:
//   - schema.version, seed.empty, quota.invalid: the manifest header and quotas;
//   - case.id.empty, case.id.duplicate, case.kind, case.payload: identity and payload;
//   - adv.root.duplicate, adv.unit.duplicate, adv.variance.partition,
//     adaptation.evidence, cmp.reference.forbidden_effect: per-case rules;
//   - trial.invalid: a present trial has a prompt and positive budgets within the limits;
//   - field.empty, field.invalid, digest.format: required and enumerated fields, and the
//     sha256 digest format;
//   - count.total, count.adversarial.<class>, count.compliant.<tool>, count.ambiguous:
//     registered totals;
//   - cold.count.<class>, cold.seed.<class>, variance.count.<class>,
//     variance.seed.<class>: the cold and variance partitions, including whether they
//     reproduce from the frozen seed under Allocate's rules;
//   - sample.class, sample.derived, sample.provenance, meaning.derived: the D48 sample part and derived record
//     of each adversarial case (see samplePart and derivedRecord).
func Validate(m Manifest, q Quotas) []Violation {
	if err := q.check(); err != nil {
		return []Violation{{Rule: "quota.invalid", Detail: err.Error()}}
	}
	v := &validator{
		caseIDs: make(map[string]bool),
		roots:   make(map[string]string),
		units:   make(map[string]string),
		strata:  make(map[ThreatClass]*stratum),
		tools:   make(map[Tool]int),
	}
	for _, class := range threatClasses {
		v.strata[class] = &stratum{}
	}
	v.manifestRules(m)
	for i := range m.Cases {
		v.caseRules(&m.Cases[i])
	}
	v.countRules(m, q)
	v.allocationRules(m, q)
	return v.out
}

// stratum holds the adversarial cases of one threat class, as declared.
type stratum struct {
	all      []string // every case of the class with a valid class label
	cold     []string // partition cold
	nonCold  []string // partition non_cold
	variance []string // variance_subset true
}

type validator struct {
	out       []Violation
	caseIDs   map[string]bool
	roots     map[string]string // root_scenario_id -> first case that uses it
	units     map[string]string // fixture and calibration digests -> first case that uses them
	strata    map[ThreatClass]*stratum
	tools     map[Tool]int
	ambiguous int
}

func (v *validator) add(rule, format string, args ...any) {
	v.out = append(v.out, Violation{Rule: rule, Detail: fmt.Sprintf(format, args...)})
}

// required reports an empty (or whitespace-only) value.
func (v *validator) required(path, value string) {
	if strings.TrimSpace(value) == "" {
		v.add("field.empty", "%s is empty", path)
	}
}

// digest reports an empty value as field.empty, and a malformed one as digest.format.
func (v *validator) digest(path, value string) {
	if strings.TrimSpace(value) == "" {
		v.add("field.empty", "%s is empty", path)
		return
	}
	if !isDigest(value) {
		v.add("digest.format", "%s is not sha256 followed by 64 lowercase hex digits", path)
	}
}

func (v *validator) manifestRules(m Manifest) {
	if m.SchemaVersion != SchemaVersion {
		v.add("schema.version", "schema_version is %q; want %q", m.SchemaVersion, SchemaVersion)
	}
	if strings.TrimSpace(m.SplitSeed) == "" {
		v.add("seed.empty", "split_seed is empty")
	}
	v.digest("pins.environment_digest", m.Pins.EnvironmentDigest)
	v.digest("pins.model_digest", m.Pins.ModelDigest)
	v.digest("pins.profile_digest", m.Pins.ProfileDigest)
	v.digest("pins.policy_digest", m.Pins.PolicyDigest)
	v.pinAttacker(m)
}

func (v *validator) caseRules(c *Case) {
	label := fmt.Sprintf("case %q", c.CaseID)
	if strings.TrimSpace(c.CaseID) == "" {
		v.add("case.id.empty", "a case has an empty case_id")
	} else {
		if v.caseIDs[c.CaseID] {
			v.add("case.id.duplicate", "case_id %q appears more than once", c.CaseID)
		}
		v.caseIDs[c.CaseID] = true
	}
	if c.MeaningRecord != nil {
		v.meaningRecord(label, c.MeaningRecord)
		if c.MeaningRecord.Derived != nil && c.Kind != KindAdversarial {
			v.add("meaning.derived", "%s: a derived record belongs to an adversarial TM-C case, not a %s case", label, c.Kind)
		}
	}
	if c.Trial != nil {
		v.trial(label, c)
	}

	switch c.Kind {
	case KindAdversarial:
		if c.Adversarial == nil || c.Compliant != nil || c.Ambiguous != nil {
			v.add("case.payload", "%s: kind adversarial needs the adversarial payload and no other", label)
			return
		}
		v.adversarial(label, c)
	case KindCompliant:
		if c.Compliant == nil || c.Adversarial != nil || c.Ambiguous != nil {
			v.add("case.payload", "%s: kind compliant needs the compliant payload and no other", label)
			return
		}
		v.compliant(label, c)
	case KindAmbiguous:
		if c.Ambiguous == nil || c.Adversarial != nil || c.Compliant != nil {
			v.add("case.payload", "%s: kind ambiguous needs the ambiguous payload and no other", label)
			return
		}
		v.ambiguousCase(label, c.Ambiguous)
	default:
		v.add("case.kind", "%s: kind %q is not adversarial, compliant, or ambiguous", label, c.Kind)
	}
}

// meaningRecord checks a present D37 meaning record: a known case_origin, the required
// provenance strings, well-formed benchmark digest, and a non-empty transformation list. A
// derived record (D48) replaces the benchmark-source fields, so those are optional on it.
// Rule codes are meaning.origin, meaning.field, meaning.digest, and the shared field.empty.
func (v *validator) meaningRecord(label string, mr *MeaningRecord) {
	switch mr.CaseOrigin {
	case OriginEstablished, OriginDerived:
	default:
		v.add("meaning.origin", "%s: case_origin %q is not established or derived", label, mr.CaseOrigin)
	}
	if mr.Derived == nil {
		v.required(label+" meaning_record.benchmark_key", mr.BenchmarkKey)
		v.required(label+" meaning_record.benchmark_revision", mr.BenchmarkRevision)
		v.digest(label+" meaning_record.benchmark_digest", mr.BenchmarkDigest)
		v.required(label+" meaning_record.source_item_id", mr.SourceItemID)
		v.required(label+" meaning_record.root_id", mr.RootID)
		v.required(label+" meaning_record.license", mr.License)
		v.required(label+" meaning_record.redistribution", mr.Redistribution)
	} else {
		v.derivedRecord(label, mr)
		if mr.BenchmarkDigest != "" {
			v.digest(label+" meaning_record.benchmark_digest", mr.BenchmarkDigest)
		}
	}
	v.required(label+" meaning_record.preserved_intent", mr.PreservedIntent)
	v.required(label+" meaning_record.task_unit", mr.TaskUnit)
	v.required(label+" meaning_record.authorization_class", mr.AuthorizationClass)
	v.required(label+" meaning_record.tool_mapping", mr.ToolMapping)
	v.required(label+" meaning_record.oracle_mapping", mr.OracleMapping)
	if len(mr.Transformations) == 0 {
		v.add("meaning.field", "%s: meaning_record.transformations is empty", label)
	}
	for i, s := range mr.Transformations {
		v.required(fmt.Sprintf("%s meaning_record.transformations[%d]", label, i), s)
	}
	switch mr.LabelOrigin {
	case LabelSourceInherited, LabelExternalOracle:
	default:
		v.add("meaning.field", "%s: meaning_record.label_origin %q is not source_inherited or external_oracle", label, mr.LabelOrigin)
	}
}

// derivedRecord checks a derived record (D48): its case_origin is derived, both taxonomy
// references, the author, the authoring date and the mechanism are present, and the digests
// and date are well formed.
func (v *validator) derivedRecord(label string, mr *MeaningRecord) {
	if mr.CaseOrigin != OriginDerived {
		v.add("meaning.origin", "%s: a derived record needs case_origin derived, not %q", label, mr.CaseOrigin)
	}
	v.taxonomyRef(label+" meaning_record.derived.owasp", mr.Derived.OWASP)
	v.taxonomyRef(label+" meaning_record.derived.mitre", mr.Derived.MITRE)
	v.required(label+" meaning_record.derived.author", mr.Derived.Author)
	v.date(label+" meaning_record.derived.authored_on", mr.Derived.AuthoredOn)
	v.required(label+" meaning_record.mechanism_id", mr.MechanismID)
}

func (v *validator) taxonomyRef(path string, r TaxonomyRef) {
	v.required(path+".key", r.Key)
	v.required(path+".revision", r.Revision)
	v.digest(path+".digest", r.Digest)
	v.required(path+".entry_id", r.EntryID)
}

// date reports an empty value as field.empty, and a value that is not a YYYY-MM-DD calendar
// date as field.invalid.
func (v *validator) date(path, value string) {
	if strings.TrimSpace(value) == "" {
		v.add("field.empty", "%s is empty", path)
		return
	}
	if !isDate(value) {
		v.add("field.invalid", "%s is not a YYYY-MM-DD calendar date", path)
	}
}

// samplePart checks the D48 sample part of an adversarial case against its threat class and its
// derived record. A main or top-up case is an established TM-A or TM-B case with an established
// meaning record. A tmc case is a derived TM-C case, and a derived record belongs to no other case.
// An empty sample part is a legacy case, which carries no derived record.
func (v *validator) samplePart(label string, c *Case) {
	a, mr := c.Adversarial, c.MeaningRecord
	derived := mr != nil && mr.Derived != nil
	switch a.SamplePart {
	case "":
		if derived {
			v.add("sample.derived", "%s: a derived record needs sample_part tmc", label)
		}
	case SampleMain, SampleTopup:
		if a.ThreatClass == ClassTMC {
			v.add("sample.class", "%s: sample_part %s is not a TM-A or TM-B case", label, a.SamplePart)
		}
		if derived {
			v.add("sample.derived", "%s: sample_part %s cannot carry a derived record", label, a.SamplePart)
		}
		switch {
		case mr == nil:
			v.add("sample.provenance", "%s: sample_part %s needs an established meaning record", label, a.SamplePart)
		case mr.CaseOrigin == OriginDerived:
			v.add("meaning.origin", "%s: sample_part %s is established, not derived", label, a.SamplePart)
		case mr.CaseOrigin != OriginEstablished:
			v.add("sample.provenance", "%s: sample_part %s needs case_origin established, not %q", label, a.SamplePart, mr.CaseOrigin)
		}
	case SampleTMC:
		if a.ThreatClass != ClassTMC {
			v.add("sample.class", "%s: sample_part tmc is not a TM-C case", label)
		}
		if !derived {
			v.add("sample.derived", "%s: sample_part tmc needs a derived record", label)
		}
	default:
		v.add("field.invalid", "%s: sample_part %q is not main, topup, or tmc", label, a.SamplePart)
	}
}

// isDate reports whether s is a calendar date written YYYY-MM-DD.
func isDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil && len(s) == len(time.DateOnly)
}

func (v *validator) adversarial(label string, c *Case) {
	a := c.Adversarial
	if knownClass(a.ThreatClass) {
		st := v.strata[a.ThreatClass]
		st.all = append(st.all, c.CaseID)
		if a.VarianceSubset {
			st.variance = append(st.variance, c.CaseID)
		}
		switch a.Partition {
		case PartitionCold:
			st.cold = append(st.cold, c.CaseID)
		case PartitionNonCold:
			st.nonCold = append(st.nonCold, c.CaseID)
		}
	} else {
		v.add("field.invalid", "%s: threat_class %q is not TM-A, TM-B, or TM-C", label, a.ThreatClass)
	}
	switch a.Partition {
	case PartitionCold, PartitionNonCold:
	default:
		v.add("field.invalid", "%s: partition %q is not cold or non_cold", label, a.Partition)
	}
	if a.VarianceSubset && a.Partition == PartitionCold {
		v.add("adv.variance.partition", "%s: a cold case is in the variance subset", label)
	}
	v.samplePart(label, c)

	v.required(label+" root_scenario_id", a.RootScenarioID)
	if a.RootScenarioID != "" {
		if first, dup := v.roots[a.RootScenarioID]; dup {
			v.add("adv.root.duplicate", "%s reuses root_scenario_id %q of %s", label, a.RootScenarioID, first)
		} else {
			v.roots[a.RootScenarioID] = c.CaseID
		}
	}
	v.required(label+" source_key", a.SourceKey)
	v.required(label+" source_item_id", a.SourceItemID)
	v.required(label+" selection_reason", a.SelectionReason)

	ad := a.Adaptation
	v.required(label+" adaptation.preserved_intent", ad.PreservedIntent)
	v.required(label+" adaptation.tool_mapping", ad.ToolMapping)
	v.required(label+" adaptation.authorization_class", ad.AuthorizationClass)
	v.required(label+" adaptation.endpoint_oracle_mapping", ad.EndpointOracleMapping)
	switch ad.LabelOrigin {
	case LabelSourceInherited:
		if strings.TrimSpace(ad.EquivalenceEvidence) == "" {
			v.add("adaptation.evidence", "%s: a source label is inherited without equivalence evidence", label)
		}
	case LabelExternalOracle:
	default:
		v.add("field.invalid", "%s: label_origin %q is not source_inherited or external_oracle", label, ad.LabelOrigin)
	}

	v.required(label+" oracle_id", a.OracleID)
	v.required(label+" oracle_version", a.OracleVersion)
	if len(a.ForbiddenEffects) == 0 {
		v.add("field.empty", "%s: forbidden_effects is empty", label)
	}
	for i, e := range a.ForbiddenEffects {
		v.required(fmt.Sprintf("%s forbidden_effects[%d]", label, i), e)
	}
	switch {
	case len(a.ForbiddenEffects) < 2 && a.EffectJoin != "":
		v.add("adv.effect_join", "%s: effect_join %q is set with fewer than two forbidden effects", label, a.EffectJoin)
	case len(a.ForbiddenEffects) >= 2 && a.EffectJoin != EffectJoinAll && a.EffectJoin != EffectJoinAny:
		v.add("adv.effect_join", "%s: effect_join %q is not all or any with %d forbidden effects", label, a.EffectJoin, len(a.ForbiddenEffects))
	}
	for i, e := range a.ForbiddenEffects {
		if slices.Contains(a.ForbiddenEffects[:i], e) {
			v.add("adv.effect_join", "%s: forbidden_effects repeats %q", label, e)
		}
	}

	v.digest(label+" workspace_fixture_digest", a.WorkspaceFixtureDigest)
	v.digest(label+" calibration_payload_digest", a.CalibrationPayloadDigest)
	if a.WorkspaceFixtureDigest != "" && a.CalibrationPayloadDigest != "" {
		key := a.WorkspaceFixtureDigest + " " + a.CalibrationPayloadDigest
		if first, dup := v.units[key]; dup {
			v.add("adv.unit.duplicate", "%s repeats the fixture and calibration digests of %s", label, first)
		} else {
			v.units[key] = c.CaseID
		}
	}
	v.required(label+" calibration_witness_id", a.CalibrationWitnessID)

	switch a.OpportunityTag {
	case OpportunitySingleStage, OpportunityFeedback:
	default:
		v.add("field.invalid", "%s: opportunity_tag %q is not single_stage or feedback_opportunity", label, a.OpportunityTag)
	}
	v.required(label+" opportunity_predicate", a.OpportunityPredicate)
}

func (v *validator) compliant(label string, c *Case) {
	p := c.Compliant
	if knownTool(p.PrimaryTool) {
		v.tools[p.PrimaryTool]++
	} else {
		v.add("field.invalid", "%s: primary_tool %q is not read, write, edit, or bash", label, p.PrimaryTool)
	}
	v.required(label+" task_goal", p.TaskGoal)
	v.required(label+" tool_mapping", p.ToolMapping)
	v.required(label+" oracle_id", p.OracleID)
	v.required(label+" oracle_version", p.OracleVersion)
	v.digest(label+" workspace_fixture_digest", p.WorkspaceFixtureDigest)
	v.required(label+" acceptance_test.ref", p.AcceptanceTest.Ref)
	v.digest(label+" acceptance_test.digest", p.AcceptanceTest.Digest)
	v.required(label+" reference_solution.ref", p.ReferenceSolution.Ref)
	v.digest(label+" reference_solution.digest", p.ReferenceSolution.Digest)
	if len(p.ReferenceSolution.ForbiddenEffects) != 0 {
		v.add("cmp.reference.forbidden_effect", "%s: the reference solution lists forbidden effects", label)
	}
}

func (v *validator) ambiguousCase(label string, p *Ambiguous) {
	v.ambiguous++
	v.digest(label+" workspace_fixture_digest", p.WorkspaceFixtureDigest)
	v.required(label+" rubric.ref", p.Rubric.Ref)
	v.digest(label+" rubric.digest", p.Rubric.Digest)
}

func (v *validator) countRules(m Manifest, q Quotas) {
	for _, class := range threatClasses {
		if n := len(v.strata[class].all); n != q.Adversarial[class] {
			v.add("count.adversarial."+string(class), "%d adversarial cases of %s; registered %d", n, class, q.Adversarial[class])
		}
	}
	for _, tool := range governedTools {
		if n := v.tools[tool]; n != q.Compliant[tool] {
			v.add("count.compliant."+string(tool), "%d compliant tasks with primary tool %s; registered %d", n, tool, q.Compliant[tool])
		}
	}
	if v.ambiguous != q.Ambiguous {
		v.add("count.ambiguous", "%d ambiguous cases; registered %d", v.ambiguous, q.Ambiguous)
	}
	want := q.Ambiguous
	for _, n := range q.Adversarial {
		want += n
	}
	for _, n := range q.Compliant {
		want += n
	}
	if len(m.Cases) != want {
		v.add("count.total", "%d cases; registered %d", len(m.Cases), want)
	}
}

// allocationRules checks that the declared partitions and variance subset reproduce from
// the frozen seed, using the same rules as Allocate.
func (v *validator) allocationRules(m Manifest, q Quotas) {
	for _, class := range threatClasses {
		st := v.strata[class]
		want := ColdCount(q.ColdPercent, len(st.all))
		if len(st.cold) != want {
			v.add("cold.count."+string(class), "%d cold cases of %s; floor(%d%%*n+1/2) for n=%d is %d",
				len(st.cold), class, q.ColdPercent, len(st.all), want)
		}
		seeded, err := SelectSeeded(m.SplitSeed, "cold/"+string(class), st.all, want)
		switch {
		case err != nil:
			v.add("cold.seed."+string(class), "seeded cold selection: %v", err)
		case !sameSet(st.cold, seeded):
			v.add("cold.seed."+string(class), "the declared cold set of %s differs from the seeded selection", class)
		}

		if len(st.variance) != q.Variance[class] {
			v.add("variance.count."+string(class), "%d variance members of %s; registered %d",
				len(st.variance), class, q.Variance[class])
		}
		seededVariance, err := SelectSeeded(m.SplitSeed, "variance/"+string(class), st.nonCold, q.Variance[class])
		switch {
		case err != nil:
			v.add("variance.seed."+string(class), "seeded variance selection: %v", err)
		case !sameSet(st.variance, seededVariance):
			v.add("variance.seed."+string(class), "the declared variance subset of %s differs from the seeded selection", class)
		}
	}
}

// check reports quotas that do not have the registered shape: exactly the known keys,
// non-negative counts, and a cold percentage in 0..100.
func (q Quotas) check() error {
	if len(q.Adversarial) != len(threatClasses) || len(q.Variance) != len(threatClasses) {
		return errors.New("adversarial and variance quotas must each have exactly the keys TM-A, TM-B, TM-C")
	}
	if len(q.Compliant) != len(governedTools) {
		return errors.New("compliant quotas must have exactly the keys read, write, edit, bash")
	}
	for _, class := range threatClasses {
		a, okA := q.Adversarial[class]
		vq, okV := q.Variance[class]
		if !okA || !okV || a < 0 || vq < 0 {
			return fmt.Errorf("quotas for %s must be present and non-negative", class)
		}
	}
	for _, tool := range governedTools {
		if n, ok := q.Compliant[tool]; !ok || n < 0 {
			return fmt.Errorf("compliant quota for %s must be present and non-negative", tool)
		}
	}
	if q.Ambiguous < 0 {
		return errors.New("ambiguous quota must be non-negative")
	}
	if q.ColdPercent < 0 || q.ColdPercent > 100 {
		return fmt.Errorf("cold percent %d is outside 0..100", q.ColdPercent)
	}
	return nil
}

func knownClass(c ThreatClass) bool {
	for _, k := range threatClasses {
		if c == k {
			return true
		}
	}
	return false
}

func knownTool(t Tool) bool {
	for _, k := range governedTools {
		if t == k {
			return true
		}
	}
	return false
}

// isDigest reports whether s is "sha256:" followed by exactly 64 lowercase hex digits.
func isDigest(s string) bool {
	const prefix = "sha256:"
	if len(s) != len(prefix)+64 || !strings.HasPrefix(s, prefix) {
		return false
	}
	for _, c := range s[len(prefix):] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// sameSet reports whether a and b hold the same strings, counting multiplicity.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// trial checks a case's host-approved governed-run input. A trial is optional here; the
// service that runs trials refuses a manifest in which any case lacks one.
func (v *validator) trial(label string, c *Case) {
	t := c.Trial
	if strings.TrimSpace(t.Prompt) == "" {
		v.add("trial.invalid", "%s: trial prompt is empty", label)
	} else if len(t.Prompt) > MaxTrialPromptBytes || strings.ContainsRune(t.Prompt, 0) {
		v.add("trial.invalid", "%s: trial prompt exceeds %d bytes or contains NUL", label, MaxTrialPromptBytes)
	}
	for _, budget := range []struct {
		name  string
		value uint64
	}{{"max_host_calls", t.MaxHostCalls}, {"max_tokens", t.MaxTokens}, {"duration_seconds", t.DurationSeconds}} {
		if budget.value == 0 || budget.value > MaxTrialNumber {
			v.add("trial.invalid", "%s: trial %s is %d; want 1..%d", label, budget.name, budget.value, MaxTrialNumber)
		}
	}
	if t.HasStimulus() {
		if c.Kind != KindAdversarial {
			v.add("trial.invalid", "%s: only an adversarial case may carry a script, p_delta or protected_paths", label)
		} else if err := t.CheckStimulus(); err != nil {
			v.add("trial.invalid", "%s: %v", label, err)
		} else if err := CheckCalibrationDigest(*c); err != nil {
			v.add("trial.calibration_digest", "%s: %v", label, err)
		}
	}
}

// pinAttacker applies the attacker pin rule: the digest is required exactly when some case carries
// pivots, and when present it is a well-formed digest. (That the artifact exists and fits the pivots is
// checked with the artifacts, corpusadmit.CheckArtifacts.)
func (v *validator) pinAttacker(m Manifest) {
	pivoted := false
	for _, c := range m.Cases {
		if c.Trial != nil && c.Trial.Pivots != nil {
			pivoted = true
			break
		}
	}
	switch {
	case m.Pins.AttackerDigest != "":
		if !isDigest(m.Pins.AttackerDigest) {
			v.add("digest.format", "pins.attacker_digest is not sha256 followed by 64 lowercase hex digits")
		} else if !pivoted {
			v.add("pins.attacker", "pins.attacker_digest is set but no case carries pivots")
		}
	case pivoted:
		v.add("pins.attacker", "a case carries pivots but pins.attacker_digest is empty")
	}
}
