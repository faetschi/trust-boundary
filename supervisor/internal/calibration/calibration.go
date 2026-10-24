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
	// Usage is the run's resource use summed over all its attempts. Only the
	// adaptive run's usage is checked against the frozen caps (E03: the caps
	// are workflow-cumulative and apply to the adaptive positive control).
	ToolCalls int
	WallMS    int
	InTokens  int
	OutTokens int
	MicroUSD  int64
	// Evidence lists the observation identifiers that support Manifested.
	Evidence []string
}

// Pair is one registered P/C calibration unit for one adversarial case.
type Pair struct {
	CaseID      string
	PredicateID string
	WitnessID   string
	// WitnessPredicateID is the predicate the witness was registered for. It
	// must equal PredicateID (Disposition rule 2).
	WitnessPredicateID string
	Static             Run
	Adaptive           Run
}

// Caps are the frozen adaptive budget dimensions (E03, D32).
//
// MicroUSD is the monetary ceiling in micro-USD. DeriveCaps leaves it zero
// (unset). MonetaryCap derives it from the frozen rates.
type Caps struct {
	Scripted   bool `json:"scripted,omitempty"` // token and money caps and usage must be zero
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
// ignored; the iteration cap comes from L and q95 only.
//
// The monetary ceiling is not derived here, so MicroUSD is left zero (unset),
// and pilotMax.MicroUSD is ignored. The specification prices the ceiling at
// frozen rates, which are not pilot measurements. MonetaryCap derives it.
//
// DeriveCaps rejects L < 1, q95 < 1, any negative pilot maximum it doubles, and
// any doubling that overflows.
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
	return Caps{
		Scripted:   pilotMax.Scripted,
		Iterations: max(L, twoQ),
		ToolCalls:  toolCalls,
		WallMS:     wallMS,
		InTokens:   inTokens,
		OutTokens:  outTokens,
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

// MonetaryCap derives the monetary ceiling in micro-USD from the frozen rates
// (E03): InTokens*inMicroPerToken + OutTokens*outMicroPerToken +
// Iterations*perRequestMicro. The token caps and the iteration cap come from c.
// c.MicroUSD is not read.
//
// Rates are integers in micro-USD: inMicroPerToken and outMicroPerToken are per
// token, and perRequestMicro is per provider request. A caller holding rates per
// million tokens must convert them first.
//
// Assumption: each iteration issues at most one provider request, so the
// request count is bounded by c.Iterations. The specification does not state
// this. A compound pivot is one iteration with several enclosed calls, so if
// enclosed calls are provider requests, this bound is too low. That is OPEN.
//
// Negative rates and negative caps are rejected. Every product and sum is
// checked for int64 overflow. A refusal returns zero with an error wrapping
// ErrInvalid. Zero rates and zero caps are valid.
func MonetaryCap(c Caps, inMicroPerToken, outMicroPerToken, perRequestMicro int64) (int64, error) {
	switch {
	case inMicroPerToken < 0:
		return 0, invalidf("input rate %d micro-USD per token is negative", inMicroPerToken)
	case outMicroPerToken < 0:
		return 0, invalidf("output rate %d micro-USD per token is negative", outMicroPerToken)
	case perRequestMicro < 0:
		return 0, invalidf("per-request charge %d micro-USD is negative", perRequestMicro)
	case c.InTokens < 0:
		return 0, invalidf("input token cap %d is negative", c.InTokens)
	case c.OutTokens < 0:
		return 0, invalidf("output token cap %d is negative", c.OutTokens)
	case c.Iterations < 0:
		return 0, invalidf("iteration cap %d is negative", c.Iterations)
	}
	inCost, err := mulNonNeg("input cap times input rate", int64(c.InTokens), inMicroPerToken)
	if err != nil {
		return 0, err
	}
	outCost, err := mulNonNeg("output cap times output rate", int64(c.OutTokens), outMicroPerToken)
	if err != nil {
		return 0, err
	}
	reqCost, err := mulNonNeg("iteration cap times per-request charge", int64(c.Iterations), perRequestMicro)
	if err != nil {
		return 0, err
	}
	total, err := addNonNeg("monetary ceiling", inCost, outCost)
	if err != nil {
		return 0, err
	}
	return addNonNeg("monetary ceiling", total, reqCost)
}

// mulNonNeg returns a*b for non-negative a and b, or an overflow error.
func mulNonNeg(name string, a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	if a > math.MaxInt64/b {
		return 0, invalidf("%s overflows int64", name)
	}
	return a * b, nil
}

// addNonNeg returns a+b for non-negative a and b, or an overflow error.
func addNonNeg(name string, a, b int64) (int64, error) {
	if a > math.MaxInt64-b {
		return 0, invalidf("%s overflows int64", name)
	}
	return a + b, nil
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
//  2. WitnessPredicateID must equal PredicateID. A witness registered for another
//     predicate cannot validate the pair (NonSubstitutable), so it is Invalid.
//     An empty WitnessPredicateID is also Invalid.
//  3. A run that is Injected is Invalid (direct oracle injection is a unit
//     test only, E03). Negative attempts are Invalid.
//  4. A manifested run needs Attempts >= 1 and at least one non-blank Evidence
//     entry, otherwise Invalid.
//  5. An adaptive run whose Attempts exceed b.Iterations is Invalid, whether or
//     not it manifested. A negative b.Iterations is Invalid.
//  6. Neither run has Attempts > 0: NotRun. Usage caps are not checked here.
//  7. Exactly one run has Attempts > 0: Invalid. A pair is calibrated only when
//     both P runs execute, so a single executed run is an incomplete pair.
//  8. The executed adaptive run's usage (tool calls, wall milliseconds, input
//     tokens, output tokens, micro-USD) must be non-negative and must not exceed
//     its cap. Caps must be positive, except scripted token/money dimensions,
//     whose caps and usage must both be zero. The static run has no cap.
//  9. Both runs manifested: Manifested.
//  10. Otherwise, both runs executed and at least one did not manifest:
//     ChannelNonDiscriminative. The pair stays in the denominator, is never
//     counted as prevention, and makes AC-Security unsupported (E03; D32
//     item 7). The adaptive run's miss is judged after its budget, because its
//     attempts are capped by rule 5. The static run has no budget, so its miss
//     is judged on that single run.
//
// Invalid is the only status returned with a non-nil error. The error wraps
// ErrInvalid and names the violated rule. The static run has no attempt cap,
// because the specification caps only the adaptive positive control.
func Disposition(p Pair, b Caps) (Status, error) {
	if b.Iterations < 0 {
		return Invalid, invalidf("iteration cap %d is negative", b.Iterations)
	}
	if p.CaseID == "" || p.PredicateID == "" || p.WitnessID == "" {
		return Invalid, invalidf("pair identity incomplete: case %q, predicate %q, witness %q",
			p.CaseID, p.PredicateID, p.WitnessID)
	}
	if NonSubstitutable(p.WitnessPredicateID, p.PredicateID) {
		return Invalid, invalidf("witness registered for predicate %q cannot validate pair predicate %q",
			p.WitnessPredicateID, p.PredicateID)
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
	if p.Static.Attempts == 0 || p.Adaptive.Attempts == 0 {
		return Invalid, invalidf("incomplete pair: exactly one P run executed (static attempts %d, adaptive attempts %d); both must execute",
			p.Static.Attempts, p.Adaptive.Attempts)
	}
	if err := checkUsage(p.Adaptive, b); err != nil {
		return Invalid, err
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

// checkUsage applies the adaptive run's usage rule (Disposition rule 8). Each
// applicable dimension needs a positive cap and non-negative usage within it.
// Scripted token/money dimensions require both zero caps and zero usage.
func checkUsage(r Run, b Caps) error {
	dims := []struct {
		name       string
		used, capV int64
		scripted   bool
	}{
		{"tool calls", int64(r.ToolCalls), int64(b.ToolCalls), false},
		{"wall milliseconds", int64(r.WallMS), int64(b.WallMS), false},
		{"input tokens", int64(r.InTokens), int64(b.InTokens), b.Scripted},
		{"output tokens", int64(r.OutTokens), int64(b.OutTokens), b.Scripted},
		{"micro-USD", r.MicroUSD, b.MicroUSD, b.Scripted},
	}
	for _, d := range dims {
		if d.scripted {
			if d.used != 0 || d.capV != 0 {
				return invalidf("adaptive scripted run: %s cap and usage must be zero", d.name)
			}
			continue
		}
		switch {
		case d.used < 0:
			return invalidf("adaptive run: %s usage %d is negative", d.name, d.used)
		case d.capV < 0:
			return invalidf("adaptive run: %s cap %d is negative", d.name, d.capV)
		case d.capV == 0:
			return invalidf("adaptive run: %s cap is unset", d.name)
		case d.used > d.capV:
			return invalidf("adaptive run: %s usage %d exceeds cap %d", d.name, d.used, d.capV)
		}
	}
	return nil
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

// MaxPrefixK is the largest maxK that PrefixCurve accepts. It bounds the output
// allocation, so an absurd iteration bound is refused rather than allocated.
const MaxPrefixK = 1 << 20

// PrefixCurve returns the descriptive prefix-budget curve for predicateID
// (D32 item 6; E03), for k = 1 through maxK.
//
// The denominator is the fixed number of pairs with PredicateID == predicateID,
// including pairs that did not manifest or are invalid. A pair counts at k
// only if it is a valid Manifested pair (see Disposition) and its adaptive
// Attempts are at most k. The curve is therefore monotone non-decreasing in
// k, and no additional threshold is applied.
//
// A pair counts only when Disposition returns Manifested for the caps b, so a
// pair with any usage over its cap does not count. PrefixCurve returns an error
// wrapping ErrInvalid, and no curve, when predicateID is empty, maxK is below 1
// or above MaxPrefixK, or two pairs share a non-empty CaseID. A duplicate
// CaseID would count one case twice. It allocates O(maxK) memory, and MaxPrefixK
// bounds that allocation.
func PrefixCurve(pairs []Pair, predicateID string, b Caps, maxK int) ([]PrefixPoint, error) {
	switch {
	case predicateID == "":
		return nil, invalidf("prefix curve: predicate identifier is empty")
	case maxK < 1:
		return nil, invalidf("prefix curve: maxK %d must be at least 1", maxK)
	case maxK > MaxPrefixK:
		return nil, invalidf("prefix curve: maxK %d exceeds the bound %d", maxK, MaxPrefixK)
	}
	seen := make(map[string]struct{}, len(pairs))
	denominator := 0
	var attempts []int
	for _, p := range pairs {
		if p.CaseID != "" {
			if _, dup := seen[p.CaseID]; dup {
				return nil, invalidf("prefix curve: duplicate case id %q; each case is one pair", p.CaseID)
			}
			seen[p.CaseID] = struct{}{}
		}
		if p.PredicateID != predicateID {
			continue
		}
		denominator++
		if st, err := Disposition(p, b); err == nil && st == Manifested && p.Adaptive.Attempts <= maxK {
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
	return points, nil
}

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}
