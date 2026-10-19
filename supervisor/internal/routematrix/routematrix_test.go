package routematrix

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Class names are synthetic fixture labels, not a spec vocabulary.
const (
	fixtureClassA SensitiveClass = "fixture-class-a"
	fixtureClassB SensitiveClass = "fixture-class-b"
)

// sampleRef is one well-formed reference used to show what happens when a single
// test is cited for every cell.
const sampleRef = "supervisor/internal/sessionrepo.TestSessionRepositoryDeniedReadMatrixE2E"

// allowedFor returns a treatment that the spec permits for the route.
func allowedFor(r Route) Treatment {
	switch r {
	case RouteDeclaredRead:
		return TreatmentOmitted
	case RouteBashView:
		return TreatmentMarker
	case RouteDirectRuntime:
		return TreatmentUnreachable
	default:
		return TreatmentProposalDenied
	}
}

// fixtureRef returns a well-formed reference unique to one (route, class, kind) slot.
func fixtureRef(r Route, c SensitiveClass, k EvidenceKind) string {
	return "supervisor/internal/routematrix/fixtures/" + string(r) + "/" + string(c) + ".Test_" + string(k)
}

// coveredCell registers a Covered cell with one evidence entry per kind the route
// requires, each with its own fixture reference.
func coveredCell(r Route, c SensitiveClass) Cell {
	cell := Cell{Route: r, Class: c, Treatment: allowedFor(r), Status: StatusCovered}
	for _, k := range requiredEvidence[r] {
		cell.Evidence = append(cell.Evidence, Evidence{Kind: k, TestRef: fixtureRef(r, c, k)})
	}
	return cell
}

// fullMatrix covers every route for both fixture classes with a valid treatment
// and every required evidence kind, each with a unique reference.
func fullMatrix() Matrix {
	m := Matrix{Classes: []SensitiveClass{fixtureClassA, fixtureClassB}}
	for _, r := range Routes {
		for _, c := range m.Classes {
			m.Cells = append(m.Cells, coveredCell(r, c))
		}
	}
	return m
}

// fixtureLookup resolves exactly the references that fullMatrix registers.
func fixtureLookup() func(string) bool {
	known := make(map[string]bool)
	for _, c := range fullMatrix().Cells {
		for _, e := range c.Evidence {
			known[e.TestRef] = true
		}
	}
	return func(ref string) bool { return known[ref] }
}

// copyMatrix returns a matrix with fresh slices, including each cell's evidence,
// so tests never alias fullMatrix.
func copyMatrix(m Matrix) Matrix {
	out := Matrix{
		Classes: append([]SensitiveClass(nil), m.Classes...),
		Cells:   make([]Cell, len(m.Cells)),
	}
	for i, c := range m.Cells {
		c.Evidence = append([]Evidence(nil), c.Evidence...)
		out.Cells[i] = c
	}
	return out
}

// mutateCell returns a copy of m with the (route, class) cell edited by f.
func mutateCell(t *testing.T, m Matrix, r Route, c SensitiveClass, f func(*Cell)) Matrix {
	t.Helper()
	out := copyMatrix(m)
	for i := range out.Cells {
		if out.Cells[i].Route == r && out.Cells[i].Class == c {
			f(&out.Cells[i])
			return out
		}
	}
	t.Fatalf("no cell for route %q class %q", r, c)
	return out
}

// mutateEvidence returns a copy of m with the evidence entry of kind k in the
// (route, class) cell edited by f.
func mutateEvidence(t *testing.T, m Matrix, r Route, c SensitiveClass, k EvidenceKind, f func(*Evidence)) Matrix {
	t.Helper()
	return mutateCell(t, m, r, c, func(cell *Cell) {
		for i := range cell.Evidence {
			if cell.Evidence[i].Kind == k {
				f(&cell.Evidence[i])
				return
			}
		}
		t.Fatalf("no %q evidence for route %q class %q", k, r, c)
	})
}

// dropEvidence returns a copy of m without the evidence entry of kind k in the
// (route, class) cell.
func dropEvidence(t *testing.T, m Matrix, r Route, c SensitiveClass, k EvidenceKind) Matrix {
	t.Helper()
	return mutateCell(t, m, r, c, func(cell *Cell) {
		var kept []Evidence
		for _, e := range cell.Evidence {
			if e.Kind != k {
				kept = append(kept, e)
			}
		}
		if len(kept) == len(cell.Evidence) {
			t.Fatalf("no %q evidence to drop for route %q class %q", k, r, c)
		}
		cell.Evidence = kept
	})
}

