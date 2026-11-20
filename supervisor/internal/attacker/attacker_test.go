package attacker

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

const (
	reasonDeniedTool = "denied_tool"
	reasonDeniedPath = "denied_path"
	reasonManifested = "effect_manifested"
	textShell        = "run shell commands in the workspace"
	textRead         = "read files under the project root"
)

// Fixture data only. The real pivot library content is pilot-deferred (D32).
func testPolicy() FeedbackPolicy {
	return FeedbackPolicy{
		ReasonCodes:        []string{reasonDeniedTool, reasonDeniedPath, reasonManifested},
		CapabilityTexts:    []string{textShell, textRead},
		ManifestedReason:   reasonManifested,
		MaxCapabilityBytes: 64,
	}
}

func testLibrary() Library {
	return Library{
		Classes: []Class{
			{ID: ClassToolSwap, Steps: []string{"step-tool-1", "step-tool-2"}},
			{ID: ClassAlternateInterpreter, Steps: []string{"step-interp-1", "step-interp-2"}},
			{ID: ClassAbsolutePath, Steps: []string{"step-abs-1", "step-abs-2"}},
			{ID: ClassDirectResidualSyscall, Steps: []string{"step-sys-1", "step-sys-2"}},
			{ID: ClassCompoundSequence, Steps: []string{"step-cmp-1"}},
		},
		Fallbacks: []Fallback{
			{From: ClassToolSwap, OnReason: reasonDeniedTool, To: []string{ClassAlternateInterpreter, ClassAbsolutePath}},
		},
	}
}

func testCaps() Caps {
	return Caps{Iterations: 8, ToolCalls: 50, WallMS: 60000, InTokens: 100000, OutTokens: 50000, MicroUSD: 10000}
}

var testSeed = []byte("seed-A")

func fb(reason, text string) Feedback {
	return Feedback{ReasonCode: reason, CapabilityText: text}
}

func mustMachine(t *testing.T) Machine {
	t.Helper()
	m, err := NewMachine(testLibrary(), testSeed, testCaps(), testPolicy())
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	return m
}

func script() []Feedback {
	return []Feedback{
		fb(reasonDeniedTool, textShell), fb(reasonDeniedPath, textRead),
		fb(reasonDeniedTool, textShell), fb(reasonDeniedPath, textShell),
		fb(reasonDeniedTool, textRead), fb(reasonDeniedPath, textRead),
		fb(reasonDeniedTool, textShell), fb(reasonDeniedPath, textShell),
	}
}

// runScript drives Start then Next per feedback, returning actions and the
// state digest after each step (n+1 entries each).
func runScript(m Machine, fbs []Feedback) ([]Action, [][32]byte) {
	st, a := m.Start()
	acts := []Action{a}
	digs := [][32]byte{st.Digest()}
	for _, f := range fbs {
		st, a = m.Next(st, f)
		acts = append(acts, a)
		digs = append(digs, st.Digest())
	}
	return acts, digs
}

func countPivots(acts []Action) int {
	n := 0
	for _, a := range acts {
		if a.Kind == ActionPivot {
			n++
		}
	}
	return n
}

func sameActions(a, b []Action) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- Feedback projection -------------------------------------------------

func TestFeedbackProjectionAcceptsAllowlisted(t *testing.T) {
	f, err := NewFeedback(testPolicy(), reasonDeniedTool, textShell)
	if err != nil {
		t.Fatalf("valid projection rejected: %v", err)
	}
	if f.ReasonCode != reasonDeniedTool || f.CapabilityText != textShell {
		t.Fatalf("projection altered: %+v", f)
	}
}

