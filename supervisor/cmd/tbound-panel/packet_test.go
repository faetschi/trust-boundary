package main

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
)

var pilotManifest = filepath.Join("..", "..", "..", "corpus", "pilot", "manifest.draft.json")

func TestRenderedPilotPromptsAreBlinded(t *testing.T) {
	corpus, err := loadCorpus(pilotManifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(corpus.Manifest.Cases) != 33 {
		t.Fatalf("pilot has %d cases", len(corpus.Manifest.Cases))
	}
	models := []string{"google/gemini-2.5-pro", "qwen/qwen3-235b-a22b-2507", "mistralai/mistral-large-2411"}
	plan, err := blind(corpus, models, 42)
	if err != nil {
		t.Fatal(err)
	}
	caseIDs := regexp.MustCompile(`PILOT-|ADV-|CMP-|AMB-|WIT-`)
	rootLeak := regexp.MustCompile(`(?i)\b(cold|non_cold|variance_subset|selection_reason|allocation)\b`)
	seen := map[string]bool{}
	for _, phase := range phases {
		for _, m := range models {
			for _, c := range corpus.Manifest.Cases {
				id := plan.ids[raterKey(phase, m)][c.CaseID]
				if !regexp.MustCompile(`^item-[0-9a-f]{10}$`).MatchString(id) || seen[id] {
					t.Fatalf("blind id %q not random-looking or reused", id)
				}
				seen[id] = true
				p, err := renderCase(corpus, c, id, phase)
				if err != nil {
					t.Fatalf("%s: %v", c.CaseID, err)
				}
				text := systemPrompts[phase] + "\n" + p.User
				for label, re := range forbidden {
					if loc := re.FindStringIndex(text); loc != nil {
						t.Fatalf("%s for %s: %s leaks %q", c.CaseID, m, label, text[loc[0]:loc[1]])
					}
				}
				if caseIDs.MatchString(text) || strings.Contains(text, c.CaseID) || rootLeak.MatchString(text) {
					t.Fatalf("%s: prompt leaks the case id or stratum", c.CaseID)
				}
				if !strings.Contains(p.User, `"item_id": "`+id+`"`) {
					t.Fatalf("%s: prompt does not carry its blind id", c.CaseID)
				}
				if phase == phaseBlind {
					assertNoClaims(t, c, p.User)
				} else if c.Kind == "ambiguous" && (len(p.Items) != 5 || strings.Contains(p.User, "Note for the run")) {
					t.Fatalf("%s: ambiguous packet items %v or rubric run note", c.CaseID, p.Items)
				}
			}
		}
	}
	k := func(m string) string { return plan.order[raterKey(phaseChecklist, m)][0] }
	if k(models[0]) == k(models[1]) && k(models[1]) == k(models[2]) {
		t.Fatal("per-rater orders are not shuffled independently")
	}
}

// assertNoClaims checks that a phase-1 prompt (before its answer shape) names no claimed
// label, oracle, route, intent or source of the case.
func assertNoClaims(t *testing.T, c evalcorpus.Case, user string) {
	t.Helper()
	body, _, _ := strings.Cut(user, "## Answer shape")
	claims := []string{"Claimed", "claimed", "EFF-", "TM-", "oracle", "Expected route", "adversarial", "compliant", "ambiguous", "rubric", "Step 1"}
	if mr := c.MeaningRecord; mr != nil {
		claims = append(claims, mr.AuthorizationClass, mr.PreservedIntent, mr.ToolMapping, mr.OracleMapping, mr.BenchmarkKey, mr.SourceItemID, mr.TaskUnit)
	}
	switch {
	case c.Adversarial != nil:
		a := c.Adversarial
		claims = append(claims, a.OracleID, a.OracleVersion, a.Adaptation.EndpointOracleMapping, a.Adaptation.ToolMapping)
		for _, turn := range c.Trial.Script {
			claims = append(claims, compactJSON(turn))
		}
	case c.Compliant != nil:
		a := c.Compliant
		claims = append(claims, a.TaskGoal, a.ToolMapping, a.OracleID, a.OracleVersion, "Primary tool")
	}
	for _, s := range claims {
		if s != "" && strings.Contains(body, s) {
			t.Fatalf("%s: phase-1 prompt contains the claim %q", c.CaseID, s)
		}
	}
}

func TestRenderRedactsAndFailsClosed(t *testing.T) {
	corpus, err := loadCorpus(pilotManifest)
	if err != nil {
		t.Fatal(err)
	}
	c := corpus.Manifest.Cases[0]
	mr := *c.MeaningRecord
	mr.Transformations = append([]string{"Denied under Configuration C in the pilot."}, mr.Transformations...)
	c.MeaningRecord = &mr
	p, err := renderCase(corpus, c, "item-0000000000", phaseChecklist)
	if err != nil || strings.Contains(p.User, "Configuration C") || !strings.Contains(p.User, redactedLine) {
		t.Fatalf("free text with a configuration name was not redacted: %v", err)
	}
	tr := *c.Trial
	tr.Prompt = "the gate said DENY earlier"
	c.Trial = &tr
	for _, phase := range phases {
		if _, err := renderCase(corpus, c, "item-0000000000", phase); err == nil {
			t.Fatalf("phase %d: verbatim stimulus with a verdict token rendered instead of failing closed", phase)
		}
	}
}
