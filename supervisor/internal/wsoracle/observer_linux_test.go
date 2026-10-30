package wsoracle

import (
	"os"
	"path/filepath"
	"testing"
)

// fixture is one live tree and one separate sealed-generation tree, both under
// a private temp base. The live tree starts with keep.txt.
type fixture struct {
	live   string
	sealed string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	base := t.TempDir()
	f := fixture{live: filepath.Join(base, "live"), sealed: filepath.Join(base, "sealed")}
	writeTree(t, filepath.Join(f.live, "keep.txt"), "K")
	writeTree(t, filepath.Join(f.sealed, "keep.txt"), "K")
	return f
}

func mustEntry(t *testing.T, m Manifest, path string) Entry {
	t.Helper()
	e, ok := findEntry(m, path)
	if !ok {
		t.Fatalf("manifest %q has no entry %q", m.Label, path)
	}
	return e
}

// ws05Observed runs the observer over a real sealed tree and live tree. It
// applies the publication step between the pre and post snapshots.
func ws05Observed(t *testing.T, f fixture, ledger []LedgerEntry, apply func(live string)) Report {
	t.Helper()
	baseline := Snapshot(f.live, "baseline")
	sealed := Snapshot(f.sealed, "sealed")
	pre := Snapshot(f.live, "pre-publication")
	if apply != nil {
		apply(f.live)
	}
	post := Snapshot(f.live, "post-publication")
	return EvaluateWS05(WS05Input{
		Baseline:        baseline,
		Ledger:          ledger,
		Sealed:          sealed,
		PrePublication:  pre,
		PostPublication: post,
	})
}

func TestEndToEndWS05UnattributedWriteReachingLiveIsDetected(t *testing.T) {
	f := newFixture(t)
	writeTree(t, filepath.Join(f.sealed, "rogue.txt"), "R")
	r := ws05Observed(t, f, nil, func(live string) {
		writeTree(t, filepath.Join(live, "rogue.txt"), "R")
	})
	wantVerdict(t, r, VerdictEffectPresent)
	wantSingleFinding(t, r, "rogue.txt", ChangeCreate, ReasonUnattributedLiveChange)
	if !r.Findings[0].MatchesSealed {
		t.Fatalf("applied object should match the sealed object: %+v", r.Findings[0])
	}
	wantUnattributed(t, r, "rogue.txt")
}

func TestEndToEndWS05ApprovedWriteAppliedExactlyIsNoEffect(t *testing.T) {
	f := newFixture(t)
	writeTree(t, filepath.Join(f.sealed, "ok.txt"), "OK")
	sealed := Snapshot(f.sealed, "sealed")
	approved := mustEntry(t, sealed, "ok.txt")
	ledger := []LedgerEntry{approve("e1", 1, LedgerChange{Path: "ok.txt", After: &approved})}
	r := ws05Observed(t, f, ledger, func(live string) {
		writeTree(t, filepath.Join(live, "ok.txt"), "OK")
	})
	wantVerdict(t, r, VerdictNoEffect)
	wantNoFindings(t, r)
	wantUnattributed(t, r)
}

func TestEndToEndWS05UnattributedDeletionReachingLiveIsDetected(t *testing.T) {
	f := newFixture(t)
	writeTree(t, filepath.Join(f.live, "old.txt"), "O")
	r := ws05Observed(t, f, nil, func(live string) {
		if err := os.Remove(filepath.Join(live, "old.txt")); err != nil {
			t.Fatal(err)
		}
	})
	wantVerdict(t, r, VerdictEffectPresent)
	wantSingleFinding(t, r, "old.txt", ChangeDelete, ReasonUnattributedLiveChange)
	wantUnattributed(t, r, "old.txt")
}

func TestEndToEndWS05StagedUnattributedWriteThatIsBlockedIsNoEffect(t *testing.T) {
	f := newFixture(t)
	writeTree(t, filepath.Join(f.sealed, "rogue.txt"), "R")
	r := ws05Observed(t, f, nil, nil)
	wantVerdict(t, r, VerdictNoEffect)
	wantNoFindings(t, r)
	wantUnattributed(t, r, "rogue.txt")
}

func TestEndToEndWS05VanishedLiveRootIsEvidenceLost(t *testing.T) {
	f := newFixture(t)
	baseline := Snapshot(f.live, "baseline")
	sealed := Snapshot(f.sealed, "sealed")
	pre := Snapshot(f.live, "pre-publication")
	if err := os.RemoveAll(f.live); err != nil {
		t.Fatal(err)
	}
	post := Snapshot(f.live, "post-publication")
	r := EvaluateWS05(WS05Input{Baseline: baseline, Sealed: sealed, PrePublication: pre, PostPublication: post})
	wantVerdict(t, r, VerdictEvidenceLost)
	wantGap(t, r, GapRootUnavailable, "post-publication")
}

func TestEndToEndWS05ReplacedLiveRootIsEvidenceLost(t *testing.T) {
	f := newFixture(t)
	baseline := Snapshot(f.live, "baseline")
	sealed := Snapshot(f.sealed, "sealed")
	pre := Snapshot(f.live, "pre-publication")
	// The old root keeps its inode under a new name, so the replacement cannot
	// reuse it and the identity comparison is meaningful.
	if err := os.Rename(f.live, f.live+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.live, 0o755); err != nil {
		t.Fatal(err)
	}
	post := Snapshot(f.live, "post-publication")
	r := EvaluateWS05(WS05Input{Baseline: baseline, Sealed: sealed, PrePublication: pre, PostPublication: post})
	wantVerdict(t, r, VerdictEvidenceLost)
	wantGap(t, r, GapRootIdentity, "post-publication")
}

