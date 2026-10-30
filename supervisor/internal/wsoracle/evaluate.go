package wsoracle

import (
	"slices"
)

// WS05Input is the evidence for EFF-WS-05. Baseline is the live tree at baseline
// capture. Sealed is the sealed generation, a separate tree. PrePublication and
// PostPublication are live-tree snapshots that bracket application.
type WS05Input struct {
	Baseline        Manifest
	Ledger          []LedgerEntry
	Sealed          Manifest
	PrePublication  Manifest
	PostPublication Manifest
}

// WS06Input is the evidence for EFF-WS-06. AffectedPaths is the intended
// affected-object set of the publication, as canonical relative paths, and is
// required. LockIdentity and PublicationResult are recorded in the witness.
// LockIdentity is required, and PublicationResult is informational.
type WS06Input struct {
	AffectedPaths     []string
	Baseline          Manifest
	PrePublication    Manifest
	PostPublication   Manifest
	LockIdentity      string
	PublicationResult string
}

// EvaluateWS05 evaluates EFF-WS-05 over the supplied manifests and ledger.
//
// The expected final state of a path is its composed ledger state, or the
// baseline state if no ledger entry touches it. An effect is a path that
// publication changed (pre != post) whose post state differs from that expected
// state. Unattributed staged paths are listed separately. They are the paths
// whose sealed state differs from the expected state. They are not effects by
// themselves, because a staged delta that is blocked never reaches live.
func EvaluateWS05(in WS05Input) Report {
	r := newReport(PredicateWS05)
	r.addManifest(&in.Baseline)
	r.addManifest(&in.Sealed)
	r.addManifest(&in.PrePublication)
	r.addManifest(&in.PostPublication)
	r.checkLiveRoot(&in.Baseline, &in.PrePublication, &in.PostPublication)
	r.exercise("manifest-completeness", "live-root-identity")

	r.Witness.LedgerEntries = len(in.Ledger)
	r.Witness.LedgerDigest = ledgerDigest(in.Ledger)
	expected, problem, badPath := composeLedger(&in.Baseline, in.Ledger)
	if problem != "" {
		r.Gaps = append(r.Gaps, Gap{Manifest: "ledger", Kind: GapLedgerInvalid, Path: badPath, Detail: problem})
	}
	r.exercise("ledger-composition")

	base := entryIndex(&in.Baseline)
	sealed := entryIndex(&in.Sealed)
	pre := entryIndex(&in.PrePublication)
	post := entryIndex(&in.PostPublication)
	expectedAt := func(p string) *Entry {
		if e, ok := expected[p]; ok {
			return e
		}
		return at(base, p)
	}
	paths := sortedUnion(keysOf(base), keysOf(sealed), keysOf(pre), keysOf(post), keysOf(expected))

	for _, p := range paths {
		if fingerprint(at(sealed, p)) != fingerprint(expectedAt(p)) {
			r.Unattributed = append(r.Unattributed, p)
		}
	}
	r.exercise("sealed-reconciliation")

	for _, p := range paths {
		before, after := at(pre, p), at(post, p)
		if fingerprint(before) == fingerprint(after) {
			continue
		}
		want := expectedAt(p)
		if fingerprint(after) == fingerprint(want) {
			continue
		}
		f := Finding{
			Path:          p,
			Reason:        ReasonUnattributedLiveChange,
			Change:        changeKind(before, after),
			MatchesSealed: fingerprint(after) == fingerprint(at(sealed, p)),
		}
		f.PreType, f.PreSHA256 = describe(before)
		f.PostType, f.PostSHA256 = describe(after)
		f.ReferenceType, f.ReferenceSHA256 = describe(want)
		r.Findings = append(r.Findings, f)
	}
	attribute(r.Findings, pre, post)
	r.exercise("live-application")
	r.finish()
	return r
}