func withRef(ref string) func(*Evidence) {
	return func(e *Evidence) { e.TestRef = ref }
}

func mustCoverage(t *testing.T, m Matrix, lookup func(string) bool) Report {
	t.Helper()
	rep, err := Coverage(m, lookup)
	if err != nil {
		t.Fatalf("Coverage: %v", err)
	}
	return rep
}

// assertIncomplete requires Complete to be false and some reason to mention substr.
func assertIncomplete(t *testing.T, rep Report, substr string) {
	t.Helper()
	if rep.Complete {
		t.Errorf("Complete = true, want false (reasons %v)", rep.Reasons)
		return
	}
	for _, r := range rep.Reasons {
		if strings.Contains(r, substr) {
			return
		}
	}
	t.Errorf("no reason mentions %q; reasons = %v", substr, rep.Reasons)
}

func assertErr(t *testing.T, err error, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want errors.Is %v", err, want)
	}
}

func TestValidFullMatrixPasses(t *testing.T) {
	if err := Validate(fullMatrix()); err != nil {
		t.Fatalf("Validate(full valid matrix) = %v, want nil", err)
	}
}

func TestMissingCellRejected(t *testing.T) {
	m := fullMatrix()
	m.Cells = m.Cells[:len(m.Cells)-1]
	assertErr(t, Validate(m), ErrMissingCell)
}

func TestDuplicateCellRejected(t *testing.T) {
	m := fullMatrix()
	dup := coveredCell(RouteBashView, fixtureClassA)
	dup.Treatment = TreatmentUnreachable
	m.Cells = append(m.Cells, dup)
	assertErr(t, Validate(m), ErrDuplicateCell)
}

func TestUnknownRouteRejected(t *testing.T) {
	m := mutateCell(t, fullMatrix(), RouteBashView, fixtureClassA, func(c *Cell) {
		c.Route = Route("made-up-route")
	})
	assertErr(t, Validate(m), ErrUnknownRoute)
}

func TestUnknownTreatmentRejected(t *testing.T) {
	m := mutateCell(t, fullMatrix(), RouteDeclaredRead, fixtureClassB, func(c *Cell) {
		c.Treatment = Treatment("confidential")
	})
	assertErr(t, Validate(m), ErrUnknownTreatment)
}

func TestUnknownStatusRejected(t *testing.T) {
	m := mutateCell(t, fullMatrix(), RouteDeclaredRead, fixtureClassB, func(c *Cell) {
		c.Status = Status("Passed")
	})
	assertErr(t, Validate(m), ErrUnknownStatus)
}

func TestUnregisteredClassRejected(t *testing.T) {
	m := mutateCell(t, fullMatrix(), RouteBashView, fixtureClassB, func(c *Cell) {
		c.Class = SensitiveClass("fixture-class-z")
	})
	assertErr(t, Validate(m), ErrUnknownClass)
}

func TestTreatmentAllowedPerRoute(t *testing.T) {
	cases := []struct {
		route     Route
		treatment Treatment
		wantErr   error
	}{
		{RouteDeclaredRead, TreatmentOmitted, nil},
		{RouteDeclaredRead, TreatmentMarker, nil},
		{RouteDeclaredRead, TreatmentUnreachable, nil},
		{RouteDeclaredRead, TreatmentProposalDenied, nil},
		{RouteBashView, TreatmentOmitted, nil},
		{RouteBashView, TreatmentMarker, nil},
		{RouteBashView, TreatmentUnreachable, nil},
		{RouteBashView, TreatmentProposalDenied, nil},
		{RouteDirectRuntime, TreatmentOmitted, nil},
		{RouteDirectRuntime, TreatmentMarker, nil},
		{RouteDirectRuntime, TreatmentUnreachable, nil},
		{RouteDirectRuntime, TreatmentProposalDenied, ErrTreatmentNotAllowed},
		{RouteProviderBroker, TreatmentOmitted, ErrTreatmentNotAllowed},
		{RouteProviderBroker, TreatmentMarker, ErrTreatmentNotAllowed},
		{RouteProviderBroker, TreatmentUnreachable, ErrTreatmentNotAllowed},
		{RouteProviderBroker, TreatmentProposalDenied, nil},
	}
	for _, tc := range cases {
		m := mutateCell(t, fullMatrix(), tc.route, fixtureClassA, func(c *Cell) {
			c.Treatment = tc.treatment
		})
		err := Validate(m)
		if tc.wantErr == nil {
			if err != nil {
				t.Errorf("route %q treatment %q: Validate = %v, want nil", tc.route, tc.treatment, err)
			}
			continue
		}
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("route %q treatment %q: Validate = %v, want %v", tc.route, tc.treatment, err, tc.wantErr)
		}
	}
}

