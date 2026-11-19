package evalreport

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/usdledger"
)

const (
	kindAdversarial = "adversarial"
	kindCompliant   = "compliant"
)

// manifestIndex maps manifest case IDs to strata and counts distinct IDs.
type manifestIndex struct {
	stratumOf map[string]map[string]string // kind -> case ID -> stratum key
	distinct  map[string][]string          // kind -> sorted distinct IDs
	duplicate map[string][]string          // kind -> sorted IDs listed more than once
	ambiguous int
	strata    map[string]*Stratum
}

func indexManifest(m evalcorpus.Manifest) (*manifestIndex, error) {
	idx := &manifestIndex{
		stratumOf: map[string]map[string]string{kindAdversarial: {}, kindCompliant: {}},
		distinct:  map[string][]string{kindAdversarial: {}, kindCompliant: {}},
		duplicate: map[string][]string{kindAdversarial: {}, kindCompliant: {}},
		strata:    map[string]*Stratum{},
	}
	dup := map[string]map[string]bool{kindAdversarial: {}, kindCompliant: {}}
	for i := range m.Cases {
		c := &m.Cases[i]
		var kind, key string
		switch c.Kind {
		case evalcorpus.KindAdversarial:
			kind, key = kindAdversarial, kindAdversarial+"/unclassified"
			if a := c.Adversarial; a != nil {
				key = fmt.Sprintf("%s/%s/%s", kindAdversarial, orUnset(string(a.ThreatClass)), orUnset(string(a.Partition)))
				if !inH1(c) {
					key = fmt.Sprintf("%s/%s/%s/%s", kindAdversarial, a.SamplePart, a.ThreatClass, orUnset(string(a.Partition)))
				}
			}
		case evalcorpus.KindCompliant:
			kind, key = kindCompliant, kindCompliant+"/unclassified"
			if p := c.Compliant; p != nil {
				key = kindCompliant + "/" + orUnset(string(p.PrimaryTool))
			}
		case evalcorpus.KindAmbiguous:
			idx.ambiguous++
			continue
		default:
			return nil, fmt.Errorf("evalreport: case %q has unknown kind %q", c.CaseID, c.Kind)
		}
		if _, seen := idx.stratumOf[kind][c.CaseID]; seen {
			dup[kind][c.CaseID] = true
			continue
		}
		idx.stratumOf[kind][c.CaseID] = key
		idx.distinct[kind] = append(idx.distinct[kind], c.CaseID)
		st := idx.strata[key]
		if st == nil {
			st = &Stratum{Key: key, Kind: kind}
			idx.strata[key] = st
		}
		st.Registered++
	}
	for kind := range idx.distinct {
		slices.Sort(idx.distinct[kind])
		idx.duplicate[kind] = append([]string{}, slices.Sorted(maps.Keys(dup[kind]))...)
	}
	return idx, nil
}

func orUnset(s string) string {
	if s == "" {
		return "unassigned"
	}
	return s
}

// stratumFor returns the stratum of a registered record ID.
func (idx *manifestIndex) stratumFor(kind, id string) (*Stratum, error) {
	st := idx.strata[idx.stratumOf[kind][id]]
	if st == nil {
		return nil, fmt.Errorf("evalreport: internal: scored %s %q has no stratum", kind, id)
	}
	return st, nil
}

// Sentinel errors for rejected inputs. Build wraps them in an *InputError that
// lists the offending IDs, so errors.Is selects the class and errors.As reaches
// the IDs.
var (
	// ErrUnregisteredRecord means a trial record names an ID that the manifest
	// does not register under that record's kind.
	ErrUnregisteredRecord = errors.New("evalreport: trial records for IDs not registered in the manifest")
	// ErrDuplicatePrimary means a case has repeated primary records that
	// evalscore does not allow: only earlier attempts that are evidenced
	// external-outage reruns may be superseded.
	ErrDuplicatePrimary = errors.New("evalreport: repeated primary records not explained by an evidenced external-outage rerun")
	// ErrScoringRejected means evalscore rejected the record set for a reason that
	// is not a repeated primary record, such as a wrong schema or a malformed record.
	ErrScoringRejected = errors.New("evalreport: evalscore rejected the trial records")
)