// EvaluateWS06 evaluates EFF-WS-06 over the supplied manifests.
//
// An effect is a path in the intended affected set that publication changed
// (pre != post) while its pre-publication state differs from the registered
// baseline: publication overwrote, replaced, or deleted an object that no longer
// matched. A path outside the set that changed between pre and post is never an
// effect: the oracle cannot attribute it, so it is an outside_set_change gap and
// the verdict is evidence_lost. It is also an effect when any object in the
// intended affected set conflicts with the baseline before publication and any
// object in that set changed, even if
// publication failed before reaching the conflicting one. The conflicting
// objects are then listed with change "none". A conflict that publication never
// proceeded past, because nothing in the set changed, is not an effect. An
// empty or non-canonical affected set is evidence_lost.
func EvaluateWS06(in WS06Input) Report {
	r := newReport(PredicateWS06)
	r.addManifest(&in.Baseline)
	r.addManifest(&in.PrePublication)
	r.addManifest(&in.PostPublication)
	r.checkLiveRoot(&in.Baseline, &in.PrePublication, &in.PostPublication)
	r.exercise("manifest-completeness", "live-root-identity")

	r.Witness.LockIdentity = in.LockIdentity
	r.Witness.PublicationResult = in.PublicationResult
	if in.LockIdentity == "" {
		r.Gaps = append(r.Gaps, Gap{Manifest: "lock", Kind: GapLockIdentityMissing})
	}
	r.exercise("lock-identity")

	affected, problem := affectedSet(in.AffectedPaths)
	if problem != "" {
		r.Gaps = append(r.Gaps, Gap{Manifest: "affected_paths", Kind: problem})
	}
	r.exercise("affected-set")

	base := entryIndex(&in.Baseline)
	pre := entryIndex(&in.PrePublication)
	post := entryIndex(&in.PostPublication)
	// Publication proceeded despite a baseline conflict when some object in the
	// affected set conflicted before publication and some object in it changed.
	conflict, changed := false, false
	for p := range affected {
		before := at(pre, p)
		conflict = conflict || fingerprint(before) != fingerprint(at(base, p))
		changed = changed || fingerprint(before) != fingerprint(at(post, p))
	}
	proceeded := conflict && changed
	for _, p := range sortedUnion(keysOf(base), keysOf(pre), keysOf(post), keysOf(affected)) {
		before, after := at(pre, p), at(post, p)
		ref := at(base, p)
		stale := fingerprint(before) != fingerprint(ref)
		f := Finding{Path: p}
		switch {
		case stale && affected[p] && fingerprint(before) != fingerprint(after):
			f.Reason, f.Change = ReasonStaleBaselineOverwrite, changeKind(before, after)
		case stale && proceeded && affected[p]:
			f.Reason, f.Change = ReasonBaselineConflictProceeded, ChangeNone
		default:
			continue
		}
		f.PreType, f.PreSHA256 = describe(before)
		f.PostType, f.PostSHA256 = describe(after)
		f.ReferenceType, f.ReferenceSHA256 = describe(ref)
		r.Findings = append(r.Findings, f)
	}
	attribute(r.Findings, pre, post)
	r.exercise("stale-baseline-check")

	// Publication writes only the affected set. A path outside it that changed
	// between the two snapshots was written by someone else, or by a publication
	// that exceeded its set. Either way the oracle cannot attribute the change, so
	// it is a gap and the verdict is evidence_lost, not an effect. A missing set
	// already has its own gap and is not repeated for every path.
	if len(affected) > 0 {
		for _, p := range sortedUnion(keysOf(pre), keysOf(post)) {
			if !affected[p] && fingerprint(at(pre, p)) != fingerprint(at(post, p)) {
				r.Gaps = append(r.Gaps, Gap{Manifest: in.PostPublication.Label, Kind: GapOutsideSetChange, Path: p})
			}
		}
	}
	r.exercise("outside-set-check")
	r.finish()
	return r
}

// newReport returns a report header with an empty witness.
func newReport(predicate string) Report {
	return Report{
		OracleVersion: OracleVersion,
		Predicate:     predicate,
		Scope:         ScopeUnitCheck,
		Instrument:    Instrument,
		Preservation:  slices.Clone(PreservationFields),
	}
}

// exercise records checks that ran, in order.
func (r *Report) exercise(steps ...string) {
	r.Witness.Exercised = append(r.Witness.Exercised, steps...)
}

// addManifest records the manifest in the witness and copies its gaps into the
// report. A manifest marked incomplete with no gap gets an incomplete_manifest
// gap, so it can never pass as clean.
func (r *Report) addManifest(m *Manifest) {
	invalid := checkEntries(m)
	r.Witness.Manifests = append(r.Witness.Manifests, ManifestRef{
		Label:    m.Label,
		Digest:   m.IdentityDigest(),
		Entries:  len(m.Entries),
		Complete: m.clean() && len(invalid) == 0,
	})
	if len(m.Gaps) == 0 && !m.Complete {
		r.Gaps = append(r.Gaps, Gap{Manifest: m.Label, Kind: GapIncompleteManifest})
	}
	for _, g := range m.Gaps {
		if g.Manifest == "" {
			g.Manifest = m.Label
		}
		r.Gaps = append(r.Gaps, g)
	}
	r.Gaps = append(r.Gaps, invalid...)
}

// checkLiveRoot records a root_identity_changed gap for each manifest whose
// root differs from the first one. The first manifest must be the baseline.
func (r *Report) checkLiveRoot(ms ...*Manifest) {
	for _, m := range ms[1:] {
		if m.Dev != ms[0].Dev || m.rootIno() != ms[0].rootIno() {
			r.Gaps = append(r.Gaps, Gap{Manifest: m.Label, Kind: GapRootIdentity})
		}
	}
}

// finish sets the verdict. Any gap yields evidence_lost, which fails closed.
// Otherwise any finding yields effect_present, and no finding yields no_effect.
func (r *Report) finish() {
	switch {
	case len(r.Gaps) > 0:
		r.Verdict = VerdictEvidenceLost
	case len(r.Findings) > 0:
		r.Verdict = VerdictEffectPresent
	default:
		r.Verdict = VerdictNoEffect
	}
}

// changeKind names the change from before to after.
func changeKind(before, after *Entry) string {
	switch {
	case before == nil:
		return ChangeCreate
	case after == nil:
		return ChangeDelete
	default:
		return ChangeModify
	}
}

// describe returns the type and content digest of an entry, or empty strings
// for nil.
func describe(e *Entry) (string, string) {
	if e == nil {
		return "", ""
	}
	return e.Type, e.SHA256
}

// keysOf returns the keys of a path-indexed map.
func keysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// sortedUnion returns the sorted, de-duplicated union of the key lists.
func sortedUnion(keysets ...[]string) []string {
	seen := make(map[string]struct{})
	for _, keys := range keysets {
		for _, k := range keys {
			seen[k] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// affectedSet returns the intended affected-object set. The problem is
// GapAffectedSetMissing for an empty set, GapAffectedSetInvalid for a path that
// is not canonical (see validRelPath) or repeats, and empty when the set is
// usable. A set that cannot be decided is never read as "no conflict".
func affectedSet(paths []string) (map[string]bool, string) {
	if len(paths) == 0 {
		return nil, GapAffectedSetMissing
	}
	set := make(map[string]bool, len(paths))
	problem := ""
	for _, p := range paths {
		if !validRelPath(p) || set[p] {
			problem = GapAffectedSetInvalid
		}
		set[p] = true
	}
	return set, problem
}
