// Package routematrix is a pure coverage checker for the E06 exposure matrix
// (MSE_MA_Thesis/tbound/AH-technical/11-evidence-contract.md, "E06 - exposure
// matrix and read effects").
//
// It registers the four E06 routes (declared read, Bash command view, direct
// Pi/runtime access, provider broker) against a caller-supplied list of
// sensitive classes. It checks that every (route, class) cell is present
// exactly once, that each treatment is one the spec allows for its route, that
// each cell's status and test reference are well formed, and it reports
// coverage and a canonical digest of the registered matrix.
//
// This package does not claim that E06 passes. It does not run tests, does not
// resolve test names (the caller supplies a lookup to RefsExist), does not shell
// out to go test, and does not read evidence. A Complete report means only that
// every cell is registered as Covered with a well-formed reference; whether the
// referenced test exists and passes is a separate question for the caller and
// the evidence pipeline. Green results from this package never establish a
// containment or H1 claim.
//
// The package performs no I/O, reads no clock, starts no goroutines, and is not
// wired into any other package.
//
// Treatments per route, as the spec's E06 table allows them:
//
//   - declared-read, bash-view: omitted, marker, unreachable, proposal-denied.
//   - direct-runtime: omitted, marker, unreachable. The spec's proposal
//     restriction for this route is "not a proposal rule", so proposal-denied
//     is rejected.
//   - provider-broker: proposal-denied only. The spec says this route is
//     "never treated as confidential", so omitted, marker and unreachable are
//     rejected as confidentiality claims.
//
// Fail-closed rules. Complete is true only when Validate passes and every cell
// is Covered. A Pending_Guest or Missing cell makes Complete false. An invalid
// matrix (including an empty one) yields an error from Coverage and Complete
// false.
//
// OPEN spec questions, recorded here rather than resolved in code:
//
//  1. The spec does not name the sensitive classes. They are caller input, and
//     this package defines no class vocabulary.
//  2. The spec's treatment column for the provider broker is not a treatment in
//     the four-value set. Allowing proposal-denied as the only broker treatment
//     is an interpretation, not a quotation.
//  3. The spec does not define a test-reference format. The accepted form,
//     "package/path.TestName", is an assumption of this package. Only the
//     syntax is checked; existence is left to RefsExist.
//  4. The Cell struct carries a Reason string for Pending_Guest cells. The brief
//     named only the Cell fields Route, Class, Treatment, TestRef and Status.
//     A Reason field was needed to carry the required pending reason.
package routematrix
