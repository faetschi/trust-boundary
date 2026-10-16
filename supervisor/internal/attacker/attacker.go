package attacker

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"slices"
)

// Class IDs (D32 item 1). The set is closed: a library must contain exactly
// these five classes.
const (
	ClassToolSwap              = "tool_swap"
	ClassAlternateInterpreter  = "alternate_interpreter"
	ClassAbsolutePath          = "absolute_path"
	ClassDirectResidualSyscall = "direct_residual_syscall"
	ClassCompoundSequence      = "compound_sequence"
)

var canonicalClasses = []string{
	ClassToolSwap,
	ClassAlternateInterpreter,
	ClassAbsolutePath,
	ClassDirectResidualSyscall,
	ClassCompoundSequence,
}

const (
	maxIdentLen               = 64
	maxCapabilityBoundCeiling = 1024
)

// CanonicalClassIDs returns the five registered class IDs in spec order.
func CanonicalClassIDs() []string { return slices.Clone(canonicalClasses) }

// validIdent accepts 1 to 64 bytes of [a-z0-9._-].
func validIdent(s string) bool {
	if len(s) == 0 || len(s) > maxIdentLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '.', c == '-':
		default:
			return false
		}
	}
	return true
}

// printableASCII accepts bytes 0x20 to 0x7e only.
func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// Feedback is the only transition input of the adaptive machine: the stable
// public reason code and the generic capability text. It has no other field.
// Build it with NewFeedback. The machine re-validates it on every Next.
type Feedback struct {
	ReasonCode     string
	CapabilityText string
}

// FeedbackPolicy is the caller-supplied allowlist and bound set.
type FeedbackPolicy struct {
	ReasonCodes        []string
	CapabilityTexts    []string
	ManifestedReason   string
	MaxCapabilityBytes int
}

// Validate checks the policy is internally consistent.
func (p FeedbackPolicy) Validate() error {
	if p.MaxCapabilityBytes < 1 || p.MaxCapabilityBytes > maxCapabilityBoundCeiling {
		return fmt.Errorf("attacker: MaxCapabilityBytes %d outside [1,%d]", p.MaxCapabilityBytes, maxCapabilityBoundCeiling)
	}
	if len(p.ReasonCodes) == 0 {
		return fmt.Errorf("attacker: policy has no reason codes")
	}
	seen := map[string]bool{}
	for _, r := range p.ReasonCodes {
		if !validIdent(r) {
			return fmt.Errorf("attacker: malformed reason code in policy")
		}
		if seen[r] {
			return fmt.Errorf("attacker: duplicate reason code %q in policy", r)
		}
		seen[r] = true
	}
	if !validIdent(p.ManifestedReason) || !slices.Contains(p.ReasonCodes, p.ManifestedReason) {
		return fmt.Errorf("attacker: manifested reason must be a member of the reason-code allowlist")
	}
	if len(p.CapabilityTexts) == 0 {
		return fmt.Errorf("attacker: policy has no capability texts")
	}
	seenText := map[string]bool{}
	for _, t := range p.CapabilityTexts {
		if len(t) == 0 || len(t) > p.MaxCapabilityBytes {
			return fmt.Errorf("attacker: allowlisted capability text outside length bound")
		}
		if !printableASCII(t) {
			return fmt.Errorf("attacker: allowlisted capability text is not printable ASCII")
		}
		if seenText[t] {
			return fmt.Errorf("attacker: duplicate allowlisted capability text")
		}
		seenText[t] = true
	}
	return nil
}

// CheckFeedback validates a projection against the policy. Error messages
// never echo the rejected content, which may be raw tool output.
func (p FeedbackPolicy) CheckFeedback(f Feedback) error {
	if f.ReasonCode == "" {
		return fmt.Errorf("attacker: empty reason code")
	}
	if len(f.ReasonCode) > maxIdentLen {
		return fmt.Errorf("attacker: reason code too long")
	}
	if !slices.Contains(p.ReasonCodes, f.ReasonCode) {
		return fmt.Errorf("attacker: reason code not in allowlist")
	}
	if len(f.CapabilityText) == 0 {
		return fmt.Errorf("attacker: empty capability text")
	}
	if len(f.CapabilityText) > p.MaxCapabilityBytes {
		return fmt.Errorf("attacker: capability text too long")
	}
	if !slices.Contains(p.CapabilityTexts, f.CapabilityText) {
		return fmt.Errorf("attacker: capability text not in generic allowlist")
	}
	return nil
}

