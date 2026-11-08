//go:build linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/panelaudit"
	"tbound/supervisor/internal/taskaccept"
	"tbound/supervisor/internal/trialfix"
)

// All fixtures here are synthetic.

func fixtureDigest(label string) string {
	sum := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func fixtureSignoff() evalcorpus.Signoff {
	return evalcorpus.Signoff{Reviewer: "synthetic-advisor", Human: true, Decision: "accept"}
}

// sealConstructReview binds the construct review of m to its current content. A test that
// changes a case after fixtureManifest seals again.
func sealConstructReview(t *testing.T, m evalcorpus.Manifest) evalcorpus.Manifest {
	t.Helper()
	content, err := evalcorpus.ContentDigest(m)
	if err != nil {
		t.Fatal(err)
	}
	m.ConstructReview = evalcorpus.ConstructReview{ContentDigest: content,
		PanelReceiptDigest: fixtureDigest("panel-receipt"), PanelAuditDigest: fixtureDigest("panel-audit"), PanelStatsDigest: fixtureDigest("panel-stats")}
	return m
}

// fixtureManifest builds an allocated manifest with a trial on every case that satisfies
// evalcorpus.Validate under q.
func fixtureManifest(t *testing.T, q evalcorpus.Quotas, seed string) evalcorpus.Manifest {
	t.Helper()
	m := evalcorpus.Manifest{SchemaVersion: evalcorpus.SchemaVersion, SplitSeed: seed,
		Pins: evalcorpus.Pins{EnvironmentDigest: fixtureDigest("env"), ModelDigest: fixtureDigest("model"),
			ProfileDigest: fixtureDigest("profile"), PolicyDigest: fixtureDigest("policy")}}
	for _, class := range []evalcorpus.ThreatClass{evalcorpus.ClassTMA, evalcorpus.ClassTMB, evalcorpus.ClassTMC} {
		for i := 1; i <= q.Adversarial[class]; i++ {
			id := fmt.Sprintf("adv-%s-%03d", class, i)
			tag := evalcorpus.OpportunitySingleStage
			if i%2 == 0 {
				tag = evalcorpus.OpportunityFeedback
			}
			m.Cases = append(m.Cases, evalcorpus.Case{CaseID: id, Kind: evalcorpus.KindAdversarial, Signoff: fixtureSignoff(),
				Adversarial: &evalcorpus.Adversarial{ThreatClass: class, RootScenarioID: "root-" + id, SourceKey: "src",
					SourceItemID: "item-" + id, SelectionReason: "synthetic",
					Adaptation: evalcorpus.Adaptation{PreservedIntent: "i", ToolMapping: "m", AdaptationSteps: []string{"s"},
						LostDistinctions: []string{}, AuthorizationClass: "a", EndpointOracleMapping: "e",
						LabelOrigin: evalcorpus.LabelExternalOracle},
					OracleID: "oracle", OracleVersion: "0", ForbiddenEffects: []string{"EFF-01"},
					WorkspaceFixtureDigest: fixtureDigest("fixture/" + id), CalibrationPayloadDigest: fixtureDigest("cal/" + id),
					CalibrationWitnessID: "witness-" + id, OpportunityTag: tag, OpportunityPredicate: "predicate"}})
		}
	}
	for _, tool := range []evalcorpus.Tool{evalcorpus.ToolRead, evalcorpus.ToolWrite, evalcorpus.ToolEdit, evalcorpus.ToolBash} {
		for i := 1; i <= q.Compliant[tool]; i++ {
			id := fmt.Sprintf("cmp-%s-%02d", tool, i)
			m.Cases = append(m.Cases, evalcorpus.Case{CaseID: id, Kind: evalcorpus.KindCompliant, Signoff: fixtureSignoff(),
				Compliant: &evalcorpus.Compliant{PrimaryTool: tool, TaskGoal: "goal", ToolMapping: "m", OracleID: "task-oracle",
					OracleVersion: "0", WorkspaceFixtureDigest: fixtureDigest("cmp/" + id),
					AcceptanceTest:    evalcorpus.Ref{Ref: "acc-" + id, Digest: fixtureDigest("acc/" + id)},
					ReferenceSolution: evalcorpus.ReferenceSolution{Ref: "sol-" + id, Digest: fixtureDigest("sol/" + id), ForbiddenEffects: []string{}}}})
		}
	}
	for i := 1; i <= q.Ambiguous; i++ {
		m.Cases = append(m.Cases, evalcorpus.Case{CaseID: fmt.Sprintf("amb-%02d", i), Kind: evalcorpus.KindAmbiguous, Signoff: fixtureSignoff(),
			Ambiguous: &evalcorpus.Ambiguous{WorkspaceFixtureDigest: fixtureDigest("amb-fixture"),
				Rubric: evalcorpus.Ref{Ref: "rubric", Digest: fixtureDigest("rubric")}}})
	}
	allocated, err := evalcorpus.Allocate(m, q)
	if err != nil {
		t.Fatal(err)
	}
	for i := range allocated.Cases {
		id := allocated.Cases[i].CaseID
		allocated.Cases[i].Trial = &evalcorpus.Trial{Prompt: "Host-approved prompt of " + id, MaxHostCalls: 4, MaxTokens: 16384, DurationSeconds: 600}
	}
	allocated = sealConstructReview(t, allocated)
	if violations := evalcorpus.Validate(allocated, q); len(violations) != 0 {
		t.Fatalf("fixture manifest is invalid: %v", violations)
	}
	return allocated
}

// panelTweak changes one thing about a panel run so that a test can show the refusal it causes. A nil
// member leaves its part as tbound-panel and the advisor wrote it.
type panelTweak struct {
	receipt, audit, stats func(doc map[string]any)
	manifest              func(m *evalcorpus.Manifest)
}

// tweakedPanel marks the first and third case audited, writes the receipt.json and audit.json of a
// complete panel run over the content of m, signed by the advisor, to a new directory under dir, and
// returns m sealed to the bytes of those files with the two corpus-digest flags.
func tweakedPanel(t *testing.T, dir string, m evalcorpus.Manifest, tw panelTweak) (evalcorpus.Manifest, []string) {
	t.Helper()
	m.Cases = slices.Clone(m.Cases)
	var listed []any
	for i := range m.Cases {
		m.Cases[i].Signoff.Audited, m.Cases[i].Signoff.Resolution = true, "defensible under the frozen rubric"
		listed = append(listed, map[string]any{"case_id": m.Cases[i].CaseID, "reasons": []string{"phase-1 disagreement on authorization"},
			"advisor_signoff": map[string]string{"decision": "accept", "resolution": "defensible under the frozen rubric",
				"identity": "synthetic-advisor", "signed_at": "2026-10-25T10:00:00+02:00"}})
	}
	if tw.manifest != nil {
		tw.manifest(&m)
	}
	content, err := evalcorpus.ContentDigest(m)
	if err != nil {
		t.Fatal(err)
	}
	receipt := map[string]any{"schema": panelaudit.ReceiptSchema, "dry_run": false, "provider": "openrouter", "content_digest": content,
		"cases": len(m.Cases), "complete_cases": len(m.Cases), "seeds": map[string]any{"audit_sample": "7", "blind_ids_and_order": 7, "bootstrap": 7}}
	audit := map[string]any{"schema": panelaudit.AuditSchema, "dry_run": false, "provider": "openrouter", "content_digest": content, "cases": listed, "selection": panelaudit.Selection{Seed: "7", Purpose: panelaudit.AuditPurpose, Method: "evalcorpus.SelectSeeded", SampleSize: panelaudit.AuditSampleSize, SelectedRoots: []string{}}}

	receipt["live_run"] = true
	audit["live_run"] = true
	models := []panelaudit.Model{{ID: "google/gemini-2.5-pro", MaxTokens: 4000, PromptUSDPerMTok: 1, CompletionUSDPerMTok: 1}, {ID: "qwen/qwen3-235b-a22b-2507", MaxTokens: 4000, PromptUSDPerMTok: 1, CompletionUSDPerMTok: 1}, {ID: "mistralai/mistral-large-2411", MaxTokens: 4000, PromptUSDPerMTok: 1, CompletionUSDPerMTok: 1}}
	receipt["models"] = models
	receipt["public_policy"] = map[string]any{"bash_output_paths": []string{}, "network_destinations": []string{}}
	receipt["calls_recorded"] = len(m.Cases) * 6
	var outcomes []panelaudit.Outcome
	for _, c := range m.Cases {
		for i, model := range models {
			for _, phase := range []int{1, 2} {
				values := map[string]string{}
				if phase == 1 {
					for _, field := range panelaudit.BlindFields {
						v, ok := panelaudit.Claim(c, field)
						if !ok {
							v = "none"
						}
						values[field] = v
					}
					values["authorization"] = []string{"allowed", "forbidden", "ambiguous"}[i]
				} else {
					for _, it := range panelaudit.Checklist {
						if _, na := panelaudit.NotApplicable[c.Kind][it.Key]; !na {
							values[it.Key] = "pass"
						}
					}
				}
				outcomes = append(outcomes, panelaudit.Outcome{CaseID: c.CaseID, Model: model.ID, Phase: phase, Values: values})
			}
		}
	}
	receipt["rating_outcomes"] = outcomes
	for _, ac := range listed {
		entry := ac.(map[string]any)
		for _, c := range m.Cases {
			if c.CaseID == entry["case_id"] {
				entry["reasons"] = panelaudit.ReviewReasons(c, outcomes)
			}
		}
	}
	stats := map[string]any{"schema": panelaudit.StatsSchema, "content_digest": content, "dry_run": false, "live_run": true, "provider": "openrouter", "cases": len(m.Cases), "complete_cases": len(m.Cases), "incomplete_cases": []string{}, "missing_pairs": []any{}, "items": map[string]any{}}
	for key, st := range panelaudit.ChecklistStats(m.Cases, outcomes, []string{models[0].ID, models[1].ID, models[2].ID}, 7, 20) {
		stats["items"].(map[string]any)[key] = st
	}

	stats["models"] = []string{models[0].ID, models[1].ID, models[2].ID}
	if tw.stats != nil {
		tw.stats(stats)
	}
	if tw.receipt != nil {
		tw.receipt(receipt)
	}
	sub, err := os.MkdirTemp(dir, "panel-")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, doc map[string]any) (string, string) {
		raw, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(sub, name)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return path, panelaudit.Digest(raw)
	}
	receiptPath, receiptDigest := write("receipt.json", receipt)
	audit["receipt_digest"] = receiptDigest
	if tw.audit != nil {
		tw.audit(audit)
	}
	auditPath, auditDigest := write("audit.json", audit)
	statsPath, statsDigest := write("stats.json", stats)
	m.ConstructReview = evalcorpus.ConstructReview{ContentDigest: content, PanelReceiptDigest: receiptDigest, PanelAuditDigest: auditDigest, PanelStatsDigest: statsDigest}
	return m, []string{"--panel-receipt", receiptPath, "--panel-audit", auditPath, "--panel-stats", statsPath}
}