func TestEvidenceKindRulesEnforced(t *testing.T) {
	cases := map[string]struct {
		m       Matrix
		wantErr error
	}{
		"unknown kind": {
			m: mutateCell(t, fullMatrix(), RouteBashView, fixtureClassA, func(c *Cell) {
				c.Evidence = append(c.Evidence, Evidence{Kind: EvidenceKind("Bogus"), TestRef: sampleRef})
			}),
			wantErr: ErrUnknownEvidence,
		},
		"kind not applicable to read route": {
			m: mutateCell(t, fullMatrix(), RouteBashView, fixtureClassA, func(c *Cell) {
				c.Evidence = append(c.Evidence, Evidence{Kind: EvidenceProviderChannelDisclosure, TestRef: sampleRef})
			}),
			wantErr: ErrEvidenceNotAllowed,
		},
		"read kind on broker": {
			m: mutateCell(t, fullMatrix(), RouteProviderBroker, fixtureClassA, func(c *Cell) {
				c.Evidence = append(c.Evidence, Evidence{Kind: EvidenceAttemptedRead, TestRef: sampleRef})
			}),
			wantErr: ErrEvidenceNotAllowed,
		},
		"duplicate kind in cell": {
			m: mutateCell(t, fullMatrix(), RouteDirectRuntime, fixtureClassB, func(c *Cell) {
				c.Evidence = append(c.Evidence, Evidence{Kind: EvidenceSuccessfulRead, TestRef: sampleRef})
			}),
			wantErr: ErrDuplicateEvidence,
		},
	}
	for name, tc := range cases {
		if err := Validate(tc.m); !errors.Is(err, tc.wantErr) {
			t.Errorf("%s: Validate = %v, want %v", name, err, tc.wantErr)
		}
	}
}

func TestEvidenceEntryWithEmptyRefRejected(t *testing.T) {
	m := mutateEvidence(t, fullMatrix(), RouteBashView, fixtureClassA, EvidenceAttemptedRead, withRef(""))
	assertErr(t, Validate(m), ErrMissingTestRef)
}

func TestWellFormedTestRefsAccepted(t *testing.T) {
	for _, ref := range []string{
		"supervisor/internal/sessionrepo.TestSessionRepositoryDeniedReadMatrixE2E",
		"a/b.Test1",
		"a/b.Test_x",
		"x.y/z.TestA",
	} {
		m := mutateEvidence(t, fullMatrix(), RouteBashView, fixtureClassA, EvidenceAttemptedRead, withRef(ref))
		if err := Validate(m); err != nil {
			t.Errorf("ref %q: Validate = %v, want nil", ref, err)
		}
	}
}

func TestMalformedTestRefRejected(t *testing.T) {
	bad := []string{
		"sessionrepo.TestX",                          // no package path
		"supervisor/internal/sessionrepo_test.go:33", // file:line form
		"supervisor/internal/sessionrepo.Testx",      // lowercase after Test
		"supervisor/internal/sessionrepo.TestX.Sub",  // extra dot
		"supervisor//sessionrepo.TestX",              // empty segment
		"supervisor/internal/../x.TestX",             // dot segment
		"/supervisor/internal.TestX",                 // leading slash
		"supervisor/internal/sessionrepo.TestX()",    // parentheses
		"supervisor/internal/sessionrepo.TestX Y",    // whitespace
		"supervisor/internal/sessionrepo.Test",       // no name after Test
		"supervisor/internal/sessionrepo.BenchmarkX", // not a Test name
	}
	for _, ref := range bad {
		m := mutateEvidence(t, fullMatrix(), RouteDirectRuntime, fixtureClassA, EvidenceSuccessfulRead, withRef(ref))
		if err := Validate(m); !errors.Is(err, ErrMalformedTestRef) {
			t.Errorf("ref %q: Validate = %v, want ErrMalformedTestRef", ref, err)
		}
	}
}

