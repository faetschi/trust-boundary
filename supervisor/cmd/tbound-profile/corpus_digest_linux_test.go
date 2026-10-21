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
	"strings"
	"testing"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/taskaccept"
	"tbound/supervisor/internal/trialfix"
)

// All fixtures here are synthetic.

func fixtureDigest(label string) string {
	sum := sha256.Sum256([]byte(label))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func fixtureReviews() []evalcorpus.Review {
	return []evalcorpus.Review{
		{Role: "reviewer-a", Reviewer: "synthetic-reviewer-1", Human: true, Blinded: true, Decision: "accept"},
		{Role: "reviewer-b", Reviewer: "synthetic-reviewer-2", Human: true, Blinded: true, Decision: "accept"},
	}
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
			m.Cases = append(m.Cases, evalcorpus.Case{CaseID: id, Kind: evalcorpus.KindAdversarial, Reviews: fixtureReviews(),
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
			m.Cases = append(m.Cases, evalcorpus.Case{CaseID: id, Kind: evalcorpus.KindCompliant, Reviews: fixtureReviews(),
				Compliant: &evalcorpus.Compliant{PrimaryTool: tool, TaskGoal: "goal", ToolMapping: "m", OracleID: "task-oracle",
					OracleVersion: "0", WorkspaceFixtureDigest: fixtureDigest("cmp/" + id),
					AcceptanceTest:    evalcorpus.Ref{Ref: "acc-" + id, Digest: fixtureDigest("acc/" + id)},
					ReferenceSolution: evalcorpus.ReferenceSolution{Ref: "sol-" + id, Digest: fixtureDigest("sol/" + id), ForbiddenEffects: []string{}}}})
		}
	}
	for i := 1; i <= q.Ambiguous; i++ {
		m.Cases = append(m.Cases, evalcorpus.Case{CaseID: fmt.Sprintf("amb-%02d", i), Kind: evalcorpus.KindAmbiguous, Reviews: fixtureReviews(),
			Ambiguous: &evalcorpus.Ambiguous{WorkspaceFixtureDigest: fixtureDigest("amb-fixture"),
				Rubric: evalcorpus.Ref{Ref: "rubric", Digest: fixtureDigest("rubric")}}})
	}
	allocated, err := evalcorpus.Allocate(m, q)
	if err != nil {
		t.Fatal(err)
	}
	for i := range allocated.Cases {
		id := allocated.Cases[i].CaseID
		allocated.Cases[i].Trial = &evalcorpus.Trial{Prompt: "Host-approved prompt of " + id, MaxHostCalls: 4, MaxTokens: 10000, DurationSeconds: 600}
	}
	if violations := evalcorpus.Validate(allocated, q); len(violations) != 0 {
		t.Fatalf("fixture manifest is invalid: %v", violations)
	}
	return allocated
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

func TestCorpusDigestPrintsTheDigestTheServiceAccepts(t *testing.T) {
	m := fixtureManifest(t, evalcorpus.PilotQuotas(), "corpus-digest-seed")
	path := writeManifest(t, t.TempDir(), "corpus.json", m)
	want, err := evalcorpus.Digest(m)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI("corpus-digest", "--manifest", path, "--quota-profile", "pilot")
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
	again, _, err := runCLI("corpus-digest", "--manifest", path, "--quota-profile", "pilot")
	if err != nil || again != out {
		t.Fatalf("second run %q %v", again, err)
	}
}

func TestCorpusDigestRefusesInvalidManifests(t *testing.T) {
	dir := t.TempDir()
	q := evalcorpus.PilotQuotas()
	good := fixtureManifest(t, q, "corpus-digest-seed")

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

	for name, path := range map[string]string{
		"duplicate case id":  writeManifest(t, dir, "dup.json", duplicate),
		"case without trial": writeManifest(t, dir, "notrial.json", noTrial),
		"blank prompt":       writeManifest(t, dir, "blank.json", blankPrompt),
		"wrong pilot counts": writeManifest(t, dir, "short.json", short),
		"not json":           notJSON,
		"unknown field":      unknownField,
		"missing file":       filepath.Join(dir, "absent.json"),
	} {
		out, _, err := runCLI("corpus-digest", "--manifest", path, "--quota-profile", "pilot")
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		if out != "" {
			t.Errorf("%s: printed %q on failure", name, out)
		}
	}
}

func TestCorpusDigestPilotManifestIsRefusedUnderThesisQuotas(t *testing.T) {
	m := fixtureManifest(t, evalcorpus.PilotQuotas(), "corpus-digest-seed")
	path := writeManifest(t, t.TempDir(), "corpus.json", m)
	for _, args := range [][]string{
		{"corpus-digest", "--manifest", path},
		{"corpus-digest", "--manifest", path, "--quota-profile", "thesis"},
	} {
		out, _, err := runCLI(args...)
		if err == nil || !strings.Contains(err.Error(), "count.") || out != "" {
			t.Fatalf("%v: out %q err %v, want a count.* refusal", args, out, err)
		}
	}
	if _, _, err := runCLI("corpus-digest", "--manifest", path, "--quota-profile", "bogus"); err == nil {
		t.Fatal("unknown quota profile accepted")
	}
	if _, _, err := runCLI("corpus-digest", "--quota-profile", "pilot"); err == nil {
		t.Fatal("missing --manifest accepted")
	}
}

func TestCorpusDigestHostCeilings(t *testing.T) {
	m := fixtureManifest(t, evalcorpus.PilotQuotas(), "corpus-digest-seed") // budgets 4 / 10000 / 600
	path := writeManifest(t, t.TempDir(), "corpus.json", m)
	if _, _, err := runCLI("corpus-digest", "--manifest", path, "--quota-profile", "pilot",
		"--max-host-calls", "4", "--max-tokens", "10000", "--duration-seconds", "600"); err != nil {
		t.Fatalf("ceilings equal to the budgets: %v", err)
	}
	if _, _, err := runCLI("corpus-digest", "--manifest", path, "--quota-profile", "pilot",
		"--max-host-calls", "3", "--max-tokens", "10000", "--duration-seconds", "600"); err == nil ||
		!strings.Contains(err.Error(), "host ceilings") {
		t.Fatalf("a budget over the ceiling was accepted: %v", err)
	}
	if _, _, err := runCLI("corpus-digest", "--manifest", path, "--quota-profile", "pilot", "--max-host-calls", "4"); err == nil {
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
	path := writeManifest(t, t.TempDir(), "corpus.json", m)
	want, err := evalcorpus.Digest(m)
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI("corpus-digest", "--manifest", path, "--quota-profile", "pilot", "--artifacts", artifacts)
	if err != nil || out != want+"\n" {
		t.Fatalf("with matching artifacts: out %q err %v, want %q", out, err, want)
	}

	// One artifact removed: the digest is not printed.
	name, _ := corpusadmit.FixtureArtifactName(m.Cases[0].Adversarial.WorkspaceFixtureDigest)
	if err := os.Remove(filepath.Join(artifacts, name)); err != nil {
		t.Fatal(err)
	}
	out, _, err = runCLI("corpus-digest", "--manifest", path, "--quota-profile", "pilot", "--artifacts", artifacts)
	if err == nil || out != "" || !strings.Contains(err.Error(), m.Cases[0].CaseID) {
		t.Fatalf("with a missing artifact: out %q err %v, want a refusal naming %s", out, err, m.Cases[0].CaseID)
	}

	// Without the flag the artifacts are not looked at.
	if _, _, err := runCLI("corpus-digest", "--manifest", path, "--quota-profile", "pilot"); err != nil {
		t.Fatalf("without --artifacts: %v", err)
	}
	if _, _, err := runCLI("corpus-digest", "--manifest", path, "--quota-profile", "pilot", "--artifacts", filepath.Join(artifacts, "absent")); err == nil {
		t.Fatal("an artifact directory that does not exist was accepted")
	}
}

func TestCorpusDigestRefusesRejectedAndPlaceholderReviews(t *testing.T) {
	dir := t.TempDir()
	q := evalcorpus.PilotQuotas()

	rejected := fixtureManifest(t, q, "corpus-digest-seed")
	rejected.Cases[2].Reviews[0].Decision = "reject"
	rejected.Cases[2].Reviews[1].Decision = "reject"
	rejectedID := rejected.Cases[2].CaseID
	placeholder := fixtureManifest(t, q, "corpus-digest-seed")
	placeholder.Cases[4].Reviews[0].Reviewer = "TODO-REVIEWER-OWNER"
	placeholder.Cases[4].Reviews[0].Role = "TODO-ROLE-1"
	placeholderID := placeholder.Cases[4].CaseID
	freeText := fixtureManifest(t, q, "corpus-digest-seed")
	freeText.Cases[5].Reviews[1].Decision = "TODO-DECISION"

	for _, tc := range []struct {
		name, id string
		m        evalcorpus.Manifest
		rule     string
	}{
		{"rejected case", rejectedID, rejected, "review.rejected"},
		{"placeholder reviewer and role", placeholderID, placeholder, "review.placeholder"},
		{"free-text decision", freeText.Cases[5].CaseID, freeText, "review.decision"},
	} {
		path := writeManifest(t, dir, strings.ReplaceAll(tc.name, " ", "-")+".json", tc.m)
		out, _, err := runCLI("corpus-digest", "--manifest", path, "--quota-profile", "pilot")
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if out != "" {
			t.Errorf("%s: printed %q on failure", tc.name, out)
		}
		if !strings.Contains(err.Error(), tc.rule) || !strings.Contains(err.Error(), tc.id) {
			t.Errorf("%s: error %q does not name rule %s and case %s", tc.name, err, tc.rule, tc.id)
		}
	}
}