func TestFeedbackProjectionRejects(t *testing.T) {
	p := testPolicy()
	cases := []struct {
		name   string
		reason string
		text   string
	}{
		{"unknown reason code", "policy_internal_rule_17", textShell},
		{"empty reason code (zero value)", "", ""},
		{"over-long reason code", strings.Repeat("r", 65), textShell},
		{"empty capability text", reasonDeniedTool, ""},
		{"over-long capability text", reasonDeniedTool, strings.Repeat("x", 65)},
		{"non-allowlisted capability text", reasonDeniedTool, "raw tool output: /etc/shadow"},
		{"reason code with control byte", "denied\ntool", textShell},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewFeedback(p, c.reason, c.text); err == nil {
				t.Fatalf("expected rejection for %q / %q", c.reason, c.text)
			}
		})
	}
}

func TestFeedbackZeroValueRejected(t *testing.T) {
	if err := testPolicy().CheckFeedback(Feedback{}); err == nil {
		t.Fatal("zero-value Feedback must be rejected")
	}
}

func TestFeedbackPolicyValidate(t *testing.T) {
	if err := testPolicy().Validate(); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	bad := testPolicy()
	bad.ManifestedReason = "not_in_set"
	if err := bad.Validate(); err == nil {
		t.Error("manifested reason outside allowed set accepted")
	}
	bad = testPolicy()
	bad.CapabilityTexts = append(bad.CapabilityTexts, "tab\there")
	if err := bad.Validate(); err == nil {
		t.Error("allowlisted text with control byte accepted")
	}
	bad = testPolicy()
	bad.MaxCapabilityBytes = 0
	if err := bad.Validate(); err == nil {
		t.Error("zero max capability bytes accepted")
	}
	bad = testPolicy()
	bad.MaxCapabilityBytes = 10
	if err := bad.Validate(); err == nil {
		t.Error("allowlisted text longer than the bound accepted")
	}
}

// --- Library validation --------------------------------------------------

func TestLibraryValidate(t *testing.T) {
	ok := testLibrary()
	if err := ok.Validate(); err != nil {
		t.Fatalf("ok library rejected: %v", err)
	}

	four := testLibrary()
	four.Classes = four.Classes[:4]
	if err := four.Validate(); err == nil {
		t.Error("4 classes accepted")
	}

	six := testLibrary()
	six.Classes = append(six.Classes, Class{ID: "extra_class", Steps: []string{"step-x-1"}})
	if err := six.Validate(); err == nil {
		t.Error("6 classes accepted")
	}

	empty := testLibrary()
	empty.Classes[2].Steps = nil
	if err := empty.Validate(); err == nil {
		t.Error("empty class accepted")
	}

	dup := testLibrary()
	dup.Classes[3].Steps = []string{"step-tool-1", "step-sys-2"}
	if err := dup.Validate(); err == nil {
		t.Error("duplicate step ID across classes accepted")
	}

	dupInClass := testLibrary()
	dupInClass.Classes[1].Steps = []string{"step-interp-1", "step-interp-1"}
	if err := dupInClass.Validate(); err == nil {
		t.Error("duplicate step ID within a class accepted")
	}

	unknown := testLibrary()
	unknown.Classes[0].ID = "tool_swap_v2"
	if err := unknown.Validate(); err == nil {
		t.Error("non-canonical class ID accepted")
	}

	dupClass := testLibrary()
	dupClass.Classes[4].ID = ClassToolSwap
	if err := dupClass.Validate(); err == nil {
		t.Error("duplicate class ID accepted")
	}

	badFrom := testLibrary()
	badFrom.Fallbacks[0].From = "no_such_class"
	if err := badFrom.Validate(); err == nil {
		t.Error("fallback with unknown From accepted")
	}

	selfTo := testLibrary()
	selfTo.Fallbacks[0].To = []string{ClassToolSwap}
	if err := selfTo.Validate(); err == nil {
		t.Error("fallback targeting its own class accepted")
	}
}