func TestMalformedRefOnPendingCellRejected(t *testing.T) {
	m := mutateCell(t, fullMatrix(), RouteDirectRuntime, fixtureClassA, func(c *Cell) {
		c.Status = StatusPendingGuest
		c.Reason = "guest run pending"
		c.Evidence = []Evidence{{Kind: EvidenceAttemptedRead, TestRef: "not-a-ref"}}
	})
	assertErr(t, Validate(m), ErrMalformedTestRef)
}

func TestPendingWithoutReasonRejected(t *testing.T) {
	m := mutateCell(t, fullMatrix(), RouteDeclaredRead, fixtureClassB, func(c *Cell) {
		c.Status = StatusPendingGuest
		c.Evidence = nil
		c.Reason = ""
	})
	assertErr(t, Validate(m), ErrMissingReason)
}

func TestPendingWithReasonAccepted(t *testing.T) {
	m := mutateCell(t, fullMatrix(), RouteDeclaredRead, fixtureClassB, func(c *Cell) {
		c.Status = StatusPendingGuest
		c.Evidence = nil
		c.Reason = "requires guest VM run"
	})
	if err := Validate(m); err != nil {
		t.Fatalf("Validate(pending with reason) = %v, want nil", err)
	}
}

func TestEmptyMatrixInvalid(t *testing.T) {
	cases := map[string]Matrix{
		"zero value":   {},
		"classes only": {Classes: []SensitiveClass{fixtureClassA}},
		"cells only":   {Cells: []Cell{coveredCell(RouteBashView, fixtureClassA)}},
		"empty slices": {Classes: []SensitiveClass{}, Cells: []Cell{}},
	}
	for name, m := range cases {
		if err := Validate(m); !errors.Is(err, ErrEmptyMatrix) {
			t.Errorf("%s: Validate = %v, want ErrEmptyMatrix", name, err)
		}
		if _, err := Coverage(m, fixtureLookup()); !errors.Is(err, ErrEmptyMatrix) {
			t.Errorf("%s: Coverage err = %v, want ErrEmptyMatrix", name, err)
		}
	}
}

func TestClassListValidation(t *testing.T) {
	m := fullMatrix()
	m.Classes = []SensitiveClass{fixtureClassA, fixtureClassA}
	assertErr(t, Validate(m), ErrDuplicateClass)

	m = fullMatrix()
	m.Classes = []SensitiveClass{fixtureClassA, ""}
	assertErr(t, Validate(m), ErrEmptyClass)
}

func TestCoverageCountsAndCompleteWhenAllCovered(t *testing.T) {
	rep := mustCoverage(t, fullMatrix(), fixtureLookup())
	if !rep.Complete {
		t.Errorf("Complete = false for all-covered matrix; reasons %v", rep.Reasons)
	}
	if len(rep.Reasons) != 0 {
		t.Errorf("Reasons = %v, want none", rep.Reasons)
	}
	if rep.Total != 8 {
		t.Errorf("Total = %d, want 8", rep.Total)
	}
	if rep.ByStatus[StatusCovered] != 8 || rep.ByStatus[StatusPendingGuest] != 0 || rep.ByStatus[StatusMissing] != 0 {
		t.Errorf("ByStatus = %v", rep.ByStatus)
	}
	for _, r := range Routes {
		if rep.ByRoute[r][StatusCovered] != 2 {
			t.Errorf("ByRoute[%q][Covered] = %d, want 2", r, rep.ByRoute[r][StatusCovered])
		}
	}
}

func TestCompleteFalseWithPendingOrMissing(t *testing.T) {
	pending := mutateCell(t, fullMatrix(), RouteBashView, fixtureClassA, func(c *Cell) {
		c.Status = StatusPendingGuest
		c.Evidence = nil
		c.Reason = "guest run pending"
	})
	missing := mutateCell(t, fullMatrix(), RouteProviderBroker, fixtureClassB, func(c *Cell) {
		c.Status = StatusMissing
		c.Evidence = nil
	})
	rep := mustCoverage(t, pending, fixtureLookup())
	assertIncomplete(t, rep, "status Pending_Guest")
	if rep.ByStatus[StatusPendingGuest] != 1 || rep.ByRoute[RouteBashView][StatusPendingGuest] != 1 {
		t.Errorf("pending counts wrong: %v / %v", rep.ByStatus, rep.ByRoute[RouteBashView])
	}
	rep = mustCoverage(t, missing, fixtureLookup())
	assertIncomplete(t, rep, "status Missing")
	if rep.ByStatus[StatusMissing] != 1 || rep.ByRoute[RouteProviderBroker][StatusMissing] != 1 {
		t.Errorf("missing counts wrong: %v / %v", rep.ByStatus, rep.ByRoute[RouteProviderBroker])
	}
}