// InputError is the error Build returns for ErrUnregisteredRecord,
// ErrDuplicatePrimary and ErrScoringRejected. Adversarial and Compliant are
// sorted, distinct IDs, and are empty for ErrScoringRejected.
type InputError struct {
	Err         error // ErrUnregisteredRecord, ErrDuplicatePrimary or ErrScoringRejected
	Adversarial []string
	Compliant   []string
	Cause       error // evalscore's error, when it produced this one
}

func (e *InputError) Error() string {
	s := e.Err.Error()
	if len(e.Adversarial) > 0 {
		s += fmt.Sprintf("; adversarial %q", e.Adversarial)
	}
	if len(e.Compliant) > 0 {
		s += fmt.Sprintf("; compliant %q", e.Compliant)
	}
	if e.Cause != nil {
		s += ": " + e.Cause.Error()
	}
	return s
}

// Unwrap lets errors.Is match both the sentinel and the evalscore cause.
func (e *InputError) Unwrap() []error { return []error{e.Err, e.Cause} }

func advKey(r evalscore.AdversarialRecord) string { return r.CaseID }
func cmpKey(r evalscore.CompliantRecord) string   { return r.TaskID }

func advFirst(r evalscore.AdversarialRecord) evalscore.AdversarialRecord { r.Attempt = 1; return r }
func cmpFirst(r evalscore.CompliantRecord) evalscore.CompliantRecord     { r.Attempt = 1; return r }

// unregistered returns the sorted distinct record IDs of one kind that the
// manifest does not register under that kind.
func unregistered[T any](idx *manifestIndex, kind string, recs []T, key func(T) string) []string {
	ids := map[string]bool{}
	for _, r := range recs {
		if _, ok := idx.stratumOf[kind][key(r)]; !ok {
			ids[key(r)] = true
		}
	}
	return slices.Sorted(maps.Keys(ids))
}

// rejectedRepeats returns the sorted IDs that have several records, that
// evalscore.Summarize rejects when given those records alone, and whose rejection
// comes from the repetition. It decides nothing itself: evalscore's validation is
// the only rule. A group is a repetition problem only if every one of its records
// is accepted by evalscore when taken alone as a first attempt. If any single
// record is rejected by itself, the rejection is about that record or the input
// as a whole (a wrong schema, an empty channel), not about the repeat. It is
// called only after Summarize has rejected the whole set, to say which cases to
// fix.
func rejectedRepeats[T any](recs []T, key func(T) string, alone func([]T) evalscore.Input, asFirst func(T) T) []string {
	groups := map[string][]T{}
	for _, r := range recs {
		groups[key(r)] = append(groups[key(r)], r)
	}
	bad := []string{}
	for _, id := range slices.Sorted(maps.Keys(groups)) {
		if len(groups[id]) < 2 {
			continue
		}
		if _, err := evalscore.Summarize(alone(groups[id])); err == nil {
			continue
		}
		repeatOnly := true
		for _, r := range groups[id] {
			if _, err := evalscore.Summarize(alone([]T{asFirst(r)})); err != nil {
				repeatOnly = false
				break
			}
		}
		if repeatOnly {
			bad = append(bad, id)
		}
	}
	return bad
}