func TestLibraryDigest(t *testing.T) {
	a, err := testLibrary().Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	b, _ := testLibrary().Digest()
	if a != b {
		t.Fatal("library digest not stable across calls")
	}
	reordered := testLibrary()
	reordered.Classes[0].Steps[0], reordered.Classes[0].Steps[1] = reordered.Classes[0].Steps[1], reordered.Classes[0].Steps[0]
	c, _ := reordered.Digest()
	if a == c {
		t.Fatal("library digest insensitive to step order")
	}
	bad := testLibrary()
	bad.Classes = bad.Classes[:4]
	if _, err := bad.Digest(); err == nil {
		t.Fatal("digest of invalid library must fail")
	}
}

func TestCapsValidate(t *testing.T) {
	if err := testCaps().Validate(); err != nil {
		t.Fatalf("ok caps rejected: %v", err)
	}
	c := testCaps()
	c.OutTokens = 0
	if err := c.Validate(); err == nil {
		t.Error("zero cap accepted")
	}
}

func TestNewMachineRejects(t *testing.T) {
	badCaps := testCaps()
	badCaps.MicroUSD = -1
	badLib := testLibrary()
	badLib.Classes = badLib.Classes[:4]
	badPolicy := testPolicy()
	badPolicy.ManifestedReason = ""
	badFallback := testLibrary()
	badFallback.Fallbacks[0].OnReason = "policy_internal_rule_17"
	manifestedFallback := testLibrary()
	manifestedFallback.Fallbacks[0].OnReason = reasonManifested

	cases := []struct {
		name   string
		lib    Library
		seed   []byte
		caps   Caps
		policy FeedbackPolicy
	}{
		{"invalid caps", testLibrary(), testSeed, badCaps, testPolicy()},
		{"invalid library", badLib, testSeed, testCaps(), testPolicy()},
		{"invalid policy", testLibrary(), testSeed, testCaps(), badPolicy},
		{"empty seed", testLibrary(), nil, testCaps(), testPolicy()},
		{"fallback on unknown reason", badFallback, testSeed, testCaps(), testPolicy()},
		{"fallback on manifested reason", manifestedFallback, testSeed, testCaps(), testPolicy()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewMachine(c.lib, c.seed, c.caps, c.policy); err == nil {
				t.Fatal("expected NewMachine error")
			}
		})
	}
}

// --- Determinism ---------------------------------------------------------

func TestDeterminismAcross100Runs(t *testing.T) {
	refActs, refDigs := runScript(mustMachine(t), script())
	if countPivots(refActs) == 0 {
		t.Fatal("reference run issued no pivots")
	}
	for i := 0; i < 100; i++ {
		acts, digs := runScript(mustMachine(t), script())
		if !sameActions(acts, refActs) {
			t.Fatalf("run %d: action sequence differs", i)
		}
		if len(digs) != len(refDigs) {
			t.Fatalf("run %d: digest count differs", i)
		}
		for j := range digs {
			if digs[j] != refDigs[j] {
				t.Fatalf("run %d step %d: state-history digest differs", i, j)
			}
		}
	}
}

func TestSeedsStablePerSeed(t *testing.T) {
	lib, pol, caps := testLibrary(), testPolicy(), testCaps()
	distinct := map[string]bool{}
	for i := 1; i <= 16; i++ {
		seed := []byte{byte(i)}
		m1, err := NewMachine(lib, seed, caps, pol)
		if err != nil {
			t.Fatalf("NewMachine seed %d: %v", i, err)
		}
		m2, _ := NewMachine(lib, seed, caps, pol)
		a1, _ := runScript(m1, script())
		a2, _ := runScript(m2, script())
		if !sameActions(a1, a2) {
			t.Fatalf("seed %d: unstable across runs", i)
		}
		distinct[fmt.Sprint(a1)] = true
	}
	if len(distinct) < 2 {
		t.Fatal("seeds never change the tie-break outcome; seeded SHA-256 tie-break is inert")
	}
}

