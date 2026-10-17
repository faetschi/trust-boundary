package evalreport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"

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

// stratumFor returns the stratum of a record ID, creating the "unregistered"
// stratum of its kind for IDs that are not in the manifest.
func (idx *manifestIndex) stratumFor(kind, id string) *Stratum {
	key, ok := idx.stratumOf[kind][id]
	if !ok {
		key = kind + "/unregistered"
	}
	st := idx.strata[key]
	if st == nil {
		st = &Stratum{Key: key, Kind: kind}
		idx.strata[key] = st
	}
	return st
}

// repeated returns the sorted IDs whose records cannot be reduced to one
// primary trial. ASSUMPTION: a repeat is legitimate only as an evidenced
// external-outage rerun; evalscore.Summarize would otherwise fail the whole
// report, so such cases are reported as duplicates and withheld instead.
// Attempt gaps are left to evalscore, which rejects them.
func repeated[T any](recs []T, key func(T) string, attempt func(T) int, classify func(T) evalscore.Result) []string {
	groups := map[string][]T{}
	for _, r := range recs {
		groups[key(r)] = append(groups[key(r)], r)
	}
	bad := []string{}
	for _, id := range slices.Sorted(maps.Keys(groups)) {
		g := groups[id]
		if len(g) < 2 {
			continue
		}
		slices.SortStableFunc(g, func(a, b T) int { return attempt(a) - attempt(b) })
		isBad := false
		for i := 0; i < len(g)-1 && !isBad; i++ {
			if attempt(g[i]) == attempt(g[i+1]) {
				isBad = true
				break
			}
			res := classify(g[i])
			rerun := !res.Open && res.Disposition == evalscore.ExternalInfrastructureRerun
			invalid := res.Open && res.Reason == evalscore.ReasonInvalidEnum
			isBad = !rerun && !invalid
		}
		if isBad {
			bad = append(bad, id)
		}
	}
	return bad
}

func without[T any](recs []T, key func(T) string, drop []string) []T {
	out := make([]T, 0, len(recs))
	for _, r := range recs {
		if !slices.Contains(drop, key(r)) {
			out = append(out, r)
		}
	}
	return out
}

func advKey(r evalscore.AdversarialRecord) string  { return r.CaseID }
func advAttempt(r evalscore.AdversarialRecord) int { return r.Attempt }
func advClassify(r evalscore.AdversarialRecord) evalscore.Result {
	return r.Classify()
}
func cmpKey(r evalscore.CompliantRecord) string  { return r.TaskID }
func cmpAttempt(r evalscore.CompliantRecord) int { return r.Attempt }
func cmpClassify(r evalscore.CompliantRecord) evalscore.Result {
	return r.Classify()
}