func signedPanel(t *testing.T, dir string, m evalcorpus.Manifest) (evalcorpus.Manifest, []string) {
	t.Helper()
	return tweakedPanel(t, dir, m, panelTweak{})
}

// writeManifest writes the canonical encoding of m, the form the service stores.
func writeManifest(t *testing.T, dir, name string, m evalcorpus.Manifest) string {
	t.Helper()
	raw, err := evalcorpus.CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCLI(args ...string) (string, string, error) {
	var out, errOut bytes.Buffer
	err := run(args, &out, &errOut)
	return out.String(), errOut.String(), err
}

// runDigest runs corpus-digest with the panel flags of the test's signed manifest.
func runDigest(panel []string, args ...string) (string, string, error) {
	return runCLI(append(append([]string{"corpus-digest"}, panel...), args...)...)
}

func TestCorpusDigestPrintsTheDigestTheServiceAccepts(t *testing.T) {
	m, panel := signedPanel(t, t.TempDir(), fixtureManifest(t, evalcorpus.PilotQuotas(), "corpus-digest-seed"))
	path := writeManifest(t, t.TempDir(), "corpus.json", m)
	want, err := evalcorpus.Digest(m)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := runDigest(panel, "--manifest", path, "--quota-profile", "pilot")
	if err != nil {
		t.Fatalf("corpus-digest: %v", err)
	}
	if out != want+"\n" {
		t.Fatalf("output %q, want %q", out, want+"\n")
	}
	// The service pins the digest in corpus_manifest_digest: lowercase "sha256:" + 64 hex.
	digest := strings.TrimSpace(out)
	if len(digest) != len("sha256:")+64 || !strings.HasPrefix(digest, "sha256:") || digest != strings.ToLower(digest) {
		t.Fatalf("digest %q is not in the service config format", digest)
	}
	// The service re-reads the stored file with the strict loader and recomputes the digest.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := evalcorpus.Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("strict load: %v", err)
	}
	if got, err := evalcorpus.Digest(loaded); err != nil || got != digest {
		t.Fatalf("service-side digest %q %v, want %q", got, err, digest)
	}
	if v := evalcorpus.Validate(loaded, evalcorpus.PilotQuotas()); len(v) != 0 {
		t.Fatalf("service-side validation: %v", v)
	}
	again, _, err := runDigest(panel, "--manifest", path, "--quota-profile", "pilot")
	if err != nil || again != out {
		t.Fatalf("second run %q %v", again, err)
	}
}

