package calibration

import (
	"errors"
	"strings"
	"testing"
)

func pilotCase(id string, attempts, toolCalls, wallMS int) PilotCase {
	return PilotCase{CaseID: id, Status: Manifested, Adaptive: Run{Manifested: true, Attempts: attempts, ToolCalls: toolCalls, WallMS: wallMS}}
}

func TestDerivePilotCapsDoublesThePilotMaximaAndQ95(t *testing.T) {
	cases := []PilotCase{pilotCase("a", 1, 4, 1000), pilotCase("b", 3, 9, 2000), pilotCase("c", 2, 5, 3000)}
	for _, tc := range []struct {
		L, wantIterations int
	}{{5, 6}, {7, 7}} {
		got, err := DerivePilotCaps(cases, tc.L, true)
		if err != nil {
			t.Fatal(err)
		}
		want := Caps{Scripted: true, Iterations: tc.wantIterations, ToolCalls: 18, WallMS: 6000}
		if got.Q95 != 3 || got.L != tc.L || got.Caps != want || got.PilotMax != (Caps{Scripted: true, ToolCalls: 9, WallMS: 3000}) {
			t.Fatalf("L=%d: got %+v, want q95 3 and caps %+v", tc.L, got, want)
		}
	}
}

func TestDerivePilotCapsRefusesAPilotWithACaseThatDidNotManifest(t *testing.T) {
	cases := []PilotCase{pilotCase("a", 1, 4, 1000), {CaseID: "b", Status: ChannelNonDiscriminative}}
	_, err := DerivePilotCaps(cases, 5, true)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("got %v, want an invalid derivation naming case b", err)
	}
	if _, err := DerivePilotCaps(nil, 5, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an empty pilot: got %v, want ErrInvalid", err)
	}
}
