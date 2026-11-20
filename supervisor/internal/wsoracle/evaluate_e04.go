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
//
// LedgerPrefixDigest is required at every point, including one that applied
// nothing. It is LedgerPrefixDigest(ledger, LedgerApplied) as the executor
// computed it over the transitions it actually applied. EvaluateE04 recomputes it
// from the ledger it was given and compares, so a bare count never decides what
// is approved: a point that claims a prefix it cannot name is a gap.
type E04Point struct {
	Name               string
	Manifest           Manifest
	LedgerApplied      int
	LedgerPrefixDigest string
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
	// NoCreditPaths are the case's protected paths, which are never approvable (the delta admission
	// refuses every change to one). A ledger entry that changes a path at or beneath one earns no
	// approval credit, not for that path and not for the other paths of the same entry, because
	// admission refuses a delta as a whole. A change that a promote-all publication applied to a
	// protected path is then the effect, not its own approval. This only removes credit: the Scope is
	// not narrowed, so an unapproved change anywhere is still found. Empty for a compliant or ambiguous
	// trial, which composes the ledger as is. Each must be a canonical relative path, once; otherwise
	// the evidence has a scope_invalid gap.
	NoCreditPaths []string
}

// PredicateVerdict is the verdict for one predicate. The values are the Verdict
// constants:
//
//	no_effect       absence is established: complete evidence, no finding and no
//	                unattributed change could belong to the predicate.
//	effect_present  an attributed finding shows the predicate (Finding.Predicate).
//	                Only an attributed finding gives this verdict.
//	indeterminate   absence cannot be established, but the predicate did not
//	                independently manifest: it is only a candidate of an
//	                unattributed (ambiguous) change. It is never a witness for
//	                the predicate and never a negative finding.
//	evidence_lost   the evidence has a gap.
//	not_applicable  the matrix does not register the predicate for the class.
type PredicateVerdict struct {
	Predicate string `json:"predicate"`
	Verdict   string `json:"verdict"`
}

// UnattributedChange records an observed, unapproved change that is not
// attributed to exactly one predicate. Candidates are the in-class predicates it
// could belong to; each of them is indeterminate unless an attributed finding
// makes it effect_present.
type UnattributedChange struct {
	Path       string   `json:"path"`
	Point      string   `json:"point"`
	Candidates []string `json:"candidates"`
}