// NewFeedback is the only constructor for a Feedback. It takes exactly the two
// projected fields, so no extra information can be carried in.
func NewFeedback(p FeedbackPolicy, reasonCode, capabilityText string) (Feedback, error) {
	if err := p.Validate(); err != nil {
		return Feedback{}, err
	}
	f := Feedback{ReasonCode: reasonCode, CapabilityText: capabilityText}
	if err := p.CheckFeedback(f); err != nil {
		return Feedback{}, err
	}
	return f, nil
}

// Class is one pivot class with an ordered list of step IDs.
type Class struct {
	ID    string
	Steps []string
}

// Fallback redirects pivoting away from From. OnReason "" means
// exhaustion-only. To lists candidate target classes; several candidates are
// resolved by the seeded SHA-256 tie-break.
type Fallback struct {
	From     string
	OnReason string
	To       []string
}

// Library is the pre-registered pivot library. Its content is data supplied
// by the pilot; this package validates it and does not embed it.
type Library struct {
	Classes   []Class
	Fallbacks []Fallback
}

// Validate requires exactly the five canonical classes, no empty class, no
// duplicate class or step ID, and fallback references that resolve.
func (l Library) Validate() error {
	if len(l.Classes) != len(canonicalClasses) {
		return fmt.Errorf("attacker: library has %d classes, want exactly %d", len(l.Classes), len(canonicalClasses))
	}
	seenClass := map[string]bool{}
	seenStep := map[string]bool{}
	for _, c := range l.Classes {
		if !slices.Contains(canonicalClasses, c.ID) {
			return fmt.Errorf("attacker: class %q is not a registered class", c.ID)
		}
		if seenClass[c.ID] {
			return fmt.Errorf("attacker: duplicate class %q", c.ID)
		}
		seenClass[c.ID] = true
		if len(c.Steps) == 0 {
			return fmt.Errorf("attacker: class %q has no steps", c.ID)
		}
		for _, s := range c.Steps {
			if !validIdent(s) {
				return fmt.Errorf("attacker: malformed step ID in class %q", c.ID)
			}
			if seenStep[s] {
				return fmt.Errorf("attacker: duplicate step ID %q", s)
			}
			seenStep[s] = true
		}
	}
	seenFallback := map[string]bool{}
	for _, fb := range l.Fallbacks {
		if !seenClass[fb.From] {
			return fmt.Errorf("attacker: fallback From %q is not a library class", fb.From)
		}
		if fb.OnReason != "" && !validIdent(fb.OnReason) {
			return fmt.Errorf("attacker: malformed fallback reason")
		}
		key := fb.From + "\x00" + fb.OnReason
		if seenFallback[key] {
			return fmt.Errorf("attacker: duplicate fallback for class %q", fb.From)
		}
		seenFallback[key] = true
		if len(fb.To) == 0 {
			return fmt.Errorf("attacker: fallback from %q has no targets", fb.From)
		}
		seenTo := map[string]bool{}
		for _, to := range fb.To {
			if !seenClass[to] || to == fb.From {
				return fmt.Errorf("attacker: fallback target %q invalid for %q", to, fb.From)
			}
			if seenTo[to] {
				return fmt.Errorf("attacker: duplicate fallback target %q", to)
			}
			seenTo[to] = true
		}
	}
	return nil
}

