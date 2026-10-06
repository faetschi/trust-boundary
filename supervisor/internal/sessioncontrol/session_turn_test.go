package sessioncontrol

import "testing"

func TestActiveTurnReferencesAreAssignedByManager(t *testing.T) {
	manager, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	entry := &session{id: "fixture-test", busy: true, turns: 2}
	for _, kind := range []string{"text_delta", "tool_pending", "tool_completed"} {
		manager.appendLocked(entry, Event{Kind: kind})
		if got := entry.events[len(entry.events)-1].TurnID; got != "turn-0000000000000002" {
			t.Fatalf("%s did not reference the admitted turn", kind)
		}
	}
	entry.busy = false
	manager.appendLocked(entry, Event{Kind: "text_delta"})
	if entry.events[len(entry.events)-1].TurnID != "" {
		t.Fatal("unowned output was attributed to a completed turn")
	}
}