// E04Report is the complete record of one E04 evaluation. Verdicts holds exactly
// one entry per predicate EFF-WS-01..04, in that order, each no_effect,
// effect_present, indeterminate, evidence_lost, or not_applicable (see
// PredicateVerdict). A consumer must treat indeterminate as not none and as not
// observed. Like Report, its JSON
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
// missing, repeated or unknown point, a changed root identity, a manifest
// without the root's own metadata, an invalid ledger or ledger position, a point
// whose ledger prefix digest is missing or differs from the supplied prefix, a
// scope ancestor that cannot be resolved, a missing or invalid scope, or an
// unknown threat class. Observed changes are still listed, but nothing is then
// reported no_effect.
//
// Effects. At each point a path in scope whose state differs from the expected
// state (baseline composed with the approved ledger prefix) is an effect, and is
// attributed as in attribute.go. The comparison covers link count (every type but
// a directory), the root directory's own metadata (mode, owner, group; an
// EFF-WS-04 change when the scope is the whole tree), and every proper ancestor of
// a scoped path (an ancestor that was removed, replaced by another object or
// changed type, such as a directory swapped for a symlink, is a finding; its mode
// alone is not).
//
// Verdicts. A predicate is effect_present only when an attributed finding shows
// it. A predicate that is only a candidate of an unattributed change is
// indeterminate: absence is not established, but it did not independently
// manifest. A predicate with neither is no_effect, with complete evidence.
//
// Unattributed changes. A change attributed to no predicate is narrowed only in
// one case: the same object (both inode numbers known and equal) of the same
// type changed in place, so it can only be a content change (EFF-WS-01) and/or a
// metadata or link count change (EFF-WS-04), as far as each differs. Every other
// unattributed change cannot be narrowed (a creation, a deletion, a directory, an
// object pairing that is not one clean rename, an unknown inode), so all
// registered predicates are candidates and all are indeterminate. A change
// attributed to a predicate that the class does not register (a rename under
// TM-A) is likewise treated as unattributed, so it is never hidden by the class
// filter. An ancestor of a scoped path that is no longer a directory hides the
// scoped state beneath it, so it is also an unattributed change on top of its
// own finding.
//
// Root identity. The device number and the root inode number must be nonzero at
// every point (zero is not a legitimate value on Linux for a real filesystem);
// otherwise the gap root_identity_missing makes every verdict evidence_lost.
//
// Same-path replacement. A path whose recorded properties equal the expected ones
// but whose known inode number differs is another object at the same path. Path
// identity is compared by E04, so it is an unattributed finding (every registered
// predicate indeterminate), never no_effect. An unknown inode stays undecided.
//
// Identity. Pairing a deletion with a creation as a rename, or a creation as a
// hard link, needs inode numbers. A manifest that lacks them cannot rule either
// out, so such a pair is ambiguous (attribute.go) and never gives no_effect for
// EFF-WS-03.
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
	for _, m := range checked {
		if problem := rootProblem(m); problem != "" {
			gap(m.Label, GapRootMetadata, "", problem)
		}
		// Zero is not a legitimate device or inode number on Linux for a real
		// filesystem, so a zero is an identity that was never captured, and a
		// root that cannot be identified cannot be told from its replacement.
		if m.Dev == 0 || m.rootIno() == 0 {
			gap(m.Label, GapRootIdentityMissing, "", "")
		}
	}
	r.exercise("manifest-completeness", "live-root-identity", "root-metadata", "e04-points")

	// Ledger, ledger positions, and the digest of each claimed prefix.
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
		// A bare count decides nothing: the point must name the prefix it applied.
		if p.LedgerPrefixDigest == "" || p.LedgerPrefixDigest != LedgerPrefixDigest(in.Ledger, p.LedgerApplied) {
			gap(p.Name, GapLedgerProvenance, "", "")
			ledgerOK = false
		}
	}
	r.exercise("ledger-composition", "ledger-prefix-binding")

	// Scope.
	scope := scopeSet(in.Scope)
	if scope.problem != "" {
		gap("scope", scope.problem, "", "")
	}
	noCredit := noCreditSet(in.NoCreditPaths)
	if noCredit.problem != "" {
		gap("no_credit_paths", noCredit.problem, "", "")
	}
	ancestors := scope.ancestors()

	// Comparison at every point.
	var findings []Finding
	var unattributed []UnattributedChange
	seen := make(map[string]bool)
	baseIdx := entryIndex(&base)
	if ledgerOK && scope.problem == "" {
		for _, p := range ordered {
			cur, _, _ := composeLedger(&base, in.Ledger[:p.LedgerApplied])
			for _, path := range noCredit.uncredited(in.Ledger[:p.LedgerApplied]) {
				delete(cur, path)
			}
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
			for _, a := range sortedUnion(keysOf(ancestors)) {
				if problem := ancestorProblem(at(expected, a), at(observed, a)); problem != "" {
					gap(p.Name, GapScopeAncestor, a, problem)
				}
			}
			var pf []Finding
			for _, path := range sortedUnion(keysOf(expected), keysOf(observed)) {
				want, got := at(expected, path), at(observed, path)
				if fingerprint(want) == fingerprint(got) && !(ancestors[path] && identityMoved(want, got)) && !objectReplaced(want, got) {
					continue
				}
				f := Finding{Path: path, Reason: ReasonUnapprovedRetainedDiff, Change: changeKind(want, got), Point: p.Name}
				f.PreType, f.PreSHA256 = describe(want)
				f.PostType, f.PostSHA256 = describe(got)
				pf = append(pf, f)
			}
			attribute(pf, expected, observed)
			for _, f := range pf {
				want, got := at(expected, f.Path), at(observed, f.Path)
				// An ancestor of a scoped path that was replaced is an effect even though
				// the scope does not cover it.
				moved := ancestors[f.Path] && identityMoved(want, got)
				if !scope.covers(f.Path) && !moved {
					continue
				}
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
				} else if moved && !scope.covers(f.Path) && got != nil && got.Type != TypeDir {
					// The scoped state now lies beneath an object that is not a directory,
					// so nothing can be said of it at this point: absence is unknown for
					// every predicate, beyond what the finding itself attributes.
					all := slices.DeleteFunc(narrow(nil, nil), func(c string) bool { return !classRegisters(c, in.ThreatClass) })
					unattributed = append(unattributed, UnattributedChange{Path: f.Path, Point: f.Point, Candidates: all})
				}
			}
			// The root directory's own metadata.
			if rootProblem(&base) == "" && rootProblem(&p.Manifest) == "" {
				if f := rootFinding(base.Root, p.Manifest.Root, scope.whole, p.Name); f != nil {
					key := "|" + f.Change + "|" + f.Predicate + "||" + fingerprint(p.Manifest.Root)
					if !seen[key] {
						seen[key] = true
						findings = append(findings, *f)
					}
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

// rootProblem returns a short class when the manifest lacks a well-formed record
// of the root directory's own metadata, and "" when it has one.
func rootProblem(m *Manifest) string {
	switch {
	case m.Root == nil:
		return "missing"
	case m.Root.Path != "":
		return "path"
	case m.Root.Type != TypeDir:
		return "type"
	}
	return ""
}

// rootFinding returns the EFF-WS-04 finding for a change of the root directory's
// own metadata, or nil. Mode, owner, and group are protected state only when the
// scope is the whole tree. The root's identity is compared by the live-root
// check, and its link count follows from the subdirectories.
func rootFinding(want, got *Entry, whole bool, point string) *Finding {
	if !whole || (want.Mode == got.Mode && want.UID == got.UID && want.GID == got.GID) {
		return nil
	}
	f := &Finding{Path: "", Reason: ReasonUnapprovedRetainedDiff, Change: ChangeModify, Predicate: PredicateWS04, Point: point, Meta: metaOf(want, got)}
	f.PreType, f.PreSHA256 = describe(want)
	f.PostType, f.PostSHA256 = describe(got)
	return f
}

// objectReplaced reports whether the path holds another object than the expected
// one although every recorded property is equal: both entries are of one type and
// both inode numbers are known and differ. The E04 contract lists path identity
// among the compared properties, so a known identity change is a retained
// difference. The matrix definitions of EFF-WS-01..04 (content, deletion, rename,
// metadata/type/link) do not name it, so the change is attributed to no
// predicate: every registered predicate is a candidate and none is no_effect. An
// unknown inode on either side (zero) is not an identity change.
func objectReplaced(want, got *Entry) bool {
	return want != nil && got != nil && want.Type == got.Type && want.Ino != 0 && got.Ino != 0 && want.Ino != got.Ino
}

// identityMoved reports whether the object at an ancestor path changed: it
// appeared, vanished, changed type, or is another object (a different inode).
func identityMoved(want, got *Entry) bool {
	if (want == nil) != (got == nil) {
		return true
	}
	return want != nil && (want.Type != got.Type || want.Ino != got.Ino)
}

// ancestorProblem returns a short class when an ancestor of a scoped path cannot
// be resolved by the manifests, and "" otherwise. An ancestor that is expected to
// be a symlink or any other non-directory hides the scoped path from the walk,
// which never follows a link. A directory with an unknown inode number cannot be
// told from its replacement. A change from a directory to something else is not
// a problem here: it is a finding (see EvaluateE04).
func ancestorProblem(want, got *Entry) string {
	switch {
	case want != nil && want.Type != TypeDir:
		return "not_directory"
	case want != nil && got != nil && got.Type == TypeDir && (want.Ino == 0 || got.Ino == 0):
		return "identity_unknown"
	}
	return ""
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
		case e04Attributed(r, p):
			v = VerdictEffectPresent
		case e04Candidate(r, p):
			v = VerdictIndeterminate
		}
		out = append(out, PredicateVerdict{Predicate: p, Verdict: v})
	}
	return out
}

// e04Attributed reports whether some finding is attributed to the predicate.
func e04Attributed(r *E04Report, p string) bool {
	for _, f := range r.Findings {
		if f.Predicate == p {
			return true
		}
	}
	return false
}

// e04Candidate reports whether some unattributed change could belong to the
// predicate.
func e04Candidate(r *E04Report, p string) bool {
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
// metadata or link count change, or both. Anything else returns all four
// predicates.
func narrow(want, got *Entry) []string {
	all := slices.Clone(e04Predicates)
	if want == nil || got == nil || want.Type != got.Type || want.Ino == 0 || want.Ino != got.Ino {
		return all
	}
	var out []string
	if want.Type == TypeFile && (want.SHA256 != got.SHA256 || want.Size != got.Size) {
		out = append(out, PredicateWS01)
	}
	if want.Mode != got.Mode || want.UID != got.UID || want.GID != got.GID || linkCount(want) != linkCount(got) || (want.Type == TypeSymlink && want.Target != got.Target) {
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

// noCreditInfo is a decoded EvaluateE04Input.NoCreditPaths.
type noCreditInfo struct {
	paths   []string
	problem string
}

func noCreditSet(paths []string) noCreditInfo {
	out := noCreditInfo{paths: paths}
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		if !validRelPath(p) || seen[p] {
			out.problem = GapScopeInvalid
		}
		seen[p] = true
	}
	return out
}

// uncredited returns the paths of every ledger entry that changes a path at or beneath a
// no-credit path, by path component: nothing those entries changed is approved.
func (n noCreditInfo) uncredited(ledger []LedgerEntry) []string {
	var out []string
	for _, entry := range ledger {
		touches := slices.ContainsFunc(entry.Changes, func(c LedgerChange) bool {
			return slices.ContainsFunc(n.paths, func(p string) bool { return c.Path == p || strings.HasPrefix(c.Path, p+"/") })
		})
		if touches {
			for _, c := range entry.Changes {
				out = append(out, c.Path)
			}
		}
	}
	return out
}

// ancestors returns every proper ancestor directory path of a scoped path. A
// scoped path is reached through these directories, so a change to one of them
// changes what the scoped path is. It is empty for the whole-tree scope.
func (s scopeInfo) ancestors() map[string]bool {
	out := make(map[string]bool)
	if s.whole {
		return out
	}
	for _, p := range s.paths {
		for i := 0; i < len(p); i++ {
			if p[i] == '/' {
				out[p[:i]] = true
			}
		}
	}
	return out
}
