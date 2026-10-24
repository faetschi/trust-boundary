package main

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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
	for _, m := range models {
		for _, c := range corpus.Manifest.Cases {
			id := plan.ids[m][c.CaseID]
			if !regexp.MustCompile(`^item-[0-9a-f]{10}$`).MatchString(id) || seen[id] {
				t.Fatalf("blind id %q not random-looking or reused", id)
			}
			seen[id] = true
			p, err := renderCase(corpus, c, id)
			if err != nil {
				t.Fatalf("%s: %v", c.CaseID, err)
			}
			text := systemPrompt + "\n" + p.User
			for label, re := range forbidden {
				if loc := re.FindStringIndex(text); loc != nil {
					t.Fatalf("%s for %s: %s leaks %q", c.CaseID, m, label, text[loc[0]:loc[1]])
				}
			}
			if caseIDs.MatchString(text) || strings.Contains(text, c.CaseID) || rootLeak.MatchString(text) {
				t.Fatalf("%s: prompt leaks the case id or stratum", c.CaseID)
			}
			if !strings.Contains(p.User, id) || !strings.Contains(p.User, `"item_id": "`+id+`"`) {
				t.Fatalf("%s: prompt does not carry its blind id", c.CaseID)
			}
			if c.Kind == "ambiguous" && (len(p.Items) != 5 || strings.Contains(p.User, "Note for the run")) {
				t.Fatalf("%s: ambiguous packet items %v or rubric run note", c.CaseID, p.Items)
			}
		}
	}
	if plan.order[models[0]][0] == plan.order[models[1]][0] && plan.order[models[1]][0] == plan.order[models[2]][0] {
		t.Fatal("per-rater orders are not shuffled independently")
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
	p, err := renderCase(corpus, c, "item-0000000000")
	if err != nil || strings.Contains(p.User, "Configuration C") || !strings.Contains(p.User, redactedLine) {
		t.Fatalf("free text with a configuration name was not redacted: %v", err)
	}
	tr := *c.Trial
	tr.Prompt = "the gate said DENY earlier"
	c.Trial = &tr
	if _, err := renderCase(corpus, c, "item-0000000000"); err == nil {
		t.Fatal("verbatim stimulus with a verdict token rendered instead of failing closed")
	}
}
