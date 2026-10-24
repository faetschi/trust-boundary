//go:build linux

package main

import (
	"strings"
	"testing"

	"tbound/supervisor/internal/sessionlaunch"
)

func TestRecoveryPreviewOpensAGovernedStoreWithAnEffect(t *testing.T) {
	retained := runProductPredecessorTurn(t)
	journal, err := openExistingRecoveryJournal(retained.paths.StoreJournalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	trace, err := journal.Trace()
	if err != nil {
		t.Fatal(err)
	}
	effects := 0
	for _, effect := range trace.Effects {
		if strings.HasPrefix(effect.ID, "sessionrepo-effect-") {
			effects++
		}
	}
	if effects == 0 {
		t.Fatal("the retained governed Store has no operation effect")
	}
	root, err := openExistingPrivateRecoveryDirectory(retained.paths.StoreRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	options := retained.options
	options.Journal = journal
	managed, err := sessionlaunch.OpenRecoveryPreview(root, options)
	if err != nil {
		t.Fatalf("recovery preview of a governed Store with %d effects: %v", effects, err)
	}
	if err := managed.Close(); err != nil {
		t.Fatal(err)
	}
}
