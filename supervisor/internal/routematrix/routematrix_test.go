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

func coveredCell(r Route, c SensitiveClass) Cell {
	return Cell{Route: r, Class: c, Treatment: allowedFor(r), TestRef: sampleRef, Status: StatusCovered}
}

// fullMatrix covers every route for both fixture classes with a valid treatment.
func fullMatrix() Matrix {
	m := Matrix{Classes: []SensitiveClass{fixtureClassA, fixtureClassB}}
	for _, r := range Routes {
		for _, c := range m.Classes {
			m.Cells = append(m.Cells, coveredCell(r, c))
		}
	}
	return m
}

// copyMatrix returns a matrix with fresh slices so tests never alias fullMatrix.
func copyMatrix(m Matrix) Matrix {
	return Matrix{
		Classes: append([]SensitiveClass(nil), m.Classes...),
		Cells:   append([]Cell(nil), m.Cells...),
	}
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

func TestBrokerNeverClaimsConfidentiality(t *testing.T) {
	for _, tr := range []Treatment{TreatmentOmitted, TreatmentMarker, TreatmentUnreachable} {
		m := mutateCell(t, fullMatrix(), RouteProviderBroker, fixtureClassB, func(c *Cell) {
			c.Treatment = tr
		})
		if err := Validate(m); !errors.Is(err, ErrTreatmentNotAllowed) {
			t.Errorf("broker with %q: Validate = %v, want ErrTreatmentNotAllowed", tr, err)
		}
	}
}

func TestCoveredWithoutTestRefRejected(t *testing.T) {
	m := mutateCell(t, fullMatrix(), RouteBashView, fixtureClassA, func(c *Cell) {
		c.TestRef = ""
	})
	assertErr(t, Validate(m), ErrMissingTestRef)
}

func TestWellFormedTestRefsAccepted(t *testing.T) {
	for _, ref := range []string{
		"supervisor/internal/sessionrepo.TestSessionRepositoryDeniedReadMatrixE2E",
		"a/b.Test1",
		"a/b.Test_x",
		"x.y/z.TestA",
	} {
		m := mutateCell(t, fullMatrix(), RouteBashView, fixtureClassA, func(c *Cell) {
			c.TestRef = ref
		})
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
		m := mutateCell(t, fullMatrix(), RouteDirectRuntime, fixtureClassA, func(c *Cell) {
			c.TestRef = ref
		})
		if err := Validate(m); !errors.Is(err, ErrMalformedTestRef) {
			t.Errorf("ref %q: Validate = %v, want ErrMalformedTestRef", ref, err)
		}
	}
}

func TestMalformedRefOnPendingCellRejected(t *testing.T) {
	m := mutateCell(t, fullMatrix(), RouteDirectRuntime, fixtureClassA, func(c *Cell) {
		c.Status = StatusPendingGuest
		c.Reason = "guest run pending"
		c.TestRef = "not-a-ref"
	})
	assertErr(t, Validate(m), ErrMalformedTestRef)
}

func TestPendingWithoutReasonRejected(t *testing.T) {
	m := mutateCell(t, fullMatrix(), RouteDeclaredRead, fixtureClassB, func(c *Cell) {
		c.Status = StatusPendingGuest
		c.TestRef = ""
		c.Reason = ""
	})
	assertErr(t, Validate(m), ErrMissingReason)
}

func TestPendingWithReasonAccepted(t *testing.T) {
	m := mutateCell(t, fullMatrix(), RouteDeclaredRead, fixtureClassB, func(c *Cell) {
		c.Status = StatusPendingGuest
		c.TestRef = ""
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
		if _, err := Coverage(m); !errors.Is(err, ErrEmptyMatrix) {
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
	rep, err := Coverage(fullMatrix())
	if err != nil {
		t.Fatalf("Coverage: %v", err)
	}
	if !rep.Complete {
		t.Errorf("Complete = false for all-covered matrix")
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
		c.TestRef = ""
		c.Reason = "guest run pending"
	})
	missing := mutateCell(t, fullMatrix(), RouteProviderBroker, fixtureClassB, func(c *Cell) {
		c.Status = StatusMissing
		c.TestRef = ""
	})
	for name, m := range map[string]Matrix{"pending": pending, "missing": missing} {
		rep, err := Coverage(m)
		if err != nil {
			t.Fatalf("%s: Coverage: %v", name, err)
		}
		if rep.Complete {
			t.Errorf("%s: Complete = true, want false (fail closed)", name)
		}
	}
	rep, err := Coverage(pending)
	if err != nil {
		t.Fatalf("Coverage(pending): %v", err)
	}
	if rep.ByStatus[StatusPendingGuest] != 1 || rep.ByRoute[RouteBashView][StatusPendingGuest] != 1 {
		t.Errorf("pending counts wrong: %v / %v", rep.ByStatus, rep.ByRoute[RouteBashView])
	}
	rep, err = Coverage(missing)
	if err != nil {
		t.Fatalf("Coverage(missing): %v", err)
	}
	if rep.ByStatus[StatusMissing] != 1 || rep.ByRoute[RouteProviderBroker][StatusMissing] != 1 {
		t.Errorf("missing counts wrong: %v / %v", rep.ByStatus, rep.ByRoute[RouteProviderBroker])
	}
}

func TestCoverageRejectsInvalidMatrix(t *testing.T) {
	m := fullMatrix()
	m.Cells = m.Cells[:len(m.Cells)-1]
	rep, err := Coverage(m)
	assertErr(t, err, ErrMissingCell)
	if rep.Complete {
		t.Errorf("Complete = true for invalid matrix")
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
	mutations := map[string]func(*Cell){
		"route":     func(c *Cell) { c.Route = RouteDeclaredRead },
		"class":     func(c *Cell) { c.Class = fixtureClassB },
		"treatment": func(c *Cell) { c.Treatment = TreatmentProposalDenied },
		"testref":   func(c *Cell) { c.TestRef = "supervisor/internal/other.TestOther" },
		"status":    func(c *Cell) { c.Status = StatusPendingGuest },
		"reason":    func(c *Cell) { c.Reason = "x" },
	}
	for name, f := range mutations {
		m := mutateCell(t, base, route, class, f)
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
	c := Matrix{Cells: []Cell{{Class: "ab", TestRef: "c"}}}
	d := Matrix{Cells: []Cell{{Class: "a", TestRef: "bc"}}}
	if Digest(c) == Digest(d) {
		t.Errorf("field boundary collision in cells")
	}
}

func TestRefsExistReportsMissingRefs(t *testing.T) {
	m := fullMatrix()
	missingA := "supervisor/internal/a.TestMissingA"
	missingB := "supervisor/internal/b.TestMissingB"
	m = mutateCell(t, m, RouteBashView, fixtureClassA, func(c *Cell) { c.TestRef = missingA })
	m = mutateCell(t, m, RouteDeclaredRead, fixtureClassB, func(c *Cell) { c.TestRef = missingB })
	m = mutateCell(t, m, RouteDirectRuntime, fixtureClassB, func(c *Cell) { c.TestRef = missingA })

	lookup := func(ref string) bool { return ref == sampleRef }
	err := RefsExist(m, lookup)
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
	lookup := func(ref string) bool { return ref == sampleRef }
	if err := RefsExist(fullMatrix(), lookup); err != nil {
		t.Fatalf("RefsExist(all present) = %v, want nil", err)
	}
}

func TestRefsExistSkipsEmptyRefsAndRejectsNilLookup(t *testing.T) {
	m := mutateCell(t, fullMatrix(), RouteProviderBroker, fixtureClassA, func(c *Cell) {
		c.Status = StatusPendingGuest
		c.TestRef = ""
		c.Reason = "pending"
	})
	if err := RefsExist(m, func(string) bool { return true }); err != nil {
		t.Errorf("empty pending ref should not be looked up: %v", err)
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
			if _, err := Coverage(m); err == nil {
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