func TestEndToEndWS05UnreadableLiveFileIsEvidenceLost(t *testing.T) {
	skipIfRoot(t)
	f := newFixture(t)
	locked := filepath.Join(f.live, "locked.txt")
	writeTree(t, locked, "L")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	restoreModes(t, locked)
	r := ws05Observed(t, f, nil, nil)
	wantVerdict(t, r, VerdictEvidenceLost)
	wantGap(t, r, GapUnreadable, "pre-publication")
}

func TestEndToEndWS06HostEditThenStaleOverwriteIsDetected(t *testing.T) {
	f := newFixture(t)
	writeTree(t, filepath.Join(f.live, "f.txt"), "A")
	baseline := Snapshot(f.live, "baseline")
	writeTree(t, filepath.Join(f.live, "f.txt"), "H") // host edit after the baseline
	pre := Snapshot(f.live, "pre-publication")
	writeTree(t, filepath.Join(f.live, "f.txt"), "P") // publication overwrites
	post := Snapshot(f.live, "post-publication")
	r := EvaluateWS06(WS06Input{Baseline: baseline, AffectedPaths: []string{"f.txt"}, PrePublication: pre, PostPublication: post, LockIdentity: "lock-1", PublicationResult: "SUCCEEDED"})
	wantVerdict(t, r, VerdictEffectPresent)
	wantSingleFinding(t, r, "f.txt", ChangeModify, ReasonStaleBaselineOverwrite)
	if r.Findings[0].ReferenceSHA256 != digestOf("A") {
		t.Fatalf("reference should be the baseline digest: %+v", r.Findings[0])
	}
}

func TestEndToEndWS06OverwriteWithoutHostEditIsNoEffect(t *testing.T) {
	f := newFixture(t)
	writeTree(t, filepath.Join(f.live, "f.txt"), "A")
	baseline := Snapshot(f.live, "baseline")
	pre := Snapshot(f.live, "pre-publication")
	writeTree(t, filepath.Join(f.live, "f.txt"), "P")
	post := Snapshot(f.live, "post-publication")
	r := EvaluateWS06(WS06Input{Baseline: baseline, AffectedPaths: []string{"f.txt"}, PrePublication: pre, PostPublication: post, LockIdentity: "lock-1"})
	wantVerdict(t, r, VerdictNoEffect)
	wantNoFindings(t, r)
}

func TestEndToEndWS06HostEditThenAbortedPublicationIsNoEffect(t *testing.T) {
	f := newFixture(t)
	writeTree(t, filepath.Join(f.live, "f.txt"), "A")
	baseline := Snapshot(f.live, "baseline")
	writeTree(t, filepath.Join(f.live, "f.txt"), "H")
	pre := Snapshot(f.live, "pre-publication")
	post := Snapshot(f.live, "post-publication")
	r := EvaluateWS06(WS06Input{Baseline: baseline, AffectedPaths: []string{"f.txt"}, PrePublication: pre, PostPublication: post, LockIdentity: "lock-1"})
	wantVerdict(t, r, VerdictNoEffect)
	wantNoFindings(t, r)
}

func TestEndToEndWS06VanishedLiveRootIsEvidenceLost(t *testing.T) {
	f := newFixture(t)
	writeTree(t, filepath.Join(f.live, "f.txt"), "A")
	baseline := Snapshot(f.live, "baseline")
	pre := Snapshot(f.live, "pre-publication")
	if err := os.RemoveAll(f.live); err != nil {
		t.Fatal(err)
	}
	post := Snapshot(f.live, "post-publication")
	r := EvaluateWS06(WS06Input{Baseline: baseline, AffectedPaths: []string{"f.txt"}, PrePublication: pre, PostPublication: post, LockIdentity: "lock-1"})
	wantVerdict(t, r, VerdictEvidenceLost)
	wantGap(t, r, GapRootUnavailable, "post-publication")
}

// Since v7 a report's manifest references cover object identity, so the same
// workload in two roots gives two reports with two digests, while the content
// digest of the manifests behind them stays equal. One report's digest is stable.
func TestEndToEndReportDigestCoversIdentityAndManifestContentDigestIsReproducible(t *testing.T) {
	run := func() (string, string) {
		f := newFixture(t)
		writeTree(t, filepath.Join(f.live, "f.txt"), "A")
		baseline := Snapshot(f.live, "baseline")
		writeTree(t, filepath.Join(f.live, "f.txt"), "H")
		pre := Snapshot(f.live, "pre-publication")
		writeTree(t, filepath.Join(f.live, "f.txt"), "P")
		post := Snapshot(f.live, "post-publication")
		r := EvaluateWS06(WS06Input{Baseline: baseline, AffectedPaths: []string{"f.txt"}, PrePublication: pre, PostPublication: post, LockIdentity: "lock-1"})
		wantVerdict(t, r, VerdictEffectPresent)
		d, err := r.Digest()
		if err != nil {
			t.Fatal(err)
		}
		if again, _ := r.Digest(); again != d {
			t.Fatalf("one report has two digests: %s vs %s", d, again)
		}
		for i, ref := range r.Witness.Manifests {
			if want := []*Manifest{&baseline, &pre, &post}[i].IdentityDigest(); ref.Digest != want {
				t.Fatalf("manifest ref %d = %s, want the identity digest %s", i, ref.Digest, want)
			}
		}
		return d, baseline.Digest() + pre.Digest() + post.Digest()
	}
	ra, ca := run()
	rb, cb := run()
	if ca != cb {
		t.Fatalf("identical workloads in different roots gave different content digests")
	}
	if ra == rb {
		t.Fatalf("reports over different objects share the digest %s: the references do not cover identity", ra)
	}
}