// scoringError explains why evalscore rejected t. If some repeated case is
// rejected because of its repetition (see rejectedRepeats), the error is an
// ErrDuplicatePrimary naming those cases, with evalscore's message attached.
// Otherwise the rejection is not about repeats, and the error is an ErrScoringRejected
// that carries evalscore's error as its cause and names no case.
func scoringError(t evalscore.Input, cause error) error {
	adv := rejectedRepeats(t.Adversarial, advKey, func(g []evalscore.AdversarialRecord) evalscore.Input {
		return evalscore.Input{Schema: t.Schema, Adversarial: g}
	}, advFirst)
	cmp := rejectedRepeats(t.Compliant, cmpKey, func(g []evalscore.CompliantRecord) evalscore.Input {
		return evalscore.Input{Schema: t.Schema, Compliant: g}
	}, cmpFirst)
	if len(adv) == 0 && len(cmp) == 0 {
		return &InputError{Err: ErrScoringRejected, Cause: cause}
	}
	return &InputError{Err: ErrDuplicatePrimary, Adversarial: adv, Compliant: cmp, Cause: cause}
}

// Build assembles the report. Inputs are not modified. It fails closed: it
// returns an error, never a report, for a malformed ledger snapshot, a manifest
// with an unknown case kind, a record whose ID is not registered
// (ErrUnregisteredRecord), and any record set that evalscore rejects, naming
// repeated primary records as ErrDuplicatePrimary. No record is filtered before
// scoring. A registered case without a record yields a report whose coverage is
// not exact and whose H1.RuleSatisfied is false.
func Build(in Inputs) (Report, error) {
	if err := checkSnapshot(in.Ledger); err != nil {
		return Report{}, err
	}
	if err := checkAttributionDigests(in.AttributionsDigest, in.AttributionRubricDigest); err != nil {
		return Report{}, err
	}
	manifestDigest, err := evalcorpus.Digest(in.Manifest)
	if err != nil {
		return Report{}, fmt.Errorf("evalreport: manifest digest: %w", err)
	}
	trialsDigest, err := digestTrials(in.Trials)
	if err != nil {
		return Report{}, err
	}
	ledgerDigest, err := digestLedger(in.Ledger)
	if err != nil {
		return Report{}, err
	}
	full := in
	in, separate := splitH1(in) // from here on, in holds the H1 population only (D48)
	idx, err := indexManifest(in.Manifest)
	if err != nil {
		return Report{}, err
	}

	if adv, cmp := unregistered(idx, kindAdversarial, in.Trials.Adversarial, advKey),
		unregistered(idx, kindCompliant, in.Trials.Compliant, cmpKey); len(adv)+len(cmp) > 0 {
		return Report{}, &InputError{Err: ErrUnregisteredRecord, Adversarial: adv, Compliant: cmp}
	}

	scoring := in.Trials // the record slices are shared; Summarize clones before sorting
	// The registered identities come from the manifest, whatever the caller set.
	scoring.Registered = &evalscore.RegisteredIDs{
		AdversarialIDs: in.Manifest.CaseIDs(evalcorpus.KindAdversarial),
		CompliantIDs:   in.Manifest.CaseIDs(evalcorpus.KindCompliant),
	}
	sum, err := evalscore.Summarize(scoring)
	if err != nil {
		return Report{}, scoringError(in.Trials, err)
	}
	cdh, err := cdhOf(in, idx)
	if err != nil {
		return Report{}, err
	}
	postDenial, err := postDenialOf(in.Trials.PostDenialTrials, idx)
	if err != nil {
		return Report{}, err
	}

	recordIDs := map[string]map[string]bool{kindAdversarial: {}, kindCompliant: {}}
	for _, r := range in.Trials.Adversarial {
		recordIDs[kindAdversarial][r.CaseID] = true
	}
	for _, r := range in.Trials.Compliant {
		recordIDs[kindCompliant][r.TaskID] = true
	}
	scored := map[string]int{}
	for _, c := range sum.Cases {
		st, err := idx.stratumFor(c.Kind, c.ID)
		if err != nil {
			return Report{}, err
		}
		st.Scored++
		addDisposition(&st.Dispositions, c.Disposition)
		scored[c.Kind]++
	}
	for _, ir := range sum.InvalidRecords {
		st, err := idx.stratumFor(ir.Kind, ir.ID)
		if err != nil {
			return Report{}, err
		}
		st.InvalidRecords++
	}
	cov := Coverage{
		UnscoredAmbiguous: idx.ambiguous,
		Adversarial:       coverKind(idx, kindAdversarial, recordIDs[kindAdversarial], scored[kindAdversarial]),
		Compliant:         coverKind(idx, kindCompliant, recordIDs[kindCompliant], scored[kindCompliant]),
	}
	cov.Exact = cov.Adversarial.exact() && cov.Compliant.exact()

	strata := make([]Stratum, 0, len(idx.strata))
	for _, key := range slices.Sorted(maps.Keys(idx.strata)) {
		strata = append(strata, *idx.strata[key])
	}

	r := Report{
		Schema: Schema,
		Bindings: Bindings{
			ManifestDigest:       manifestDigest,
			TrialsDigest:         trialsDigest,
			LedgerID:             in.Ledger.LedgerID,
			LedgerHead:           in.Ledger.HeadSHA256,
			LedgerRecords:        in.Ledger.Records,
			LedgerSnapshotDigest: ledgerDigest,

			AttributionsDigest:      in.AttributionsDigest,
			AttributionRubricDigest: in.AttributionRubricDigest,
		},
		Coverage:               cov,
		Strata:                 strata,
		Configurations:         sum.Configurations,
		PostDenialContinuation: postDenial,
		CDH:                    cdh,
		Calibration:            calibrationOf(sum.Configurations.P),
		Observed:               sum.Observed,
		InvalidRecords:         sum.InvalidRecords,
		Clauses:                sum.Clauses,
		Metrics:                sum.Metrics,
		H1:                     requireExactCoverage(sum.H1, cov.Exact),
		Reruns:                 rerunCases(sum.Cases),
		ProviderRetries:        providerRetries(in.Trials.ProviderRetries),
		Spend:                  spendOf(in.Ledger, PlannedUnits(full.Manifest)),
	}
	if r.Separate, err = separateStrata(full, separate); err != nil {
		return Report{}, err
	}
	if full.D48 != nil {
		if r.Groups, err = groupsOf(full, *full.D48); err != nil {
			return Report{}, err
		}
	}
	r.Flags = flagsOf(r)
	if r.Digest, err = r.ComputeDigest(); err != nil {
		return Report{}, err
	}
	return r, nil
}

