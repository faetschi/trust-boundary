// Package routematrix is a pure coverage checker for the E06 exposure matrix
// (MSE_MA_Thesis/tbound/AH-technical/11-evidence-contract.md, "E06 - exposure
// matrix and read effects").
//
// It registers the four E06 routes (declared read, Bash command view, direct
// Pi/runtime access, provider broker) against a caller-supplied list of
// sensitive classes. It checks that every (route, class) cell is present
// exactly once, that each treatment and evidence kind is one this package allows
// for its route, and that each cell's status and test references are well formed.
// It reports coverage, the reasons a report is incomplete, and a canonical digest
// of the registered matrix.
//
// This package does not claim that E06 passes. It does not run tests, does not
// shell out to go test, and does not read evidence. The caller supplies a lookup
// that resolves test references; Coverage and RefsExist call it and do not decide
// existence themselves. A Complete report means only that every cell is Covered
// with every required evidence kind, every test reference resolves through the
// caller's lookup, and no test reference is reused. Whether the referenced tests
// pass, and whether they evidence anything, is a separate question for the caller
// and the evidence pipeline. Green results from this package never establish a
// containment or H1 claim.
//
// The package performs no I/O, reads no clock, starts no goroutines, and is not
// wired into any other package.
//
// Treatments per route. The spec's E06 table has no treatment column. The
// per-route policy below is the implementer's reading of the table's filtered
// materialization and proposal restriction columns. It is OPEN (OPEN 2), not a
// quotation:
//
//   - declared-read, bash-view: omitted, marker, unreachable, proposal-denied.
//   - direct-runtime: omitted, marker, unreachable. The spec's proposal
//     restriction for this route is "not a proposal rule", so proposal-denied
//     is rejected.
//   - provider-broker: proposal-denied only. The spec says this route is
//     "never treated as confidential", so omitted, marker and unreachable are
//     rejected as confidentiality claims. This broker rule is OPEN (OPEN 2).
//
// Evidence per route. The E06 text says "Read attempts, successful reads, and
// provider-channel disclosure are separate evidence types." Its required-test
// column names a denied-target or direct read attempt for the three read routes
// and a provider-channel classification for the broker. This package reads that as:
//
//   - declared-read, bash-view, direct-runtime: AttemptedRead and SuccessfulRead.
//   - provider-broker: ProviderChannelDisclosure.
//
// A cell may carry only the kinds its route requires. Each kind has its own test
// reference. A Covered cell that lacks a required kind is not Complete. The
// successful-read requirement for the read routes is OPEN (OPEN 5).
//
// Test references. Every test reference must be unique across the matrix: each
// reference occupies one (route, class, kind) slot only. This is an implementer
// choice, so one test cannot evidence two cells or two kinds. A reused reference
// makes the report incomplete.
//
// Fail-closed rules. Complete is true only when Validate passes, the lookup is
// not nil, every cell is Covered, every Covered cell carries every required
// evidence kind, every test reference resolves, and no test reference is reused.
// Each failed condition appears in Report.Reasons. A Pending_Guest or Missing cell
// makes Complete false. An invalid matrix (including an empty one) yields an error
// from Coverage and a zero Report.
//
// OPEN spec questions, recorded here rather than resolved in code:
//
//  1. The spec does not name the sensitive classes. They are caller input, and
//     this package defines no class vocabulary.
//  2. The per-route treatment policy is not in the spec. The E06 table has no
//     treatment column. The provider-broker rule (proposal-denied as the only
//     treatment) is an interpretation of "never treated as confidential", not a
//     quotation.
//  3. The spec does not define a test-reference format. The accepted form,
//     "package/path.TestName", is an assumption of this package. Only the
//     syntax is checked; existence is left to the caller's lookup.
//  4. The Cell struct carries a Reason string for Pending_Guest cells, and an
//     Evidence list of per-kind test references in place of the single TestRef
//     named in the brief. The brief named the Cell fields Route, Class,
//     Treatment, TestRef and Status. Reason and Evidence were added so that a
//     pending reason and the separate evidence types can be recorded.
//  5. The E06 required-test column names attempted or denied-target reads for the
//     read routes, not successful reads. Requiring SuccessfulRead on those routes
//     is an interpretation of the "separate evidence types" sentence, not a
//     quotation. The kind-to-route mapping is therefore OPEN.
package routematrix