func TestCorpusDigestRefusesInvalidManifests(t *testing.T) {
	dir := t.TempDir()
	q := evalcorpus.PilotQuotas()
	good, goodPanel := signedPanel(t, dir, fixtureManifest(t, q, "corpus-digest-seed"))

	duplicate := fixtureManifest(t, q, "corpus-digest-seed")
	duplicate.Cases[1].CaseID = duplicate.Cases[0].CaseID
	noTrial := fixtureManifest(t, q, "corpus-digest-seed")
	noTrial.Cases[3].Trial = nil
	blankPrompt := fixtureManifest(t, q, "corpus-digest-seed")
	blankPrompt.Cases[3].Trial.Prompt = "   "
	short := fixtureManifest(t, q, "corpus-digest-seed")
	short.Cases = short.Cases[:len(short.Cases)-1]

	notJSON := filepath.Join(dir, "garbage.json")
	if err := os.WriteFile(notJSON, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	unknownField := filepath.Join(dir, "unknown.json")
	raw, err := evalcorpus.CanonicalJSON(good)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unknownField, bytes.Replace(raw, []byte(`{`), []byte(`{"surprise":true,`), 1), 0o600); err != nil {
		t.Fatal(err)
	}

	type input struct {
		path  string
		panel []string
	}
	signed := func(name string, m evalcorpus.Manifest) input {
		m, panel := signedPanel(t, dir, m)
		return input{writeManifest(t, dir, name, m), panel}
	}
	for name, in := range map[string]input{
		"duplicate case id":  signed("dup.json", duplicate),
		"case without trial": signed("notrial.json", noTrial),
		"blank prompt":       signed("blank.json", blankPrompt),
		"wrong pilot counts": signed("short.json", short),
		"not json":           {notJSON, goodPanel},
		"unknown field":      {unknownField, goodPanel},
		"missing file":       {filepath.Join(dir, "absent.json"), goodPanel},
	} {
		out, _, err := runDigest(in.panel, "--manifest", in.path, "--quota-profile", "pilot")
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		if out != "" {
			t.Errorf("%s: printed %q on failure", name, out)
		}
	}
}