// requireExactCoverage is a fail-closed guard, not re-scoring: H1.RuleSatisfied
// is false whenever coverage is not exact, even if evalscore's value were true.
// Every other H1 field, including Blockers, stays as evalscore computed it; the
// "coverage_not_exact" flag reports the cause.
func requireExactCoverage(h evalscore.H1Summary, exact bool) evalscore.H1Summary {
	if !exact {
		h.RuleSatisfied = false
	}
	return h
}

func (k KindCoverage) exact() bool {
	return len(k.Missing) == 0 && len(k.ManifestDuplicate) == 0
}

// coverKind compares the distinct registered IDs of one kind with the record IDs
// of that kind (all of which are registered by now). The lists are sorted and
// never nil.
func coverKind(idx *manifestIndex, kind string, records map[string]bool, scored int) KindCoverage {
	k := KindCoverage{
		Registered:        len(idx.distinct[kind]),
		Scored:            scored,
		Missing:           []string{},
		ManifestDuplicate: idx.duplicate[kind],
	}
	for _, id := range idx.distinct[kind] {
		if !records[id] {
			k.Missing = append(k.Missing, id)
			idx.strata[idx.stratumOf[kind][id]].Missing++
		}
	}
	return k
}

func addDisposition(c *evalscore.DispositionCounts, label string) {
	switch label {
	case string(evalscore.ValidPass):
		c.ValidPass++
	case string(evalscore.ValidSecurityFailure):
		c.ValidSecurityFailure++
	case string(evalscore.ValidFrictionObstruction):
		c.ValidFrictionObstruction++
	case string(evalscore.ConservativeFailure):
		c.ConservativeFailure++
	case string(evalscore.ExternalInfrastructureRerun):
		c.ExternalInfrastructureRerun++
	case string(evalscore.Unsupported):
		c.Unsupported++
	case string(evalscore.ValidTaskFailure):
		c.ValidTaskFailure++
	default: // "OPEN"
		c.Open++
	}
}

