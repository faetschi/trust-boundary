package main

import (
	"strings"
	"testing"

	"tbound/supervisor/internal/attacker"
)

func TestPivotStatesCountsDistinctLibrarySteps(t *testing.T) {
	lib := attackerSpecFixture().Library
	lib.Classes = append(lib.Classes, attacker.SpecClass{ID: "again", Steps: []string{"ts-1", "x-1"}})
	if got := pivotStates(lib); got != 6 {
		t.Fatalf("L = %d, want the 6 distinct steps", got)
	}
}

func TestCapsRefusesAManifestWithoutAnAttackerPin(t *testing.T) {
	e := newEnv(t)
	e.write()
	_, err := e.cliCfg(nil, "caps", "--out", e.path("caps.json"))
	if err == nil || !strings.Contains(err.Error(), "pins no attacker artifact") {
		t.Fatalf("got %v, want a refusal naming the missing attacker pin", err)
	}
}
