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
	// Provenance and Anchor are set only on a guest check that an import turned into pass or fail: the
	// files the result was recomputed from, and what vouches for them.
	Provenance []ConstructSource `json:"provenance,omitempty"`
	Anchor     string            `json:"anchor,omitempty"`
}

// ConstructSource is one source file of an imported result, by the name it was read under and its sha256.
type ConstructSource struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// ConstructAnchor is the Anchor of every imported result. The originals are files the operator retained:
// the audit chains have no external anchor, so an operator who fabricates consistent originals is not
// detected (a recorded limitation of the thesis).
const ConstructAnchor = "operator-retained, no external anchor"

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
	Summary        map[string]ConstructCounts `json:"summary"`
	Checks         []ConstructCheck           `json:"checks"`
	Cases          []ConstructCase            `json:"cases"`
	DeferredReview []evalcorpus.Violation     `json:"deferred_review,omitempty"`
}

func errorCheck(name string, err error, unchecked ...string) ConstructCheck {
	r := ConstructCheck{Name: name, Status: "pass", Reason: "existing admission check passed"}
	for _, gap := range unchecked {
		r.Reason += "; " + gap + " not checked"
	}
	if err != nil {
		r.Status, r.Reason = "fail", err.Error()
	}
	return r
}

// CheckConstructs composes production validation, admission and self-checks.
// Guest-only means required evidence is still pending, never a local pass.
func CheckConstructs(m evalcorpus.Manifest, set fs.FS, quotas evalcorpus.Quotas, other *evalcorpus.Manifest, runner taskaccept.NodeRunner) ConstructReport {
	r := ConstructReport{Schema: "tbound-construct-checks/v1", Passed: true, Summary: map[string]ConstructCounts{}}
	digest, err := evalcorpus.Digest(m)
	r.ManifestDigest = digest
	r.Checks = append(r.Checks, errorCheck("manifest-digest", err))
	violations := evalcorpus.Validate(m, quotas)
	validation := ConstructCheck{Name: "manifest-validation", Status: "pass", Reason: "evalcorpus.Validate passed all deterministic manifest rules"}
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
	r.Checks = append(r.Checks, errorCheck("attacker-artifact", CheckAttacker(m, arts, 0), "pre-effect retries"))
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
		cr.Checks = append(cr.Checks, errorCheck("trial-admission", CheckTrials([]evalcorpus.Case{c}, nil), "budget ceilings"))
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
	r.Passed = true
	r.Summary = map[string]ConstructCounts{}
	add := func(check ConstructCheck) {
		counts := r.Summary[check.Name]
		switch check.Status {
		case "pass":
			counts.Pass++
		case "fail":
			counts.Fail++
			r.Passed = false
		case "guest-only":
			counts.GuestOnly++
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

// ResolveGuestChecks imports evidence only for pending guest checks, then
// recomputes the report's counts and failure status from the resulting checks.
func (r *ConstructReport) ResolveGuestChecks(resolve func(string, ConstructCheck) ConstructCheck) {
	for i := range r.Cases {
		for j, check := range r.Cases[i].Checks {
			if check.Status == "guest-only" {
				r.Cases[i].Checks[j] = resolve(r.Cases[i].CaseID, check)
			}
		}
	}
	r.summarize()
}