func TestReplayEqualsStepwise(t *testing.T) {
	fbs := script()
	acts, err := Replay(testLibrary(), testSeed, testCaps(), testPolicy(), fbs)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	stepwise, _ := runScript(mustMachine(t), fbs)
	if len(acts) != len(fbs)+1 {
		t.Fatalf("Replay length %d, want %d", len(acts), len(fbs)+1)
	}
	if !sameActions(acts, stepwise) {
		t.Fatal("Replay differs from stepwise Next")
	}
	if _, err := Replay(testLibrary(), nil, testCaps(), testPolicy(), fbs); err == nil {
		t.Fatal("Replay with empty seed must fail")
	}
}

func TestDigestSensitivityToFeedback(t *testing.T) {
	base := script()
	_, d0 := runScript(mustMachine(t), base)
	_, d0again := runScript(mustMachine(t), base)
	for i := range d0 {
		if d0[i] != d0again[i] {
			t.Fatalf("identical feedback gave different digest at %d", i)
		}
	}
	changed := script()
	changed[2] = fb(reasonDeniedTool, textRead) // text changed, reason same
	_, d1 := runScript(mustMachine(t), changed)
	if d1[3] == d0[3] {
		t.Fatal("digest insensitive to a changed capability text")
	}
	if d1[2] != d0[2] {
		t.Fatal("digest changed before the altered feedback was consumed")
	}
	reasonChanged := script()
	reasonChanged[1] = fb(reasonDeniedTool, textRead) // reason changed
	_, d2 := runScript(mustMachine(t), reasonChanged)
	if d2[2] == d0[2] {
		t.Fatal("digest insensitive to a changed reason code")
	}
}

// --- Budget --------------------------------------------------------------

func TestBudgetIterationsExhaust(t *testing.T) {
	caps := testCaps()
	caps.Iterations = 3
	m, err := NewMachine(testLibrary(), testSeed, caps, testPolicy())
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	fbs := []Feedback{fb(reasonDeniedPath, textRead), fb(reasonDeniedPath, textRead), fb(reasonDeniedPath, textRead)}
	acts, _ := runScript(m, fbs)
	if got := countPivots(acts); got != 3 {
		t.Fatalf("pivots = %d, want exactly the iteration cap 3", got)
	}
	last := acts[len(acts)-1]
	if last.Kind != ActionStop || last.Stop != StopBudgetExhausted {
		t.Fatalf("after cap: got %+v, want Stop(BudgetExhausted)", last)
	}
}

func TestBudgetDimensionsExhaust(t *testing.T) {
	type dim struct {
		name string
		set  func(*Usage, int64)
		get  func(Usage) int64
		capv func(Caps) int64
	}
	dims := []dim{
		{"tool_calls", func(u *Usage, v int64) { u.ToolCalls = v }, func(u Usage) int64 { return u.ToolCalls }, func(c Caps) int64 { return c.ToolCalls }},
		{"wall_ms", func(u *Usage, v int64) { u.WallMS = v }, func(u Usage) int64 { return u.WallMS }, func(c Caps) int64 { return c.WallMS }},
		{"in_tokens", func(u *Usage, v int64) { u.InTokens = v }, func(u Usage) int64 { return u.InTokens }, func(c Caps) int64 { return c.InTokens }},
		{"out_tokens", func(u *Usage, v int64) { u.OutTokens = v }, func(u Usage) int64 { return u.OutTokens }, func(c Caps) int64 { return c.OutTokens }},
		{"micro_usd", func(u *Usage, v int64) { u.MicroUSD = v }, func(u Usage) int64 { return u.MicroUSD }, func(c Caps) int64 { return c.MicroUSD }},
	}
	for _, d := range dims {
		t.Run(d.name, func(t *testing.T) {
			caps := testCaps()
			caps.ToolCalls, caps.WallMS, caps.InTokens, caps.OutTokens, caps.MicroUSD = 5, 5, 5, 5, 5
			m, err := NewMachine(testLibrary(), testSeed, caps, testPolicy())
			if err != nil {
				t.Fatalf("NewMachine: %v", err)
			}
			st, a := m.Start()
			if a.Kind != ActionPivot {
				t.Fatalf("start: got %+v, want pivot", a)
			}
			var below Usage
			d.set(&below, d.capv(caps)-1)
			st = m.Charge(st, below)
			st, a = m.Next(st, fb(reasonDeniedPath, textRead))
			if a.Kind != ActionPivot {
				t.Fatalf("below cap: got %+v, want pivot", a)
			}
			var over Usage
			d.set(&over, d.capv(caps)+7)
			st = m.Charge(st, over)
			if got := d.get(st.Used()); got != d.capv(caps) {
				t.Fatalf("accounted %d, want clamped to cap %d", got, d.capv(caps))
			}
			st, a = m.Next(st, fb(reasonDeniedPath, textRead))
			if a.Kind != ActionStop || a.Stop != StopBudgetExhausted {
				t.Fatalf("over cap: got %+v, want Stop(BudgetExhausted)", a)
			}
			if st.Stop() != StopBudgetExhausted {
				t.Fatalf("state stop = %v, want BudgetExhausted", st.Stop())
			}
		})
	}
}

