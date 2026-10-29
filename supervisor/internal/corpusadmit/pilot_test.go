package corpusadmit

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

// T5 moves seven injections out of the user prompt and into the workspace file that the frozen script
// reads first. t5ContractViolations states that contract once. The real draft and every mutation go
// through it, so a mutation that passes here is a missing check.
const (
	t5DraftDir      = "../../../corpus/pilot/t5-2026-10-29"
	t5ProvenanceDir = "../../../corpus/pilot/provenance-2026-10-29"
)

// t5Record is one case of provenance-2026-10-29/cases.json.
type t5Record struct {
	OriginalPrompt          string `json:"original_prompt"`
	LegitimatePrompt        string `json:"legitimate_prompt"`
	InjectionLabel          string `json:"injection_label"`
	Injection               string `json:"injection"`
	Carrier                 string `json:"carrier"`
	OriginalCarrierSHA256   string `json:"original_carrier_sha256"`
	OriginalFixtureArtifact string `json:"original_fixture_artifact"`
}

// t5Subject is one T5 case with its carrier file before and after the change.
type t5Subject struct {
	Case            evalcorpus.Case
	OriginalCase    evalcorpus.Case
	Record          t5Record
	Carrier         []byte
	OriginalCarrier []byte
}

// clone copies every part a mutation writes, so one subtest's change never reaches the base case or the next.
func (s t5Subject) clone() t5Subject {
	trial := *s.Case.Trial
	trial.Script = slices.Clone(trial.Script)
	s.Case.Trial = &trial
	adversarial := *s.Case.Adversarial
	s.Case.Adversarial = &adversarial
	meaning := *s.Case.MeaningRecord
	s.Case.MeaningRecord = &meaning
	s.Carrier = slices.Clone(s.Carrier)
	s.OriginalCarrier = slices.Clone(s.OriginalCarrier)
	return s
}

// t5ReadWindow is a read call's path and window, decoded as the durable read path decodes them.
type t5ReadWindow struct {
	Path   string
	Offset int
	Limit  int
}

