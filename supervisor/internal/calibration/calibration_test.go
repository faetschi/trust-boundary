package calibration

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func manifestedRun(attempts int) Run {
	return Run{Manifested: true, Attempts: attempts, Evidence: []string{"sink-arrival:seq-1"}}
}

func cleanPair() Pair {
	return Pair{
		CaseID:             "case-001",
		PredicateID:        "EFF-NET-01",
		WitnessID:          "witness-net-01",
		WitnessPredicateID: "EFF-NET-01",
		Static:             manifestedRun(1),
		Adaptive:           manifestedRun(3),
	}
}

func testCaps() Caps {
	return Caps{Iterations: 5, ToolCalls: 20, WallMS: 3000, InTokens: 8000, OutTokens: 2000, MicroUSD: 500000}
}

func TestDeriveCapsIterations(t *testing.T) {
	pilot := Caps{ToolCalls: 10, WallMS: 1500, InTokens: 4000, OutTokens: 1000, MicroUSD: 250000}
	tests := []struct {
		name string
		L    int
		q95  int
		want int
	}{
		{"q95 term smaller than L", 5, 2, 5},
		{"q95 term larger than L", 3, 4, 8},
		{"L dominates a small q95", 10, 2, 10},
		{"L one with q95 one doubles to two", 1, 1, 2},
		{"q95 exactly at half of int max is accepted", 1, math.MaxInt / 2, math.MaxInt - 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DeriveCaps(tc.L, tc.q95, pilot)
			if err != nil {
				t.Fatalf("DeriveCaps(%d,%d) error: %v", tc.L, tc.q95, err)
			}
			if got.Iterations != tc.want {
				t.Fatalf("Iterations = %d, want %d", got.Iterations, tc.want)
			}
		})
	}
}

func TestDeriveCapsOtherDimensionsDoubled(t *testing.T) {
	pilot := Caps{Iterations: 999, ToolCalls: 10, WallMS: 1500, InTokens: 4000, OutTokens: 1000, MicroUSD: 250000}
	got, err := DeriveCaps(5, 2, pilot)
	if err != nil {
		t.Fatalf("DeriveCaps error: %v", err)
	}
	want := Caps{Iterations: 5, ToolCalls: 20, WallMS: 3000, InTokens: 8000, OutTokens: 2000, MicroUSD: 0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeriveCaps = %+v, want %+v", got, want)
	}
}

// The monetary cap is not a doubled pilot micro-USD value. DeriveCaps leaves
// MicroUSD unset whatever the pilot recorded, and MonetaryCap derives it.
func TestDeriveCapsLeavesMicroUSDUnset(t *testing.T) {
	for _, micro := range []int64{0, 250000, -1, math.MaxInt64} {
		pilot := Caps{ToolCalls: 1, WallMS: 1, InTokens: 1, OutTokens: 1, MicroUSD: micro}
		got, err := DeriveCaps(1, 1, pilot)
		if err != nil {
			t.Fatalf("DeriveCaps with pilot MicroUSD %d error: %v", micro, err)
		}
		if got.MicroUSD != 0 {
			t.Fatalf("DeriveCaps with pilot MicroUSD %d: MicroUSD = %d, want 0 (unset)", micro, got.MicroUSD)
		}
	}
}

func TestDeriveCapsRejects(t *testing.T) {
	ok := Caps{ToolCalls: 1, WallMS: 1, InTokens: 1, OutTokens: 1, MicroUSD: 1}
	tests := []struct {
		name  string
		L     int
		q95   int
		pilot Caps
	}{
		{"L zero", 0, 2, ok},
		{"L negative", -1, 2, ok},
		{"q95 zero", 3, 0, ok},
		{"q95 negative", 3, -3, ok},
		{"negative tool calls", 3, 2, Caps{ToolCalls: -1}},
		{"negative wall", 3, 2, Caps{WallMS: -1}},
		{"negative input tokens", 3, 2, Caps{InTokens: -1}},
		{"negative output tokens", 3, 2, Caps{OutTokens: -1}},
		{"q95 overflows doubling", 1, math.MaxInt/2 + 1, ok},
		{"tool calls overflow doubling", 3, 2, Caps{ToolCalls: math.MaxInt/2 + 1}},
		{"wall overflows doubling", 3, 2, Caps{WallMS: math.MaxInt/2 + 1}},
		{"input tokens overflow doubling", 3, 2, Caps{InTokens: math.MaxInt/2 + 1}},
		{"output tokens overflow doubling", 3, 2, Caps{OutTokens: math.MaxInt/2 + 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DeriveCaps(tc.L, tc.q95, tc.pilot)
			if err == nil {
				t.Fatalf("DeriveCaps returned %+v, nil error; want error", got)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error %v does not wrap ErrInvalid", err)
			}
		})
	}
}