func calibrationOf(p evalscore.PConfig) Calibration {
	c := Calibration{Pairs: p.Pairs}
	for _, ch := range p.Channels {
		c.StaticManifested += ch.StaticManifested
		c.AdaptiveManifested += ch.AdaptiveManifested
		c.BothManifested += ch.Manifested
		c.NonDiscriminative += ch.NonDiscriminative
	}
	return c
}

func snapErr(format string, args ...any) error {
	return fmt.Errorf("evalreport: ledger snapshot: "+format, args...)
}

var sha256DigestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// checkAttributionDigests accepts either two digests or none.
func checkAttributionDigests(attributions, rubric string) error {
	if attributions == "" && rubric == "" {
		return nil
	}
	if !sha256DigestRE.MatchString(attributions) || !sha256DigestRE.MatchString(rubric) {
		return fmt.Errorf("evalreport: attributions digest %q and rubric digest %q must both be sha256:<64 hex> or both empty", attributions, rubric)
	}
	return nil
}

// addNonNegative adds two non-negative values and reports overflow.
func addNonNegative(a, b int64) (int64, bool) {
	if a > math.MaxInt64-b {
		return 0, false
	}
	return a + b, true
}

// checkSnapshot requires a bindable head and a well-formed, self-consistent
// snapshot, so a hand-built one cannot make the spend section disagree with
// itself or report a budget it does not have.
//
// usdledger defines one threshold relationship: Config.validate requires
// MinPerUnitWorstCaseMicroUSD in (0, CapMicroUSD], and Snapshot.StopRule is
// RemainingMicroUSD < MinPerUnitWorstCaseMicroUSD. Both are enforced here. The
// threshold's value is a configuration choice that is not stored in the
// ledger file, so nothing else is checked about it. RemainingMicroUSD alone may
// be negative (a recorded overage commits more than the cap).
func checkSnapshot(s usdledger.Snapshot) error {
	if len(s.HeadSHA256) != 64 {
		return snapErr("head must be 64 lowercase hex digits")
	}
	for _, c := range s.HeadSHA256 {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return snapErr("head must be 64 lowercase hex digits")
		}
	}
	for _, f := range []struct {
		name string
		v    int64
	}{
		{"cap", s.CapMicroUSD}, {"min per unit worst case", s.MinPerUnitWorstCaseMicroUSD},
		{"open", s.OpenMicroUSD}, {"uncertain", s.UncertainMicroUSD},
		{"settled", s.SettledMicroUSD}, {"committed", s.CommittedMicroUSD},
		{"open units", int64(s.OpenUnits)}, {"uncertain units", int64(s.UncertainUnits)},
		{"settled units", int64(s.SettledUnits)},
	} {
		if f.v < 0 {
			return snapErr("%s is negative (%d)", f.name, f.v)
		}
	}
	if s.CapMicroUSD == 0 {
		return snapErr("cap must be positive")
	}
	if s.MinPerUnitWorstCaseMicroUSD == 0 || s.MinPerUnitWorstCaseMicroUSD > s.CapMicroUSD {
		return snapErr("min per unit worst case %d must be in (0, cap %d]", s.MinPerUnitWorstCaseMicroUSD, s.CapMicroUSD)
	}
	sum, ok := addNonNegative(s.OpenMicroUSD, s.UncertainMicroUSD)
	if ok {
		sum, ok = addNonNegative(sum, s.SettledMicroUSD)
	}
	if !ok {
		return snapErr("open+uncertain+settled overflows int64")
	}
	if s.CommittedMicroUSD != sum {
		return snapErr("committed %d != open+uncertain+settled %d", s.CommittedMicroUSD, sum)
	}
	// Cap and committed are both non-negative, so the difference cannot overflow.
	if s.RemainingMicroUSD != s.CapMicroUSD-s.CommittedMicroUSD {
		return snapErr("remaining %d != cap-committed", s.RemainingMicroUSD)
	}
	if want := s.RemainingMicroUSD < s.MinPerUnitWorstCaseMicroUSD; s.StopRule != want {
		return snapErr("stop rule %t, but remaining %d vs threshold %d requires %t",
			s.StopRule, s.RemainingMicroUSD, s.MinPerUnitWorstCaseMicroUSD, want)
	}
	if _, ok := totalUnits(s); !ok {
		return snapErr("unit counts overflow int")
	}
	return nil
}

