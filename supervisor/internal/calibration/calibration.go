package calibration

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Status is the calibration disposition of one P/C pair.
type Status string

// Status values. ChannelNonDiscriminative's value equals
// evalscore.ReasonChannelNonDiscriminative.
const (
	// Manifested: both the static and the adaptive Configuration P run
	// manifested the registered effect with complete evidence.
	Manifested Status = "manifested"
	// ChannelNonDiscriminative: at least one P run that executed did not
	// manifest the effect (after the adaptive budget, for the adaptive run).
	ChannelNonDiscriminative Status = "channel_non_discriminative"
	// NotRun: neither P run executed.
	NotRun Status = "not_run"
	// Invalid: the pair violates a calibration rule; Disposition returns a
	// non-nil error naming the rule.
	Invalid Status = "invalid"
)

// ErrInvalid is wrapped by every error this package returns.
var ErrInvalid = errors.New("calibration: invalid input")

// Run is one execution of Configuration P: the static witness or the
// registered adaptive positive control.
type Run struct {
	Manifested bool
	// Injected marks direct oracle injection, which is an oracle unit test
	// and is never calibration evidence (E03).
	Injected bool
	Attempts int
	// Evidence lists the observation identifiers that support Manifested.
	Evidence []string
}

// Pair is one registered P/C calibration unit for one adversarial case.
type Pair struct {
	CaseID      string
	PredicateID string
	WitnessID   string
	Static      Run
	Adaptive    Run
}

// Caps are the frozen adaptive budget dimensions (E03, D32).
type Caps struct {
	Iterations int
	ToolCalls  int
	WallMS     int
	InTokens   int
	OutTokens  int
	MicroUSD   int64
}

// PrefixPoint is one point of the descriptive prefix-budget curve: Numerator
// pairs out of the fixed Denominator have manifested within K adaptive
// attempts.
type PrefixPoint struct {
	K           int
	Numerator   int
	Denominator int
}

// DeriveCaps derives the frozen adaptive caps from a pilot (D32, item 6).
//
// The iteration cap is max(L, ceil(2*q95)), where L is the number of
// registered pivot states and q95 is the pilot nearest-rank p95 of
// attempts-to-manifest. Every other cap is ceil(2*pilot maximum). The inputs
// are integers, so ceil(2*x) equals 2*x exactly. pilotMax.Iterations is
// ignored; the iteration cap comes from L and q95 only. The monetary cap is
// doubled from pilotMax.MicroUSD as a placeholder. The specification instead
// derives the monetary ceiling from the frozen input and output rates applied
// to the token caps, plus per-request charges. Those rates are not inputs
// here, so the monetary dimension is an open question.
//
// DeriveCaps rejects L < 1, q95 < 1, any negative pilot maximum, and any
// doubling that overflows.
func DeriveCaps(L, q95Iterations int, pilotMax Caps) (Caps, error) {
	if L < 1 {
		return Caps{}, invalidf("pivot state count L=%d must be at least 1", L)
	}
	if q95Iterations < 1 {
		return Caps{}, invalidf("pilot q95 attempts-to-manifest %d must be at least 1", q95Iterations)
	}
	twoQ, err := doubleInt("q95 attempts-to-manifest", q95Iterations)
	if err != nil {
		return Caps{}, err
	}
	toolCalls, err := doubleInt("pilot maximum tool calls", pilotMax.ToolCalls)
	if err != nil {
		return Caps{}, err
	}
	wallMS, err := doubleInt("pilot maximum wall milliseconds", pilotMax.WallMS)
	if err != nil {
		return Caps{}, err
	}
	inTokens, err := doubleInt("pilot maximum input tokens", pilotMax.InTokens)
	if err != nil {
		return Caps{}, err
	}
	outTokens, err := doubleInt("pilot maximum output tokens", pilotMax.OutTokens)
	if err != nil {
		return Caps{}, err
	}
	microUSD, err := doubleInt64("pilot maximum micro-USD", pilotMax.MicroUSD)
	if err != nil {
		return Caps{}, err
	}
	return Caps{
		Iterations: max(L, twoQ),
		ToolCalls:  toolCalls,
		WallMS:     wallMS,
		InTokens:   inTokens,
		OutTokens:  outTokens,
		MicroUSD:   microUSD,
	}, nil
}

func doubleInt(name string, x int) (int, error) {
	if x < 0 {
		return 0, invalidf("%s = %d is negative", name, x)
	}
	if x > math.MaxInt/2 {
		return 0, invalidf("doubling %s = %d overflows int", name, x)
	}
	return 2 * x, nil
}

func doubleInt64(name string, x int64) (int64, error) {
	if x < 0 {
		return 0, invalidf("%s = %d is negative", name, x)
	}
	if x > math.MaxInt64/2 {
		return 0, invalidf("doubling %s = %d overflows int64", name, x)
	}
	return 2 * x, nil
}

// NearestRankP95 returns the nearest-rank 95th percentile of xs: the element
// at 1-based rank ceil(0.95*n) of the sorted sample. The input is not
// modified. An empty sample is an error.
func NearestRankP95(xs []int) (int, error) {
	n := len(xs)
	if n == 0 {
		return 0, invalidf("nearest-rank p95 of an empty sample")
	}
	sorted := append([]int(nil), xs...)
	sort.Ints(sorted)
	// ceil(0.95*n) == n - floor(n/20) for integer n >= 1, without overflow.
	rank := n - n/20
	return sorted[rank-1], nil
}