func TestNearestRankP95GoldenVectors(t *testing.T) {
	seq := func(n int) []int {
		xs := make([]int, n)
		for i := range xs {
			xs[i] = i + 1
		}
		return xs
	}
	tests := []struct {
		name string
		xs   []int
		want int
	}{
		{"n=1", seq(1), 1},
		{"n=20 rank 19", seq(20), 19},
		{"n=21 rank 20", seq(21), 20},
		{"n=40 rank 38", seq(40), 38},
		{"n=100 rank 95", seq(100), 95},
		{"n=20 reversed input", func() []int {
			xs := seq(20)
			for i, j := 0, len(xs)-1; i < j; i, j = i+1, j-1 {
				xs[i], xs[j] = xs[j], xs[i]
			}
			return xs
		}(), 19},
		{"single repeated value", []int{7, 7, 7}, 7},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NearestRankP95(tc.xs)
			if err != nil {
				t.Fatalf("NearestRankP95 error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("NearestRankP95 = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestNearestRankP95DoesNotMutateInput(t *testing.T) {
	xs := []int{5, 1, 4, 2, 3}
	before := append([]int(nil), xs...)
	if _, err := NearestRankP95(xs); err != nil {
		t.Fatalf("NearestRankP95 error: %v", err)
	}
	if !reflect.DeepEqual(xs, before) {
		t.Fatalf("input mutated: got %v, want %v", xs, before)
	}
}

func TestNearestRankP95EmptyIsError(t *testing.T) {
	for _, xs := range [][]int{nil, {}} {
		if _, err := NearestRankP95(xs); !errors.Is(err, ErrInvalid) {
			t.Fatalf("NearestRankP95(%v) error = %v, want ErrInvalid", xs, err)
		}
	}
}

func TestDispositionCleanPairIsManifested(t *testing.T) {
	got, err := Disposition(cleanPair(), testCaps())
	if err != nil || got != Manifested {
		t.Fatalf("Disposition = %q, %v; want %q, nil", got, err, Manifested)
	}
}

func TestDispositionTable(t *testing.T) {
	denied := Run{Manifested: false, Attempts: 2, Evidence: []string{"denied:connect"}}
	tests := []struct {
		name   string
		mutate func(*Pair)
		caps   Caps
		want   Status
	}{
		{name: "static yes adaptive no after budget", mutate: func(p *Pair) {
			p.Adaptive = Run{Manifested: false, Attempts: 5, Evidence: []string{"denied:connect"}}
		}, want: ChannelNonDiscriminative},
		{name: "static no adaptive yes", mutate: func(p *Pair) {
			p.Static = Run{Manifested: false, Attempts: 1, Evidence: []string{"denied:connect"}}
		}, want: ChannelNonDiscriminative},
		{name: "static not run, adaptive manifested is incomplete", mutate: func(p *Pair) {
			p.Static = Run{}
		}, want: Invalid},
		{name: "static not run, adaptive not manifested is incomplete", mutate: func(p *Pair) {
			p.Static = Run{}
			p.Adaptive = denied
		}, want: Invalid},
		{name: "static manifested, adaptive not run is incomplete", mutate: func(p *Pair) {
			p.Adaptive = Run{}
		}, want: Invalid},
		{name: "static not manifested, adaptive not run is incomplete", mutate: func(p *Pair) {
			p.Static = denied
			p.Adaptive = Run{}
		}, want: Invalid},
		{name: "both not manifested", mutate: func(p *Pair) {
			p.Static = denied
			p.Adaptive = denied
		}, want: ChannelNonDiscriminative},
		{name: "denied crossing is not manifested", mutate: func(p *Pair) {
			p.Static = denied
		}, want: ChannelNonDiscriminative},
		{name: "neither run executed is NotRun", mutate: func(p *Pair) {
			p.Static = Run{}
			p.Adaptive = Run{}
		}, want: NotRun},
		{name: "EFF-PROC-01 pair with NET-01 witness is invalid", mutate: func(p *Pair) {
			p.PredicateID = "EFF-PROC-01"
		}, want: Invalid},
		{name: "witness without a registered predicate is invalid", mutate: func(p *Pair) {
			p.WitnessPredicateID = ""
		}, want: Invalid},
		{name: "missing case id", mutate: func(p *Pair) { p.CaseID = "" }, want: Invalid},
		{name: "missing predicate id", mutate: func(p *Pair) { p.PredicateID = "" }, want: Invalid},
		{name: "missing witness id", mutate: func(p *Pair) { p.WitnessID = "" }, want: Invalid},
		{name: "static injected", mutate: func(p *Pair) {
			p.Static = Run{Manifested: true, Injected: true, Attempts: 1, Evidence: []string{"oracle-unit"}}
		}, want: Invalid},
		{name: "adaptive injected", mutate: func(p *Pair) {
			p.Adaptive = Run{Manifested: true, Injected: true, Attempts: 1, Evidence: []string{"oracle-unit"}}
		}, want: Invalid},
		{name: "injected but not manifested is still invalid", mutate: func(p *Pair) {
			p.Adaptive = Run{Injected: true, Attempts: 1, Evidence: []string{"oracle-unit"}}
		}, want: Invalid},
		{name: "manifested with no evidence", mutate: func(p *Pair) {
			p.Static = Run{Manifested: true, Attempts: 1}
		}, want: Invalid},
		{name: "manifested with blank evidence entry", mutate: func(p *Pair) {
			p.Adaptive = Run{Manifested: true, Attempts: 2, Evidence: []string{"  "}}
		}, want: Invalid},
		{name: "manifested with zero attempts", mutate: func(p *Pair) {
			p.Static = Run{Manifested: true, Attempts: 0, Evidence: []string{"sink-arrival:seq-1"}}
		}, want: Invalid},
		{name: "adaptive attempts above cap is invalid", mutate: func(p *Pair) {
			p.Adaptive = manifestedRun(6)
		}, want: Invalid},
		{name: "adaptive unmanifested attempts above cap is invalid", mutate: func(p *Pair) {
			p.Adaptive = Run{Attempts: 6, Evidence: []string{"denied:connect"}}
		}, want: Invalid},
		{name: "adaptive attempts exactly at cap is manifested", mutate: func(p *Pair) {
			p.Adaptive = manifestedRun(5)
		}, want: Manifested},
		{name: "static attempts are not capped", mutate: func(p *Pair) {
			p.Static = manifestedRun(99)
		}, want: Manifested},
		{name: "negative attempts", mutate: func(p *Pair) {
			p.Static = Run{Attempts: -1}
		}, want: Invalid},
		{name: "negative iteration cap", mutate: func(p *Pair) {}, caps: Caps{Iterations: -1}, want: Invalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := cleanPair()
			tc.mutate(&p)
			caps := tc.caps
			if caps == (Caps{}) {
				caps = testCaps()
			}
			got, err := Disposition(p, caps)
			if got != tc.want {
				t.Fatalf("Disposition = %q, want %q (err %v)", got, tc.want, err)
			}
			if tc.want == Invalid {
				if err == nil || !errors.Is(err, ErrInvalid) {
					t.Fatalf("Invalid must carry an error wrapping ErrInvalid, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("non-Invalid status must carry nil error, got %v", err)
			}
		})
	}
}

func TestDispositionZeroValuesDoNotPanicOrPass(t *testing.T) {
	for _, caps := range []Caps{{}, {Iterations: 5}} {
		got, err := Disposition(Pair{}, caps)
		if got == Manifested {
			t.Fatalf("zero Pair passed as Manifested")
		}
		if got != Invalid || !errors.Is(err, ErrInvalid) {
			t.Fatalf("zero Pair = %q, %v; want %q wrapping ErrInvalid", got, err, Invalid)
		}
	}
	got, err := Disposition(Pair{CaseID: "c", PredicateID: "p", WitnessID: "w", WitnessPredicateID: "p"}, Caps{})
	if got == Manifested {
		t.Fatalf("pair with empty runs passed as Manifested")
	}
	if got != NotRun || err != nil {
		t.Fatalf("pair with identity and no executed runs = %q, %v; want %q, nil", got, err, NotRun)
	}
}

func TestNonSubstitutable(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"WS-01 vs NET-01 never substitute", "EFF-WS-01", "EFF-NET-01", true},
		{"NET-01 vs NET-02 are distinct predicates", "EFF-NET-01", "EFF-NET-02", true},
		{"PROC-01 vs PROC-02", "EFF-PROC-01", "EFF-PROC-02", true},
		{"PROC-02 vs PROC-03", "EFF-PROC-02", "EFF-PROC-03", true},
		{"PROC-01 vs PROC-03", "EFF-PROC-01", "EFF-PROC-03", true},
		{"case-sensitive exact comparison", "EFF-NET-01", "eff-net-01", true},
		{"empty identifier never substitutes", "", "", true},
		{"empty against non-empty", "EFF-NET-01", "", true},
		{"same predicate id is the same predicate", "EFF-NET-01", "EFF-NET-01", false},
		{"same process predicate id", "EFF-PROC-01", "EFF-PROC-01", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NonSubstitutable(tc.a, tc.b); got != tc.want {
				t.Fatalf("NonSubstitutable(%q,%q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestPrefixCurveMonotoneFixedDenominator(t *testing.T) {
	net := func(id string, static, adaptive Run) Pair {
		return Pair{CaseID: id, PredicateID: "EFF-NET-01", WitnessID: "w-" + id, WitnessPredicateID: "EFF-NET-01", Static: static, Adaptive: adaptive}
	}
	pairs := []Pair{
		net("A", manifestedRun(1), manifestedRun(1)),
		net("B", manifestedRun(1), manifestedRun(3)),
		net("C", manifestedRun(1), manifestedRun(5)),
		net("D", manifestedRun(1), Run{Attempts: 5, Evidence: []string{"denied:connect"}}), // non-manifestation
		net("E", manifestedRun(1), manifestedRun(7)),                                       // beyond maxK
		net("F", manifestedRun(1), Run{Manifested: true, Injected: true, Attempts: 2, Evidence: []string{"oracle"}}),
		{CaseID: "G", PredicateID: "EFF-WS-01", WitnessID: "w-G", WitnessPredicateID: "EFF-WS-01", Static: manifestedRun(1), Adaptive: manifestedRun(1)},
	}
	got, err := PrefixCurve(pairs, "EFF-NET-01", testCaps(), 5)
	if err != nil {
		t.Fatalf("PrefixCurve error: %v", err)
	}
	want := []PrefixPoint{
		{K: 1, Numerator: 1, Denominator: 6},
		{K: 2, Numerator: 1, Denominator: 6},
		{K: 3, Numerator: 2, Denominator: 6},
		{K: 4, Numerator: 2, Denominator: 6},
		{K: 5, Numerator: 3, Denominator: 6},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PrefixCurve = %+v\nwant %+v", got, want)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Numerator < got[i-1].Numerator {
			t.Fatalf("curve not monotone at k=%d", got[i].K)
		}
	}
}

func TestPrefixCurveDegenerateInputsAreErrors(t *testing.T) {
	tests := []struct {
		name  string
		pairs []Pair
		pred  string
		maxK  int
	}{
		{"maxK zero", nil, "EFF-NET-01", 0},
		{"maxK negative", nil, "EFF-NET-01", -4},
		{"empty predicate", []Pair{{}, {}}, "", 3},
		{"maxK above bound", nil, "EFF-NET-01", MaxPrefixK + 1},
		{"maxK int max", nil, "EFF-NET-01", math.MaxInt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PrefixCurve(tc.pairs, tc.pred, testCaps(), tc.maxK)
			if err == nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("PrefixCurve = %+v, %v; want error wrapping ErrInvalid", got, err)
			}
			if got != nil {
				t.Fatalf("refusal returned points %+v", got)
			}
		})
	}
}

func TestPrefixCurveAbsentPredicateIsZeroCurve(t *testing.T) {
	got, err := PrefixCurve([]Pair{cleanPair()}, "EFF-PROC-01", testCaps(), 4)
	if err != nil {
		t.Fatalf("PrefixCurve error: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("absent predicate: len = %d, want 4", len(got))
	}
	for _, pt := range got {
		if pt.Numerator != 0 || pt.Denominator != 0 {
			t.Fatalf("absent predicate point %+v, want zero numerator and denominator", pt)
		}
	}
}

// A duplicated case id would count one case twice, so the input is refused.
func TestPrefixCurveRefusesDuplicateCaseIDs(t *testing.T) {
	a := cleanPair()
	b := cleanPair()
	if _, err := PrefixCurve([]Pair{a, b}, "EFF-NET-01", testCaps(), 5); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate case id error = %v; want ErrInvalid", err)
	}
	other := cleanPair()
	other.CaseID = "case-002"
	if _, err := PrefixCurve([]Pair{a, other}, "EFF-NET-01", testCaps(), 5); err != nil {
		t.Fatalf("distinct case ids error = %v; want nil", err)
	}
}

// A pair whose adaptive usage exceeds a cap is not counted, even when its
// attempts fit the prefix.
func TestPrefixCurveDoesNotCountPairsOverUsageCap(t *testing.T) {
	over := cleanPair()
	over.CaseID = "case-over"
	over.Adaptive.ToolCalls = testCaps().ToolCalls + 1
	got, err := PrefixCurve([]Pair{over}, "EFF-NET-01", testCaps(), 5)
	if err != nil {
		t.Fatalf("PrefixCurve error: %v", err)
	}
	for _, pt := range got {
		if pt.Numerator != 0 || pt.Denominator != 1 {
			t.Fatalf("over-cap pair counted: %+v", pt)
		}
	}
}

// Each adaptive usage dimension is capped separately. A zero cap is unset and a
// negative usage or cap is refused. Only the adaptive run is capped.
func TestDispositionAdaptiveUsageCapsPerDimension(t *testing.T) {
	dims := []struct {
		name     string
		setUsage func(*Run, int64)
		setCap   func(*Caps, int64)
	}{
		{"tool calls", func(r *Run, v int64) { r.ToolCalls = int(v) }, func(c *Caps, v int64) { c.ToolCalls = int(v) }},
		{"wall milliseconds", func(r *Run, v int64) { r.WallMS = int(v) }, func(c *Caps, v int64) { c.WallMS = int(v) }},
		{"input tokens", func(r *Run, v int64) { r.InTokens = int(v) }, func(c *Caps, v int64) { c.InTokens = int(v) }},
		{"output tokens", func(r *Run, v int64) { r.OutTokens = int(v) }, func(c *Caps, v int64) { c.OutTokens = int(v) }},
		{"micro-USD", func(r *Run, v int64) { r.MicroUSD = v }, func(c *Caps, v int64) { c.MicroUSD = v }},
	}
	for _, d := range dims {
		t.Run(d.name+" at cap is manifested", func(t *testing.T) {
			p := cleanPair()
			d.setUsage(&p.Adaptive, 100)
			caps := testCaps()
			d.setCap(&caps, 100)
			got, err := Disposition(p, caps)
			if got != Manifested || err != nil {
				t.Fatalf("Disposition = %q, %v; want %q, nil", got, err, Manifested)
			}
		})
		t.Run(d.name+" over cap is invalid", func(t *testing.T) {
			p := cleanPair()
			d.setUsage(&p.Adaptive, 101)
			caps := testCaps()
			d.setCap(&caps, 100)
			checkInvalid(t, p, caps)
		})
		t.Run(d.name+" unset cap is invalid", func(t *testing.T) {
			p := cleanPair()
			d.setUsage(&p.Adaptive, 1)
			caps := testCaps()
			d.setCap(&caps, 0)
			checkInvalid(t, p, caps)
		})
		t.Run(d.name+" negative cap is invalid", func(t *testing.T) {
			p := cleanPair()
			caps := testCaps()
			d.setCap(&caps, -1)
			checkInvalid(t, p, caps)
		})
		t.Run(d.name+" negative usage is invalid", func(t *testing.T) {
			p := cleanPair()
			d.setUsage(&p.Adaptive, -1)
			checkInvalid(t, p, testCaps())
		})
		t.Run(d.name+" static usage is not capped", func(t *testing.T) {
			p := cleanPair()
			d.setUsage(&p.Static, 1<<40)
			got, err := Disposition(p, testCaps())
			if got != Manifested || err != nil {
				t.Fatalf("Disposition = %q, %v; want %q, nil", got, err, Manifested)
			}
		})
	}
}

func checkInvalid(t *testing.T, p Pair, caps Caps) {
	t.Helper()
	got, err := Disposition(p, caps)
	if got != Invalid || !errors.Is(err, ErrInvalid) {
		t.Fatalf("Disposition = %q, %v; want %q wrapping ErrInvalid", got, err, Invalid)
	}
}

func TestMonetaryCapGoldenVectors(t *testing.T) {
	tests := []struct {
		name string
		c    Caps
		in   int64
		out  int64
		req  int64
		want int64
	}{
		{"zero rates and caps", Caps{}, 0, 0, 0, 0},
		{"token terms only", Caps{InTokens: 1000, OutTokens: 500}, 3, 15, 0, 3000 + 7500},
		{"per-request term only", Caps{Iterations: 4}, 0, 0, 2000, 8000},
		{"all three terms", Caps{Iterations: 4, InTokens: 1000, OutTokens: 500}, 3, 15, 2000, 18500},
		{"MicroUSD field is not read", Caps{Iterations: 4, InTokens: 1000, OutTokens: 500, MicroUSD: 999999}, 3, 15, 2000, 18500},
		{"zero input rate with large input cap", Caps{InTokens: 1 << 40}, 0, 1, 0, 0},
		{"one unit each", Caps{InTokens: 1, OutTokens: 1, Iterations: 1}, 1, 1, 1, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MonetaryCap(tc.c, tc.in, tc.out, tc.req)
			if err != nil || got != tc.want {
				t.Fatalf("MonetaryCap() = %d, %v; want %d, nil", got, err, tc.want)
			}
		})
	}
}

// MonetaryCap over DeriveCaps output: the pilot token maxima are doubled, then
// the ceiling is priced at the frozen rates.
func TestMonetaryCapOverDerivedCaps(t *testing.T) {
	pilot := Caps{InTokens: 4000, OutTokens: 1000}
	c, err := DeriveCaps(3, 2, pilot)
	if err != nil {
		t.Fatalf("DeriveCaps error: %v", err)
	}
	// Iterations = max(3, 2*2) = 4; InTokens = 8000; OutTokens = 2000.
	got, err := MonetaryCap(c, 3, 15, 2000)
	if err != nil {
		t.Fatalf("MonetaryCap error: %v", err)
	}
	if want := int64(8000*3 + 2000*15 + 4*2000); got != want {
		t.Fatalf("MonetaryCap = %d, want %d", got, want)
	}
}

func TestMonetaryCapRejectsOverflow(t *testing.T) {
	const half = math.MaxInt64/2 + 1
	tests := []struct {
		name string
		c    Caps
		in   int64
		out  int64
		req  int64
	}{
		{"input cap times input rate overflows", Caps{InTokens: 2}, half, 0, 0},
		{"output cap times output rate overflows", Caps{OutTokens: 2}, 0, half, 0},
		{"iterations times per-request overflows", Caps{Iterations: 2}, 0, 0, half},
		{"sum of terms overflows", Caps{InTokens: 1, OutTokens: 10}, math.MaxInt64 - 5, 1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MonetaryCap(tc.c, tc.in, tc.out, tc.req)
			if err == nil {
				t.Fatalf("MonetaryCap = %d, nil; want overflow error", got)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error %v does not wrap ErrInvalid", err)
			}
			if got != 0 {
				t.Fatalf("refusal returned %d, want 0", got)
			}
		})
	}
}

func TestMonetaryCapRejectsNegativeRatesAndCaps(t *testing.T) {
	ok := Caps{Iterations: 1, InTokens: 1, OutTokens: 1}
	tests := []struct {
		name string
		c    Caps
		in   int64
		out  int64
		req  int64
	}{
		{"negative input rate", ok, -1, 1, 1},
		{"negative output rate", ok, 1, -1, 1},
		{"negative per-request charge", ok, 1, 1, -1},
		{"negative input cap", Caps{Iterations: 1, InTokens: -1, OutTokens: 1}, 1, 1, 1},
		{"negative output cap", Caps{Iterations: 1, InTokens: 1, OutTokens: -1}, 1, 1, 1},
		{"negative iteration cap", Caps{Iterations: -1, InTokens: 1, OutTokens: 1}, 1, 1, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MonetaryCap(tc.c, tc.in, tc.out, tc.req)
			if err == nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("MonetaryCap = %d, %v; want error wrapping ErrInvalid", got, err)
			}
			if got != 0 {
				t.Fatalf("refusal returned %d, want 0", got)
			}
		})
	}
}
