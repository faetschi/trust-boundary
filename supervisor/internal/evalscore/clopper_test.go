package evalscore

import (
	"fmt"
	"math"
	"testing"
)

// Synthetic-only check of the code. These tests do not verify, support, or
// refute G1, H1, or any other thesis claim.

func assertNear(t *testing.T, label string, got, want, tol float64) {
	t.Helper()
	if math.IsNaN(got) || math.Abs(got-want) > tol {
		t.Fatalf("%s: got %.12f, want %.12f (tol %g)", label, got, want, tol)
	}
}

// The reference values below were computed outside this package by exact
// decimal binomial-tail bisection (python3 -I, 80-digit Decimal), so they are
// an independent oracle for the Go implementation.
func TestClopperPearsonIndependentReferenceValues(t *testing.T) {
	tests := []struct {
		x, n   int
		lo, hi float64
	}{
		{x: 5, n: 20, lo: 0.086571469101, hi: 0.491045871708},
		{x: 3, n: 10, lo: 0.066739511178, hi: 0.652452850060},
		{x: 0, n: 20, lo: 0, hi: 0.168433470983},
		{x: 20, n: 20, lo: 0.831566529017, hi: 1},
	}
	for _, tt := range tests {
		lo, hi, err := ClopperPearson(tt.x, tt.n, 0.05)
		if err != nil {
			t.Fatalf("ClopperPearson(%d,%d): %v", tt.x, tt.n, err)
		}
		assertNear(t, fmt.Sprintf("%d/%d lower", tt.x, tt.n), lo, tt.lo, 1e-9)
		assertNear(t, fmt.Sprintf("%d/%d upper", tt.x, tt.n), hi, tt.hi, 1e-9)
	}
}

func TestClopperPearsonEndpointsClosedForm(t *testing.T) {
	for _, n := range []int{1, 2, 20, 299} {
		fn := float64(n)
		lo, hi, err := ClopperPearson(0, n, 0.05)
		if err != nil {
			t.Fatalf("0/%d: %v", n, err)
		}
		assertNear(t, fmt.Sprintf("0/%d two-sided lower", n), lo, 0, 1e-12)
		assertNear(t, fmt.Sprintf("0/%d two-sided upper", n), hi, 1-math.Pow(0.025, 1/fn), 1e-10)

		lo, hi, err = ClopperPearson(n, n, 0.05)
		if err != nil {
			t.Fatalf("%d/%d: %v", n, n, err)
		}
		assertNear(t, fmt.Sprintf("%d/%d two-sided lower", n, n), lo, math.Pow(0.025, 1/fn), 1e-10)
		assertNear(t, fmt.Sprintf("%d/%d two-sided upper", n, n), hi, 1, 1e-12)

		up, err := ClopperPearsonUpper(0, n, 0.05)
		if err != nil {
			t.Fatalf("one-sided upper 0/%d: %v", n, err)
		}
		assertNear(t, fmt.Sprintf("0/%d one-sided upper", n), up, 1-math.Pow(0.05, 1/fn), 1e-10)

		low, err := ClopperPearsonLower(n, n, 0.05)
		if err != nil {
			t.Fatalf("one-sided lower %d/%d: %v", n, n, err)
		}
		assertNear(t, fmt.Sprintf("%d/%d one-sided lower", n, n), low, math.Pow(0.05, 1/fn), 1e-10)
	}
}

// tbound-thesis.md (section 7.2 and the section 7.2.1 decision text) states
// 0.9969% at 0/299 and 1.0002% at 0/298, and 299 is the minimum zero-event
// denominator meeting the 1% bound.
func TestZeroEventOneSidedBoundsMatchThesis(t *testing.T) {
	up299, err := ClopperPearsonUpper(0, 299, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	up298, err := ClopperPearsonUpper(0, 298, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	assertNear(t, "0/299 one-sided upper", up299, 0.009969, 5e-7)
	assertNear(t, "0/298 one-sided upper", up298, 0.010002, 5e-7)
	if !(up299 <= 0.01) {
		t.Fatalf("0/299 upper %.12f exceeds 1%%", up299)
	}
	if !(up298 > 0.01) {
		t.Fatalf("0/298 upper %.12f does not exceed 1%%", up298)
	}
}

func TestClopperPearsonSymmetryAndContainment(t *testing.T) {
	for n := 1; n <= 40; n++ {
		for x := 0; x <= n; x++ {
			label := fmt.Sprintf("%d/%d", x, n)
			lo, hi, err := ClopperPearson(x, n, 0.05)
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			mlo, mhi, err := ClopperPearson(n-x, n, 0.05)
			if err != nil {
				t.Fatalf("%s mirror: %v", label, err)
			}
			assertNear(t, label+" symmetry lower", lo, 1-mhi, 1e-10)
			assertNear(t, label+" symmetry upper", hi, 1-mlo, 1e-10)
			p := float64(x) / float64(n)
			if p < lo-1e-12 || p > hi+1e-12 {
				t.Fatalf("%s: point %.6f outside [%.6f, %.6f]", label, p, lo, hi)
			}
		}
	}
}

func TestClopperPearsonRejectsInvalidArguments(t *testing.T) {
	tests := []struct {
		name  string
		x, n  int
		alpha float64
	}{
		{name: "zero trials", x: 0, n: 0, alpha: 0.05},
		{name: "negative successes", x: -1, n: 5, alpha: 0.05},
		{name: "successes above trials", x: 6, n: 5, alpha: 0.05},
		{name: "alpha zero", x: 1, n: 5, alpha: 0},
		{name: "alpha one", x: 1, n: 5, alpha: 1},
		{name: "alpha NaN", x: 1, n: 5, alpha: math.NaN()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := ClopperPearson(tt.x, tt.n, tt.alpha); err == nil {
				t.Error("two-sided: want error")
			}
			if _, err := ClopperPearsonUpper(tt.x, tt.n, tt.alpha); err == nil {
				t.Error("one-sided upper: want error")
			}
			if _, err := ClopperPearsonLower(tt.x, tt.n, tt.alpha); err == nil {
				t.Error("one-sided lower: want error")
			}
		})
	}
}