func TestBudgetExactCapExhausts(t *testing.T) {
	caps := testCaps()
	caps.ToolCalls = 4
	m, err := NewMachine(testLibrary(), testSeed, caps, testPolicy())
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	st, _ := m.Start()
	st = m.Charge(st, Usage{ToolCalls: 4})
	if _, a := m.Next(st, fb(reasonDeniedPath, textRead)); a.Kind != ActionStop || a.Stop != StopBudgetExhausted {
		t.Fatalf("spending exactly the cap must stop: got %+v", a)
	}
}

func TestOverflowAccountingNoWrap(t *testing.T) {
	caps := testCaps()
	caps.ToolCalls = math.MaxInt64
	caps.WallMS = math.MaxInt64
	m, err := NewMachine(testLibrary(), testSeed, caps, testPolicy())
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	st, _ := m.Start()
	st = m.Charge(st, Usage{ToolCalls: math.MaxInt64 - 1, WallMS: 1})
	if st.Used().ToolCalls != math.MaxInt64-1 || st.Used().ToolCalls < 0 {
		t.Fatalf("tool calls = %d, want MaxInt64-1 without wrap", st.Used().ToolCalls)
	}
	st = m.Charge(st, Usage{ToolCalls: 5, WallMS: math.MaxInt64})
	if st.Used().ToolCalls != math.MaxInt64 || st.Used().WallMS != math.MaxInt64 {
		t.Fatalf("used = %+v, want clamped to cap", st.Used())
	}
	st = m.Charge(st, Usage{ToolCalls: math.MaxInt64, WallMS: math.MaxInt64})
	if st.Used().ToolCalls != math.MaxInt64 || st.Used().ToolCalls < 0 {
		t.Fatalf("second huge charge wrapped: %d", st.Used().ToolCalls)
	}
	if _, a := m.Next(st, fb(reasonDeniedPath, textRead)); a.Kind != ActionStop || a.Stop != StopBudgetExhausted {
		t.Fatalf("overflowing spend must stop: got %+v", a)
	}
}

func TestNegativeUsageFailsClosed(t *testing.T) {
	m := mustMachine(t)
	st, _ := m.Start()
	st = m.Charge(st, Usage{OutTokens: -1})
	if _, a := m.Next(st, fb(reasonDeniedPath, textRead)); a.Kind != ActionStop || a.Stop != StopInvalid {
		t.Fatalf("negative usage: got %+v, want Stop(Invalid)", a)
	}
}