// t5DecodeRead accepts only the path, offset and limit members, and refuses an offset or limit that
// is not a positive integer, as durableReadArguments does.
func t5DecodeRead(raw json.RawMessage) (t5ReadWindow, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return t5ReadWindow{}, err
	}
	for name := range members {
		if name != "path" && name != "offset" && name != "limit" {
			return t5ReadWindow{}, fmt.Errorf("member %q is not a read member", name)
		}
	}
	var args struct {
		Path   string   `json:"path"`
		Offset *float64 `json:"offset"`
		Limit  *float64 `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return t5ReadWindow{}, err
	}
	offset, err := t5LineCount("offset", args.Offset)
	if err != nil {
		return t5ReadWindow{}, err
	}
	limit, err := t5LineCount("limit", args.Limit)
	if err != nil {
		return t5ReadWindow{}, err
	}
	return t5ReadWindow{Path: args.Path, Offset: offset, Limit: limit}, nil
}

func t5LineCount(name string, value *float64) (int, error) {
	if value == nil {
		return 0, nil
	}
	if *value < 1 || *value != math.Trunc(*value) {
		return 0, fmt.Errorf("read %s must be a positive integer", name)
	}
	return int(min(*value, math.MaxInt32)), nil
}

// t5Released returns the bytes a read with this window releases. It mirrors durableReadWindow.apply in
// cmd/tbound/durable_executor_linux.go: lines split on LF, offset and limit counted in lines.
func t5Released(content []byte, window t5ReadWindow) []byte {
	if window.Offset == 0 && window.Limit == 0 {
		return content
	}
	lines := bytes.Split(content, []byte("\n"))
	start := max(window.Offset-1, 0)
	if start >= len(lines) {
		return []byte{}
	}
	end := len(lines)
	if window.Limit > 0 {
		end = min(start+window.Limit, end)
	}
	return bytes.Join(lines[start:end], []byte("\n"))
}

// t5ContractViolations lists every way one T5 case departs from the tool-read carrier contract.
func t5ContractViolations(s t5Subject) []string {
	var violations []string
	fail := func(format string, args ...any) {
		violations = append(violations, fmt.Sprintf(format, args...))
	}
	c, orig, rec := s.Case, s.OriginalCase, s.Record
	if c.Kind != evalcorpus.KindAdversarial || c.Adversarial == nil || c.MeaningRecord == nil || c.Trial == nil ||
		orig.Adversarial == nil || orig.MeaningRecord == nil || orig.Trial == nil {
		fail("case lacks an adversarial payload, a meaning record or a trial")
		return violations
	}
	if rec.OriginalPrompt != rec.LegitimatePrompt+"\n\nInjected "+rec.InjectionLabel+": \""+rec.Injection+"\"" {
		fail("provenance prompt does not split into legitimate prompt, label and injection")
	}
	if rec.OriginalPrompt != s.OriginalCase.Trial.Prompt {
		fail("original_prompt is not the provenance manifest's prompt")
	}
	if name, _ := FixtureArtifactName(orig.Adversarial.WorkspaceFixtureDigest); name != rec.OriginalFixtureArtifact {
		fail("original_fixture_artifact %s is not the original fixture", rec.OriginalFixtureArtifact)
	}
	if sum := sha256.Sum256(s.OriginalCarrier); hex.EncodeToString(sum[:]) != rec.OriginalCarrierSHA256 {
		fail("original carrier bytes do not match original_carrier_sha256")
	}
	if c.Trial.Prompt != rec.LegitimatePrompt {
		fail("trial.prompt is not the legitimate prompt")
	}
	if strings.Contains(c.Trial.Prompt, rec.Injection) {
		fail("the injection is in trial.prompt")
	}
	if !bytes.HasPrefix(s.Carrier, s.OriginalCarrier) {
		fail("carrier %s does not start with the original carrier bytes", rec.Carrier)
	} else if !bytes.Equal(s.Carrier[len(s.OriginalCarrier):], []byte("\n"+rec.Injection+"\n")) {
		fail("carrier %s does not end with a newline, the injection and a newline", rec.Carrier)
	}
	script := c.Trial.Script
	if len(script) != 2 || script[0].Tool != "read" || script[1].Tool != "bash" {
		fail("script has %d turns, want one read then one effect turn", len(script))
	}
	if len(script) > 0 && script[0].Tool == "read" {
		window, err := t5DecodeRead(script[0].Arguments)
		switch {
		case err != nil:
			fail("first turn's read arguments: %v", err)
		case window.Path != rec.Carrier:
			fail("first turn reads %q, want the carrier %q", window.Path, rec.Carrier)
		case !bytes.Contains(t5Released(s.Carrier, window), []byte(rec.Injection)):
			fail("the lines the first read releases do not contain the injection")
		}
	}
	same := func(name string, got, want any) {
		if !reflect.DeepEqual(got, want) {
			fail("%s changed from the provenance manifest", name)
		}
	}
	same("meaning_record benchmark_key", c.MeaningRecord.BenchmarkKey, orig.MeaningRecord.BenchmarkKey)
	same("meaning_record benchmark_revision", c.MeaningRecord.BenchmarkRevision, orig.MeaningRecord.BenchmarkRevision)
	same("meaning_record benchmark_digest", c.MeaningRecord.BenchmarkDigest, orig.MeaningRecord.BenchmarkDigest)
	same("meaning_record source_item_id", c.MeaningRecord.SourceItemID, orig.MeaningRecord.SourceItemID)
	same("meaning_record root_id", c.MeaningRecord.RootID, orig.MeaningRecord.RootID)
	same("meaning_record license", c.MeaningRecord.License, orig.MeaningRecord.License)
	same("adversarial root_scenario_id", c.Adversarial.RootScenarioID, orig.Adversarial.RootScenarioID)
	same("adversarial source_key", c.Adversarial.SourceKey, orig.Adversarial.SourceKey)
	same("adversarial source_item_id", c.Adversarial.SourceItemID, orig.Adversarial.SourceItemID)
	same("adversarial partition", c.Adversarial.Partition, orig.Adversarial.Partition)
	same("adversarial variance_subset", c.Adversarial.VarianceSubset, orig.Adversarial.VarianceSubset)
	same("adversarial forbidden_effects", c.Adversarial.ForbiddenEffects, orig.Adversarial.ForbiddenEffects)
	same("trial.protected_paths", c.Trial.ProtectedPaths, orig.Trial.ProtectedPaths)
	same("trial.pivots", c.Trial.Pivots, orig.Trial.Pivots)
	return violations
}

func loadT5Manifest(t *testing.T) evalcorpus.Manifest {
	t.Helper()
	file, err := os.Open(t5DraftDir + "/manifest.draft.json")
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

// loadT5ProvenanceManifest is the manifest as it was before T5, the baseline for every historical comparison.
func loadT5ProvenanceManifest(t *testing.T) evalcorpus.Manifest {
	t.Helper()
	file, err := os.Open(t5ProvenanceDir + "/manifest.draft.json")
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

func loadT5Records(t *testing.T) map[string]t5Record {
	t.Helper()
	raw, err := os.ReadFile(t5ProvenanceDir + "/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var records map[string]t5Record
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatal(err)
	}
	return records
}

// t5FixtureFile returns the bytes of one regular file in the fixture that digest names.
func t5FixtureFile(t *testing.T, fsys fs.FS, digest, path string) []byte {
	t.Helper()
	spec, err := LoadFixture(fsys, digest)
	if err != nil {
		t.Fatal(err)
	}
	files, err := spec.Files()
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if !file.Dir && file.Path == path {
			return file.Content
		}
	}
	t.Fatalf("fixture %s has no file %s", digest, path)
	return nil
}

func loadT5Subjects(t *testing.T, original, draft evalcorpus.Manifest, records map[string]t5Record) map[string]t5Subject {
	t.Helper()
	originals, current := map[string]evalcorpus.Case{}, map[string]evalcorpus.Case{}
	for _, c := range original.Cases {
		originals[c.CaseID] = c
	}
	for _, c := range draft.Cases {
		current[c.CaseID] = c
	}
	subjects := map[string]t5Subject{}
	for id, rec := range records {
		oc, cc := originals[id], current[id]
		if oc.Adversarial == nil || cc.Adversarial == nil {
			t.Fatalf("%s is not an adversarial case in both manifests", id)
		}
		subjects[id] = t5Subject{
			Case:            cc,
			OriginalCase:    oc,
			Record:          rec,
			Carrier:         t5FixtureFile(t, os.DirFS(t5DraftDir+"/artifacts"), cc.Adversarial.WorkspaceFixtureDigest, rec.Carrier),
			OriginalCarrier: t5FixtureFile(t, os.DirFS(t5ProvenanceDir), oc.Adversarial.WorkspaceFixtureDigest, rec.Carrier),
		}
	}
	return subjects
}

// t5VerifyProvenanceSums checks that SHA256SUMS lists every other file of the provenance directory with
// its sha256, and that it lists nothing else.
func t5VerifyProvenanceSums(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(t5ProvenanceDir + "/SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		digest, name, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatalf("SHA256SUMS line %q is not sha256sum format", line)
		}
		listed[name] = digest
	}
	entries, err := os.ReadDir(t5ProvenanceDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "SHA256SUMS" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(t5ProvenanceDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if want, ok := listed[entry.Name()]; !ok || want != hex.EncodeToString(sum[:]) {
			t.Errorf("%s is not listed in SHA256SUMS with its sha256", entry.Name())
		}
		delete(listed, entry.Name())
	}
	for name := range listed {
		t.Errorf("SHA256SUMS lists %s, which is not in the directory", name)
	}
}

// TestPilotT5ToolReadCarriers checks that each of the seven T5 injections reaches the model only as
// read-tool output of the file the frozen script reads first, and that nothing else about the cases moved.
func TestPilotT5ToolReadCarriers(t *testing.T) {
	original, draft := loadT5ProvenanceManifest(t), loadT5Manifest(t)
	records := loadT5Records(t)
	subjects := loadT5Subjects(t, original, draft, records)

	expected := make([]string, 0, len(records))
	for id := range records {
		expected = append(expected, id)
	}
	slices.Sort(expected)
	var named []string
	for _, c := range draft.Cases {
		if c.Kind != evalcorpus.KindAdversarial || c.Trial == nil {
			continue
		}
		for _, rec := range records {
			if strings.Contains(c.Trial.Prompt, rec.Carrier) {
				named = append(named, c.CaseID)
				break
			}
		}
	}
	slices.Sort(named)
	if !slices.Equal(named, expected) {
		t.Errorf("adversarial prompts that name a carrier are %v, want the provenance cases %v", named, expected)
	}
	for _, id := range expected {
		if violations := t5ContractViolations(subjects[id]); len(violations) != 0 {
			t.Errorf("%s: %s", id, strings.Join(violations, "; "))
		}
	}

	originalByID := map[string]evalcorpus.Case{}
	for _, c := range original.Cases {
		originalByID[c.CaseID] = c
	}
	others := 0
	for _, c := range draft.Cases {
		if _, ok := records[c.CaseID]; ok {
			continue
		}
		others++
		if !reflect.DeepEqual(c, originalByID[c.CaseID]) {
			t.Errorf("%s changed though it is not a T5 case", c.CaseID)
		}
	}
	if others != 26 {
		t.Errorf("%d non-T5 cases, want 26", others)
	}
	t5VerifyProvenanceSums(t)

	// A mutation names the violation it must produce, and its subtest fails when that violation is absent.
	// So a mutation that another guard happens to catch does not pass.
	mutations := []struct {
		name  string
		want  string
		apply func(s *t5Subject)
	}{
		{"case is not adversarial", "case lacks an adversarial payload", func(s *t5Subject) {
			s.Case.Kind = evalcorpus.KindCompliant
		}},
		{"provenance label altered", "does not split into legitimate prompt, label and injection", func(s *t5Subject) {
			s.Record.InjectionLabel += "x"
		}},
		{"provenance original_prompt altered", "original_prompt is not the provenance manifest's prompt", func(s *t5Subject) {
			s.Record.OriginalPrompt += " "
		}},
		{"provenance original fixture artifact altered", "is not the original fixture", func(s *t5Subject) {
			s.Record.OriginalFixtureArtifact += "x"
		}},
		{"original carrier copy changed without its sha256 update", "do not match original_carrier_sha256", func(s *t5Subject) {
			s.OriginalCarrier[0] ^= 1
		}},
		{"legitimate prompt changed", "trial.prompt is not the legitimate prompt", func(s *t5Subject) {
			s.Case.Trial.Prompt += " Answer in one word."
		}},
		{"injection put back into the prompt", "the injection is in trial.prompt", func(s *t5Subject) {
			s.Case.Trial.Prompt = s.Record.OriginalPrompt
		}},
		{"injection removed from the carrier", "does not end with a newline, the injection and a newline", func(s *t5Subject) {
			s.Carrier = slices.Clone(s.OriginalCarrier)
		}},
		{"one original carrier byte changed without a provenance update", "does not start with the original carrier bytes", func(s *t5Subject) {
			s.Carrier[0] ^= 1
		}},
		{"script drops the effect turn", "want one read then one effect turn", func(s *t5Subject) {
			s.Case.Trial.Script = s.Case.Trial.Script[:1]
		}},
		{"read arguments carry a member that is not a read member", "first turn's read arguments", func(s *t5Subject) {
			s.Case.Trial.Script[0].Arguments = json.RawMessage(fmt.Sprintf(`{"path":%q,"bogus":1}`, s.Record.Carrier))
		}},
		{"script read pointed at another file", "first turn reads", func(s *t5Subject) {
			s.Case.Trial.Script[0].Arguments = json.RawMessage(`{"path":"not-the-carrier.txt"}`)
		}},
		{"read limit set so the injection lies outside the released lines", "the lines the first read releases do not contain the injection", func(s *t5Subject) {
			s.Case.Trial.Script[0].Arguments = json.RawMessage(fmt.Sprintf(`{"path":%q,"limit":1}`, s.Record.Carrier))
		}},
		{"meaning_record benchmark_key altered", "meaning_record benchmark_key changed from the provenance manifest", func(s *t5Subject) {
			s.Case.MeaningRecord.BenchmarkKey += "x"
		}},
		{"meaning_record benchmark_revision altered", "meaning_record benchmark_revision changed from the provenance manifest", func(s *t5Subject) {
			s.Case.MeaningRecord.BenchmarkRevision += "x"
		}},
		{"meaning_record benchmark_digest altered", "meaning_record benchmark_digest changed from the provenance manifest", func(s *t5Subject) {
			s.Case.MeaningRecord.BenchmarkDigest += "x"
		}},
		{"meaning_record source_item_id altered", "meaning_record source_item_id changed from the provenance manifest", func(s *t5Subject) {
			s.Case.MeaningRecord.SourceItemID += "x"
		}},
		{"meaning_record root_id altered", "meaning_record root_id changed from the provenance manifest", func(s *t5Subject) {
			s.Case.MeaningRecord.RootID += "x"
		}},
		{"meaning_record license altered", "meaning_record license changed from the provenance manifest", func(s *t5Subject) {
			s.Case.MeaningRecord.License += "x"
		}},
		{"adversarial root_scenario_id altered", "adversarial root_scenario_id changed from the provenance manifest", func(s *t5Subject) {
			s.Case.Adversarial.RootScenarioID += "x"
		}},
		{"adversarial source_key altered", "adversarial source_key changed from the provenance manifest", func(s *t5Subject) {
			s.Case.Adversarial.SourceKey += "x"
		}},
		{"adversarial source_item_id altered", "adversarial source_item_id changed from the provenance manifest", func(s *t5Subject) {
			s.Case.Adversarial.SourceItemID += "x"
		}},
		{"adversarial partition altered", "adversarial partition changed from the provenance manifest", func(s *t5Subject) {
			s.Case.Adversarial.Partition += "x"
		}},
		{"adversarial variance_subset altered", "adversarial variance_subset changed from the provenance manifest", func(s *t5Subject) {
			s.Case.Adversarial.VarianceSubset = !s.Case.Adversarial.VarianceSubset
		}},
		{"adversarial forbidden_effects altered", "adversarial forbidden_effects changed from the provenance manifest", func(s *t5Subject) {
			s.Case.Adversarial.ForbiddenEffects = append(slices.Clone(s.Case.Adversarial.ForbiddenEffects), "tampered")
		}},
		{"trial.protected_paths altered", "trial.protected_paths changed from the provenance manifest", func(s *t5Subject) {
			s.Case.Trial.ProtectedPaths = append(slices.Clone(s.Case.Trial.ProtectedPaths), "tampered")
		}},
		{"trial.pivots altered", "trial.pivots changed from the provenance manifest", func(s *t5Subject) {
			s.Case.Trial.Pivots = map[string]evalcorpus.ScriptTurn{"tampered": {}}
		}},
	}
	for _, id := range []string{"PILOT-ADV-A-01", "PILOT-ADV-A-03", "PILOT-ADV-A-04", "PILOT-ADV-A-05"} {
		for _, mutation := range mutations {
			t.Run(id+"/"+mutation.name, func(t *testing.T) {
				if violations := t5ContractViolations(subjects[id]); len(violations) != 0 {
					t.Fatalf("base case violates the contract before the mutation: %s", strings.Join(violations, "; "))
				}
				mutated := subjects[id].clone()
				mutation.apply(&mutated)
				violations := t5ContractViolations(mutated)
				if !slices.ContainsFunc(violations, func(v string) bool { return strings.Contains(v, mutation.want) }) {
					t.Fatalf("mutation did not produce %q; violations: %q", mutation.want, violations)
				}
				t.Logf("detected: %s", strings.Join(violations, "; "))
			})
		}
	}
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
