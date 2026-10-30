package wsoracle

import "testing"

// Round-2 review findings. Synthetic manifests; these check code paths only.

func wantNoGap(t *testing.T, r Report, kind string) {
	t.Helper()
	for _, g := range r.Gaps {
		if g.Kind == kind {
			t.Fatalf("unexpected gap %+v", g)
		}
	}
}

func gapPaths(r Report, kind string) []string {
	var out []string
	for _, g := range r.Gaps {
		if g.Kind == kind {
			out = append(out, g.Path)
		}
	}
	return out
}

// The reviewer's scenario: u.txt is outside the affected set, and a host edit
// changes it before publication and again during it. Publication did not
// write u.txt, so it is not a stale-baseline overwrite by publication, and the
// oracle cannot attribute the change either. The verdict is evidence_lost, with
// an outside_set_change gap, and never effect_present.
func TestWS06HostEditOutsideAffectedSetIsNotAnEffectButIsEvidenceLost(t *testing.T) {
	r := EvaluateWS06(WS06Input{
		AffectedPaths:   []string{"a.txt"},
		Baseline:        manifest("baseline", fileEntry("a.txt", "A0"), fileEntry("u.txt", "U0")),
		PrePublication:  manifest("pre-publication", fileEntry("a.txt", "A0"), fileEntry("u.txt", "U1")),
		PostPublication: manifest("post-publication", fileEntry("a.txt", "A1"), fileEntry("u.txt", "U2")),
		LockIdentity:    "lock",
	})
	wantVerdict(t, r, VerdictEvidenceLost)
	wantNoFindings(t, r)
	wantGap(t, r, GapOutsideSetChange, "post-publication")
	if got := gapPaths(r, GapOutsideSetChange); len(got) != 1 || got[0] != "u.txt" {
		t.Fatalf("outside_set_change paths = %v, want [u.txt]", got)
	}
}

// A change only during publication, to a path outside the set and not stale
// against the baseline, is equally unattributable.
func TestWS06ChangeOutsideAffectedSetDuringPublicationIsEvidenceLost(t *testing.T) {
	r := EvaluateWS06(WS06Input{
		AffectedPaths:   []string{"a.txt"},
		Baseline:        manifest("baseline", fileEntry("a.txt", "A0"), fileEntry("u.txt", "U0")),
		PrePublication:  manifest("pre-publication", fileEntry("a.txt", "A0"), fileEntry("u.txt", "U0")),
		PostPublication: manifest("post-publication", fileEntry("a.txt", "A1"), fileEntry("u.txt", "U2")),
		LockIdentity:    "lock",
	})
	wantVerdict(t, r, VerdictEvidenceLost)
	wantNoFindings(t, r)
	wantGap(t, r, GapOutsideSetChange, "post-publication")

	// A created or deleted path outside the set is a change as well.
	created := EvaluateWS06(WS06Input{
		AffectedPaths:   []string{"a.txt"},
		Baseline:        manifest("baseline", fileEntry("a.txt", "A0")),
		PrePublication:  manifest("pre-publication", fileEntry("a.txt", "A0")),
		PostPublication: manifest("post-publication", fileEntry("a.txt", "A1"), fileEntry("n.txt", "N")),
		LockIdentity:    "lock",
	})
	wantVerdict(t, created, VerdictEvidenceLost)
	wantGap(t, created, GapOutsideSetChange, "post-publication")
}

// In-set behaviour is unchanged: a single stale in-set overwrite is an effect
// with no gap, and a stale in-set object publication never touched is no effect.
func TestWS06InSetStaleOverwriteKeepsItsFindingAndRaisesNoOutsideGap(t *testing.T) {
	r := EvaluateWS06(WS06Input{
		AffectedPaths:   []string{"a.txt"},
		Baseline:        manifest("baseline", fileEntry("a.txt", "A0"), fileEntry("u.txt", "U0")),
		PrePublication:  manifest("pre-publication", fileEntry("a.txt", "H"), fileEntry("u.txt", "U0")),
		PostPublication: manifest("post-publication", fileEntry("a.txt", "A1"), fileEntry("u.txt", "U0")),
		LockIdentity:    "lock",
	})
	wantVerdict(t, r, VerdictEffectPresent)
	wantSingleFinding(t, r, "a.txt", ChangeModify, ReasonStaleBaselineOverwrite)
	wantNoGap(t, r, GapOutsideSetChange)
}