// totalUnits adds the three non-negative unit counts and reports overflow.
func totalUnits(s usdledger.Snapshot) (int, bool) {
	total := s.OpenUnits
	for _, n := range []int{s.UncertainUnits, s.SettledUnits} {
		if total > math.MaxInt-n {
			return 0, false
		}
		total += n
	}
	return total, true
}

func spendOf(s usdledger.Snapshot, planned int) Spend {
	total, _ := totalUnits(s) // checkSnapshot has already rejected overflow
	sp := Spend{
		LedgerID:              s.LedgerID,
		CapMicroUSD:           s.CapMicroUSD,
		RegisteredCapMicroUSD: RegisteredCapMicroUSD,
		OpenMicroUSD:          s.OpenMicroUSD,
		UncertainMicroUSD:     s.UncertainMicroUSD,
		SettledMicroUSD:       s.SettledMicroUSD,
		CommittedMicroUSD:     s.CommittedMicroUSD,
		RemainingMicroUSD:     s.RemainingMicroUSD,
		OpenUnits:             s.OpenUnits,
		UncertainUnits:        s.UncertainUnits,
		SettledUnits:          s.SettledUnits,
		TotalUnits:            total,
		PlannedUnits:          planned,
		StopRule:              s.StopRule,
		Overspent:             s.Overspent,
		CapMatchesRegistered:  s.CapMicroUSD == RegisteredCapMicroUSD,
		OverCap:               s.CommittedMicroUSD > s.CapMicroUSD || s.CommittedMicroUSD > RegisteredCapMicroUSD,
	}
	sp.UnitsOverPlanned = sp.TotalUnits > planned
	sp.WithinBudget = sp.CapMatchesRegistered && !sp.OverCap && !sp.Overspent
	return sp
}

// digestLedger binds the whole usdledger.Snapshot: the canonical JSON of the
// struct, so every exported field (including any added later) is covered.
func digestLedger(s usdledger.Snapshot) (string, error) {
	raw, err := canonical(s, "")
	if err != nil {
		return "", err
	}
	return domainDigest(LedgerDigestDomain, raw), nil
}

// rerunCases are the cases whose registered rerun was taken, is pending, or was spent or not taken.
func rerunCases(cases []evalscore.CaseOutcome) []evalscore.CaseOutcome {
	var out []evalscore.CaseOutcome
	for _, c := range cases {
		if c.Original != nil || c.Disposition == string(evalscore.ExternalInfrastructureRerun) ||
			c.Reason == evalscore.ReasonRerunSpent || c.Reason == evalscore.ReasonRerunNotTaken {
			out = append(out, c)
		}
	}
	return out
}

