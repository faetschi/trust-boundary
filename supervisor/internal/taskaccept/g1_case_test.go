package taskaccept

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/guestevidence"
	"tbound/supervisor/internal/trialfix"
)

// corpus/g1 is the draft engineering case for EVQ-PROVIDER-REAL-EXCHANGE: read, edit, Bash, read over
// the real provider. These tests hold its pinned artifacts to the same rules as the pilot cases.

type g1Ref struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}

type g1Case struct {
	Schema          string            `json:"schema"`
	Status          string            `json:"status"`
	ClaimBearing    bool              `json:"claim_bearing"`
	CaseID          string            `json:"case_id"`
	Route           string            `json:"route"`
	TaskID          string            `json:"task_id"`
	Prompt          string            `json:"prompt"`
	ToolSequence    []string          `json:"tool_sequence"`
	BashOutputPaths []string          `json:"bash_output_paths"`
	Ceilings        map[string]uint64 `json:"ceilings"`
	Fixture         g1Ref             `json:"fixture"`
	AcceptanceTest  g1Ref             `json:"acceptance_test"`
	ReferenceSolu   g1Ref             `json:"reference_solution"`
	FinalState      map[string]string `json:"final_state"`
}

func loadG1(t *testing.T) (g1Case, string) {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "corpus", "g1")
	data, err := os.ReadFile(filepath.Join(dir, "g1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c g1Case
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("g1.json: %v", err)
	}
	return c, dir
}

func TestG1CaseIsADraftEngineeringCaseOfTheG1Sequence(t *testing.T) {
	c, _ := loadG1(t)
	if c.Schema != "tbound-g1-case/v1" || c.ClaimBearing || c.Route != "engineering" || !strings.Contains(c.Status, "owner approval required") {
		t.Fatalf("case header = %+v", c)
	}
	if !slices.Equal(c.ToolSequence, guestevidence.G1ToolSequence) {
		t.Errorf("tool sequence %v, the validator requires %v", c.ToolSequence, guestevidence.G1ToolSequence)
	}
	for _, name := range []string{"src/release.txt", "out/release.sha256"} {
		if !strings.Contains(c.Prompt, name) {
			t.Errorf("the prompt does not name %s", name)
		}
	}
	// Bash makes generation g2 only through a declared output path.
	if len(c.BashOutputPaths) != 1 || !strings.Contains(c.Prompt, c.BashOutputPaths[0]) {
		t.Errorf("bash output paths %v", c.BashOutputPaths)
	}
}

func TestG1ArtifactsAreThePinnedBytesAndTheAcceptanceIsSensitive(t *testing.T) {
	c, dir := loadG1(t)
	read := func(ref g1Ref) []byte {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, "artifacts", ref.Ref))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	fixRaw, accRaw, solRaw := read(c.Fixture), read(c.AcceptanceTest), read(c.ReferenceSolu)
	for _, a := range []struct {
		ref g1Ref
		raw []byte
	}{{c.AcceptanceTest, accRaw}, {c.ReferenceSolu, solRaw}} {
		if DigestOf(a.raw) != a.ref.Digest || !strings.HasSuffix(a.ref.Ref, strings.TrimPrefix(a.ref.Digest, "sha256:")+".json") {
			t.Errorf("%s is not the file its digest names", a.ref.Ref)
		}
	}
	fixture, err := trialfix.Parse(bytes.NewReader(fixRaw))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := trialfix.Digest(fixture); err != nil || got != c.Fixture.Digest {
		t.Fatalf("fixture digest %q (%v), the case pins %q", got, err, c.Fixture.Digest)
	}
	accept, err := ParseAccept(accRaw)
	if err != nil {
		t.Fatal(err)
	}
	solution, err := ParseSolution(solRaw)
	if err != nil {
		t.Fatal(err)
	}
	if err := SelfCheck(fixture, accept, solution); err != nil {
		t.Fatalf("self check: %v", err)
	}
	// The final state the README tells the owner to compare is what the acceptance test pins.
	var acc struct {
		Checks []Check `json:"checks"`
	}
	if err := json.Unmarshal(accRaw, &acc); err != nil {
		t.Fatal(err)
	}
	pinned := map[string]string{}
	for _, ch := range acc.Checks {
		if ch.Op == OpFileSHA256 {
			pinned[ch.Path] = ch.Digest
		}
	}
	if len(pinned) != len(c.FinalState) {
		t.Fatalf("final state %v, acceptance pins %v", c.FinalState, pinned)
	}
	for p, d := range c.FinalState {
		if pinned[p] != d {
			t.Errorf("final state %s = %s, acceptance pins %s", p, d, pinned[p])
		}
	}
}
