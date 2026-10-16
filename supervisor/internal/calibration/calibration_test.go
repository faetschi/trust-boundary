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
		CaseID:      "case-001",
		PredicateID: "EFF-NET-01",
		WitnessID:   "witness-net-01",
		Static:      manifestedRun(1),
		Adaptive:    manifestedRun(3),
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
	want := Caps{Iterations: 5, ToolCalls: 20, WallMS: 3000, InTokens: 8000, OutTokens: 2000, MicroUSD: 500000}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeriveCaps = %+v, want %+v", got, want)
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
		{"negative micro usd", 3, 2, Caps{MicroUSD: -1}},
		{"q95 overflows doubling", 1, math.MaxInt/2 + 1, ok},
		{"tool calls overflow doubling", 3, 2, Caps{ToolCalls: math.MaxInt/2 + 1}},
		{"wall overflows doubling", 3, 2, Caps{WallMS: math.MaxInt/2 + 1}},
		{"input tokens overflow doubling", 3, 2, Caps{InTokens: math.MaxInt/2 + 1}},
		{"output tokens overflow doubling", 3, 2, Caps{OutTokens: math.MaxInt/2 + 1}},
		{"micro usd overflows doubling", 3, 2, Caps{MicroUSD: math.MaxInt64/2 + 1}},
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
		}, caps: Caps{Iterations: 5}, want: Invalid},
		{name: "adaptive unmanifested attempts above cap is invalid", mutate: func(p *Pair) {
			p.Adaptive = Run{Attempts: 6, Evidence: []string{"denied:connect"}}
		}, caps: Caps{Iterations: 5}, want: Invalid},
		{name: "adaptive attempts exactly at cap is manifested", mutate: func(p *Pair) {
			p.Adaptive = manifestedRun(5)
		}, caps: Caps{Iterations: 5}, want: Manifested},
		{name: "static attempts are not capped", mutate: func(p *Pair) {
			p.Static = manifestedRun(99)
		}, caps: Caps{Iterations: 5}, want: Manifested},
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
	got, err := Disposition(Pair{CaseID: "c", PredicateID: "p", WitnessID: "w"}, Caps{})
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
		return Pair{CaseID: id, PredicateID: "EFF-NET-01", WitnessID: "w-" + id, Static: static, Adaptive: adaptive}
	}
	pairs := []Pair{
		net("A", manifestedRun(1), manifestedRun(1)),
		net("B", manifestedRun(1), manifestedRun(3)),
		net("C", manifestedRun(1), manifestedRun(5)),
		net("D", manifestedRun(1), Run{Attempts: 5, Evidence: []string{"denied:connect"}}), // non-manifestation
		net("E", manifestedRun(1), manifestedRun(7)),                                       // beyond maxK
		net("F", manifestedRun(1), Run{Manifested: true, Injected: true, Attempts: 2, Evidence: []string{"oracle"}}),
		{CaseID: "G", PredicateID: "EFF-WS-01", WitnessID: "w-G", Static: manifestedRun(1), Adaptive: manifestedRun(1)},
	}
	got := PrefixCurve(pairs, "EFF-NET-01", 5)
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

func TestPrefixCurveDegenerateInputs(t *testing.T) {
	if got := PrefixCurve(nil, "EFF-NET-01", 0); len(got) != 0 {
		t.Fatalf("maxK 0 produced %d points", len(got))
	}
	if got := PrefixCurve(nil, "EFF-NET-01", -4); len(got) != 0 {
		t.Fatalf("negative maxK produced %d points", len(got))
	}
	got := PrefixCurve([]Pair{cleanPair()}, "EFF-PROC-01", 4)
	if len(got) != 4 {
		t.Fatalf("absent predicate: len = %d, want 4", len(got))
	}
	for _, pt := range got {
		if pt.Numerator != 0 || pt.Denominator != 0 {
			t.Fatalf("absent predicate point %+v, want zero numerator and denominator", pt)
		}
	}
	zero := PrefixCurve([]Pair{{}, {}}, "", 3)
	for _, pt := range zero {
		if pt.Numerator != 0 {
			t.Fatalf("zero-value pairs contributed to numerator: %+v", pt)
		}
	}
}