// flagsOf lists the report-level conditions that need attention, in a fixed
// order. H1 blockers stay in H1.Blockers, exactly as evalscore computed them.
func flagsOf(r Report) []string {
	flags := []string{}
	add := func(cond bool, name string) {
		if cond {
			flags = append(flags, name)
		}
	}
	add(!r.Coverage.Exact, "coverage_not_exact")
	add(r.Spend.OverCap, "spend_over_cap")
	add(!r.Spend.CapMatchesRegistered, "spend_cap_mismatch")
	add(r.Spend.Overspent, "ledger_overspent")
	add(r.Spend.StopRule, "ledger_stop_rule")
	add(r.Spend.UnitsOverPlanned, "units_over_planned")
	add(r.Groups != nil && !r.Groups.Certification.Certified, "bound_not_certified")
	return flags
}

// CanonicalJSON returns the canonical JSON of r, including its digest.
func (r Report) CanonicalJSON() ([]byte, error) { return canonical(r, "") }

// ComputeDigest recomputes the report digest from r, ignoring r.Digest.
func (r Report) ComputeDigest() (string, error) {
	raw, err := canonical(r, "digest")
	if err != nil {
		return "", err
	}
	return domainDigest(DigestDomain, raw), nil
}

// digestTrials binds the raw record set: every record, including those that
// scoring withholds, sorted by canonical form so record order does not matter.
// Trials.Registered is derived from the manifest and is not part of the set.
func digestTrials(t evalscore.Input) (string, error) {
	adv, err := canonicalSet(t.Adversarial)
	if err != nil {
		return "", err
	}
	cmp, err := canonicalSet(t.Compliant)
	if err != nil {
		return "", err
	}
	postDenial, err := canonicalSet(t.PostDenialTrials)
	if err != nil {
		return "", err
	}
	retries, err := canonicalSet(t.ProviderRetries)
	if err != nil {
		return "", err
	}
	raw, err := canonical(struct {
		CDH              *evalscore.CDHInput `json:"cdh,omitempty"`
		Schema           string              `json:"schema"`
		EVQPassed        bool                `json:"evq_passed"`
		Adversarial      []json.RawMessage   `json:"adversarial"`
		Compliant        []json.RawMessage   `json:"compliant"`
		PostDenialTrials []json.RawMessage   `json:"post_denial_trials,omitempty"`
		ProviderRetries  []json.RawMessage   `json:"provider_retries,omitempty"`
		RerunsClosed     bool                `json:"reruns_closed,omitempty"`
	}{canonicalCDH(t.CDH), t.Schema, t.EVQPassed, adv, cmp, postDenial, retries, t.RerunsClosed}, "")
	if err != nil {
		return "", err
	}
	return domainDigest(TrialsDigestDomain, raw), nil
}

func canonicalSet[T any](recs []T) ([]json.RawMessage, error) {
	parts := make([]string, 0, len(recs))
	for _, r := range recs {
		b, err := canonical(r, "")
		if err != nil {
			return nil, err
		}
		parts = append(parts, string(b))
	}
	sort.Strings(parts)
	out := make([]json.RawMessage, len(parts))
	for i, p := range parts {
		out[i] = json.RawMessage(p)
	}
	return out, nil
}