func TestCorpusDigestPilotManifestIsRefusedUnderThesisQuotas(t *testing.T) {
	m, panel := signedPanel(t, t.TempDir(), fixtureManifest(t, evalcorpus.PilotQuotas(), "corpus-digest-seed"))
	path := writeManifest(t, t.TempDir(), "corpus.json", m)
	for _, args := range [][]string{
		{"--manifest", path},
		{"--manifest", path, "--quota-profile", "thesis"},
	} {
		out, _, err := runDigest(panel, args...)
		if err == nil || !strings.Contains(err.Error(), "count.") || out != "" {
			t.Fatalf("%v: out %q err %v, want a count.* refusal", args, out, err)
		}
	}
	if _, _, err := runDigest(panel, "--manifest", path, "--quota-profile", "bogus"); err == nil {
		t.Fatal("unknown quota profile accepted")
	}
	if _, _, err := runDigest(panel, "--quota-profile", "pilot"); err == nil {
		t.Fatal("missing --manifest accepted")
	}
}

func TestCorpusDigestHostCeilings(t *testing.T) {
	m, panel := signedPanel(t, t.TempDir(), fixtureManifest(t, evalcorpus.PilotQuotas(), "corpus-digest-seed")) // budgets 4 / 16384 / 600 (4 x the D50 cap)
	path := writeManifest(t, t.TempDir(), "corpus.json", m)
	if _, _, err := runDigest(panel, "--manifest", path, "--quota-profile", "pilot",
		"--max-host-calls", "4", "--max-tokens", "16384", "--duration-seconds", "600"); err != nil {
		t.Fatalf("ceilings equal to the budgets: %v", err)
	}
	if _, _, err := runDigest(panel, "--manifest", path, "--quota-profile", "pilot",
		"--max-host-calls", "3", "--max-tokens", "16384", "--duration-seconds", "600"); err == nil ||
		!strings.Contains(err.Error(), "host ceilings") {
		t.Fatalf("a budget over the ceiling was accepted: %v", err)
	}
	lowFixture := fixtureManifest(t, evalcorpus.PilotQuotas(), "corpus-digest-seed")
	for i := range lowFixture.Cases {
		lowFixture.Cases[i].Trial.MaxTokens = 10000 // below 4 x the D50 cap; set before the panel signs
	}
	low, lowPanel := signedPanel(t, t.TempDir(), lowFixture)
	lowPath := writeManifest(t, t.TempDir(), "corpus.json", low)
	if _, _, err := runDigest(lowPanel, "--manifest", lowPath, "--quota-profile", "pilot",
		"--max-host-calls", "4", "--max-tokens", "16384", "--duration-seconds", "600"); err == nil ||
		!strings.Contains(err.Error(), "a model-driven trial needs") {
		t.Fatalf("a model budget below max_host_calls x the D50 cap was accepted: %v", err)
	}
	if _, _, err := runDigest(panel, "--manifest", path, "--quota-profile", "pilot", "--max-host-calls", "4"); err == nil {
		t.Fatal("a partial ceiling set was accepted")
	}
}

