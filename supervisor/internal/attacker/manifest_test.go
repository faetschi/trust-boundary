package attacker

import "testing"

// D32 item 2: the transition input is limited to the enforcement surface's
// reason code and capability text. An oracle "manifested" verdict is not
// feedback. The runner records it with MarkManifested.

func TestManifestedReasonFeedbackIsInvalidNotManifested(t *testing.T) {
	m := mustMachine(t)
	st, _ := m.Start()
	st, a := m.Next(st, fb(reasonManifested, textShell))
	if a.Kind != ActionStop || a.Stop != StopInvalid {
		t.Fatalf("got %+v, want Stop(Invalid)", a)
	}
	if st.Stop() != StopInvalid {
		t.Fatalf("state stop = %v, want Invalid", st.Stop())
	}
}

func TestManifestedReasonIsNotConstructibleFeedback(t *testing.T) {
	if _, err := NewFeedback(testPolicy(), reasonManifested, textShell); err == nil {
		t.Fatal("NewFeedback accepted the oracle manifested reason")
	}
	if err := testPolicy().CheckFeedback(fb(reasonManifested, textRead)); err == nil {
		t.Fatal("CheckFeedback accepted the oracle manifested reason")
	}
}

func TestManifestedReasonInReplayFailsClosed(t *testing.T) {
	acts, err := Replay(testLibrary(), testSeed, testCaps(), testPolicy(),
		[]Feedback{fb(reasonDeniedTool, textShell), fb(reasonManifested, textShell), fb(reasonDeniedPath, textRead)})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if acts[2].Stop != StopInvalid || acts[3].Stop != StopInvalid {
		t.Fatalf("actions = %+v, want Invalid then sticky Invalid", acts)
	}
}

func TestMarkManifestedGivesTerminalManifested(t *testing.T) {
	m := mustMachine(t)
	st, _ := m.Start()
	st, a := m.MarkManifested(st)
	if a.Kind != ActionStop || a.Stop != StopManifested {
		t.Fatalf("got %+v, want Stop(Manifested)", a)
	}
	if st.Stop() != StopManifested {
		t.Fatalf("state stop = %v, want Manifested", st.Stop())
	}
	// Sticky: neither feedback, a second mark, nor a charge changes it.
	d := st.Digest()
	next, a := m.Next(st, fb(reasonDeniedTool, textShell))
	if a.Stop != StopManifested || next.Digest() != d {
		t.Fatalf("Next after manifested: %+v", a)
	}
	next, a = m.MarkManifested(st)
	if a.Stop != StopManifested || next.Digest() != d {
		t.Fatalf("second mark: %+v", a)
	}
	if m.Charge(st, Usage{ToolCalls: 1}).Digest() != d {
		t.Fatal("Charge changed a manifested state")
	}
}

func TestMarkManifestedBeatsExhaustion(t *testing.T) {
	caps := testCaps()
	caps.ToolCalls = 2
	m, err := NewMachine(testLibrary(), testSeed, caps, testPolicy())
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	st, _ := m.Start()
	st = m.Charge(st, Usage{ToolCalls: 9})
	if _, a := m.MarkManifested(st); a.Stop != StopManifested {
		t.Fatalf("manifested effect must be reported over budget exhaustion: got %+v", a)
	}
}

func TestMarkManifestedKeepsAnEarlierTerminal(t *testing.T) {
	m := mustMachine(t)
	st, _ := m.Start()
	st, _ = m.Next(st, fb("unregistered_reason", textShell))
	if _, a := m.MarkManifested(st); a.Stop != StopInvalid {
		t.Fatalf("got %+v, want the earlier Invalid kept", a)
	}
}

func TestMarkManifestedFailsClosedOnBadStates(t *testing.T) {
	var zero Machine
	var zs State
	if _, a := zero.MarkManifested(zs); a.Stop != StopInvalid {
		t.Fatalf("zero machine: %+v", a)
	}
	m := mustMachine(t)
	if _, a := m.MarkManifested(zs); a.Stop != StopInvalid {
		t.Fatalf("zero state: %+v", a)
	}
	other, err := NewMachine(testLibrary(), []byte("seed-B"), testCaps(), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	stOther, _ := other.Start()
	if _, a := m.MarkManifested(stOther); a.Stop != StopInvalid {
		t.Fatalf("state from another machine: %+v", a)
	}
}
