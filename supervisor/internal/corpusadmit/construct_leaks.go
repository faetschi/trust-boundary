package corpusadmit

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/taskaccept"
	"tbound/supervisor/internal/trialfix"
)

// ExactLeaks is the shared exact-output comparison from the pilot prompt and
// bash-fixture tests. It trims output whitespace, not the inspected material.
func ExactLeaks(text string, values []string) []string {
	var leaks []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && strings.Contains(text, value) && !slices.Contains(leaks, value) {
			leaks = append(leaks, value)
		}
	}
	return leaks
}

var canaryMarker = regexp.MustCompile(`(?i)(?:marker[= :]+)([a-z0-9_-]+)`)

func constructLeaks(c evalcorpus.Case, fixture trialfix.Spec, accept taskaccept.AcceptSpec, solution taskaccept.SolutionSpec) []ConstructCheck {
	prompt := ConstructCheck{Name: "leak-prompt", Status: "pass", Reason: "no exact witness value, oracle identifier or canary marker in prompt"}
	fx := ConstructCheck{Name: "leak-fixture", Status: "pass", Reason: "no exact completion witness value in fixture files"}
	if c.Trial == nil {
		prompt.Status, prompt.Reason = "fail", "missing prompt"
		fx.Status, fx.Reason = "not-checked", "missing trial"
		return []ConstructCheck{prompt, fx}
	}
	files, err := fixture.Files()
	if err != nil {
		prompt.Status, prompt.Reason = "fail", err.Error()
		fx.Status, fx.Reason = "fail", err.Error()
		return []ConstructCheck{prompt, fx}
	}
	var values []string
	switch c.Kind {
	case evalcorpus.KindCompliant:
		values = taskaccept.WitnessValues(accept, solution)
	case evalcorpus.KindAdversarial:
		if c.Adversarial != nil {
			values = append(values, c.Adversarial.OracleID, c.Adversarial.OracleVersion)
			values = append(values, c.Adversarial.ForbiddenEffects...)
		}
		for _, f := range files {
			if !f.Dir {
				values = append(values, string(f.Content))
				for _, match := range canaryMarker.FindAllSubmatch(f.Content, -1) {
					values = append(values, string(match[1]))
				}
			}
		}
		fx.Status, fx.Reason = "not-checked", "adversarial fixtures intentionally carry synthetic canaries; prompt is checked for disclosure"
	default:
		prompt.Status, prompt.Reason = "not-checked", "ambiguous rubric case has no exact completion/effect witness"
		fx.Status, fx.Reason = "not-checked", prompt.Reason
	}
	if n := len(ExactLeaks(c.Trial.Prompt, values)); n != 0 {
		prompt.Status, prompt.Reason = "fail", fmt.Sprintf("prompt contains %d exact witness/oracle/canary value(s)", n)
	}
	if c.Kind == evalcorpus.KindCompliant && c.Compliant != nil && c.Compliant.PrimaryTool == evalcorpus.ToolRead {
		// A retrieval task reads its answer from the fixture, so the witness is there by design.
		fx.Status, fx.Reason = "not-checked", "read retrieval: the witness is in the fixture by design; the prompt is checked"
	} else if c.Kind == evalcorpus.KindCompliant {
		for _, f := range files {
			if !f.Dir && len(ExactLeaks(string(f.Content), values)) != 0 {
				fx.Status, fx.Reason = "fail", "fixture contains exact completion witness value: "+f.Path
				break
			}
		}
	}
	return []ConstructCheck{prompt, fx}
}
