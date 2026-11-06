package usdledger

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeChain writes recs as a chained ledger file the way commit does and
// returns the head after each record. It needs no lock, so Verify's read path
// is tested on every platform.
func writeChain(t *testing.T, path string, recs ...record) []string {
	t.Helper()
	var out []byte
	heads := []string{}
	prev := zeroHash
	for i, rec := range recs {
		rec.Seq, rec.Prev = uint64(i), prev
		body, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		out = append(append(out, body...), '\n')
		prev = hashHex(body)
		heads = append(heads, prev)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return heads
}

func TestVerifyReadsACopiedLedger(t *testing.T) {
	cfg := Config{LedgerID: "copy", CapMicroUSD: 1_000_000, MinPerUnitWorstCaseMicroUSD: 1000}
	path := filepath.Join(t.TempDir(), "usd-ledger")
	heads := writeChain(t, path,
		record{Kind: genesisKind, Ledger: "copy", Cap: 1_000_000},
		record{Kind: reserveKind, Unit: "u1", Micro: 5000},
		record{Kind: settleKind, Unit: "u1", Micro: 4000},
		record{Kind: reserveKind, Unit: "u2", Micro: 5000})
	before, _ := os.ReadFile(path)

	cfg.ExpectedHeadSHA256, cfg.AnchorRecords = heads[0], 1
	snap, err := Verify(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Records != 4 || snap.HeadSHA256 != heads[3] || snap.SettledMicroUSD != 4000 || snap.UncertainUnits != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("Verify changed the file")
	}

	cfg.ExpectedHeadSHA256, cfg.AnchorRecords = heads[3], 5
	if _, err := Verify(path, cfg); !errors.Is(err, ErrHeadMismatch) {
		t.Fatalf("checkpoint past the end: %v, want ErrHeadMismatch", err)
	}
	if _, err := Verify(filepath.Join(t.TempDir(), "absent"), cfg); !errors.Is(err, ErrHeadMismatch) {
		t.Fatalf("absent anchored ledger: %v, want ErrHeadMismatch", err)
	}
}
