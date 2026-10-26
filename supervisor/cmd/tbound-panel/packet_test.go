package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/panelaudit"
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
	corpus.Policy = panelaudit.PublicPolicy{BashOutputPaths: []string{"out/"}, NetworkDestinations: []string{"127.0.0.9"}}
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
				if !strings.Contains(p.User, "## Public policy context") || !strings.Contains(p.User, "Allowed network destinations: `127.0.0.9`") ||
					!strings.Contains(p.User, "Bash output paths: `out/`") {
					t.Fatalf("%s phase %d: public policy context missing", c.CaseID, phase)
				}
				if c.Trial.ProtectedPaths == nil && !strings.Contains(p.User, "Registered protected paths: none") {
					t.Fatalf("%s: missing the empty protected-path registration", c.CaseID)
				}
				for _, pp := range c.Trial.ProtectedPaths {
					if !strings.Contains(p.User, "`"+pp+"`") {
						t.Fatalf("%s phase %d: protected path %s not shown", c.CaseID, phase, pp)
					}
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

// The advisor's sign-offs are written into the manifest after the panel has rated it, so the
// panel records the digest of the content it rated and not only that of the whole file.
func TestLoadCorpusContentDigestIgnoresSignoffs(t *testing.T) {
	draft, err := loadCorpus(pilotManifest)
	if err != nil {
		t.Fatal(err)
	}
	m := draft.Manifest
	m.Cases = append([]evalcorpus.Case(nil), m.Cases...)
	m.Cases[0].Signoff = evalcorpus.Signoff{Reviewer: "advisor", Human: true, Decision: "accept", Audited: true, Resolution: "r"}
	m.ConstructReview.PanelAuditDigest = "sha256:" + strings.Repeat("a", 64)
	raw, err := evalcorpus.CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "signed.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	signed, err := loadCorpus(path)
	if err != nil {
		t.Fatal(err)
	}
	if signed.ContentDigest == "" || signed.ContentDigest != draft.ContentDigest {
		t.Errorf("content digest %q after the sign-offs; want %q", signed.ContentDigest, draft.ContentDigest)
	}
	if signed.Digest == draft.Digest {
		t.Error("the whole-manifest digest ignored the sign-offs")
	}
}

// The panel shows fixture files to the raters, so it loads them through the digest-checked
// loader: an artifact edited after the manifest was drafted must be refused, not rendered.
func TestRenderRefusesAFixtureThatDoesNotMatchItsDigest(t *testing.T) {
	corpus, err := loadCorpus(pilotManifest)
	if err != nil {
		t.Fatal(err)
	}
	c := corpus.Manifest.Cases[0]
	digest, _ := corpusadmit.FixtureDigest(c)
	name, ok := corpusadmit.FixtureArtifactName(digest)
	if !ok {
		t.Fatalf("case %s has no fixture digest", c.CaseID)
	}
	raw, err := os.ReadFile(filepath.Join(corpus.artifacts, name))
	if err != nil {
		t.Fatal(err)
	}
	arts := t.TempDir()
	corpus.artifacts = arts
	if err := os.WriteFile(filepath.Join(arts, name), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := renderCase(corpus, c, "item-0000000000", phaseBlind); err != nil {
		t.Fatalf("the unedited fixture was refused: %v", err)
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	edited := false
	for _, e := range spec["entries"].([]any) {
		if entry := e.(map[string]any); entry["type"] == "file" && !edited {
			entry["content_b64"] = base64.StdEncoding.EncodeToString([]byte("content added after the digest was computed"))
			edited = true
		}
	}
	if !edited {
		t.Fatal("fixture has no file entry to edit")
	}
	raw, _ = json.Marshal(spec)
	if err := os.WriteFile(filepath.Join(arts, name), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, phase := range phases {
		if _, err := renderCase(corpus, c, "item-0000000000", phase); err == nil || !strings.Contains(err.Error(), "digest") {
			t.Fatalf("phase %d: a fixture that no longer matches its digest was rendered: %v", phase, err)
		}
	}
}
