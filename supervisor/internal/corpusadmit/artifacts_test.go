package corpusadmit

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/trialfix"
)

func specWith(content string) trialfix.Spec {
	return trialfix.Spec{Schema: trialfix.Schema, Entries: []trialfix.Entry{
		{Path: "f", Type: trialfix.TypeFile, Mode: "0644", ContentB64: base64.StdEncoding.EncodeToString([]byte(content))}}}
}

func digestOf(t *testing.T, s trialfix.Spec) string {
	t.Helper()
	d, err := trialfix.Digest(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func specFile(t *testing.T, s trialfix.Spec) *fstest.MapFile {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return &fstest.MapFile{Data: raw}
}

// artifactCases returns one case of each kind, each with its own fixture, and the artifact
// file system that holds all three.
func artifactCases(t *testing.T) ([]evalcorpus.Case, fstest.MapFS) {
	t.Helper()
	adv, cmp, amb := specWith("adv"), specWith("cmp"), specWith("amb")
	cases := []evalcorpus.Case{
		{CaseID: "adv-1", Kind: evalcorpus.KindAdversarial, Adversarial: &evalcorpus.Adversarial{WorkspaceFixtureDigest: digestOf(t, adv)}},
		{CaseID: "cmp-1", Kind: evalcorpus.KindCompliant, Compliant: &evalcorpus.Compliant{WorkspaceFixtureDigest: digestOf(t, cmp)}},
		{CaseID: "amb-1", Kind: evalcorpus.KindAmbiguous, Ambiguous: &evalcorpus.Ambiguous{WorkspaceFixtureDigest: digestOf(t, amb)}},
	}
	fsys := fstest.MapFS{}
	for i, s := range []trialfix.Spec{adv, cmp, amb} {
		name, ok := FixtureArtifactName(digestOf(t, s))
		if !ok {
			t.Fatalf("no artifact name for case %d", i)
		}
		fsys[name] = specFile(t, s)
	}
	putTask(fsys, cases[1].Compliant, cmpAccept, solutionJSON("cmp done"))
	return cases, fsys
}

func TestFixtureArtifactName(t *testing.T) {
	hex := strings.Repeat("ab", 32)
	if got, ok := FixtureArtifactName("sha256:" + hex); !ok || got != "fixture-"+hex+".json" {
		t.Fatalf("FixtureArtifactName = %q, %v", got, ok)
	}
	for _, bad := range []string{"", hex, "sha256:" + hex[1:], "sha256:" + strings.ToUpper(hex), "sha512:" + hex, "sha256:../" + hex[3:]} {
		if got, ok := FixtureArtifactName(bad); ok {
			t.Errorf("FixtureArtifactName(%q) = %q, want a refusal", bad, got)
		}
	}
}

func TestFixtureDigestOfEachKind(t *testing.T) {
	cases, _ := artifactCases(t)
	for _, c := range cases {
		if d, ok := FixtureDigest(c); !ok || d == "" {
			t.Errorf("%s: FixtureDigest = %q, %v", c.CaseID, d, ok)
		}
	}
	if _, ok := FixtureDigest(evalcorpus.Case{CaseID: "x", Kind: evalcorpus.KindAmbiguous}); ok {
		t.Error("FixtureDigest answered for a case without its payload")
	}
}

func TestCheckArtifactsAcceptsMatchingArtifacts(t *testing.T) {
	cases, fsys := artifactCases(t)
	if err := CheckArtifactsWith(cases, fsys, nil); err != nil {
		t.Fatalf("CheckArtifacts: %v", err)
	}
	// Unrelated files and a shared fixture are fine.
	fsys["notes.txt"] = &fstest.MapFile{Data: []byte("x")}
	cases = append(cases, cases[0])
	cases[len(cases)-1].CaseID = "adv-2"
	if err := CheckArtifactsWith(cases, fsys, nil); err != nil {
		t.Fatalf("CheckArtifacts with a shared fixture: %v", err)
	}
}

func TestCheckArtifactsRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(cases []evalcorpus.Case, fsys fstest.MapFS)
		want   string
	}{
		"missing artifact": {func(cases []evalcorpus.Case, fsys fstest.MapFS) {
			name, _ := FixtureArtifactName(cases[2].Ambiguous.WorkspaceFixtureDigest)
			delete(fsys, name)
		}, "amb-1"},
		"artifact of another fixture": {func(cases []evalcorpus.Case, fsys fstest.MapFS) {
			name, _ := FixtureArtifactName(cases[1].Compliant.WorkspaceFixtureDigest)
			fsys[name] = specFile(t, specWith("other"))
		}, "cmp-1"},
		"malformed artifact": {func(cases []evalcorpus.Case, fsys fstest.MapFS) {
			name, _ := FixtureArtifactName(cases[0].Adversarial.WorkspaceFixtureDigest)
			fsys[name] = &fstest.MapFile{Data: []byte("{")}
		}, "adv-1"},
		"invalid digest in the case": {func(cases []evalcorpus.Case, fsys fstest.MapFS) {
			cases[2].Ambiguous.WorkspaceFixtureDigest = "sha256:zz"
		}, "amb-1"},
		"case without payload": {func(cases []evalcorpus.Case, fsys fstest.MapFS) { cases[0].Adversarial = nil }, "adv-1"},
	} {
		t.Run(name, func(t *testing.T) {
			cases, fsys := artifactCases(t)
			tc.mutate(cases, fsys)
			err := CheckArtifactsWith(cases, fsys, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want a refusal naming %q", err, tc.want)
			}
		})
	}
}

func TestLoadFixtureReturnsTheVerifiedSpec(t *testing.T) {
	cases, fsys := artifactCases(t)
	digest := cases[0].Adversarial.WorkspaceFixtureDigest
	spec, err := LoadFixture(fsys, digest)
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}
	if got, _ := trialfix.Digest(spec); got != digest {
		t.Fatalf("spec digests to %s, want %s", got, digest)
	}
	if _, err := LoadFixture(fsys, "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("LoadFixture found an artifact that does not exist")
	}
}