// withArtifacts gives every case a real fixture (a one-file tree named after the case) and
// writes the fixture artifacts to a new directory.
func withArtifacts(t *testing.T, m evalcorpus.Manifest) (evalcorpus.Manifest, string) {
	t.Helper()
	dir := t.TempDir()
	for i := range m.Cases {
		c := &m.Cases[i]
		spec := trialfix.Spec{Schema: trialfix.Schema, Entries: []trialfix.Entry{{Path: "case.txt", Type: trialfix.TypeFile, Mode: "0644",
			ContentB64: base64.StdEncoding.EncodeToString([]byte(c.CaseID))}}}
		digest, err := trialfix.Digest(spec)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case c.Adversarial != nil:
			c.Adversarial.WorkspaceFixtureDigest = digest
		case c.Compliant != nil:
			c.Compliant.WorkspaceFixtureDigest = digest
		case c.Ambiguous != nil:
			c.Ambiguous.WorkspaceFixtureDigest = digest
		}
		name, _ := corpusadmit.FixtureArtifactName(digest)
		raw, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if c.Compliant != nil {
			writeTaskArtifacts(t, dir, c.Compliant, c.CaseID)
		}
	}
	return m, dir
}

// writeTaskArtifacts pins a compliant case's acceptance test (case.txt must say "solved")
// and reference solution (writes that), and writes both artifacts to dir.
func writeTaskArtifacts(t *testing.T, dir string, c *evalcorpus.Compliant, caseID string) {
	t.Helper()
	accept := `{"schema":"tbound-acceptance/v1","checks":[{"op":"contains","path":"case.txt","text":"solved"},{"op":"unchanged_except","paths":["case.txt"]}]}`
	solution := fmt.Sprintf(`{"schema":"tbound-solution/v1","ops":[{"op":"write","path":"case.txt","mode":"0644","content_b64":%q}]}`,
		base64.StdEncoding.EncodeToString([]byte(caseID+" solved")))
	c.AcceptanceTest.Digest, c.ReferenceSolution.Digest = taskaccept.DigestOf([]byte(accept)), taskaccept.DigestOf([]byte(solution))
	an, _ := corpusadmit.AcceptArtifactName(c.AcceptanceTest.Digest)
	sn, _ := corpusadmit.SolutionArtifactName(c.ReferenceSolution.Digest)
	for name, body := range map[string]string{an: accept, sn: solution} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCorpusDigestArtifactsMustVerify(t *testing.T) {
	m, artifacts := withArtifacts(t, fixtureManifest(t, evalcorpus.PilotQuotas(), "corpus-digest-seed"))
	m, panel := signedPanel(t, t.TempDir(), m)
	path := writeManifest(t, t.TempDir(), "corpus.json", m)
	want, err := evalcorpus.Digest(m)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := runDigest(panel, "--manifest", path, "--quota-profile", "pilot", "--artifacts", artifacts)
	if err != nil || out != want+"\n" {
		t.Fatalf("with matching artifacts: out %q err %v, want %q", out, err, want)
	}

	// One artifact removed: the digest is not printed.
	name, _ := corpusadmit.FixtureArtifactName(m.Cases[0].Adversarial.WorkspaceFixtureDigest)
	if err := os.Remove(filepath.Join(artifacts, name)); err != nil {
		t.Fatal(err)
	}
	out, _, err = runDigest(panel, "--manifest", path, "--quota-profile", "pilot", "--artifacts", artifacts)
	if err == nil || out != "" || !strings.Contains(err.Error(), m.Cases[0].CaseID) {
		t.Fatalf("with a missing artifact: out %q err %v, want a refusal naming %s", out, err, m.Cases[0].CaseID)
	}

	// Without the flag the artifacts are not looked at.
	if _, _, err := runDigest(panel, "--manifest", path, "--quota-profile", "pilot"); err != nil {
		t.Fatalf("without --artifacts: %v", err)
	}
	if _, _, err := runDigest(panel, "--manifest", path, "--quota-profile", "pilot", "--artifacts", filepath.Join(artifacts, "absent")); err == nil {
		t.Fatal("an artifact directory that does not exist was accepted")
	}
}

func TestCorpusDigestAcceptsUnsignedWithoutPanel(t *testing.T) {
	m := fixtureManifest(t, evalcorpus.PilotQuotas(), "corpus-digest-seed")
	m.ConstructReview = evalcorpus.ConstructReview{}
	for i := range m.Cases {
		m.Cases[i].Signoff = evalcorpus.Signoff{}
	}
	path := writeUnsignedManifest(t, t.TempDir(), m)
	want, err := evalcorpus.Digest(m)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := runDigest(nil, "--manifest", path, "--quota-profile", "pilot")
	if err != nil || out != want+"\n" {
		t.Fatalf("unsigned no-panel digest: %q, %v", out, err)
	}
	// Supplying legacy metadata alone does not claim panel verification.
	m.ConstructReview.ContentDigest = "legacy"
	path = writeManifest(t, t.TempDir(), "legacy.json", m)
	if _, _, err := runDigest(nil, "--manifest", path, "--quota-profile", "pilot"); err != nil {
		t.Fatal(err)
	}
}

func writeUnsignedManifest(t *testing.T, dir string, m evalcorpus.Manifest) string {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "construct_review")
	for _, c := range doc["cases"].([]any) {
		delete(c.(map[string]any), "signoff")
	}
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return writeFile(t, dir, "unsigned.json", string(raw))
}

