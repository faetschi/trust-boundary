package corpusadmit

import (
	"encoding/base64"
	"strings"
	"testing"
	"testing/fstest"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/taskaccept"
)

const (
	cmpAccept   = `{"schema":"tbound-acceptance/v1","checks":[{"op":"contains","path":"f","text":"done"},{"op":"unchanged_except","paths":["f"]}]}`
	cmpWeak     = `{"schema":"tbound-acceptance/v1","checks":[{"op":"absent","path":"nothing"}]}`
	cmpStrict   = `{"schema":"tbound-acceptance/v1","checks":[{"op":"contains","path":"f","text":"never written"}]}`
	cmpSolution = `{"schema":"tbound-solution/v1","ops":[{"op":"write","path":"f","mode":"0644","content_b64":"%s"}]}`
)

func solutionJSON(content string) string {
	return strings.Replace(cmpSolution, "%s", base64.StdEncoding.EncodeToString([]byte(content)), 1)
}

// putTask stores raw acceptance and solution artifacts under their own digests and pins
// both in the compliant case.
func putTask(fsys fstest.MapFS, c *evalcorpus.Compliant, accept, solution string) {
	ad, sd := taskaccept.DigestOf([]byte(accept)), taskaccept.DigestOf([]byte(solution))
	an, _ := AcceptArtifactName(ad)
	sn, _ := SolutionArtifactName(sd)
	fsys[an] = &fstest.MapFile{Data: []byte(accept)}
	fsys[sn] = &fstest.MapFile{Data: []byte(solution)}
	c.AcceptanceTest.Digest, c.ReferenceSolution.Digest = ad, sd
}

func TestArtifactNamesOfTheTaskArtifacts(t *testing.T) {
	hex := strings.Repeat("0f", 32)
	if got, ok := AcceptArtifactName("sha256:" + hex); !ok || got != "accept-"+hex+".json" {
		t.Fatalf("AcceptArtifactName = %q, %v", got, ok)
	}
	if got, ok := SolutionArtifactName("sha256:" + hex); !ok || got != "solution-"+hex+".json" {
		t.Fatalf("SolutionArtifactName = %q, %v", got, ok)
	}
	if _, ok := AcceptArtifactName("sha256:../" + hex[3:]); ok {
		t.Error("AcceptArtifactName accepted a path")
	}
}

func TestCheckArtifactsTaskRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(c *evalcorpus.Compliant, fsys fstest.MapFS)
		want   string
	}{
		"acceptance digest mismatch": {func(c *evalcorpus.Compliant, fsys fstest.MapFS) {
			c.AcceptanceTest.Digest = "sha256:" + strings.Repeat("0", 64)
			an, _ := AcceptArtifactName(c.AcceptanceTest.Digest)
			fsys[an] = &fstest.MapFile{Data: []byte(cmpAccept)}
		}, "digests to"},
		"solution digest mismatch": {func(c *evalcorpus.Compliant, fsys fstest.MapFS) {
			sn, _ := SolutionArtifactName(c.ReferenceSolution.Digest)
			fsys[sn] = &fstest.MapFile{Data: []byte(solutionJSON("other done"))}
		}, "digests to"},
		"acceptance artifact missing": {func(c *evalcorpus.Compliant, fsys fstest.MapFS) {
			an, _ := AcceptArtifactName(c.AcceptanceTest.Digest)
			delete(fsys, an)
		}, "accept-"},
		"solution artifact missing": {func(c *evalcorpus.Compliant, fsys fstest.MapFS) {
			sn, _ := SolutionArtifactName(c.ReferenceSolution.Digest)
			delete(fsys, sn)
		}, "solution-"},
		"invalid acceptance digest": {func(c *evalcorpus.Compliant, fsys fstest.MapFS) { c.AcceptanceTest.Digest = "sha256:zz" }, "acceptance test digest"},
		"invalid solution digest":   {func(c *evalcorpus.Compliant, fsys fstest.MapFS) { c.ReferenceSolution.Digest = "" }, "reference solution digest"},
		"malformed acceptance spec": {func(c *evalcorpus.Compliant, fsys fstest.MapFS) {
			putTask(fsys, c, `{"schema":"tbound-acceptance/v1","checks":[{"op":"exec","path":"f"}]}`, solutionJSON("cmp done"))
		}, "unknown operation"},
		"acceptance spec with an unknown field": {func(c *evalcorpus.Compliant, fsys fstest.MapFS) {
			putTask(fsys, c, strings.Replace(cmpAccept, `"checks"`, `"cmd":"true","checks"`, 1), solutionJSON("cmp done"))
		}, "unknown field"},
		"solution that does not pass": {func(c *evalcorpus.Compliant, fsys fstest.MapFS) {
			putTask(fsys, c, cmpAccept, solutionJSON("cmp unfinished"))
		}, "reference solution does not pass"},
		"solution that does not apply": {func(c *evalcorpus.Compliant, fsys fstest.MapFS) {
			putTask(fsys, c, cmpAccept, `{"schema":"tbound-solution/v1","ops":[{"op":"remove","path":"missing"}]}`)
		}, "does not apply"},
		"fixture that already passes": {func(c *evalcorpus.Compliant, fsys fstest.MapFS) {
			putTask(fsys, c, cmpWeak, solutionJSON("cmp done"))
		}, "already passes"},
		"acceptance that nothing satisfies": {func(c *evalcorpus.Compliant, fsys fstest.MapFS) {
			putTask(fsys, c, cmpStrict, solutionJSON("cmp done"))
		}, "reference solution does not pass"},
	} {
		t.Run(name, func(t *testing.T) {
			cases, fsys := artifactCases(t)
			tc.mutate(cases[1].Compliant, fsys)
			err := CheckArtifactsWith(cases, fsys, nil)
			if err == nil || !strings.Contains(err.Error(), "cmp-1") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want a refusal naming cmp-1 and %q", err, tc.want)
			}
		})
	}
}

func TestCheckArtifactsOnlyChecksTasksOfCompliantCases(t *testing.T) {
	cases, fsys := artifactCases(t)
	// The other kinds pin no acceptance test; their cases are fine without those artifacts.
	if cases[0].Kind == evalcorpus.KindCompliant || cases[2].Kind == evalcorpus.KindCompliant {
		t.Fatal("unexpected case order")
	}
	if err := CheckArtifactsWith([]evalcorpus.Case{cases[0], cases[2]}, fsys, nil); err != nil {
		t.Fatalf("CheckArtifacts: %v", err)
	}
}