func TestCoverageRejectsInvalidMatrix(t *testing.T) {
	m := fullMatrix()
	m.Cells = m.Cells[:len(m.Cells)-1]
	rep, err := Coverage(m, fixtureLookup())
	assertErr(t, err, ErrMissingCell)
	if rep.Complete {
		t.Errorf("Complete = true for invalid matrix")
	}
}

// TestNilLookupNotComplete: without a lookup the references are unverified, so the
// report must not be Complete.
func TestNilLookupNotComplete(t *testing.T) {
	rep, err := Coverage(fullMatrix(), nil)
	if err != nil {
		t.Fatalf("Coverage(nil lookup): %v", err)
	}
	assertIncomplete(t, rep, "lookup")
}

func TestNonexistentRefNotComplete(t *testing.T) {
	const nonexistent = "supervisor/internal/routematrix/fixtures/nonexistent.TestNope"
	m := mutateEvidence(t, fullMatrix(), RouteBashView, fixtureClassA, EvidenceSuccessfulRead, withRef(nonexistent))
	if err := Validate(m); err != nil {
		t.Fatalf("Validate(nonexistent but well-formed ref) = %v, want nil", err)
	}
	rep := mustCoverage(t, m, fixtureLookup())
	assertIncomplete(t, rep, nonexistent)
}

// TestReusedRefNotComplete covers the three ways one test can be cited more than
// once: on every cell, on two routes, and on two kinds of the same cell.
func TestReusedRefNotComplete(t *testing.T) {
	declared := fixtureRef(RouteDeclaredRead, fixtureClassA, EvidenceAttemptedRead)

	everyCell := fullMatrix()
	for i := range everyCell.Cells {
		for j := range everyCell.Cells[i].Evidence {
			everyCell.Cells[i].Evidence[j].TestRef = sampleRef
		}
	}

	acrossRoutes := fullMatrix()
	for i := range acrossRoutes.Cells {
		c := &acrossRoutes.Cells[i]
		if c.Class != fixtureClassA {
			continue
		}
		for j := range c.Evidence {
			if c.Evidence[j].Kind == EvidenceAttemptedRead || c.Evidence[j].Kind == EvidenceProviderChannelDisclosure {
				c.Evidence[j].TestRef = declared
			}
		}
	}

	sameCell := mutateEvidence(t, fullMatrix(), RouteBashView, fixtureClassB, EvidenceSuccessfulRead, withRef(fixtureRef(RouteBashView, fixtureClassB, EvidenceAttemptedRead)))

	cases := map[string]Matrix{
		"one ref on every cell":           everyCell,
		"declared-read ref on all routes": acrossRoutes,
		"same ref for two kinds in cell":  sameCell,
	}
	for name, m := range cases {
		if err := Validate(m); err != nil {
			t.Fatalf("%s: Validate = %v, want nil (reuse is a coverage fact, not a shape error)", name, err)
		}
		rep := mustCoverage(t, m, func(ref string) bool { return true })
		assertIncomplete(t, rep, "reused")
	}
}

func TestMissingRequiredEvidenceKindNotComplete(t *testing.T) {
	cases := []struct {
		name string
		m    Matrix
		kind EvidenceKind
	}{
		{"declared-read attempted", dropEvidence(t, fullMatrix(), RouteDeclaredRead, fixtureClassA, EvidenceAttemptedRead), EvidenceAttemptedRead},
		{"bash-view successful", dropEvidence(t, fullMatrix(), RouteBashView, fixtureClassB, EvidenceSuccessfulRead), EvidenceSuccessfulRead},
		{"direct-runtime successful", dropEvidence(t, fullMatrix(), RouteDirectRuntime, fixtureClassA, EvidenceSuccessfulRead), EvidenceSuccessfulRead},
		{"broker disclosure", dropEvidence(t, fullMatrix(), RouteProviderBroker, fixtureClassB, EvidenceProviderChannelDisclosure), EvidenceProviderChannelDisclosure},
	}
	for _, tc := range cases {
		if err := Validate(tc.m); err != nil {
			t.Fatalf("%s: Validate = %v, want nil (missing kind is a coverage fact)", tc.name, err)
		}
		rep := mustCoverage(t, tc.m, fixtureLookup())
		assertIncomplete(t, rep, "missing required evidence kind "+string(tc.kind))
	}
}

