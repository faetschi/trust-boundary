package corpusadmit

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/taskaccept"
)

func loadPilotManifest(t *testing.T) evalcorpus.Manifest {
	t.Helper()
	file, err := os.Open("../../../corpus/pilot/manifest.draft.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	manifest, err := evalcorpus.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestPilotHasTwentyDistinctCompliantTasksWithSensitiveWitnesses(t *testing.T) {
	manifest := loadPilotManifest(t)
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
	if err := CheckManifestArtifactsWith(manifest, os.DirFS("../../../corpus/pilot/artifacts"), pilotNode(t)); err != nil {
		t.Fatal(err)
	}
}

func TestPilotReadAndBashPromptsDoNotRevealCompletionOutput(t *testing.T) {
	// Write/edit tasks specify the requested contents; read/bash tasks must obtain results from the fixture.
	for _, c := range loadPilotManifest(t).Cases {
		if c.Compliant == nil || (c.Compliant.PrimaryTool != evalcorpus.ToolRead && c.Compliant.PrimaryTool != evalcorpus.ToolBash) {
			continue
		}
		t.Run(c.CaseID, func(t *testing.T) {
			name, _ := SolutionArtifactName(c.Compliant.ReferenceSolution.Digest)
			raw, err := readArtifact(os.DirFS("../../../corpus/pilot/artifacts"), name, c.Compliant.ReferenceSolution.Digest)
			if err != nil {
				t.Fatal(err)
			}
			var solution struct {
				Ops []taskaccept.Op `json:"ops"`
			}
			if err := json.Unmarshal(raw, &solution); err != nil {
				t.Fatal(err)
			}
			for _, op := range solution.Ops {
				content, err := base64.StdEncoding.DecodeString(op.ContentB64)
				if err != nil {
					t.Fatal(err)
				}
				output := strings.TrimSpace(string(content))
				if len(ExactLeaks(c.Trial.Prompt, []string{output})) != 0 {
					t.Errorf("prompt reveals completion output %q", output)
				}
			}
		})
	}
}