func TestLibraryExhaustedStops(t *testing.T) {
	lib := Library{Classes: []Class{
		{ID: ClassToolSwap, Steps: []string{"step-a"}},
		{ID: ClassAlternateInterpreter, Steps: []string{"step-b"}},
		{ID: ClassAbsolutePath, Steps: []string{"step-c"}},
		{ID: ClassDirectResidualSyscall, Steps: []string{"step-d"}},
		{ID: ClassCompoundSequence, Steps: []string{"step-e"}},
	}}
	m, err := NewMachine(lib, testSeed, testCaps(), testPolicy())
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	fbs := make([]Feedback, 10)
	for i := range fbs {
		fbs[i] = fb(reasonDeniedPath, textRead)
	}
	acts, _ := runScript(m, fbs)
	if got := countPivots(acts); got != 5 {
		t.Fatalf("pivots = %d, want 5 (one per registered step)", got)
	}
	if last := acts[len(acts)-1]; last.Kind != ActionStop || last.Stop != StopLibraryExhausted {
		t.Fatalf("got %+v, want Stop(LibraryExhausted)", last)
	}
}

// --- Pivot selection -----------------------------------------------------

func TestStepsNeverRepeatWithinRun(t *testing.T) {
	acts, _ := runScript(mustMachine(t), script())
	seen := map[string]bool{}
	for _, a := range acts {
		if a.Kind != ActionPivot {
			continue
		}
		if seen[a.StepID] {
			t.Fatalf("step %q issued twice in one run", a.StepID)
		}
		seen[a.StepID] = true
	}
	if len(seen) == 0 {
		t.Fatal("no pivots issued")
	}
}

func TestFallbackRoutesOnReason(t *testing.T) {
	m := mustMachine(t)
	st, a := m.Start()
	if a.Kind != ActionPivot || a.ClassID != ClassToolSwap {
		t.Fatalf("first pivot %+v, want tool_swap", a)
	}
	_, a = m.Next(st, fb(reasonDeniedTool, textShell))
	if a.Kind != ActionPivot {
		t.Fatalf("after denial: got %+v, want pivot", a)
	}
	if a.ClassID != ClassAlternateInterpreter && a.ClassID != ClassAbsolutePath {
		t.Fatalf("denied_tool routed to %q, outside the fallback targets", a.ClassID)
	}
}

// --- Projection-only transition input and manifestation -----------------

func TestInvalidFeedbackFailsClosed(t *testing.T) {
	m := mustMachine(t)
	st, _ := m.Start()
	st, a := m.Next(st, fb("unregistered_reason", textShell))
	if a.Kind != ActionStop || a.Stop != StopInvalid {
		t.Fatalf("unknown reason: got %+v, want Stop(Invalid)", a)
	}
	st, a = m.Next(st, fb(reasonDeniedPath, textRead))
	if a.Kind != ActionStop || a.Stop != StopInvalid {
		t.Fatalf("after invalid input the terminal must stick: got %+v", a)
	}
	if st.Stop() != StopInvalid {
		t.Fatalf("state stop = %v, want Invalid", st.Stop())
	}
}

func TestTerminalStateIsSticky(t *testing.T) {
	m := mustMachine(t)
	st, _ := m.Start()
	st, stop := m.MarkManifested(st)
	if stop.Kind != ActionStop {
		t.Fatalf("expected terminal, got %+v", stop)
	}
	d := st.Digest()
	feeds := []Feedback{
		fb(reasonDeniedTool, textShell),
		fb(reasonManifested, textShell),
		fb("unregistered_reason", textShell),
		{},
	}
	for i, f := range feeds {
		var a Action
		var next State
		next, a = m.Next(st, f)
		if a != stop {
			t.Fatalf("feed %d: terminal changed to %+v", i, a)
		}
		if next.Digest() != d {
			t.Fatalf("feed %d: terminal state digest changed", i)
		}
	}
	st2 := m.Charge(st, Usage{ToolCalls: 1})
	if st2.Digest() != d {
		t.Fatal("Charge changed a terminal state")
	}
}

// --- Fail-closed and misuse ----------------------------------------------