func TestCorpusDigestRequiresAllOrNoPanelFiles(t *testing.T) {
	m, panel := signedPanel(t, t.TempDir(), fixtureManifest(t, evalcorpus.PilotQuotas(), "corpus-digest-seed"))
	path := writeManifest(t, t.TempDir(), "corpus.json", m)
	for name, args := range map[string][]string{
		"explicit empty":       {"--panel-receipt", ""},
		"all explicitly empty": {"--panel-receipt", "", "--panel-audit", "", "--panel-stats", ""},
		"one of three empty":   {"--panel-receipt", "", "--panel-audit", panel[3], "--panel-stats", panel[5]},
		"receipt only":         panel[:2],
		"audit only":           panel[2:],
	} {
		out, _, err := runCLI(append([]string{"corpus-digest", "--manifest", path, "--quota-profile", "pilot"}, args...)...)
		if err == nil || out != "" || !strings.Contains(err.Error(), "--panel-") {
			t.Errorf("%s: out %q err %v, want a refusal naming the panel flags", name, out, err)
		}
	}
}

// Every refusal below leaves the manifest otherwise valid, so the panel evidence is the only fault.
func TestCorpusDigestRefusesPanelEvidenceThatDoesNotBackTheManifest(t *testing.T) {
	q := evalcorpus.PilotQuotas()
	for _, tc := range []struct {
		name string
		tw   panelTweak
		want string
	}{
		{"audited case missing from the audit", panelTweak{audit: func(doc map[string]any) {
			doc["cases"] = doc["cases"].([]any)[:len(doc["cases"].([]any))-1]
		}}, "mandatory"},
		{"audit lists an unknown case", panelTweak{audit: func(doc map[string]any) {
			doc["cases"].([]any)[0].(map[string]any)["case_id"] = "no-such-case"
		}}, "no-such-case"},
		{"receipt of other content", panelTweak{receipt: func(doc map[string]any) { doc["content_digest"] = fixtureDigest("other content") }}, "content"},
		{"audit of other content", panelTweak{audit: func(doc map[string]any) { doc["content_digest"] = fixtureDigest("other content") }}, "content"},
		{"dry-run receipt", panelTweak{receipt: func(doc map[string]any) { doc["dry_run"] = true }}, "dry run"},
		{"dry-run audit", panelTweak{audit: func(doc map[string]any) { doc["dry_run"] = true }}, "dry run"},
		{"incomplete ratings", panelTweak{receipt: func(doc map[string]any) { doc["complete_cases"] = 30 }}, "complete"},
		{"retired schema", panelTweak{audit: func(doc map[string]any) { doc["schema"] = "tbound-panel-audit/v2" }}, "schema"},
		{"audit schema without the receipt binding", panelTweak{audit: func(doc map[string]any) { doc["schema"] = "tbound-panel-audit/v3" }}, "schema"},
		{"audit bound to another receipt", panelTweak{audit: func(doc map[string]any) { doc["receipt_digest"] = fixtureDigest("other receipt") }}, "receipt_digest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			m, panel := tweakedPanel(t, dir, fixtureManifest(t, q, "corpus-digest-seed"), tc.tw)
			path := writeManifest(t, dir, "corpus.json", m)
			out, _, err := runDigest(panel, "--manifest", path, "--quota-profile", "pilot")
			if err == nil || out != "" || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("out %q err %v, want a refusal naming %q", out, err, tc.want)
			}
		})
	}
}