func TestDigestOrderIndependent(t *testing.T) {
	m := fullMatrix()
	want := Digest(m)
	if len(want) != 64 {
		t.Fatalf("Digest length = %d, want 64 hex chars", len(want))
	}

	reversed := copyMatrix(m)
	for i, j := 0, len(reversed.Cells)-1; i < j; i, j = i+1, j-1 {
		reversed.Cells[i], reversed.Cells[j] = reversed.Cells[j], reversed.Cells[i]
	}
	for i := range reversed.Cells {
		ev := reversed.Cells[i].Evidence
		for a, b := 0, len(ev)-1; a < b; a, b = a+1, b-1 {
			ev[a], ev[b] = ev[b], ev[a]
		}
	}
	reversed.Classes = []SensitiveClass{fixtureClassB, fixtureClassA}
	if got := Digest(reversed); got != want {
		t.Errorf("reordered matrix digest = %s, want %s", got, want)
	}
}

func TestDigestDoesNotMutateInput(t *testing.T) {
	m := fullMatrix()
	before := copyMatrix(m)
	Digest(m)
	if !reflect.DeepEqual(m, before) {
		t.Errorf("Digest mutated its input")
	}
}

func TestDigestPerFieldSensitivity(t *testing.T) {
	base := fullMatrix()
	want := Digest(base)
	route, class := RouteBashView, fixtureClassA
	cellMutations := map[string]func(*Cell){
		"route":          func(c *Cell) { c.Route = RouteDeclaredRead },
		"class":          func(c *Cell) { c.Class = fixtureClassB },
		"treatment":      func(c *Cell) { c.Treatment = TreatmentProposalDenied },
		"status":         func(c *Cell) { c.Status = StatusPendingGuest },
		"reason":         func(c *Cell) { c.Reason = "x" },
		"guest artifact": func(c *Cell) { c.GuestArtifact = "guest-route-bash-view" },
	}
	for name, f := range cellMutations {
		m := mutateCell(t, base, route, class, f)
		if got := Digest(m); got == want {
			t.Errorf("changing %s did not change digest", name)
		}
	}

	evidenceMutations := map[string]Matrix{
		"evidence kind":    mutateEvidence(t, base, route, class, EvidenceAttemptedRead, func(e *Evidence) { e.Kind = EvidenceKind("Other") }),
		"evidence ref":     mutateEvidence(t, base, route, class, EvidenceSuccessfulRead, withRef("supervisor/internal/other.TestOther")),
		"evidence dropped": dropEvidence(t, base, route, class, EvidenceSuccessfulRead),
	}
	for name, m := range evidenceMutations {
		if got := Digest(m); got == want {
			t.Errorf("changing %s did not change digest", name)
		}
	}

	classRenamed := copyMatrix(base)
	classRenamed.Classes[0] = SensitiveClass("fixture-class-renamed")
	if Digest(classRenamed) == want {
		t.Errorf("renaming a class did not change digest")
	}

	dropped := copyMatrix(base)
	dropped.Cells = dropped.Cells[:len(dropped.Cells)-1]
	if Digest(dropped) == want {
		t.Errorf("dropping a cell did not change digest")
	}
}

func TestDigestLengthPrefixPreventsConcatenationCollisions(t *testing.T) {
	a := Matrix{Classes: []SensitiveClass{"ab", "c"}}
	b := Matrix{Classes: []SensitiveClass{"a", "bc"}}
	if Digest(a) == Digest(b) {
		t.Errorf("class boundary collision: %q and %q share a digest", a.Classes, b.Classes)
	}
	c := Matrix{Cells: []Cell{{Class: "ab", Reason: "c"}}}
	d := Matrix{Cells: []Cell{{Class: "a", Reason: "bc"}}}
	if Digest(c) == Digest(d) {
		t.Errorf("field boundary collision in cells")
	}
	e := Matrix{Cells: []Cell{{Evidence: []Evidence{{Kind: "ab", TestRef: "c"}}}}}
	f := Matrix{Cells: []Cell{{Evidence: []Evidence{{Kind: "a", TestRef: "bc"}}}}}
	if Digest(e) == Digest(f) {
		t.Errorf("field boundary collision in evidence")
	}
}

