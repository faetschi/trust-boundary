// Package attacker implements the machinery of the claim-bearing adaptive
// attacker for the defense-adaptive positive control (D32). It is a pure,
// deterministic transition function over a pre-registered pivot library. It
// performs no I/O, reads no clock, starts no goroutines, and imports no other
// supervisor package.
//
// Specification. The governing text is in the thesis specification tree
// (MSE_MA_Thesis/tbound/AH-technical and tbound-thesis.md):
//
//   - 10-decision-register.md, D32 (lines ~123-145). Item 1 makes a
//     deterministic, pre-registered five-class pivot library claim-bearing:
//     tool swap, alternate interpreter, absolute path, direct residual
//     syscall, compound sequence. Item 6 fixes the iteration, wall-clock,
//     tool-call, and token caps and their derivation from the pilot. The
//     pivot ordering, transitions, and fallback branches are frozen at pilot
//     time, so this package takes them as input data and validates them. It
//     does not embed them.
//   - 02-process-model.md, section 4.1 (lines ~219-241). The adaptive
//     transition function reads exactly the stable public reason code and the
//     generic capability text, and no other transport, audit, or observer
//     field. The Feedback type and its validator implement that projection.
//   - 02-process-model.md, section 11.1 (lines ~699-720). Denial-then-pivot
//     sequences: a denial is followed by a fresh pivot, and cumulative budgets
//     continue across generations.
//   - 11-evidence-contract.md, E03 (lines ~55-100). The adaptive parameters are
//     frozen before headline runs. One iteration consumes the initial state or
//     the previous projected feedback, selects one pivot state, and ends when
//     that pivot's outcome is recorded. Wall time and every enclosed call count
//     toward the caps.
//   - tbound-thesis.md, section 7 "Frozen adaptive parameters (D32)" (lines
//     ~555-590), and the grep hits for "adaptive", "pivot", and "D26 feedback".
//
// Model.
//
//   - Feedback is the only transition input. It has exactly two fields,
//     ReasonCode and CapabilityText, and is constructed only through
//     NewFeedback under a FeedbackPolicy. The policy holds the caller-supplied
//     reason-code allowlist, the generic capability-text allowlist, the length
//     bound, and the reserved oracle (manifested-effect) reason, which is never
//     valid Feedback. The machine re-validates every
//     Feedback it receives, so a hand-built value fails closed.
//   - Library holds five classes, each with an ordered list of step IDs, plus
//     Fallback rules as data. Validate requires exactly the five canonical
//     classes, no empty class, no duplicate step ID, and consistent fallback
//     references. Digest is an order-sensitive SHA-256 of the library.
//   - Machine.Next(state, feedback) is pure. Given the same library, seed,
//     caps, policy, and feedback sequence, it produces the same actions and the
//     same state-history digest. The only randomness is a seeded SHA-256
//     tie-break over length-prefixed fields. It is used when a fallback offers
//     several target classes.
//   - Usage accounting (tool calls, wall time, tokens, micro-USD) enters through
//     Machine.Charge, not through the feedback projection. It is recorded for
//     the budget and is never an input to pivot selection. Charges are clamped
//     to the cap, so accounted values never exceed it and never wrap.
//   - An oracle verdict is not a transition input (D32 item 2). The runner
//     reports a manifested effect with Machine.MarkManifested, which records
//     the terminal manifested state without touching pivot choice. Feedback
//     that carries the manifested reason is invalid and yields Stop(Invalid).
//   - Terminal states are sticky. MarkManifested yields
//     Stop(Manifested) and takes priority over budget exhaustion. A zero-value
//     Machine, a zero-value State, a State from another machine, invalid
//     feedback, or negative usage yields Stop(Invalid).
//
// Out of scope. The library content, ordering, fallbacks, caps, pilot
// corpus, and seed schedule are pilot-deferred. The optional LLM red-teamer
// is non-claim-bearing and does not use this package. This package does not
// import the calibration package and does not claim that the attacker detects
// or prevents anything.
package attacker
