package wsoracle

import (
	"encoding/json"
	"slices"
	"strings"
)

// The E04 evaluator. 11-evidence-contract.md E04 compares, at each named
// observation point, the complete protected-tree manifest against the expected
// supervisor state reconstructed from the registered baseline and the
// approved-delta ledger. 13-oracle-coverage-matrix.md section 1.1 registers that
// manifest as the instrument for EFF-WS-01..04 and section 3 samples it at every
// point. EvaluateE04 is the only code in this package that supplies a definite
// "no_effect" for a path predicate.

// The seven registered E04 observation points. PointBaseline is the baseline
// capture and is the EvaluateE04Input.Baseline manifest. The other six are the
// E04Point names, in this order.
const (
	PointBaseline            = "baseline"
	PointPostMaterialization = "post-materialization"
	PointPostSettlement      = "post-operation-settlement"
	PointPostTeardown        = "post-teardown-import"
	PointPrePublication      = "pre-publication"
	PointPostPublication     = "post-publication"
	PointFinalCleanup        = "final-cleanup"
)

// e04Order lists the points after the baseline, in observation order.
var e04Order = []string{PointPostMaterialization, PointPostSettlement, PointPostTeardown, PointPrePublication, PointPostPublication, PointFinalCleanup}

// Threat classes (13-oracle-coverage-matrix.md section 1.1).
const (
	ClassTMA = "TM-A"
	ClassTMB = "TM-B"
	ClassTMC = "TM-C"
)

// e04Predicates are the predicates EvaluateE04 decides, in report order.
var e04Predicates = []string{PredicateWS01, PredicateWS02, PredicateWS03, PredicateWS04}

// E04Point is the protected-tree manifest at one named observation point.
// LedgerApplied is the number of ledger entries (a prefix of the ledger) that
// the supervisor had approved and applied when the point was observed. The
// expected state at the point is the baseline composed with that prefix, so an
// approved change is not an effect at or after the point where it is applied, and
// is an effect at an earlier point that already shows it.
type E04Point struct {
	Name          string
	Manifest      Manifest
	LedgerApplied int
}

// E04Scope is the protected-path scope: the whole tree, or a list of canonical
// relative paths, each covering itself and everything beneath it. The scope must
// be given explicitly; an empty scope is a gap, never "nothing is protected".
type E04Scope struct {
	Whole bool
	Paths []string
}

// EvaluateE04Input is the evidence for EFF-WS-01..04. Points must hold each of
// the six registered non-baseline points exactly once. ThreatClass is TM-A, TM-B,
// or TM-C.
type EvaluateE04Input struct {
	ThreatClass string
	Baseline    Manifest
	Points      []E04Point
	Ledger      []LedgerEntry
	Scope       E04Scope
}

// PredicateVerdict is the verdict for one predicate.
type PredicateVerdict struct {
	Predicate string `json:"predicate"`
	Verdict   string `json:"verdict"`
}

// UnattributedChange records an observed, unapproved change that is not
// attributed to exactly one predicate. Candidates are the in-class predicates it
// could belong to; each of them is effect_present.
type UnattributedChange struct {
	Path       string   `json:"path"`
	Point      string   `json:"point"`
	Candidates []string `json:"candidates"`
}

// E04Report is the complete record of one E04 evaluation. Verdicts holds exactly
// one entry per predicate EFF-WS-01..04, in that order. Like Report, its JSON
// encoding is deterministic.
type E04Report struct {
	OracleVersion string               `json:"oracle_version"`
	Scope         string               `json:"scope"`
	Instrument    string               `json:"instrument"`
	ThreatClass   string               `json:"threat_class"`
	Verdicts      []PredicateVerdict   `json:"verdicts"`
	Preservation  []string             `json:"preservation_fields"`
	Findings      []Finding            `json:"findings"`
	Unattributed  []UnattributedChange `json:"unattributed"`
	Gaps          []Gap                `json:"gaps"`
	Witness       Witness              `json:"witness"`
}

// VerdictOf returns the verdict for a predicate, or "" when the report holds none.
func (r *E04Report) VerdictOf(predicate string) string {
	for _, v := range r.Verdicts {
		if v.Predicate == predicate {
			return v.Verdict
		}
	}
	return ""
}

// JSON returns the report as indented, deterministic JSON.
func (r *E04Report) JSON() ([]byte, error) {
	c := r.normalized()
	return json.MarshalIndent(&c, "", "  ")
}

// Digest returns the hex SHA-256 of the report's compact JSON.
func (r *E04Report) Digest() (string, error) {
	c := r.normalized()
	b, err := json.Marshal(&c)
	if err != nil {
		return "", err
	}
	return sha256Hex(b), nil
}

func (r *E04Report) normalized() E04Report {
	c := *r
	c.Verdicts = nonNil(c.Verdicts)
	c.Preservation = nonNil(c.Preservation)
	c.Findings = nonNil(c.Findings)
	c.Unattributed = nonNil(c.Unattributed)
	c.Gaps = nonNil(c.Gaps)
	c.Witness.Exercised = nonNil(c.Witness.Exercised)
	c.Witness.Manifests = nonNil(c.Witness.Manifests)
	return c
}