// Digest returns the order-sensitive SHA-256 of a valid library.
func (l Library) Digest() ([32]byte, error) {
	if err := l.Validate(); err != nil {
		return [32]byte{}, err
	}
	h := sha256.New()
	writeField(h, []byte("attacker/library/v1"))
	writeField(h, u64(int64(len(l.Classes))))
	for _, c := range l.Classes {
		writeField(h, []byte(c.ID))
		writeField(h, u64(int64(len(c.Steps))))
		for _, s := range c.Steps {
			writeField(h, []byte(s))
		}
	}
	writeField(h, u64(int64(len(l.Fallbacks))))
	for _, fb := range l.Fallbacks {
		writeField(h, []byte(fb.From))
		writeField(h, []byte(fb.OnReason))
		writeField(h, u64(int64(len(fb.To))))
		for _, to := range fb.To {
			writeField(h, []byte(to))
		}
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

// Caps are the cumulative budget dimensions. Iterations counts pivots issued.
type Caps struct {
	Iterations int64
	ToolCalls  int64
	WallMS     int64
	InTokens   int64
	OutTokens  int64
	MicroUSD   int64
}

// Validate requires every cap to be strictly positive.
func (c Caps) Validate() error {
	dims := []struct {
		name string
		v    int64
	}{
		{"Iterations", c.Iterations},
		{"ToolCalls", c.ToolCalls},
		{"WallMS", c.WallMS},
		{"InTokens", c.InTokens},
		{"OutTokens", c.OutTokens},
		{"MicroUSD", c.MicroUSD},
	}
	for _, d := range dims {
		if d.v <= 0 {
			return fmt.Errorf("attacker: cap %s must be positive", d.name)
		}
	}
	return nil
}

// Usage is one iteration's measured spend, recorded with Machine.Charge. It is
// accounting only and is never a pivot-selection input.
type Usage struct {
	ToolCalls int64
	WallMS    int64
	InTokens  int64
	OutTokens int64
	MicroUSD  int64
}

// StopReason is the terminal reason of a state.
type StopReason uint8

const (
	StopNone StopReason = iota
	StopBudgetExhausted
	StopManifested
	StopLibraryExhausted
	StopInvalid
)

// String returns a stable name for the reason.
func (r StopReason) String() string {
	switch r {
	case StopNone:
		return "none"
	case StopBudgetExhausted:
		return "budget_exhausted"
	case StopManifested:
		return "manifested"
	case StopLibraryExhausted:
		return "library_exhausted"
	case StopInvalid:
		return "invalid"
	default:
		return fmt.Sprintf("StopReason(%d)", uint8(r))
	}
}

// ActionKind distinguishes a pivot from a stop. The zero value is not a
// usable action.
type ActionKind uint8

const (
	ActionNone ActionKind = iota
	ActionPivot
	ActionStop
)

// Action is the machine output.
type Action struct {
	Kind    ActionKind
	ClassID string
	StepID  string
	Stop    StopReason
}

func stopAction(r StopReason) Action { return Action{Kind: ActionStop, Stop: r} }

// State is an immutable machine state. Its chain digest commits to every
// config input, feedback, charge, and stop in order.
type State struct {
	started   bool
	tag       [32]byte
	stop      StopReason
	cur       int
	tried     []bool
	iteration int64
	used      Usage
	exhausted bool
	chain     [32]byte
}

func invalidState() State { return State{stop: StopInvalid} }

// Digest returns the state-history digest.
func (s State) Digest() [32]byte { return s.chain }

// Stop returns the terminal reason, or StopNone while the state is active.
func (s State) Stop() StopReason { return s.stop }

// Iterations returns the number of pivots issued so far.
func (s State) Iterations() int64 { return s.iteration }

// Used returns the clamped cumulative usage. No field exceeds its cap.
func (s State) Used() Usage { return s.used }

type compiledClass struct {
	id    string
	steps []string
	base  int
}

type compiledFallback struct {
	from     int
	onReason string
	to       []int
}

// Machine is the deterministic adaptive transition function. The zero value
// is not usable and fails closed: every call returns Stop(Invalid).
type Machine struct {
	ok        bool
	tag       [32]byte
	seed      []byte
	caps      Caps
	policy    FeedbackPolicy
	classes   []compiledClass
	fallbacks []compiledFallback
	total     int
}

// NewMachine validates the configuration and returns a usable Machine.
func NewMachine(lib Library, seed []byte, caps Caps, policy FeedbackPolicy) (Machine, error) {
	if err := lib.Validate(); err != nil {
		return Machine{}, err
	}
	if err := caps.Validate(); err != nil {
		return Machine{}, err
	}
	if err := policy.Validate(); err != nil {
		return Machine{}, err
	}
	if len(seed) == 0 {
		return Machine{}, fmt.Errorf("attacker: seed must be non-empty")
	}
	for _, fb := range lib.Fallbacks {
		if fb.OnReason == "" {
			continue
		}
		if !slices.Contains(policy.ReasonCodes, fb.OnReason) {
			return Machine{}, fmt.Errorf("attacker: fallback reason not in policy allowlist")
		}
		if fb.OnReason == policy.ManifestedReason {
			return Machine{}, fmt.Errorf("attacker: fallback may not trigger on the manifested reason")
		}
	}
	libDigest, err := lib.Digest()
	if err != nil {
		return Machine{}, err
	}

	m := Machine{
		ok:     true,
		seed:   bytes.Clone(seed),
		caps:   caps,
		policy: clonePolicy(policy),
	}
	for _, c := range lib.Classes {
		m.classes = append(m.classes, compiledClass{id: c.ID, steps: slices.Clone(c.Steps), base: m.total})
		m.total += len(c.Steps)
	}
	index := func(id string) int {
		for i, c := range m.classes {
			if c.id == id {
				return i
			}
		}
		return -1
	}
	for _, fb := range lib.Fallbacks {
		cf := compiledFallback{from: index(fb.From), onReason: fb.OnReason}
		for _, to := range fb.To {
			cf.to = append(cf.to, index(to))
		}
		m.fallbacks = append(m.fallbacks, cf)
	}

	fields := [][]byte{libDigest[:], m.seed,
		u64(caps.Iterations), u64(caps.ToolCalls), u64(caps.WallMS),
		u64(caps.InTokens), u64(caps.OutTokens), u64(caps.MicroUSD),
		u64(int64(len(policy.ReasonCodes)))}
	for _, r := range policy.ReasonCodes {
		fields = append(fields, []byte(r))
	}
	fields = append(fields, u64(int64(len(policy.CapabilityTexts))))
	for _, t := range policy.CapabilityTexts {
		fields = append(fields, []byte(t))
	}
	fields = append(fields, []byte(policy.ManifestedReason), u64(int64(policy.MaxCapabilityBytes)))
	m.tag = digestOf([32]byte{}, "attacker/config/v1", fields...)
	return m, nil
}

// Start issues the first pivot from the initial state.
func (m Machine) Start() (State, Action) {
	if !m.ok {
		return invalidState(), stopAction(StopInvalid)
	}
	st := State{
		started: true,
		tag:     m.tag,
		tried:   make([]bool, m.total),
		chain:   digestOf(m.tag, "start"),
	}
	return m.pivot(st, Feedback{})
}

// Next consumes one projected feedback and selects the next action. It is
// pure: the same state and feedback always give the same result.
func (m Machine) Next(st State, fb Feedback) (State, Action) {
	if !m.ok || !st.started || st.tag != m.tag {
		return invalidState(), stopAction(StopInvalid)
	}
	if st.stop != StopNone {
		return st, stopAction(st.stop)
	}
	if err := m.policy.CheckFeedback(fb); err != nil {
		return m.stop(st, StopInvalid)
	}
	if fb.ReasonCode == m.policy.ManifestedReason {
		return m.stop(st, StopManifested)
	}
	return m.pivot(st, fb)
}

// Charge records one iteration's measured usage. Each dimension is clamped to
// its cap, and reaching a cap marks the state exhausted. A negative value is
// invalid. Charge is accounting only and never selects a pivot.
func (m Machine) Charge(st State, u Usage) State {
	if !m.ok || !st.started || st.tag != m.tag {
		return invalidState()
	}
	if st.stop != StopNone {
		return st
	}
	if u.ToolCalls < 0 || u.WallMS < 0 || u.InTokens < 0 || u.OutTokens < 0 || u.MicroUSD < 0 {
		next, _ := m.stop(st, StopInvalid)
		return next
	}
	var reached bool
	st.used.ToolCalls, reached = clampAdd(st.used.ToolCalls, u.ToolCalls, m.caps.ToolCalls)
	st.exhausted = st.exhausted || reached
	st.used.WallMS, reached = clampAdd(st.used.WallMS, u.WallMS, m.caps.WallMS)
	st.exhausted = st.exhausted || reached
	st.used.InTokens, reached = clampAdd(st.used.InTokens, u.InTokens, m.caps.InTokens)
	st.exhausted = st.exhausted || reached
	st.used.OutTokens, reached = clampAdd(st.used.OutTokens, u.OutTokens, m.caps.OutTokens)
	st.exhausted = st.exhausted || reached
	st.used.MicroUSD, reached = clampAdd(st.used.MicroUSD, u.MicroUSD, m.caps.MicroUSD)
	st.exhausted = st.exhausted || reached
	st.chain = digestOf(st.chain, "charge",
		u64(u.ToolCalls), u64(u.WallMS), u64(u.InTokens), u64(u.OutTokens), u64(u.MicroUSD))
	return st
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

// pivot selects the next pivot, or stops on exhaustion. It must run only on a
// valid, active state.
func (m Machine) pivot(st State, fb Feedback) (State, Action) {
	if st.exhausted || st.iteration >= m.caps.Iterations {
		return m.stop(st, StopBudgetExhausted)
	}
	c, ok := m.choose(st, fb.ReasonCode)
	if !ok {
		return m.stop(st, StopLibraryExhausted)
	}
	cc := m.classes[c]
	flat := -1
	stepID := ""
	for k, s := range cc.steps {
		if !st.tried[cc.base+k] {
			flat, stepID = cc.base+k, s
			break
		}
	}
	if flat < 0 {
		return m.stop(st, StopLibraryExhausted)
	}
	tried := slices.Clone(st.tried)
	tried[flat] = true
	st.tried = tried
	st.cur = c
	st.iteration++
	st.chain = digestOf(st.chain, "pivot",
		[]byte(cc.id), []byte(stepID), []byte(fb.ReasonCode), []byte(fb.CapabilityText), u64(st.iteration))
	return st, Action{Kind: ActionPivot, ClassID: cc.id, StepID: stepID}
}

// stop makes a terminal state, which is sticky once set.
func (m Machine) stop(st State, r StopReason) (State, Action) {
	st.stop = r
	st.chain = digestOf(st.chain, "stop", []byte{byte(r)})
	return st, stopAction(r)
}

// choose selects a class that still has untried steps, using the reason-
// triggered fallback, then the current class, then the exhaustion fallback,
// then the first class in library order. Ties go to the seeded SHA-256
// tie-break.
func (m Machine) choose(st State, reason string) (int, bool) {
	cands := m.candidates(st, reason)
	if len(cands) == 0 {
		return -1, false
	}
	if len(cands) == 1 {
		return cands[0], true
	}
	best := -1
	var bestKey [32]byte
	for _, c := range cands {
		k := digestOf(st.chain, "attacker/tiebreak/v1", m.seed, []byte(m.classes[c].id))
		if best < 0 || bytes.Compare(k[:], bestKey[:]) < 0 {
			best, bestKey = c, k
		}
	}
	return best, true
}

func (m Machine) candidates(st State, reason string) []int {
	hasUntried := func(c int) bool {
		cc := m.classes[c]
		for k := range cc.steps {
			if !st.tried[cc.base+k] {
				return true
			}
		}
		return false
	}
	filter := func(targets []int) []int {
		var out []int
		for _, t := range targets {
			if hasUntried(t) {
				out = append(out, t)
			}
		}
		return out
	}
	if reason != "" {
		if fb := m.fallbackFor(st.cur, reason); fb != nil {
			if c := filter(fb.to); len(c) > 0 {
				return c
			}
		}
	}
	if hasUntried(st.cur) {
		return []int{st.cur}
	}
	if fb := m.fallbackFor(st.cur, ""); fb != nil {
		if c := filter(fb.to); len(c) > 0 {
			return c
		}
	}
	for c := range m.classes {
		if hasUntried(c) {
			return []int{c}
		}
	}
	return nil
}

func (m Machine) fallbackFor(cur int, reason string) *compiledFallback {
	for i := range m.fallbacks {
		if m.fallbacks[i].from == cur && m.fallbacks[i].onReason == reason {
			return &m.fallbacks[i]
		}
	}
	return nil
}

// clampAdd returns used+inc, clamped to limit. The bool reports whether limit
// is reached. Both inputs are non-negative and used <= limit, so the
// arithmetic cannot overflow or wrap.
func clampAdd(used, inc, limit int64) (int64, bool) {
	if inc >= limit-used {
		return limit, true
	}
	return used + inc, false
}

func clonePolicy(p FeedbackPolicy) FeedbackPolicy {
	return FeedbackPolicy{
		ReasonCodes:        slices.Clone(p.ReasonCodes),
		CapabilityTexts:    slices.Clone(p.CapabilityTexts),
		ManifestedReason:   p.ManifestedReason,
		MaxCapabilityBytes: p.MaxCapabilityBytes,
	}
}

func writeField(h hash.Hash, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	_, _ = h.Write(n[:])
	_, _ = h.Write(b)
}

func digestOf(prev [32]byte, label string, fields ...[]byte) [32]byte {
	h := sha256.New()
	writeField(h, prev[:])
	writeField(h, []byte(label))
	for _, f := range fields {
		writeField(h, f)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func u64(v int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	return b[:]
}
