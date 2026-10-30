package evalscore

import (
	"fmt"
	"math"
)

// ClopperPearson returns the exact two-sided (1-alpha) Clopper-Pearson interval
// for x successes in n independent trials. It is the quantile form of the
// beta distribution: lower = Beta(x, n-x+1) at alpha/2 and upper =
// Beta(x+1, n-x) at 1-alpha/2.
//
// The result is a description of the observed counts under a common-probability
// Bernoulli working model (tbound-thesis.md section 7.2.1). It is not a
// population estimate.
func ClopperPearson(x, n int, alpha float64) (float64, float64, error) {
	if err := checkBinomial(x, n, alpha); err != nil {
		return 0, 0, err
	}
	a := alpha / 2
	lo, hi := 0.0, 1.0
	if x > 0 {
		lo = betaQuantile(a, float64(x), float64(n-x+1))
	}
	if x < n {
		hi = betaQuantile(1-a, float64(x+1), float64(n-x))
	}
	return lo, hi, nil
}

// ClopperPearsonUpper returns the exact one-sided (1-alpha) upper bound for x
// successes in n trials. The thesis states its PVR bound in this form
// (tbound-thesis.md section 7.2).
func ClopperPearsonUpper(x, n int, alpha float64) (float64, error) {
	if err := checkBinomial(x, n, alpha); err != nil {
		return 0, err
	}
	if x == n {
		return 1, nil
	}
	return betaQuantile(1-alpha, float64(x+1), float64(n-x)), nil
}

// ClopperPearsonLower returns the exact one-sided (1-alpha) lower bound for x
// successes in n trials.
func ClopperPearsonLower(x, n int, alpha float64) (float64, error) {
	if err := checkBinomial(x, n, alpha); err != nil {
		return 0, err
	}
	if x == 0 {
		return 0, nil
	}
	return betaQuantile(alpha, float64(x), float64(n-x+1)), nil
}

func checkBinomial(x, n int, alpha float64) error {
	switch {
	case n < 1:
		return fmt.Errorf("clopper-pearson: n = %d, want >= 1", n)
	case x < 0 || x > n:
		return fmt.Errorf("clopper-pearson: x = %d outside 0..%d", x, n)
	case !(alpha > 0 && alpha < 1):
		return fmt.Errorf("clopper-pearson: alpha = %v, want 0 < alpha < 1", alpha)
	}
	return nil
}

// betaQuantile returns p with regIncBeta(p, a, b) = q. regIncBeta is strictly
// increasing in p, so bisection gives a unique result. The fixed iteration count
// makes the output deterministic.
func betaQuantile(q, a, b float64) float64 {
	lo, hi := 0.0, 1.0
	for i := 0; i < 200; i++ {
		mid := (lo + hi) / 2
		if regIncBeta(mid, a, b) < q {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

// regIncBeta is the regularized incomplete beta function I_x(a, b), evaluated
// with the continued fraction of Numerical Recipes (betai/betacf).
func regIncBeta(x, a, b float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	front := math.Exp(logGamma(a+b) - logGamma(a) - logGamma(b) + a*math.Log(x) + b*math.Log1p(-x))
	if x < (a+1)/(a+b+2) {
		return front * betaCF(x, a, b) / a
	}
	return 1 - front*betaCF(1-x, b, a)/b
}

func logGamma(v float64) float64 {
	r, _ := math.Lgamma(v)
	return r
}

// betaCF evaluates the continued fraction for the incomplete beta function by
// the modified Lentz method.
func betaCF(x, a, b float64) float64 {
	const (
		maxIter = 10000
		eps     = 1e-15
		fpmin   = 1e-300
	)
	qab, qap, qam := a+b, a+1, a-1
	c := 1.0
	d := 1 - qab*x/qap
	if math.Abs(d) < fpmin {
		d = fpmin
	}
	d = 1 / d
	h := d
	for m := 1; m <= maxIter; m++ {
		fm := float64(m)
		m2 := 2 * fm
		aa := fm * (b - fm) * x / ((qam + m2) * (a + m2))
		d = 1 + aa*d
		if math.Abs(d) < fpmin {
			d = fpmin
		}
		c = 1 + aa/c
		if math.Abs(c) < fpmin {
			c = fpmin
		}
		d = 1 / d
		h *= d * c
		aa = -(a + fm) * (qab + fm) * x / ((a + m2) * (qap + m2))
		d = 1 + aa*d
		if math.Abs(d) < fpmin {
			d = fpmin
		}
		c = 1 + aa/c
		if math.Abs(c) < fpmin {
			c = fpmin
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < eps {
			break
		}
	}
	return h
}