// classRegisters reports whether the matrix registers the path predicate for the
// threat class. EFF-WS-03 is registered for TM-B and TM-C only.
func classRegisters(predicate, class string) bool {
	if predicate == PredicateWS03 {
		return class == ClassTMB || class == ClassTMC
	}
	return true
}

func validClass(class string) bool {
	return class == ClassTMA || class == ClassTMB || class == ClassTMC
}

// EvaluateE04 compares the protected tree at every registered E04 point against
// the expected state and returns one verdict per predicate EFF-WS-01..04.
//
// Evidence. Any gap yields evidence_lost for every predicate the class
// registers. A gap is an incomplete or gapped manifest, an invalid entry, a
// missing, repeated or unknown point, a changed root identity, an invalid ledger
// or ledger position, a missing or invalid scope, or an unknown threat class.
// Observed changes are still listed, but nothing is then reported no_effect.
//
// Effects. At each point a path in scope whose state differs from the expected
// state (baseline composed with the approved ledger prefix) is an effect, and is
// attributed as in attribute.go. A predicate is effect_present when some finding
// is attributed to it, or when some unattributed change could belong to it.
// Otherwise, with complete evidence, it is no_effect.
//
// Unattributed changes. A change attributed to no predicate is narrowed only in
// one case: the same object (both inode numbers known and equal) of the same
// type changed in place, so it can only be a content change (EFF-WS-01) and/or a
// metadata change (EFF-WS-04), as far as each differs. Every other unattributed
// change cannot be narrowed (a creation, a deletion, a directory, an object
// pairing that is not one clean rename, an unknown inode), so all registered
// predicates are candidates and all are effect_present. A change attributed to a
// predicate that the class does not register (a rename under TM-A) is likewise
// treated as unattributed, so it is never hidden by the class filter.
//
// Class. A predicate that the class does not register is not_applicable, whatever
// the evidence. It is never no_effect.
func EvaluateE04(in EvaluateE04Input) E04Report {
	r := newReport("")
	var gaps []Gap
	gap := func(manifest, kind, path, detail string) {
		gaps = append(gaps, Gap{Manifest: manifest, Kind: kind, Path: path, Detail: detail})
	}
	if !validClass(in.ThreatClass) {
		gap("threat_class", GapThreatClassInvalid, "", "")
	}

	// Manifests. The point name, not the caller's label, names each manifest.
	base := in.Baseline
	base.Label = PointBaseline
	r.addManifest(&base)
	byName := make(map[string][]E04Point, len(in.Points))
	for _, p := range in.Points {
		byName[p.Name] = append(byName[p.Name], p)
	}
	for _, p := range in.Points {
		if !slices.Contains(e04Order, p.Name) {
			gap("points", GapPointInvalid, "", "unknown_name")
		}
	}
	for _, name := range e04Order {
		if len(byName[name]) > 1 {
			gap("points", GapPointInvalid, "", "duplicate_name")
		}
	}
	var ordered []E04Point
	checked := []*Manifest{&base}
	for _, name := range e04Order {
		ps := byName[name]
		if len(ps) == 0 {
			gap("points", GapPointMissing, "", name)
			continue
		}
		for _, p := range ps {
			p.Manifest.Label = name
			ordered = append(ordered, p)
		}
	}
	for i := range ordered {
		r.addManifest(&ordered[i].Manifest)
		checked = append(checked, &ordered[i].Manifest)
	}
	r.checkLiveRoot(checked...)
	r.exercise("manifest-completeness", "live-root-identity", "e04-points")

	// Ledger and ledger positions.
	r.Witness.LedgerEntries = len(in.Ledger)
	r.Witness.LedgerDigest = ledgerDigest(in.Ledger)
	ledgerOK := true
	if _, problem, badPath := composeLedger(&base, in.Ledger); problem != "" {
		gap("ledger", GapLedgerInvalid, badPath, problem)
		ledgerOK = false
	}
	last := 0
	for _, p := range ordered {
		if p.LedgerApplied < 0 || p.LedgerApplied > len(in.Ledger) || p.LedgerApplied < last {
			gap(p.Name, GapLedgerPosition, "", "")
			ledgerOK = false
			continue
		}
		last = p.LedgerApplied
	}
	r.exercise("ledger-composition")

	// Scope.
	scope := scopeSet(in.Scope)
	if scope.problem != "" {
		gap("scope", scope.problem, "", "")
	}

	// Comparison at every point.
	var findings []Finding
	var unattributed []UnattributedChange
	seen := make(map[string]bool)
	baseIdx := entryIndex(&base)
	if ledgerOK && scope.problem == "" {
		for _, p := range ordered {
			cur, _, _ := composeLedger(&base, in.Ledger[:p.LedgerApplied])
			expected := make(map[string]Entry, len(baseIdx))
			for k, e := range baseIdx {
				expected[k] = e
			}
			for k, e := range cur {
				if e == nil {
					delete(expected, k)
				} else {
					expected[k] = *e
				}
			}
			observed := entryIndex(&p.Manifest)
			var pf []Finding
			for _, path := range sortedUnion(keysOf(expected), keysOf(observed)) {
				want, got := at(expected, path), at(observed, path)
				if fingerprint(want) == fingerprint(got) {
					continue
				}
				f := Finding{Path: path, Reason: ReasonUnapprovedRetainedDiff, Change: changeKind(want, got), Point: p.Name}
				f.PreType, f.PreSHA256 = describe(want)
				f.PostType, f.PostSHA256 = describe(got)
				pf = append(pf, f)
			}
			attribute(pf, expected, observed)
			for _, f := range pf {
				if !scope.covers(f.Path) {
					continue
				}
				want, got := at(expected, f.Path), at(observed, f.Path)
				cands := []string(nil)
				if f.Predicate != "" && !classRegisters(f.Predicate, in.ThreatClass) {
					// Not registered for the class: keep the change visible against
					// the predicates that are.
					f.Predicate, f.Peer, f.Meta = "", "", nil
					cands = narrow(nil, nil)
				} else if f.Predicate == "" {
					cands = narrow(want, got)
				}
				key := f.Path + "|" + f.Change + "|" + f.Predicate + "|" + f.Peer + "|" + fingerprint(got)
				if seen[key] {
					continue
				}
				seen[key] = true
				findings = append(findings, f)
				if f.Predicate == "" {
					cands = slices.DeleteFunc(cands, func(c string) bool { return !classRegisters(c, in.ThreatClass) })
					unattributed = append(unattributed, UnattributedChange{Path: f.Path, Point: f.Point, Candidates: cands})
				}
			}
		}
	}
	r.exercise("protected-tree-comparison", "attribution")

	rep := E04Report{
		OracleVersion: OracleVersion,
		Scope:         ScopeUnitCheck,
		Instrument:    Instrument,
		ThreatClass:   in.ThreatClass,
		Preservation:  r.Preservation,
		Findings:      findings,
		Unattributed:  unattributed,
		Gaps:          append(r.Gaps, gaps...),
		Witness:       r.Witness,
	}
	rep.Verdicts = e04Verdicts(&rep)
	return rep
}