func TestRefsExistReportsMissingRefs(t *testing.T) {
	m := fullMatrix()
	missingA := "supervisor/internal/a.TestMissingA"
	missingB := "supervisor/internal/b.TestMissingB"
	m = mutateEvidence(t, m, RouteBashView, fixtureClassA, EvidenceAttemptedRead, withRef(missingA))
	m = mutateEvidence(t, m, RouteDeclaredRead, fixtureClassB, EvidenceSuccessfulRead, withRef(missingB))
	m = mutateEvidence(t, m, RouteDirectRuntime, fixtureClassB, EvidenceAttemptedRead, withRef(missingA))

	err := RefsExist(m, fixtureLookup())
	if !errors.Is(err, ErrMissingRef) {
		t.Fatalf("RefsExist = %v, want ErrMissingRef", err)
	}
	if !strings.Contains(err.Error(), missingA) || !strings.Contains(err.Error(), missingB) {
		t.Errorf("error %q does not name both missing refs", err)
	}
	if n := strings.Count(err.Error(), missingA); n != 1 {
		t.Errorf("missing ref %q reported %d times, want once", missingA, n)
	}
}

func TestRefsExistAllPresent(t *testing.T) {
	if err := RefsExist(fullMatrix(), fixtureLookup()); err != nil {
		t.Fatalf("RefsExist(all present) = %v, want nil", err)
	}
}

func TestRefsExistSkipsEmptyRefsAndRejectsNilLookup(t *testing.T) {
	m := mutateCell(t, fullMatrix(), RouteProviderBroker, fixtureClassA, func(c *Cell) {
		c.Status = StatusPendingGuest
		c.Evidence = nil
		c.Reason = "pending"
	})
	lookedUpEmpty := false
	if err := RefsExist(m, func(ref string) bool {
		if ref == "" {
			lookedUpEmpty = true
		}
		return true
	}); err != nil {
		t.Errorf("empty pending ref should not be looked up: %v", err)
	}
	if lookedUpEmpty {
		t.Errorf("empty ref was passed to lookup")
	}
	assertErr(t, RefsExist(m, nil), ErrNilLookup)
}

func TestZeroValuesDoNotPanic(t *testing.T) {
	zeroCell := Cell{}
	inputs := map[string]Matrix{
		"zero matrix":   {},
		"zero cell":     {Cells: []Cell{zeroCell}},
		"zero class":    {Classes: []SensitiveClass{""}, Cells: []Cell{zeroCell}},
		"nil and empty": {Classes: nil, Cells: []Cell{}},
	}
	for name, m := range inputs {
		m := m
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: panicked: %v", name, r)
				}
			}()
			if err := Validate(m); err == nil {
				t.Errorf("%s: Validate = nil, want error", name)
			}
			if _, err := Coverage(m, nil); err == nil {
				t.Errorf("%s: Coverage err = nil, want error", name)
			}
			_ = Digest(m)
			_ = RefsExist(m, nil)
			_ = RefsExist(m, func(string) bool { return false })
		}()
	}
	if err := Validate(Matrix{}); !errors.Is(err, ErrEmptyMatrix) {
		t.Errorf("zero Matrix: Validate = %v, want ErrEmptyMatrix", err)
	}
}

// A cell can name the guest artifact kind that its production evidence needs. The name is part of the
// registered matrix (its digest), and a malformed one is refused.
func TestGuestArtifactNameIsValidatedAndDigested(t *testing.T) {
	m := mutateCell(t, fullMatrix(), RouteBashView, fixtureClassA, func(c *Cell) { c.GuestArtifact = "guest-route-bash-view" })
	if err := Validate(m); err != nil {
		t.Fatalf("a named guest artifact was refused: %v", err)
	}
	if Digest(m) == Digest(fullMatrix()) {
		t.Fatal("the guest artifact name is not in the digest")
	}
	for _, bad := range []string{"Guest", "guest route", "-x", "guest/route", strings.Repeat("a", 65)} {
		m := mutateCell(t, fullMatrix(), RouteBashView, fixtureClassA, func(c *Cell) { c.GuestArtifact = bad })
		if err := Validate(m); !errors.Is(err, ErrMalformedGuestArtifact) {
			t.Errorf("%q: err = %v, want ErrMalformedGuestArtifact", bad, err)
		}
	}
}
