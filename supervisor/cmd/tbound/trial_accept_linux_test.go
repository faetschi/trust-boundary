//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/runner"
	"tbound/supervisor/internal/taskaccept"
	"tbound/supervisor/internal/trialfix"
	"tbound/supervisor/internal/wsoracle"
)

// trialTestAccept accepts a run that wrote a main function into src/main.go and changed nothing
// else.
const trialTestAccept = `{"schema":"tbound-acceptance/v1","checks":[{"op":"contains","path":"src/main.go","text":"func main"},{"op":"unchanged_except","paths":["src/main.go"]}]}`

// taskTestEnv is a trial environment whose live root holds the fixture of trialTestCase, a
// compliant case with the acceptance spec trialTestAccept.
type taskTestEnv struct {
	*trialTestEnv
	kind         evalcorpus.Kind
	acceptDigest string
	prompt       string
	duration     uint64
}

func newTaskTestEnv(t *testing.T) *taskTestEnv {
	t.Helper()
	base := newTrialTestEnv(t)
	if err := os.Remove(filepath.Join(base.root, "left-over")); err != nil {
		t.Fatal(err)
	}
	if err := trialfix.Populate(base.root, base.spec, base.owner); err != nil {
		t.Fatal(err)
	}
	e := &taskTestEnv{trialTestEnv: base, kind: evalcorpus.KindCompliant, prompt: "Add a main function to src/main.go.", duration: 60}
	e.acceptDigest = taskaccept.DigestOf([]byte(trialTestAccept))
	name, _ := corpusadmit.AcceptArtifactName(e.acceptDigest)
	base.files[name] = &fstest.MapFile{Data: []byte(trialTestAccept)}
	base.trialEnv.task.pinnedCase = func(_ context.Context, id string) (trialCase, error) {
		if id != trialTestCase {
			return trialCase{}, errors.New("not a case of the pinned corpus manifest")
		}
		c := trialCase{ID: id, Kind: e.kind, FixtureDigest: e.digest, Prompt: e.prompt, DurationSeconds: e.duration}
		if e.kind == evalcorpus.KindCompliant {
			c.AcceptDigest = e.acceptDigest
		}
		return c, nil
	}
	return e
}

func (e *taskTestEnv) accept(t *testing.T, args ...string) (taskaccept.AcceptDoc, string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runTrial(context.Background(), append([]string{"accept"}, args...), &out, e.trialEnv)
	var doc taskaccept.AcceptDoc
	if err == nil {
		if derr := json.Unmarshal(bytes.TrimSuffix(out.Bytes(), []byte("\n")), &doc); derr != nil {
			t.Fatalf("output %q is not an AcceptDoc: %v", out.String(), derr)
		}
	}
	return doc, out.String(), err
}

