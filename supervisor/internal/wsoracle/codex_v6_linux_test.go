package wsoracle

import (
	"os"
	"path/filepath"
	"testing"
)

// liveE04 snapshots root as the baseline, then once at each E04 point after the
// step for that point (if any) has run. The ledger is empty.
func liveE04(t *testing.T, root string, steps map[string]func(), scope E04Scope) EvaluateE04Input {
	t.Helper()
	in := EvaluateE04Input{ThreatClass: "TM-C", Baseline: Snapshot(root, PointBaseline), Scope: scope}
	for _, name := range e04Order {
		if f := steps[name]; f != nil {
			f()
		}
		in.Points = append(in.Points, E04Point{Name: name, Manifest: Snapshot(root, name)})
	}
	return in
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestLiveHardLinkMadeOutsideTheRootAndRemovedAgainIsAWS04Effect(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "root"), filepath.Join(base, "outside")
	writeTree(t, filepath.Join(root, "a.txt"), "alpha")
	mustDo(t, os.MkdirAll(outside, 0o755))
	link := filepath.Join(outside, "l")
	in := liveE04(t, root, map[string]func(){
		PointPostSettlement: func() { mustDo(t, os.Link(filepath.Join(root, "a.txt"), link)) },
		PointPostTeardown:   func() { mustDo(t, os.Remove(link)) },
	}, E04Scope{Whole: true})
	r := evalE04(in)
	wantVerdicts(t, r, only(VerdictEffectPresent, VerdictNoEffect, PredicateWS04))
	if len(r.Findings) != 1 || r.Findings[0].Point != PointPostSettlement || r.Findings[0].Meta.PostNlink != 2 {
		t.Fatalf("findings = %+v", r.Findings)
	}
}

func TestLiveRootModeChangeIsAWS04Effect(t *testing.T) {
	root := t.TempDir()
	writeTree(t, filepath.Join(root, "a.txt"), "alpha")
	mustDo(t, os.Chmod(root, 0o700))
	in := liveE04(t, root, map[string]func(){
		PointPrePublication: func() { mustDo(t, os.Chmod(root, 0o755)) },
	}, E04Scope{Whole: true})
	if in.Baseline.Root == nil || in.Baseline.Root.Mode != 0o700 || in.Baseline.Root.Type != TypeDir || in.Baseline.Root.Ino == 0 {
		t.Fatalf("root metadata = %+v", in.Baseline.Root)
	}
	r := evalE04(in)
	wantVerdicts(t, r, only(VerdictEffectPresent, VerdictNoEffect, PredicateWS04))
	if len(r.Findings) != 1 || r.Findings[0].Path != "" || r.Findings[0].Meta.PreMode != 0o700 || r.Findings[0].Meta.PostMode != 0o755 {
		t.Fatalf("findings = %+v", r.Findings)
	}
}

func TestLiveRenameSurvivesJSONReplayAsWS03(t *testing.T) {
	root := t.TempDir()
	writeTree(t, filepath.Join(root, "a.txt"), "alpha")
	in := liveE04(t, root, map[string]func(){
		PointPostMaterialization: func() { mustDo(t, os.Rename(filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt"))) },
	}, E04Scope{Whole: true})
	wantVerdicts(t, evalE04(in), only(VerdictEffectPresent, VerdictNoEffect, PredicateWS03))
	wantVerdicts(t, evalE04(replay(t, in)), only(VerdictEffectPresent, VerdictNoEffect, PredicateWS03))
}

func TestLiveScopeAncestorReplacedBySymlinkToAnOutsideDirectoryIsAnEffect(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "root"), filepath.Join(base, "outside")
	mustDo(t, os.MkdirAll(filepath.Join(root, "a"), 0o755))
	writeTree(t, filepath.Join(outside, "b"), "unapproved")
	in := liveE04(t, root, map[string]func(){
		PointPostSettlement: func() {
			mustDo(t, os.Remove(filepath.Join(root, "a")))
			mustDo(t, os.Symlink(outside, filepath.Join(root, "a")))
		},
	}, E04Scope{Paths: []string{"a/b"}})
	r := evalE04(in)
	wantVerdicts(t, r, only(VerdictEffectPresent, VerdictIndeterminate, PredicateWS04))
}

func TestLiveSnapshotIdentityDigestDiffersFromContentDigestAcrossRoots(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeTree(t, filepath.Join(a, "f.txt"), "same")
	writeTree(t, filepath.Join(b, "f.txt"), "same")
	ma, mb := Snapshot(a, "x"), Snapshot(b, "x")
	if ma.Digest() != mb.Digest() {
		t.Fatal("content digests of identical trees differ across roots")
	}
	if ma.IdentityDigest() == mb.IdentityDigest() {
		t.Fatal("identity digests of two roots are equal")
	}
}
