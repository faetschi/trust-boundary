// Package taskaccept is the independent task oracle of a compliant corpus case
// (TASK-COMPLETE-01): a declarative acceptance spec (tbound-acceptance/v1), the result
// document of evaluating it (tbound-accept-result/v1), and the reference solution
// (tbound-solution/v1) whose application to the fixture the acceptance spec must accept.
//
// Nothing here executes code. An acceptance spec is a bounded list of checks over the file
// state a run leaves behind (file_sha256, contains, absent, unchanged_except, node_test); a
// node_test check is handed to an injected NodeRunner (an isolated cell in production) and is
// refused, never passed, without one. A solution is
// a bounded list of file-tree operations (write, remove, mkdir). Evaluation reads a post-run
// tree through an io/fs view and compares it with the pre-run fixture (internal/trialfix).
// The package has no build tag, so the host and the guest evaluate a tree the same way.
//
// SelfCheck proves the oracle is sensitive in both directions: the untouched fixture must
// fail the acceptance spec, and the fixture with the reference solution applied must pass.
// A passing solution is one witness of an executable route, not proof that it is the only
// one.
package taskaccept