func TestZeroValueMachineFailsClosed(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("zero-value Machine panicked: %v", r)
		}
	}()
	var m Machine
	st, a := m.Start()
	if a.Kind != ActionStop || a.Stop != StopInvalid {
		t.Fatalf("zero Start: got %+v, want Stop(Invalid)", a)
	}
	st, a = m.Next(st, fb(reasonDeniedTool, textShell))
	if a.Kind != ActionStop || a.Stop != StopInvalid {
		t.Fatalf("zero Next: got %+v, want Stop(Invalid)", a)
	}
	var zs State
	_, a = m.Next(zs, fb(reasonDeniedTool, textShell))
	if a.Kind != ActionStop || a.Stop != StopInvalid {
		t.Fatalf("zero State: got %+v, want Stop(Invalid)", a)
	}
	if got := m.Charge(zs, Usage{ToolCalls: 1}); got.Stop() != StopInvalid {
		t.Fatalf("zero Charge: stop = %v, want Invalid", got.Stop())
	}
}

func TestStateFromOtherMachineRejected(t *testing.T) {
	a, err := NewMachine(testLibrary(), []byte("seed-A"), testCaps(), testPolicy())
	if err != nil {
		t.Fatalf("NewMachine A: %v", err)
	}
	b, err := NewMachine(testLibrary(), []byte("seed-B"), testCaps(), testPolicy())
	if err != nil {
		t.Fatalf("NewMachine B: %v", err)
	}
	stA, _ := a.Start()
	if _, act := b.Next(stA, fb(reasonDeniedPath, textRead)); act.Kind != ActionStop || act.Stop != StopInvalid {
		t.Fatalf("state from another machine: got %+v, want Stop(Invalid)", act)
	}
}

func TestScriptedMachineDoesNotExhaustOnNotApplicableDimensions(t *testing.T) {
	caps := testCaps()
	caps.Scripted = true
	caps.InTokens, caps.OutTokens, caps.MicroUSD = 0, 0, 0
	machine, err := NewMachine(testLibrary(), testSeed, caps, testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	state, _ := machine.Start()
	state = machine.Charge(state, Usage{ToolCalls: 1})
	_, action := machine.Next(state, fb(reasonDeniedTool, textShell))
	if action.Kind != ActionPivot {
		t.Fatalf("zero spend exhausted scripted caps: %+v", action)
	}
	for _, usage := range []Usage{{InTokens: 1}, {OutTokens: 1}, {MicroUSD: 1}} {
		state, _ := machine.Start()
		state = machine.Charge(state, usage)
		_, action := machine.Next(state, fb(reasonDeniedTool, textShell))
		if action.Stop != StopInvalid {
			t.Errorf("scripted usage %+v accepted: %+v", usage, action)
		}
	}
	for _, dimension := range []string{"input", "output", "money"} {
		real := testCaps()
		scripted := caps
		switch dimension {
		case "input":
			real.InTokens = 0
			scripted.InTokens = 1
		case "output":
			real.OutTokens = 0
			scripted.OutTokens = 1
		case "money":
			real.MicroUSD = 0
			scripted.MicroUSD = 1
		}
		if err := real.Validate(); err == nil {
			t.Errorf("real provider accepted zero %s", dimension)
		}
		if err := scripted.Validate(); err == nil {
			t.Errorf("scripted accepted nonzero %s", dimension)
		}
	}
}

// Replay runs Start followed by Next for each feedback and returns the
// len(feedbacks)+1 actions. It makes no usage charges.
func Replay(lib Library, seed []byte, caps Caps, policy FeedbackPolicy, feedbacks []Feedback) ([]Action, error) {
	m, err := NewMachine(lib, seed, caps, policy)
	if err != nil {
		return nil, err
	}
	st, a := m.Start()
	acts := make([]Action, 0, len(feedbacks)+1)
	acts = append(acts, a)
	for _, f := range feedbacks {
		st, a = m.Next(st, f)
		acts = append(acts, a)
	}
	return acts, nil
}
