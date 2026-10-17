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

// WS06Input is the evidence for EFF-WS-06. LockIdentity and PublicationResult are
// recorded in the witness. LockIdentity is required, and PublicationResult is
// informational.
type WS06Input struct {
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
	r.exercise("live-application")
	r.finish()
	return r
}

// EvaluateWS06 evaluates EFF-WS-06 over the supplied manifests.
//
// An effect is a path that publication changed (pre != post) while its
// pre-publication state differs from the registered baseline. Publication then
// overwrote, replaced, or deleted an object that no longer matched the baseline.
// A stale path that publication left alone is not an effect. A stale path that
// publication aborted before touching is not an effect either.
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

	base := entryIndex(&in.Baseline)
	pre := entryIndex(&in.PrePublication)
	post := entryIndex(&in.PostPublication)
	for _, p := range sortedUnion(keysOf(base), keysOf(pre), keysOf(post)) {
		before, after := at(pre, p), at(post, p)
		if fingerprint(before) == fingerprint(after) {
			continue
		}
		ref := at(base, p)
		if fingerprint(before) == fingerprint(ref) {
			continue
		}
		f := Finding{
			Path:   p,
			Reason: ReasonStaleBaselineOverwrite,
			Change: changeKind(before, after),
		}
		f.PreType, f.PreSHA256 = describe(before)
		f.PostType, f.PostSHA256 = describe(after)
		f.ReferenceType, f.ReferenceSHA256 = describe(ref)
		r.Findings = append(r.Findings, f)
	}
	r.exercise("stale-baseline-check")
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
	r.Witness.Manifests = append(r.Witness.Manifests, ManifestRef{
		Label:    m.Label,
		Digest:   m.Digest(),
		Entries:  len(m.Entries),
		Complete: m.clean(),
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
}

// checkLiveRoot records a root_identity_changed gap for each manifest whose
// root differs from the first one. The first manifest must be the baseline.
func (r *Report) checkLiveRoot(ms ...*Manifest) {
	for _, m := range ms[1:] {
		if m.dev != ms[0].dev || m.ino != ms[0].ino {
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
