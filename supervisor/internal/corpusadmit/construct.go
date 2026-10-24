package corpusadmit

import (
	"fmt"
	"io/fs"
	"strings"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/taskaccept"
)

type ConstructCheck struct {
	Name       string                      `json:"name"`
	Status     string                      `json:"status"`
	Reason     string                      `json:"reason"`
	Conditions []string                    `json:"conditions,omitempty"`
	Mutations  []taskaccept.MutationResult `json:"mutations,omitempty"`
}

type ConstructCase struct {
	CaseID string           `json:"case_id"`
	Checks []ConstructCheck `json:"checks"`
}

type ConstructCounts struct {
	Pass       int `json:"pass"`
	Fail       int `json:"fail"`
	GuestOnly  int `json:"guest-only"`
	NotChecked int `json:"not-checked"`
}

type ConstructReport struct {
	Schema         string                     `json:"schema"`
	ManifestDigest string                     `json:"manifest_digest"`
	Passed         bool                       `json:"passed"`
	ReadyForPanel  bool                       `json:"ready_for_panel"`
	Summary        map[string]ConstructCounts `json:"summary"`
	Checks         []ConstructCheck           `json:"checks"`
	Cases          []ConstructCase            `json:"cases"`
	DeferredReview []evalcorpus.Violation     `json:"deferred_review,omitempty"`
}

func errorCheck(name string, err error) ConstructCheck {
	r := ConstructCheck{Name: name, Status: "pass", Reason: "existing admission check passed"}
	if err != nil {
		r.Status, r.Reason = "fail", err.Error()
	}
	return r
}

