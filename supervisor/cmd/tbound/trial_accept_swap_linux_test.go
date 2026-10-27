//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"

	"tbound/supervisor/internal/trialfix"
	"tbound/supervisor/internal/wsoracle"
)

func TestTrialAcceptSwapCannotStampFixtureDigestOnSolvedEvaluation(t *testing.T) {
	e := newTaskTestEnv(t)
	original, err := os.ReadFile(filepath.Join(e.root, "src/main.go"))
	if err != nil {
		t.Fatal(err)
	}
	fixtureDigest, err := trialfix.DigestOwned(e.spec, e.owner)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.snapshotDirFn = func(dir *os.File, label string) wsoracle.Manifest {
		calls++
		if calls == 2 {
			if err := os.WriteFile(filepath.Join(e.root, "src/main.go"), original, 0o644); err != nil {
				t.Error(err)
			}
		}
		m := wsoracle.SnapshotDir(dir, label)
		if calls == 1 {
			e.solve(t)
		}
		return m
	}
	doc, _, err := e.accept(t, trialTestCase)
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Passed {
		t.Fatalf("swap did not reach solved evaluation: %+v", doc)
	}
	if doc.TreeDigest == fixtureDigest {
		t.Fatal("solved evaluation stamped with untouched fixture digest")
	}
	// The solved content was copied before the live tree was restored.
	e.solve(t)
	want := wsSnapshotDigest(t, e)
	if doc.TreeDigest != want {
		t.Fatalf("stamped %s, evaluated content digest %s", doc.TreeDigest, want)
	}
}
