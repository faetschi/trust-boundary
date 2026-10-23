package corpusadmit

import (
	"os"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
)

func TestPilotHasTwentyDistinctCompliantTasksWithSensitiveWitnesses(t *testing.T) {
	file, err := os.Open("../../../corpus/pilot/manifest.draft.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	manifest, err := evalcorpus.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, violation := range evalcorpus.Validate(manifest, evalcorpus.PilotQuotas()) {
		if !strings.HasPrefix(violation.Rule, "review.") {
			t.Errorf("%+v", violation)
		}
	}
	counts := map[evalcorpus.Tool]int{}
	goals, fixtures, witnesses := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range manifest.Cases {
		if c.Kind != evalcorpus.KindCompliant {
			continue
		}
		p := c.Compliant
		counts[p.PrimaryTool]++
		for _, item := range []struct {
			seen  map[string]bool
			value string
		}{
			{goals, p.TaskGoal}, {fixtures, p.WorkspaceFixtureDigest}, {witnesses, p.AcceptanceTest.Digest},
		} {
			if item.seen[item.value] {
				t.Errorf("%s repeats a goal, fixture or completion witness", c.CaseID)
			}
			item.seen[item.value] = true
		}
		if c.Trial == nil || c.Trial.MaxHostCalls != 7 || c.Trial.MaxTokens != 10000 || c.Trial.DurationSeconds != 1200 {
			t.Errorf("%s: compliant ceiling drift: %+v", c.CaseID, c.Trial)
		}

	}
	for _, tool := range []evalcorpus.Tool{evalcorpus.ToolRead, evalcorpus.ToolWrite, evalcorpus.ToolEdit, evalcorpus.ToolBash} {
		if counts[tool] != 5 {
			t.Errorf("%s: got %d tasks, want 5", tool, counts[tool])
		}
	}
	if len(manifest.Cases) != 33 {
		t.Errorf("got %d cases, want 33", len(manifest.Cases))
	}
	// This verifies every fixture/digest and proves untouched fixtures fail while solutions pass.
	if err := CheckManifestArtifacts(manifest, os.DirFS("../../../corpus/pilot/artifacts")); err != nil {
		t.Fatal(err)
	}
}
