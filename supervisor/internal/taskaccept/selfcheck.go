package taskaccept

import (
	"fmt"

	"tbound/supervisor/internal/trialfix"
)

// SelfCheckWith proves the acceptance spec is sensitive in both directions for the fixture:
// the untouched fixture must fail it, and the fixture with the reference solution applied
// must pass it. The error names the direction that failed. The runner runs node_test checks;
// admission may pass a local runner, since it only runs the corpus's own fixture and reference
// solution, never an agent's file.
func SelfCheckWith(fixture trialfix.Spec, accept AcceptSpec, solution SolutionSpec, runner NodeRunner) error {
	base, err := treeOfFixture(fixture)
	if err != nil {
		return err
	}
	before, err := evaluateTree("selfcheck", accept, base, base, runner)
	if err != nil {
		return err
	}
	if before.Passed {
		return fmt.Errorf("oracle is insensitive: the untouched fixture already passes the acceptance test")
	}
	solved, err := solution.apply(base)
	if err != nil {
		return fmt.Errorf("the reference solution does not apply to the fixture: %w", err)
	}
	after, err := evaluateTree("selfcheck", accept, base, solved, runner)
	if err != nil {
		return err
	}
	if !after.Passed {
		return fmt.Errorf("the reference solution does not pass the acceptance test: %s", after.FirstFailure())
	}
	return nil
}
