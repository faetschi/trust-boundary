package wsoracle

import (
	"slices"
	"testing"
)

// These tests turn the round-2 adversarial review of wsoracle v6 into
// counterexamples. Each failed against v6.

// ---- Finding 3: the device identity is required at every E04 point. ----

func TestE04MissingRootIdentityIsAGapAtEveryPoint(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	strip := map[string]func(*Manifest){
		"dev zero":      func(m *Manifest) { m.Dev = 0 },
		"root ino zero": func(m *Manifest) { r := *m.Root; r.Ino = 0; m.Root = &r },
		"root missing":  func(m *Manifest) { m.Root = nil },
	}
	for name, f := range strip {
		// The baseline.
		in := e04In("TM-C", []Entry{a}, e04Points(0, a))
		f(&in.Baseline)
		r := evalE04(in)
		wantVerdicts(t, r, all4(VerdictEvidenceLost))
		if !hasGapIn(r.Gaps, GapRootIdentityMissing, PointBaseline) {
			t.Errorf("%s: baseline: no %s gap in %+v", name, GapRootIdentityMissing, r.Gaps)
		}
		// Every other point, alone.
		for i := range e04Names {
			in := e04In("TM-C", []Entry{a}, e04Points(0, a))
			f(&in.Points[i].Manifest)
			r := evalE04(in)
			wantVerdicts(t, r, all4(VerdictEvidenceLost))
			if !hasGapIn(r.Gaps, GapRootIdentityMissing, e04Names[i]) {
				t.Errorf("%s: %s: no %s gap in %+v", name, e04Names[i], GapRootIdentityMissing, r.Gaps)
			}
		}
	}
	// Control: identity everywhere is complete.
	wantVerdicts(t, evalE04(e04In("TM-C", []Entry{a}, e04Points(0, a))), all4(VerdictNoEffect))
	if GapRootIdentityMissing != "root_identity_missing" {
		t.Fatalf("GapRootIdentityMissing = %q", GapRootIdentityMissing)
	}
}

func hasGapIn(gaps []Gap, kind, manifest string) bool {
	return slices.ContainsFunc(gaps, func(g Gap) bool { return g.Kind == kind && g.Manifest == manifest })
}

// ---- Finding 4: a known object-identity change at the same path is a retained
// difference, never no_effect. ----

func TestE04SamePathReplacementByAnotherKnownInodeIsNeverNoEffect(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	replaced := withIno(fileEntry("a.txt", "A"), 7) // identical bytes and metadata, new object
	for _, class := range []string{"TM-A", "TM-B", "TM-C"} {
		r := evalE04(e04In(class, []Entry{a}, e04Points(0, replaced)))
		if len(r.Gaps) != 0 {
			t.Fatalf("%s: gaps = %+v", class, r.Gaps)
		}
		if len(r.Findings) != 1 || r.Findings[0].Path != "a.txt" || r.Findings[0].Predicate != "" {
			t.Fatalf("%s: findings = %+v, want one unattributed finding", class, r.Findings)
		}
		if len(r.Unattributed) != 1 || r.Unattributed[0].Path != "a.txt" {
			t.Fatalf("%s: unattributed = %+v", class, r.Unattributed)
		}
		for _, p := range e04Preds {
			switch v := r.VerdictOf(p); {
			case !classRegisters(p, class):
				if v != VerdictNotApplicable {
					t.Errorf("%s %s = %q", class, p, v)
				}
			case v != VerdictIndeterminate:
				t.Errorf("%s %s = %q, want indeterminate (never no_effect)", class, p, v)
			}
		}
	}
	// A directory replaced by another directory is the same.
	d := withIno(dirEntry("d"), 2)
	r := evalE04(e04In("TM-C", []Entry{d}, e04Points(0, withIno(dirEntry("d"), 8))))
	wantVerdicts(t, r, all4(VerdictIndeterminate))
}

func TestE04UnknownInodeKeepsTheSamePathUndecidedAsBefore(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	unknown := withIno(fileEntry("a.txt", "A"), 0)
	// Identity absent on either side: nothing can be said, and nothing is invented.
	for name, c := range map[string]struct{ base, post Entry }{"post unknown": {a, unknown}, "base unknown": {unknown, a}, "both unknown": {unknown, unknown}} {
		r := evalE04(e04In("TM-C", []Entry{c.base}, e04Points(0, c.post)))
		wantVerdicts(t, r, all4(VerdictNoEffect))
		if len(r.Findings) != 0 {
			t.Errorf("%s: findings = %+v", name, r.Findings)
		}
	}
	// The same inode is no difference.
	wantVerdicts(t, evalE04(e04In("TM-C", []Entry{a}, e04Points(0, a))), all4(VerdictNoEffect))
}

func TestE04ApprovedReplacementWithoutIdentityIsNotAnEffect(t *testing.T) {
	x := withIno(fileEntry("a.txt", "X"), 1)
	y := withIno(fileEntry("a.txt", "Y"), 0) // the ledger does not know the new inode
	ledger := []LedgerEntry{approve("l1", 1, LedgerChange{Path: "a.txt", Before: entryPtr(x), After: entryPtr(y)})}
	in := e04In("TM-C", []Entry{x}, e04Points(1, withIno(fileEntry("a.txt", "Y"), 9)))
	in.Ledger = ledger
	wantVerdicts(t, evalE04(in), all4(VerdictNoEffect))
}

// ---- Finding 5: witness references cover object identity. ----

func TestWitnessManifestRefsCoverIdentity(t *testing.T) {
	a := withIno(fileEntry("a.txt", "A"), 1)
	b := withIno(fileEntry("a.txt", "A"), 7)
	ra := evalE04(e04In("TM-C", []Entry{a}, e04Points(0, a)))
	rb := evalE04(e04In("TM-C", []Entry{a}, e04Points(0, b)))
	m := manifest(PointBaseline, a)
	if len(ra.Witness.Manifests) == 0 || ra.Witness.Manifests[0].Digest != m.IdentityDigest() {
		t.Fatalf("ref %+v is not the identity digest %s", ra.Witness.Manifests, m.IdentityDigest())
	}
	// Two observations that differ only in inode numbers have distinct references.
	if ra.Witness.Manifests[1].Digest == rb.Witness.Manifests[1].Digest {
		t.Fatalf("manifests that differ only in identity share the reference %s", ra.Witness.Manifests[1].Digest)
	}
	// Digest is the content digest and stays identity-free.
	m2 := manifest(PointBaseline, b)
	if m.Digest() != m2.Digest() || m.IdentityDigest() == m2.IdentityDigest() {
		t.Fatalf("Digest/IdentityDigest contract broke")
	}
}