// CheckConstructs composes production validation, admission and self-checks.
// Review/adjudication rules are retained separately: D35 step 1 precedes them.
// Guest-only means required evidence is still pending, never a local pass.
func CheckConstructs(m evalcorpus.Manifest, set fs.FS, quotas evalcorpus.Quotas, other *evalcorpus.Manifest, runner taskaccept.NodeRunner) ConstructReport {
	r := ConstructReport{Schema: "tbound-construct-checks/v1", Passed: true, ReadyForPanel: true, Summary: map[string]ConstructCounts{}}
	digest, err := evalcorpus.Digest(m)
	r.ManifestDigest = digest
	r.Checks = append(r.Checks, errorCheck("manifest-digest", err))
	var violations []evalcorpus.Violation
	for _, v := range evalcorpus.Validate(m, quotas) {
		if strings.HasPrefix(v.Rule, "review.") || strings.HasPrefix(v.Rule, "adjudication.") {
			r.DeferredReview = append(r.DeferredReview, v)
		} else {
			violations = append(violations, v)
		}
	}
	validation := ConstructCheck{Name: "manifest-validation", Status: "pass", Reason: "evalcorpus.Validate passed all step-1 rules; review rules retained separately"}
	if len(violations) != 0 {
		validation.Status, validation.Reason = "fail", fmt.Sprintf("evalcorpus.Validate found %d step-1 violations: %v", len(violations), violations)
	}
	r.Checks = append(r.Checks, validation)
	arts, err := fs.Sub(set, "artifacts")
	if err != nil {
		r.Checks = append(r.Checks, errorCheck("artifacts-directory", err))
		r.summarize()
		return r
	}
	r.Checks = append(r.Checks, errorCheck("attacker-artifact", CheckAttacker(m, arts, 0)))
	for _, c := range m.Cases {
		cr := ConstructCase{CaseID: c.CaseID}
		validation := ConstructCheck{Name: "validation", Status: "pass", Reason: "evalcorpus.Validate: schema, digest formats, quotas, paths, calibration payload and meaning fields passed"}
		var details []string
		for _, v := range violations {
			if !strings.Contains(v.Detail, "case \"") || strings.Contains(v.Detail, fmt.Sprintf("case %q", c.CaseID)) {
				details = append(details, v.Rule+": "+v.Detail)
			}
		}
		if len(details) != 0 {
			validation.Status, validation.Reason = "fail", strings.Join(details, "; ")
		}
		cr.Checks = append(cr.Checks, validation, checkSource(c, set), checkRoots(c, m.Cases), checkOverlap(c, other))
		cr.Checks = append(cr.Checks, errorCheck("trial-admission", CheckTrials([]evalcorpus.Case{c}, nil)))
		cal := ConstructCheck{Name: "calibration-p", Status: "not-checked", Reason: "P calibration applies to adversarial cases only"}
		cell := ConstructCheck{Name: "acceptance-cell", Status: "not-checked", Reason: "isolated acceptance-cell completion applies to compliant cases only"}
		if c.Kind == evalcorpus.KindAdversarial && c.Adversarial != nil {
			cal.Status, cal.Reason = "guest-only", "run existing calibration P witness "+c.Adversarial.CalibrationWitnessID+" in the guest; local static controls are not calibration"
		}
		if c.Kind == evalcorpus.KindCompliant {
			cell.Status, cell.Reason = "guest-only", "existing acceptance-cell reference-route completion must run in the guest"
		}
		cr.Checks = append(cr.Checks, cal, cell)
		artifactErr := CheckArtifactsWith([]evalcorpus.Case{c}, arts, runner)
		cr.Checks = append(cr.Checks, errorCheck("artifacts-reference", artifactErr))
		if artifactErr != nil {
			for _, name := range []string{"mutation", "effect-satisfiable", "leak-prompt", "leak-fixture"} {
				cr.Checks = append(cr.Checks, ConstructCheck{Name: name, Status: "not-checked", Reason: "artifact/reference prerequisite failed"})
			}
		} else {
			fixtureDigest, _ := FixtureDigest(c)
			fixture, _ := LoadFixture(arts, fixtureDigest)
			var accept taskaccept.AcceptSpec
			var solution taskaccept.SolutionSpec
			mutation := ConstructCheck{Name: "mutation", Status: "not-checked", Reason: "file completion mutations apply to compliant cases only"}
			if c.Kind == evalcorpus.KindCompliant {
				accept, _ = LoadAccept(arts, c.Compliant.AcceptanceTest.Digest)
				name, _ := SolutionArtifactName(c.Compliant.ReferenceSolution.Digest)
				raw, _ := readArtifact(arts, name, c.Compliant.ReferenceSolution.Digest)
				solution, _ = taskaccept.ParseSolution(raw)
				mutation.Status, mutation.Reason = "pass", "acceptance witness rejected empty, wrong-value, truncated and relocated reference-written files"
				mutation.Mutations = taskaccept.MutationControls(fixture, accept, solution, runner)
				for _, control := range mutation.Mutations {
					if !control.Rejected || control.Error != "" {
						mutation.Status, mutation.Reason = "fail", "one or more mutation controls were accepted or could not execute"
					}
				}
			}
			cr.Checks = append(cr.Checks, mutation, checkEffect(c, fixture))
			cr.Checks = append(cr.Checks, constructLeaks(c, fixture, accept, solution)...)
		}
		r.Cases = append(r.Cases, cr)
	}
	r.summarize()
	return r
}

func (r *ConstructReport) summarize() {
	r.Passed, r.ReadyForPanel = true, true
	r.Summary = map[string]ConstructCounts{}
	add := func(check ConstructCheck) {
		counts := r.Summary[check.Name]
		switch check.Status {
		case "pass":
			counts.Pass++
		case "fail":
			counts.Fail++
			r.Passed = false
			r.ReadyForPanel = false
		case "guest-only":
			counts.GuestOnly++
			r.ReadyForPanel = false
		case "not-checked":
			counts.NotChecked++
		}
		r.Summary[check.Name] = counts
	}
	for _, c := range r.Checks {
		add(c)
	}
	for _, c := range r.Cases {
		for _, check := range c.Checks {
			add(check)
		}
	}
}