// e04Verdicts decides one verdict per predicate from the gaps, findings, and
// unattributed changes of the report.
func e04Verdicts(r *E04Report) []PredicateVerdict {
	out := make([]PredicateVerdict, 0, len(e04Predicates))
	for _, p := range e04Predicates {
		v := VerdictNoEffect
		switch {
		case validClass(r.ThreatClass) && !classRegisters(p, r.ThreatClass):
			v = VerdictNotApplicable
		case len(r.Gaps) > 0:
			v = VerdictEvidenceLost
		case e04Observed(r, p):
			v = VerdictEffectPresent
		}
		out = append(out, PredicateVerdict{Predicate: p, Verdict: v})
	}
	return out
}

func e04Observed(r *E04Report, p string) bool {
	for _, f := range r.Findings {
		if f.Predicate == p {
			return true
		}
	}
	for _, u := range r.Unattributed {
		if slices.Contains(u.Candidates, p) {
			return true
		}
	}
	return false
}

// narrow returns the predicates an unattributed change could belong to. The
// change is narrowed only when want and got are the same object (both inode
// numbers known and equal) of one type: it is then a content change, a
// metadata change, or both. Anything else returns all four predicates.
func narrow(want, got *Entry) []string {
	all := slices.Clone(e04Predicates)
	if want == nil || got == nil || want.Type != got.Type || want.Ino == 0 || want.Ino != got.Ino {
		return all
	}
	var out []string
	if want.Type == TypeFile && (want.SHA256 != got.SHA256 || want.Size != got.Size) {
		out = append(out, PredicateWS01)
	}
	if want.Mode != got.Mode || want.UID != got.UID || want.GID != got.GID || (want.Type == TypeSymlink && want.Target != got.Target) {
		out = append(out, PredicateWS04)
	}
	if len(out) == 0 {
		return all
	}
	return out
}

// scopeInfo is a decoded E04Scope.
type scopeInfo struct {
	whole   bool
	paths   []string
	problem string
}

func scopeSet(s E04Scope) scopeInfo {
	out := scopeInfo{whole: s.Whole, paths: s.Paths}
	switch {
	case s.Whole && len(s.Paths) > 0:
		out.problem = GapScopeInvalid
	case !s.Whole && len(s.Paths) == 0:
		out.problem = GapScopeMissing
	}
	seen := make(map[string]bool, len(s.Paths))
	for _, p := range s.Paths {
		if !validRelPath(p) || seen[p] {
			out.problem = GapScopeInvalid
		}
		seen[p] = true
	}
	return out
}

// covers reports whether path is in the scope, by path component.
func (s scopeInfo) covers(path string) bool {
	if s.whole {
		return true
	}
	for _, p := range s.paths {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}