// A conflict in the set while publication changes only a path outside it is
// no baseline-conflict effect; the outside change is the unattributable part.
func TestWS06ConflictInSetWithOnlyOutsideChangeIsEvidenceLostNotEffect(t *testing.T) {
	r := EvaluateWS06(WS06Input{
		AffectedPaths:   []string{"c.txt"},
		Baseline:        manifest("baseline", fileEntry("c.txt", "C0"), fileEntry("u.txt", "U0")),
		PrePublication:  manifest("pre-publication", fileEntry("c.txt", "C1"), fileEntry("u.txt", "U0")),
		PostPublication: manifest("post-publication", fileEntry("c.txt", "C1"), fileEntry("u.txt", "U2")),
		LockIdentity:    "lock",
	})
	wantVerdict(t, r, VerdictEvidenceLost)
	wantNoFindings(t, r)
	wantGap(t, r, GapOutsideSetChange, "post-publication")
}

// With no usable affected set the missing-set gap already decides the verdict,
// and every path is not reported as an outside change.
func TestWS06MissingAffectedSetDoesNotFloodOutsideSetGaps(t *testing.T) {
	r := EvaluateWS06(WS06Input{
		Baseline:        manifest("baseline", fileEntry("a.txt", "A0")),
		PrePublication:  manifest("pre-publication", fileEntry("a.txt", "A0")),
		PostPublication: manifest("post-publication", fileEntry("a.txt", "A1")),
		LockIdentity:    "lock",
	})
	wantVerdict(t, r, VerdictEvidenceLost)
	wantGap(t, r, GapAffectedSetMissing, "affected_paths")
	wantNoGap(t, r, GapOutsideSetChange)
}

// The witness records, per manifest, whether it was complete. A manifest counts
// as complete only when it is marked Complete, carries no gap, and every entry
// passes the boundary check. Each other combination must read false.
func TestWitnessManifestCompleteReflectsFlagGapsAndEntryValidity(t *testing.T) {
	dup := []Entry{fileEntry("a.txt", "A"), fileEntry("a.txt", "B")}
	cases := []struct {
		name   string
		mutate func(m *Manifest)
		want   bool
	}{
		{"clean", func(m *Manifest) {}, true},
		{"marked incomplete", func(m *Manifest) { m.Complete = false }, false},
		{"complete flag with a gap", func(m *Manifest) { m.Gaps = []Gap{{Kind: GapUnreadable, Path: "a.txt"}} }, false},
		{"complete flag with an invalid entry", func(m *Manifest) { m.Entries = dup }, false},
		{"incomplete with a gap", func(m *Manifest) {
			m.Complete = false
			m.Gaps = []Gap{{Kind: GapUnreadable, Path: "a.txt"}}
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := manifest("post-publication", fileEntry("a.txt", "A"))
			tc.mutate(&m)
			ok := func(label string) Manifest { return manifest(label, fileEntry("a.txt", "A")) }
			ws05 := EvaluateWS05(WS05Input{Baseline: ok("baseline"), Sealed: ok("sealed"), PrePublication: ok("pre-publication"), PostPublication: m})
			ws06 := EvaluateWS06(WS06Input{AffectedPaths: []string{"a.txt"}, LockIdentity: "l",
				Baseline: ok("baseline"), PrePublication: ok("pre-publication"), PostPublication: m})
			for name, r := range map[string]Report{"WS-05": ws05, "WS-06": ws06} {
				var got, found = false, false
				for _, ref := range r.Witness.Manifests {
					switch ref.Label {
					case "post-publication":
						got, found = ref.Complete, true
					default:
						if !ref.Complete {
							t.Errorf("%s: clean manifest %q reported incomplete", name, ref.Label)
						}
					}
				}
				if !found || got != tc.want {
					t.Errorf("%s: witness complete = %v (found %v), want %v", name, got, found, tc.want)
				}
				if tc.want != (r.Verdict != VerdictEvidenceLost) {
					t.Errorf("%s: verdict %q inconsistent with complete = %v", name, r.Verdict, tc.want)
				}
			}
		})
	}
}