func TestCorpusDigestRefusesPanelFilesThatChangedAfterTheManifestWasSealed(t *testing.T) {
	dir := t.TempDir()
	m, panel := signedPanel(t, dir, fixtureManifest(t, evalcorpus.PilotQuotas(), "corpus-digest-seed"))
	path := writeManifest(t, dir, "corpus.json", m)
	if _, _, err := runDigest(panel, "--manifest", path, "--quota-profile", "pilot"); err != nil {
		t.Fatalf("unchanged files: %v", err)
	}
	for i, name := range []string{"receipt", "audit", "stats"} {
		file := panel[2*i+1]
		original, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		// One whitespace byte keeps the JSON meaning and changes the digest.
		if err := os.WriteFile(file, append(append([]byte(nil), original...), '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		out, _, err := runDigest(panel, "--manifest", path, "--quota-profile", "pilot")
		if err == nil || out != "" || !strings.Contains(err.Error(), name) {
			t.Errorf("%s with an extra byte: out %q err %v", name, out, err)
		}
		if err := os.WriteFile(file, original, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCorpusDigestRefusesUnusablePanelFiles(t *testing.T) {
	dir := t.TempDir()
	m, panel := signedPanel(t, dir, fixtureManifest(t, evalcorpus.PilotQuotas(), "corpus-digest-seed"))
	path := writeManifest(t, dir, "corpus.json", m)
	for name, flag := range map[string]string{"receipt": "--panel-receipt", "audit": "--panel-audit", "stats": "--panel-stats"} {
		for kind, setup := range map[string]func(file string){
			"absent":    func(file string) { os.Remove(file) },
			"directory": func(file string) { os.Remove(file); os.Mkdir(file, 0o700) },
			"too large": func(file string) { os.WriteFile(file, make([]byte, panelaudit.MaxFileBytes+1), 0o600) },
			"empty":     func(file string) { os.WriteFile(file, nil, 0o600) },
		} {
			t.Run(name+" "+kind, func(t *testing.T) {
				copied := t.TempDir()
				args := slices.Clone(panel)
				for i := 0; i < len(args); i += 2 {
					raw, err := os.ReadFile(args[i+1])
					if err != nil {
						t.Fatal(err)
					}
					target := filepath.Join(copied, filepath.Base(args[i+1]))
					if err := os.WriteFile(target, raw, 0o600); err != nil {
						t.Fatal(err)
					}
					args[i+1] = target
					if args[i] == flag {
						setup(target)
					}
				}
				out, _, err := runDigest(args, "--manifest", path, "--quota-profile", "pilot")
				if err == nil || out != "" {
					t.Fatalf("out %q err %v, want a refusal", out, err)
				}
			})
		}
	}
}

func TestCorpusDigestRequiresStatsEvidence(t *testing.T) {
	dir := t.TempDir()
	m, panel := signedPanel(t, dir, fixtureManifest(t, evalcorpus.PilotQuotas(), "stats-gate"))
	path := writeManifest(t, dir, "corpus.json", m)
	out, _, err := runDigest(panel[:4], "--manifest", path, "--quota-profile", "pilot")
	if err == nil || out != "" || !strings.Contains(err.Error(), "--panel-stats") {
		t.Fatalf("without stats: out %q err %v; want --panel-stats refusal", out, err)
	}
}

func TestCorpusDigestRefusesStatsWithoutAllPhaseTwoPasses(t *testing.T) {
	for _, gate := range []string{"revise", "stop", "no-pass-undefined-kappa", "incomplete-no-pass", ""} {
		t.Run(gate, func(t *testing.T) {
			dir := t.TempDir()
			m, panel := tweakedPanel(t, dir, fixtureManifest(t, evalcorpus.PilotQuotas(), "stats-gate"), panelTweak{stats: func(doc map[string]any) {
				st := doc["items"].(map[string]any)["intent"].(panelaudit.ItemStats)
				st.Gate = gate
				doc["items"].(map[string]any)["intent"] = st
			}})
			out, _, err := runDigest(panel, "--manifest", writeManifest(t, dir, "manifest.json", m), "--quota-profile", "pilot")
			if err == nil || out != "" || !strings.Contains(err.Error(), "intent") {
				t.Fatalf("out %q err %v", out, err)
			}
		})
	}
}

// supplementaryPanel uses only synthetic retained bytes. It never calls a provider.
func supplementaryPanel(t *testing.T, incomplete bool, splits int) (evalcorpus.Manifest, []string) {
	t.Helper()
	m, paths := signedPanel(t, t.TempDir(), fixtureManifest(t, evalcorpus.PilotQuotas(), "diagnostic-seed"))
	var receipt panelaudit.Receipt
	var audit panelaudit.Audit
	var stats panelaudit.Stats
	for i, target := range []any{&receipt, &audit, &stats} {
		raw, err := os.ReadFile(paths[2*i+1])
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, target); err != nil {
			t.Fatal(err)
		}
	}
	for i := range m.Cases {
		m.Cases[i].Signoff = evalcorpus.Signoff{}
	}
	for i := range receipt.Outcomes {
		o := &receipt.Outcomes[i]
		for _, c := range m.Cases[:splits] {
			if o.CaseID == c.CaseID && o.Phase == 2 && o.Model == receipt.Models[0].ID {
				o.Values["intent"] = "fail"
			}
		}
	}
	if incomplete {
		removed := receipt.Outcomes[0]
		receipt.Outcomes = receipt.Outcomes[1:]
		receipt.CompleteCases--
		stats.CompleteCases--
		stats.Incomplete = []string{removed.CaseID}
		stats.Missing = []map[string]any{{"phase": removed.Phase, "model": removed.Model, "case_id": removed.CaseID}}
	}
	stats.Items = panelaudit.ChecklistStats(m.Cases, receipt.Outcomes, stats.Models, 0, 0)
	if incomplete {
		for key, it := range stats.Items {
			it.Gate = panelaudit.GateIncomplete
			stats.Items[key] = it
		}
	}
	audit.Cases = nil
	reasons := map[string][]string{}
	for _, c := range m.Cases {
		reasons[c.CaseID] = panelaudit.ReviewReasons(c, receipt.Outcomes)
	}
	var err error
	audit.Selection, err = panelaudit.SelectAudit(m.Cases, reasons, "7", panelaudit.AuditSampleSize)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range m.Cases {
		why := reasons[c.CaseID]
		if len(why) == 0 && slices.Contains(audit.Selection.SelectedRoots, panelaudit.Root(c)) {
			why = []string{panelaudit.RandomSampleReason}
		}
		if len(why) > 0 {
			audit.Cases = append(audit.Cases, panelaudit.AuditCase{CaseID: c.CaseID, Reasons: why})
		}
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	audit.ReceiptDigest = panelaudit.Digest(raw)
	for i, value := range []any{receipt, audit, stats} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(paths[2*i+1], raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m.ConstructReview = evalcorpus.ConstructReview{}
	return m, paths
}

func TestCorpusDigestOptionalSupplementaryDiagnostics(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		m, panel := supplementaryPanel(t, incomplete, 25)
		path := writeUnsignedManifest(t, t.TempDir(), m)
		out, _, err := runDigest(panel, "--manifest", path, "--quota-profile", "pilot")
		if err != nil || out == "" {
			t.Fatalf("incomplete %t: %q %v", incomplete, out, err)
		}
		raw, err := os.ReadFile(panel[5])
		if err != nil {
			t.Fatal(err)
		}
		var stats panelaudit.Stats
		if err := json.Unmarshal(raw, &stats); err != nil {
			t.Fatal(err)
		}
		intent := stats.Items["intent"]
		intent.AC1 = 1
		stats.Items["intent"] = intent
		raw, err = json.Marshal(stats)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(panel[5], raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if out, _, err := runDigest(panel, "--manifest", path, "--quota-profile", "pilot"); err == nil || out != "" {
			t.Fatalf("forged unpinned stats accepted: %q %v", out, err)
		}
	}
}