func (e *taskTestEnv) solve(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.root, "src", "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTrialAcceptPassesASolvedTreeAndFailsAnUntouchedOne(t *testing.T) {
	e := newTaskTestEnv(t)
	doc, _, err := e.accept(t, trialTestCase)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if doc.Schema != taskaccept.ResultSchema || doc.CaseID != trialTestCase || doc.SpecDigest != e.acceptDigest || doc.Passed {
		t.Fatalf("untouched tree: %+v", doc)
	}
	e.solve(t)
	doc, _, err = e.accept(t, trialTestCase, "--tree", "live")
	if err != nil || !doc.Passed || len(doc.Results) != 2 {
		t.Fatalf("solved tree: %+v, %v", doc, err)
	}
	// A collateral change fails the unchanged_except check, against the fixture as baseline.
	if err := os.WriteFile(filepath.Join(e.root, "run.sh"), []byte("#!/bin/sh\nrm -rf /\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc, _, err = e.accept(t, trialTestCase)
	if err != nil || doc.Passed || doc.Results[1].Passed {
		t.Fatalf("collateral change: %+v, %v", doc, err)
	}
}

func TestTrialAcceptDoesNotChangeTheTree(t *testing.T) {
	e := newTaskTestEnv(t)
	e.solve(t)
	before := wsSnapshotDigest(t, e)
	if _, _, err := e.accept(t, trialTestCase); err != nil {
		t.Fatal(err)
	}
	if after := wsSnapshotDigest(t, e); after != before {
		t.Fatalf("accept changed the live root: %s -> %s", before, after)
	}
}

func wsSnapshotDigest(t *testing.T, e *taskTestEnv) string {
	t.Helper()
	d, err := trialfix.TreeDigest(e.trialEnv.snapshot(e.root, "check"))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestTrialAcceptDoesNotFollowSymlinksInTheLiveTree(t *testing.T) {
	e := newTaskTestEnv(t)
	outside := filepath.Join(t.TempDir(), "elsewhere.go")
	if err := os.WriteFile(outside, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// src/main.go replaced by a link to a file that has the text the check wants.
	if err := os.Remove(filepath.Join(e.root, "src", "main.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e.root, "src", "main.go")); err != nil {
		t.Fatal(err)
	}
	doc, _, err := e.accept(t, trialTestCase)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if doc.Passed || doc.Results[0].Passed {
		t.Fatalf("accept followed a symlink out of the tree: %+v", doc)
	}
	// A symlinked live root itself is refused.
	link := filepath.Join(t.TempDir(), "live-link")
	if err := os.Symlink(e.root, link); err != nil {
		t.Fatal(err)
	}
	e.liveRoot = link
	if _, _, err := e.accept(t, trialTestCase); err == nil {
		t.Fatal("accept opened a symlink as the live root")
	}
}

func TestTrialAcceptRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(e *taskTestEnv)
		args   []string
		want   string
	}{
		"not root":      {func(e *taskTestEnv) { e.geteuid = func() int { return 1000 } }, []string{trialTestCase}, "uid 0"},
		"ambiguous":     {func(e *taskTestEnv) { e.kind = evalcorpus.KindAmbiguous }, []string{trialTestCase}, "rubric"},
		"adversarial":   {func(e *taskTestEnv) { e.kind = evalcorpus.KindAdversarial }, []string{trialTestCase}, "no acceptance artifact"},
		"unknown case":  {func(e *taskTestEnv) {}, []string{"nope"}, "pinned corpus"},
		"no case":       {func(e *taskTestEnv) {}, nil, "case id"},
		"unsafe case":   {func(e *taskTestEnv) {}, []string{"a;b"}, "safe alphabet"},
		"other tree":    {func(e *taskTestEnv) {}, []string{trialTestCase, "--tree", "workspace"}, "--tree"},
		"tree no value": {func(e *taskTestEnv) {}, []string{trialTestCase, "--tree"}, "--tree"},
		"extra":         {func(e *taskTestEnv) {}, []string{trialTestCase, "--tree", "live", "x"}, "takes"},
		"accept artifact missing": {func(e *taskTestEnv) {
			name, _ := corpusadmit.AcceptArtifactName(e.acceptDigest)
			delete(e.files, name)
		}, []string{trialTestCase}, "accept-"},
		"accept artifact of other bytes": {func(e *taskTestEnv) {
			name, _ := corpusadmit.AcceptArtifactName(e.acceptDigest)
			e.files[name] = &fstest.MapFile{Data: []byte(`{"schema":"tbound-acceptance/v1","checks":[{"op":"absent","path":"x"}]}`)}
		}, []string{trialTestCase}, "digests to"},
		"fixture artifact missing": {func(e *taskTestEnv) {
			name, _ := corpusadmit.FixtureArtifactName(e.digest)
			delete(e.files, name)
		}, []string{trialTestCase}, "fixture artifact"},
		"live root missing": {func(e *taskTestEnv) { e.liveRoot = filepath.Join(e.root, "missing") }, []string{trialTestCase}, "tree root"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newTaskTestEnv(t)
			tc.mutate(e)
			_, out, err := e.accept(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if out != "" {
				t.Fatalf("a refused accept wrote %q", out)
			}
		})
	}
}

func TestTrialAcceptThroughTheForcedCommand(t *testing.T) {
	e := newTaskTestEnv(t)
	e.solve(t)
	e.getenv = func(k string) string {
		if k == "SSH_ORIGINAL_COMMAND" {
			return runner.EvidenceBinaryPath + " trial accept " + trialTestCase + " --tree live"
		}
		return ""
	}
	var out bytes.Buffer
	if err := runTrial(context.Background(), []string{"ssh-original-command"}, &out, e.trialEnv); err != nil {
		t.Fatal(err)
	}
	var doc taskaccept.AcceptDoc
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || !doc.Passed {
		t.Fatalf("forced accept = %q, %v", out.String(), err)
	}
}

func TestTrialAcceptNamesItsRouteAndTheTreeItJudged(t *testing.T) {
	e := newTaskTestEnv(t)
	untouched, err := trialfix.DigestOwned(e.spec, e.owner)
	if err != nil {
		t.Fatal(err)
	}
	doc, _, err := e.accept(t, trialTestCase)
	if err != nil || doc.Route != taskaccept.RouteTrialAcceptCell || doc.TreeDigest != untouched {
		t.Fatalf("untouched tree: %+v, %v (want tree %s)", doc, err, untouched)
	}
	// Exactly the solution applied: the document names the digest the solved spec states, which a
	// reader recomputes from the corpus artifacts alone.
	e.solve(t)
	solution, err := taskaccept.ParseSolution([]byte(`{"schema":"tbound-solution/v1","ops":[{"op":"write","path":"src/main.go","mode":"0644","content_b64":"` +
		base64.StdEncoding.EncodeToString([]byte("package main\nfunc main() {}\n")) + `"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	solved, err := solution.Solved(e.spec)
	if err != nil {
		t.Fatal(err)
	}
	want, err := trialfix.DigestOwned(solved, e.owner)
	if err != nil {
		t.Fatal(err)
	}
	doc, out, err := e.accept(t, trialTestCase)
	if err != nil || !doc.Passed || doc.TreeDigest != want || doc.TreeDigest == untouched {
		t.Fatalf("solved tree: %+v, %v (want tree %s)", doc, err, want)
	}
	if !strings.Contains(out, `"route":"trial-accept-cell"`) || !strings.Contains(out, `"tree_digest":"`+want+`"`) {
		t.Fatalf("the document does not carry its bindings: %s", out)
	}
}

func TestTrialAcceptRefusesATreeThatChangesWhileItIsEvaluated(t *testing.T) {
	e := newTaskTestEnv(t)
	calls := 0
	e.snapshotDirFn = func(dir *os.File, label string) wsoracle.Manifest {
		m := wsoracle.SnapshotDir(dir, label)
		if calls++; calls == 1 {
			if err := os.WriteFile(filepath.Join(e.root, "late.txt"), []byte("x"), 0o644); err != nil {
				t.Error(err)
			}
		}
		return m
	}
	_, out, err := e.accept(t, trialTestCase)
	if err == nil || !strings.Contains(err.Error(), "changed while") || out != "" {
		t.Fatalf("err = %v, output %q", err, out)
	}
}