func domainDigest(domain string, canonicalJSON []byte) string {
	h := sha256.New()
	h.Write([]byte(domain))
	h.Write([]byte{0})
	h.Write(canonicalJSON)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func encode(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// canonical encodes v, re-parses it with numbers kept as written, drops the
// named top-level member, and re-encodes it. Go sorts map keys on encoding, so
// the result has sorted keys and no insignificant whitespace.
func canonical(v any, drop string) ([]byte, error) {
	raw, err := encode(v)
	if err != nil {
		return nil, fmt.Errorf("evalreport: encode: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, fmt.Errorf("evalreport: canonicalize: %w", err)
	}
	if m, ok := generic.(map[string]any); ok && drop != "" {
		delete(m, drop)
	}
	return encode(generic)
}

// inH1 reports whether an adversarial case counts toward H1: every case but the top-up and the TM-C set (D48).
func inH1(c *evalcorpus.Case) bool {
	return c.Adversarial == nil || (c.Adversarial.SamplePart != evalcorpus.SampleTopup && c.Adversarial.SamplePart != evalcorpus.SampleTMC)
}

// splitH1 returns in restricted to the H1 population, and the cases it removed: the top-up and TM-C cases with
// their records. A record of an ID that is no case at all stays in the H1 inputs, where it is refused as unregistered.
func splitH1(in Inputs) (Inputs, separateSet) {
	var sep separateSet
	ids := map[string]bool{}
	for i := range in.Manifest.Cases {
		if c := &in.Manifest.Cases[i]; c.Kind == evalcorpus.KindAdversarial && !inH1(c) {
			ids[c.CaseID] = true
		}
	}
	if len(ids) == 0 {
		return in, sep
	}
	h1 := in
	h1.Manifest.Cases = nil
	for _, c := range in.Manifest.Cases {
		if c.Kind == evalcorpus.KindAdversarial && ids[c.CaseID] {
			sep.manifest.Cases = append(sep.manifest.Cases, c)
		} else {
			h1.Manifest.Cases = append(h1.Manifest.Cases, c)
		}
	}
	h1.Trials.Adversarial = nil
	for _, r := range in.Trials.Adversarial {
		if ids[r.CaseID] {
			sep.records = append(sep.records, r)
		} else {
			h1.Trials.Adversarial = append(h1.Trials.Adversarial, r)
		}
	}
	return h1, sep
}

type separateSet struct {
	manifest evalcorpus.Manifest
	records  []evalscore.AdversarialRecord
}

// separateStrata scores the top-up and TM-C cases on their own, against their own registered identities, and
// returns their strata (nil when there are none). Their outcomes are reported, never pooled (D48).
func separateStrata(full Inputs, sep separateSet) ([]Stratum, error) {
	if len(sep.manifest.Cases) == 0 {
		return nil, nil
	}
	in := evalscore.Input{Schema: full.Trials.Schema, EVQPassed: full.Trials.EVQPassed, Adversarial: sep.records, RerunsClosed: full.Trials.RerunsClosed,
		Registered: &evalscore.RegisteredIDs{AdversarialIDs: sep.manifest.CaseIDs(evalcorpus.KindAdversarial), CompliantIDs: []string{}}}
	sum, err := evalscore.Summarize(in)
	if err != nil {
		return nil, scoringError(evalscore.Input{Schema: in.Schema, Adversarial: sep.records}, err)
	}
	idx, err := indexManifest(sep.manifest)
	if err != nil {
		return nil, err
	}
	records := map[string]bool{}
	for _, r := range sep.records {
		records[r.CaseID] = true
	}
	for _, c := range sum.Cases {
		st, err := idx.stratumFor(c.Kind, c.ID)
		if err != nil {
			return nil, err
		}
		st.Scored++
		addDisposition(&st.Dispositions, c.Disposition)
	}
	for _, ir := range sum.InvalidRecords {
		st, err := idx.stratumFor(ir.Kind, ir.ID)
		if err != nil {
			return nil, err
		}
		st.InvalidRecords++
	}
	coverKind(idx, kindAdversarial, records, 0) // counts each stratum's missing cases
	out := make([]Stratum, 0, len(idx.strata))
	for _, key := range slices.Sorted(maps.Keys(idx.strata)) {
		out = append(out, *idx.strata[key])
	}
	return out, nil
}

// providerRetries is the trials' provider-retry trajectories in case and attempt order, or nil.
func providerRetries(in []evalscore.ProviderRetryTrajectory) []evalscore.ProviderRetryTrajectory {
	if len(in) == 0 {
		return nil
	}
	out := slices.Clone(in)
	slices.SortFunc(out, func(a, b evalscore.ProviderRetryTrajectory) int {
		return cmp.Or(strings.Compare(a.CaseID, b.CaseID), a.Attempt-b.Attempt)
	})
	return out
}
