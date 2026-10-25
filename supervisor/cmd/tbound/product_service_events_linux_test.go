//go:build linux

package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/productprotocol"
	"tbound/supervisor/internal/productsession"
)

func diffOf(before, after string) string {
	split := func(s string) []string {
		lines := strings.SplitAfter(s, "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		return lines
	}
	return renderUnifiedDiff(diffLines(split(before), split(after)))
}

func TestProductServiceUnifiedDiff(t *testing.T) {
	for name, tc := range map[string]struct{ before, after, want string }{
		"create": {"", "a\nb\n", "@@ -0,0 +1,2 @@\n+a\n+b\n"},
		"delete": {"a\nb\n", "", "@@ -1,2 +0,0 @@\n-a\n-b\n"},
		"modify in the middle": {"1\n2\n3\n4\n5\n6\n7\n8\n9\n", "1\n2\n3\n4\nfive\n6\n7\n8\n9\n",
			"@@ -2,7 +2,7 @@\n 2\n 3\n 4\n-5\n+five\n 6\n 7\n 8\n"},
		"two far hunks": {"1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n", "one\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\ntwelve\n",
			"@@ -1,4 +1,4 @@\n-1\n+one\n 2\n 3\n 4\n@@ -9,4 +9,4 @@\n 9\n 10\n 11\n-12\n+twelve\n"},
		"missing final newline": {"a\n", "a", "@@ -1,1 +1,1 @@\n-a\n+a\n\\ No newline at end of file\n"},
		"unchanged":             {"a\n", "a\n", ""},
	} {
		if got := diffOf(tc.before, tc.after); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, tc.want)
		}
	}
}

func TestProductServiceCandidateChanges(t *testing.T) {
	file := func(digest, metadata string) delta.ObjectState {
		return delta.ObjectState{Exists: true, Type: delta.ObjectRegular, ContentDigest: digest, MetadataIdentity: metadata}
	}
	dir := delta.ObjectState{Exists: true, Type: delta.ObjectDirectory, MetadataIdentity: "d"}
	baseline := delta.TreeManifest{Objects: []delta.ManifestObject{
		{Path: "a", State: file("1", "m")}, {Path: "b", State: file("2", "m")}, {Path: "c", State: file("3", "m")},
		{Path: "d", State: dir}, {Path: "same", State: file("4", "m")}, {Path: "z", State: file("5", "m")}}}
	tip := delta.TreeManifest{Objects: []delta.ManifestObject{
		{Path: "a", State: file("1", "x")}, {Path: "b", State: file("9", "m")}, {Path: "e", State: file("6", "m")},
		{Path: "f", State: dir}, {Path: "same", State: file("4", "m")}, {Path: "z", State: dir}}}
	got := map[string]string{}
	for _, change := range candidateChanges(baseline, tip) {
		got[change.path] = change.op
	}
	want := map[string]string{"a": "chmod", "b": "modify", "c": "delete", "d": "delete", "e": "create", "f": "create", "z": "modify"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changes %v, want %v", got, want)
	}
	if len(candidateChanges(baseline, baseline)) != 0 {
		t.Fatal("an unchanged tree has changes")
	}
}

func TestProductServiceBoundTextCutsAtRuneBoundaries(t *testing.T) {
	if got := boundText("short", 10); got != "short" {
		t.Fatalf("short text changed: %q", got)
	}
	got := boundText(strings.Repeat("é", 10), 5)
	if !strings.HasSuffix(got, "\n[truncated]") || strings.Contains(got, "\ufffd") || len(got) > 5+len("\n[truncated]") {
		t.Fatalf("cut text: %q", got)
	}
}

func TestProductServiceObserveWindowIsBounded(t *testing.T) {
	var events []productprotocol.Event
	for i := 0; i < productprotocol.MaxEvents+10; i++ {
		events = append(events, productprotocol.Event{Seq: uint64(i), Kind: productprotocol.EventAssistantMessage})
	}
	window := observeWindow(events, 3)
	if len(window.Events) != productprotocol.MaxEvents || window.NextCursor != 3+productprotocol.MaxEvents {
		t.Fatalf("event cap: %d events, next %d", len(window.Events), window.NextCursor)
	}
	big := strings.Repeat("x", productEventMaxTextBytes)
	events = nil
	for i := 0; i < 100; i++ {
		events = append(events, productprotocol.Event{Seq: uint64(i), Kind: productprotocol.EventAssistantMessage, Text: big})
	}
	window = observeWindow(events, 0)
	size, _ := jsonSize(window)
	if len(window.Events) == 0 || len(window.Events) == 100 || size >= productprotocol.MaxResponseBytes ||
		window.NextCursor != uint64(len(window.Events)) {
		t.Fatalf("size cap: %d events, %d bytes, next %d", len(window.Events), size, window.NextCursor)
	}
	if end := observeWindow(events, 100); len(end.Events) != 0 || end.NextCursor != 100 {
		t.Fatalf("window at the end: %+v", end)
	}
}

// A cancel request of a turn that is still running has no recorded position among the provider
// events, so it is withheld until the turn leaves the linked state; the list then only grows at its end.
func TestProductServiceEventListOnlyGrowsAtItsEnd(t *testing.T) {
	root := privateGovernedTestRoot(t)
	journalPath := filepath.Join(root, productTurnProviderJournal)
	journal, err := audit.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	projector := newProductEventProjector(&productTurnEnv{})
	turn := productsession.Turn{ID: "psess-x/turn-1", Prompt: "hi", State: productsession.StateLinked, CancelRequested: true,
		Link: &productsession.TurnLink{ProviderJournalPath: journalPath}}
	kinds := func(turn productsession.Turn) []string {
		events, err := projector.turn(turn)
		if err != nil {
			t.Fatal(err)
		}
		return eventKinds(events)
	}
	running := kinds(turn)
	if want := []string{productprotocol.EventTurnAdmitted, productprotocol.EventTurnLinked}; !reflect.DeepEqual(running, want) {
		t.Fatalf("running turn with a cancel request: %v", running)
	}
	turn.State, turn.Outcome = productsession.StateTerminal, productsession.OutcomeCancelled
	settled := kinds(turn)
	if want := []string{productprotocol.EventTurnAdmitted, productprotocol.EventTurnLinked, productprotocol.EventTurnCancelRequested,
		productprotocol.EventTurnTerminal}; !reflect.DeepEqual(settled, want) || !reflect.DeepEqual(settled[:len(running)], running) {
		t.Fatalf("settled turn: %v", settled)
	}
	if _, cached := projector.settled[turn.ID]; !cached {
		t.Fatal("a settled turn was not cached")
	}
	refused := productsession.Turn{ID: "psess-x/turn-2", State: productsession.StateTerminal, Outcome: productsession.OutcomeFailed,
		RefusalCode: productsession.CodeStaleBaseline}
	events, err := projector.turn(refused)
	if err != nil || events[len(events)-1].Reason != string(productsession.CodeStaleBaseline) {
		t.Fatalf("refused turn: %+v %v", events, err)
	}
}