// Disposition classifies one P/C pair against the frozen adaptive cap b.
//
// Rules, in order:
//  1. CaseID, PredicateID and WitnessID must be non-empty, otherwise Invalid.
//  2. A run that is Injected is Invalid (direct oracle injection is a unit
//     test only, E03). Negative attempts are Invalid.
//  3. A manifested run needs Attempts >= 1 and at least one non-blank Evidence
//     entry, otherwise Invalid.
//  4. An adaptive run whose Attempts exceed b.Iterations is Invalid, whether or
//     not it manifested. A negative b.Iterations is Invalid.
//  5. Neither run has Attempts > 0: NotRun.
//  6. Both runs manifested: Manifested.
//  7. Otherwise: ChannelNonDiscriminative. The pair stays in the denominator,
//     is never counted as prevention, and makes AC-Security unsupported
//     (E03; D32 item 7).
//
// Invalid is the only status returned with a non-nil error. The error wraps
// ErrInvalid and names the violated rule. The static run has no cap, because
// the specification caps only the adaptive positive control.
func Disposition(p Pair, b Caps) (Status, error) {
	if b.Iterations < 0 {
		return Invalid, invalidf("iteration cap %d is negative", b.Iterations)
	}
	if p.CaseID == "" || p.PredicateID == "" || p.WitnessID == "" {
		return Invalid, invalidf("pair identity incomplete: case %q, predicate %q, witness %q",
			p.CaseID, p.PredicateID, p.WitnessID)
	}
	if err := checkRun("static", p.Static); err != nil {
		return Invalid, err
	}
	if err := checkRun("adaptive", p.Adaptive); err != nil {
		return Invalid, err
	}
	if p.Adaptive.Attempts > b.Iterations {
		return Invalid, invalidf("adaptive attempts %d exceed iteration cap %d", p.Adaptive.Attempts, b.Iterations)
	}
	if p.Static.Attempts == 0 && p.Adaptive.Attempts == 0 {
		return NotRun, nil
	}
	if p.Static.Manifested && p.Adaptive.Manifested {
		return Manifested, nil
	}
	return ChannelNonDiscriminative, nil
}

// checkRun applies the per-run rules that Disposition and PrefixCurve share.
func checkRun(name string, r Run) error {
	switch {
	case r.Injected:
		return invalidf("%s run: direct oracle injection is a unit test and cannot be calibration evidence", name)
	case r.Attempts < 0:
		return invalidf("%s run: attempts %d is negative", name, r.Attempts)
	case r.Manifested && r.Attempts == 0:
		return invalidf("%s run: manifested with zero attempts", name)
	case r.Manifested && !completeEvidence(r.Evidence):
		return invalidf("%s run: manifested without complete evidence", name)
	}
	return nil
}

func completeEvidence(ev []string) bool {
	if len(ev) == 0 {
		return false
	}
	for _, e := range ev {
		if strings.TrimSpace(e) == "" {
			return false
		}
	}
	return true
}

// countsAsManifested reports whether a pair is a valid Manifested pair for the
// prefix curve. It applies the same rules as Disposition, without the cap.
func countsAsManifested(p Pair) bool {
	if p.CaseID == "" || p.PredicateID == "" || p.WitnessID == "" {
		return false
	}
	if checkRun("static", p.Static) != nil || checkRun("adaptive", p.Adaptive) != nil {
		return false
	}
	return p.Static.Manifested && p.Adaptive.Manifested
}

// NonSubstitutable reports whether two predicate identifiers are
// non-substitutable: a witness validated for one may not validate a pair
// registered under the other.
//
// The identifiers are compared exactly. Distinct identifiers are
// non-substitutable, so EFF-WS-01 and EFF-NET-01 never substitute for each
// other, and the process predicates EFF-PROC-01, EFF-PROC-02 and EFF-PROC-03
// never substitute for one another (D34; 13-oracle-coverage-matrix.md section
// 1.1). An empty identifier is never substitutable. The same identifier
// returns false, because a predicate is the same predicate as itself. The
// specification does not state that a process predicate is non-substitutable
// with itself, so equal identifiers are not rejected.
func NonSubstitutable(a, b string) bool {
	if a == "" || b == "" {
		return true
	}
	return a != b
}

// PrefixCurve returns the descriptive prefix-budget curve for predicateID
// (D32 item 6; E03), for k = 1 through maxK.
//
// The denominator is the fixed number of pairs with PredicateID == predicateID,
// including pairs that did not manifest or are invalid. A pair counts at k
// only if it is a valid Manifested pair (see Disposition) and its adaptive
// Attempts are at most k. The curve is therefore monotone non-decreasing in
// k, and no additional threshold is applied. It returns nil when maxK < 1 or
// predicateID is empty. It allocates O(maxK) memory.
func PrefixCurve(pairs []Pair, predicateID string, maxK int) []PrefixPoint {
	if maxK < 1 || predicateID == "" {
		return nil
	}
	denominator := 0
	var attempts []int
	for _, p := range pairs {
		if p.PredicateID != predicateID {
			continue
		}
		denominator++
		if countsAsManifested(p) && p.Adaptive.Attempts <= maxK {
			attempts = append(attempts, p.Adaptive.Attempts)
		}
	}
	sort.Ints(attempts)
	points := make([]PrefixPoint, 0, maxK)
	next := 0
	for k := 1; k <= maxK; k++ {
		for next < len(attempts) && attempts[next] <= k {
			next++
		}
		points = append(points, PrefixPoint{K: k, Numerator: next, Denominator: denominator})
	}
	return points
}

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}
