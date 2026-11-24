package researchroute

import (
	"errors"
	"os"
	"strings"
	"testing"

	"tbound/supervisor/internal/researchprofile"
)

const committedDir = "../../../infra/guest/research/"

// Pins the committed research profile and ledger configuration to each other:
// a change to max_input_tokens or max_output_tokens without the ledger stop
// value fails here.
func TestCommittedResearchConfig(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(committedDir + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	profileJSON := read("research-profile.json")
	profile, err := researchprofile.Load(strings.NewReader(profileJSON))
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}

	// Only frozen_at is open until the F-H1 freeze.
	reason := profile.FrozenReason()
	if !errors.Is(reason, researchprofile.ErrOpenField) || !strings.HasSuffix(reason.Error(), ": frozen_at") {
		t.Fatalf("FrozenReason = %v, want ErrOpenField naming frozen_at", reason)
	}
	// FrozenReason reports the first open field in order, with frozen_at last,
	// so a frozen copy proves nothing else is open.
	frozenJSON := strings.Replace(profileJSON, `"frozen_at": null`, `"frozen_at": "2026-11-08T00:00:00Z"`, 1)
	if frozenJSON == profileJSON {
		t.Fatal(`profile has no "frozen_at": null line`)
	}
	frozen, err := researchprofile.Load(strings.NewReader(frozenJSON))
	if err != nil {
		t.Fatalf("load frozen copy: %v", err)
	}
	if !frozen.Frozen() {
		t.Fatalf("frozen copy is not frozen: %v", frozen.FrozenReason())
	}

	ledger, err := LoadLedgerConfig(strings.NewReader(read("research-ledger.json")))
	if err != nil {
		t.Fatalf("load ledger config: %v", err)
	}
	unit, err := frozen.WorstCaseUnitMicroUSD(*frozen.MaxInputTokens, *frozen.MaxOutputTokens)
	if err != nil {
		t.Fatal(err)
	}
	if uint64(ledger.MinPerUnitWorstCaseMicroUSD) != unit {
		t.Fatalf("min_per_unit_worst_case_micro_usd = %d, want %d (worst case of one full unit)", ledger.MinPerUnitWorstCaseMicroUSD, unit)
	}
	if ledger.CapMicroUSD != 10000000 || ledger.ReserveBasis != ReserveBasisRequest {
		t.Fatalf("cap %d basis %q, want 10000000 and %q", ledger.CapMicroUSD, ledger.ReserveBasis, ReserveBasisRequest)
	}
}