// Build assembles the report. Inputs are not modified. It fails on a ledger
// snapshot that is internally inconsistent or has no 64-hex head, on a manifest
// that cannot be canonicalized or has an unknown case kind, and on trial records
// that evalscore rejects after duplicates have been withheld.
func Build(in Inputs) (Report, error) {
	if err := checkSnapshot(in.Ledger); err != nil {
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
	idx, err := indexManifest(in.Manifest)
	if err != nil {
		return Report{}, err
	}

	badAdv := repeated(in.Trials.Adversarial, advKey, advAttempt, advClassify)
	badCmp := repeated(in.Trials.Compliant, cmpKey, cmpAttempt, cmpClassify)
	scoring := evalscore.Input{
		Schema:      in.Trials.Schema,
		EVQPassed:   in.Trials.EVQPassed,
		Adversarial: without(in.Trials.Adversarial, advKey, badAdv),
		Compliant:   without(in.Trials.Compliant, cmpKey, badCmp),
		// The registered identities come from the manifest, whatever the caller set.
		Registered: &evalscore.RegisteredIDs{
			AdversarialIDs: in.Manifest.CaseIDs(evalcorpus.KindAdversarial),
			CompliantIDs:   in.Manifest.CaseIDs(evalcorpus.KindCompliant),
		},
	}
	withheld := len(in.Trials.Adversarial) - len(scoring.Adversarial) + len(in.Trials.Compliant) - len(scoring.Compliant)
	sum, err := evalscore.Summarize(scoring)
	if err != nil {
		return Report{}, fmt.Errorf("evalreport: evalscore: %w", err)
	}

	recordIDs := map[string]map[string]bool{kindAdversarial: {}, kindCompliant: {}}
	for _, r := range in.Trials.Adversarial {
		recordIDs[kindAdversarial][r.CaseID] = true
	}
	for _, r := range in.Trials.Compliant {
		recordIDs[kindCompliant][r.TaskID] = true
	}
	scoredRegistered := map[string]int{}
	for _, c := range sum.Cases {
		st := idx.stratumFor(c.Kind, c.ID)
		st.Scored++
		addDisposition(&st.Dispositions, c.Disposition)
		if _, ok := idx.stratumOf[c.Kind][c.ID]; ok {
			scoredRegistered[c.Kind]++
		}
	}
	for _, ir := range sum.InvalidRecords {
		idx.stratumFor(ir.Kind, ir.ID).InvalidRecords++
	}
	cov := Coverage{
		UnscoredAmbiguous: idx.ambiguous,
		WithheldRecords:   withheld,
		Adversarial:       coverKind(idx, kindAdversarial, recordIDs[kindAdversarial], badAdv, scoredRegistered[kindAdversarial]),
		Compliant:         coverKind(idx, kindCompliant, recordIDs[kindCompliant], badCmp, scoredRegistered[kindCompliant]),
	}
	cov.Exact = cov.Adversarial.exact() && cov.Compliant.exact()

	strata := make([]Stratum, 0, len(idx.strata))
	for _, key := range slices.Sorted(maps.Keys(idx.strata)) {
		strata = append(strata, *idx.strata[key])
	}

	r := Report{
		Schema: Schema,
		Bindings: Bindings{
			ManifestDigest: manifestDigest,
			TrialsDigest:   trialsDigest,
			LedgerID:       in.Ledger.LedgerID,
			LedgerHead:     in.Ledger.HeadSHA256,
			LedgerRecords:  in.Ledger.Records,
		},
		Coverage:       cov,
		Strata:         strata,
		Configurations: sum.Configurations,
		Calibration:    calibrationOf(sum.Configurations.P),
		Observed:       sum.Observed,
		InvalidRecords: sum.InvalidRecords,
		Clauses:        sum.Clauses,
		Metrics:        sum.Metrics,
		H1:             sum.H1,
		Spend:          spendOf(in.Ledger),
	}
	r.Flags = flagsOf(r)
	if r.Digest, err = r.ComputeDigest(); err != nil {
		return Report{}, err
	}
	return r, nil
}

func (k KindCoverage) exact() bool {
	return len(k.Missing) == 0 && len(k.Extra) == 0 && len(k.Duplicate) == 0 && len(k.ManifestDuplicate) == 0
}

// coverKind compares the distinct registered IDs of one kind with the record IDs
// of that kind. The lists are sorted and never nil.
func coverKind(idx *manifestIndex, kind string, records map[string]bool, bad []string, scored int) KindCoverage {
	k := KindCoverage{
		Registered:        len(idx.distinct[kind]),
		Scored:            scored,
		Missing:           []string{},
		Extra:             []string{},
		Duplicate:         bad,
		ManifestDuplicate: idx.duplicate[kind],
	}
	for _, id := range idx.distinct[kind] {
		if !records[id] {
			k.Missing = append(k.Missing, id)
			idx.strata[idx.stratumOf[kind][id]].Missing++
		}
	}
	for _, id := range slices.Sorted(maps.Keys(records)) {
		if _, ok := idx.stratumOf[kind][id]; !ok {
			k.Extra = append(k.Extra, id)
		}
	}
	for _, id := range bad {
		if key, ok := idx.stratumOf[kind][id]; ok {
			idx.strata[key].Duplicate++
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

// checkSnapshot requires a bindable head and self-consistent money fields, so a
// hand-built snapshot cannot make the spend section disagree with itself.
func checkSnapshot(s usdledger.Snapshot) error {
	if len(s.HeadSHA256) != 64 {
		return fmt.Errorf("evalreport: ledger head must be 64 lowercase hex digits")
	}
	for _, c := range s.HeadSHA256 {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("evalreport: ledger head must be 64 lowercase hex digits")
		}
	}
	if s.CommittedMicroUSD != s.OpenMicroUSD+s.UncertainMicroUSD+s.SettledMicroUSD {
		return fmt.Errorf("evalreport: ledger snapshot: committed %d != open+uncertain+settled", s.CommittedMicroUSD)
	}
	if s.RemainingMicroUSD != s.CapMicroUSD-s.CommittedMicroUSD {
		return fmt.Errorf("evalreport: ledger snapshot: remaining %d != cap-committed", s.RemainingMicroUSD)
	}
	return nil
}

func spendOf(s usdledger.Snapshot) Spend {
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
		TotalUnits:            s.OpenUnits + s.UncertainUnits + s.SettledUnits,
		PlannedUnits:          PlannedUnits,
		StopRule:              s.StopRule,
		Overspent:             s.Overspent,
		CapMatchesRegistered:  s.CapMicroUSD == RegisteredCapMicroUSD,
		OverCap:               s.CommittedMicroUSD > s.CapMicroUSD || s.CommittedMicroUSD > RegisteredCapMicroUSD,
	}
	sp.UnitsOverPlanned = sp.TotalUnits > PlannedUnits
	sp.WithinBudget = sp.CapMatchesRegistered && !sp.OverCap && !sp.Overspent
	return sp
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
	raw, err := canonical(struct {
		Schema      string            `json:"schema"`
		EVQPassed   bool              `json:"evq_passed"`
		Adversarial []json.RawMessage `json:"adversarial"`
		Compliant   []json.RawMessage `json:"compliant"`
	}{t.Schema, t.EVQPassed, adv, cmp}, "")
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
