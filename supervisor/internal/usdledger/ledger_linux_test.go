//go:build linux

package usdledger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	testCap       = 1000
	testMin       = 100
	helperCap     = 1000
	helperMin     = 400
	helperModeEnv = "TB_USDLEDGER_HELPER_MODE"
	helperPathEnv = "TB_USDLEDGER_HELPER_PATH"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// testConfig may create its ledger, so tests that start from an empty path use it
// as-is. Tests that check the default refusal build their own Config.
func testConfig() Config {
	return Config{LedgerID: "research-test", CapMicroUSD: testCap, MinPerUnitWorstCaseMicroUSD: testMin, AllowGenesis: true}
}

func helperConfig() Config {
	return Config{LedgerID: "research-test", CapMicroUSD: helperCap, MinPerUnitWorstCaseMicroUSD: helperMin, AllowGenesis: true}
}

// withAfterFlockHook installs a test seam that runs after the ledger lock is taken.
func withAfterFlockHook(t *testing.T, hook func(path string)) {
	t.Helper()
	prev := afterFlockHook
	afterFlockHook = hook
	t.Cleanup(func() { afterFlockHook = prev })
}

// withSyncHook makes the next record fsync use hook. The hook runs after the
// record bytes are written, which is the "landed but not durable" window.
func withSyncHook(t *testing.T, hook func(*os.File) error) {
	t.Helper()
	prev := syncRecord
	syncRecord = hook
	t.Cleanup(func() { syncRecord = prev })
}

func openTest(t *testing.T, path string, cfg Config) *Ledger {
	t.Helper()
	l, err := Open(path, cfg)
	must(t, err)
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func snapshotOf(t *testing.T, l *Ledger) Snapshot {
	t.Helper()
	s, err := l.Snapshot()
	must(t, err)
	return s
}

func wantTotals(t *testing.T, s Snapshot, open, uncertain, settled int64) {
	t.Helper()
	if s.OpenMicroUSD != open || s.UncertainMicroUSD != uncertain || s.SettledMicroUSD != settled {
		t.Fatalf("open/uncertain/settled = %d/%d/%d, want %d/%d/%d",
			s.OpenMicroUSD, s.UncertainMicroUSD, s.SettledMicroUSD, open, uncertain, settled)
	}
	committed := open + uncertain + settled
	if s.CommittedMicroUSD != committed || s.RemainingMicroUSD != s.CapMicroUSD-committed {
		t.Fatalf("committed/remaining = %d/%d, want %d/%d",
			s.CommittedMicroUSD, s.RemainingMicroUSD, committed, s.CapMicroUSD-committed)
	}
}

func readLedger(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return b
}

func lineList(b []byte) []string {
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func writeLedger(t *testing.T, path string, lines []string) {
	t.Helper()
	must(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
}

// forgeLedger writes records with correct seq and prev links, so the chain
// verifies and only the replay invariants can refuse the file.
func forgeLedger(t *testing.T, path string, recs []record) {
	t.Helper()
	var lines []string
	prev := zeroHash
	for i, rec := range recs {
		rec.Seq = uint64(i)
		rec.Prev = prev
		body, err := json.Marshal(rec)
		must(t, err)
		lines = append(lines, string(body))
		prev = hashHex(body)
	}
	writeLedger(t, path, lines)
}

func TestOpenCreatesGenesisAndStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	l := openTest(t, path, testConfig())
	s := snapshotOf(t, l)
	wantTotals(t, s, 0, 0, 0)
	if s.StopRule || s.Records != 1 || len(s.HeadSHA256) != 64 {
		t.Fatalf("fresh snapshot = %+v", s)
	}
	if n := len(lineList(readLedger(t, path))); n != 1 {
		t.Fatalf("genesis file has %d lines, want 1", n)
	}
}

func TestReserveThenSettleReleasesRemainder(t *testing.T) {
	l := openTest(t, filepath.Join(t.TempDir(), "research.jsonl"), testConfig())
	must(t, l.Reserve("u1", 600))
	wantTotals(t, snapshotOf(t, l), 600, 0, 0)
	must(t, l.Settle("u1", 250))
	wantTotals(t, snapshotOf(t, l), 0, 0, 250)
}

func TestCapBoundaryIsExact(t *testing.T) {
	l := openTest(t, filepath.Join(t.TempDir(), "research.jsonl"), testConfig())
	must(t, l.Reserve("a", 400))
	must(t, l.Reserve("b", 600))
	wantTotals(t, snapshotOf(t, l), 1000, 0, 0)
	if err := l.Reserve("c", 1); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("reserve above exact cap: %v", err)
	}
	if err := l.Reserve("huge", math.MaxInt64); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("reserve at MaxInt64: %v", err)
	}
	wantTotals(t, snapshotOf(t, l), 1000, 0, 0)
	must(t, l.Settle("b", 100))
	wantTotals(t, snapshotOf(t, l), 400, 0, 100)
	if err := l.Reserve("c", 501); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("reserve one above released remainder: %v", err)
	}
	must(t, l.Reserve("c", 500))
	wantTotals(t, snapshotOf(t, l), 900, 0, 100)
}

func TestDuplicateUnitIsRefusedAcrossSettlement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	l := openTest(t, path, testConfig())
	must(t, l.Reserve("a", 300))
	if err := l.Reserve("a", 300); !errors.Is(err, ErrUnitExists) {
		t.Fatalf("duplicate open reservation: %v", err)
	}
	must(t, l.Settle("a", 100))
	before := readLedger(t, path)
	if err := l.Reserve("a", 100); !errors.Is(err, ErrUnitExists) {
		t.Fatalf("reuse of settled unit: %v", err)
	}
	if !bytes.Equal(before, readLedger(t, path)) {
		t.Fatal("refused reserve changed the ledger")
	}
	wantTotals(t, snapshotOf(t, l), 0, 0, 100)
}

func TestOverSettleIsRefusedWithoutChangingLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	l := openTest(t, path, testConfig())
	must(t, l.Reserve("a", 500))
	before := readLedger(t, path)
	if err := l.Settle("a", 501); !errors.Is(err, ErrOverSettle) {
		t.Fatalf("settle above reservation: %v", err)
	}
	if err := l.Settle("a", -1); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("negative settle: %v", err)
	}
	if !bytes.Equal(before, readLedger(t, path)) {
		t.Fatal("refused settle changed the ledger")
	}
	wantTotals(t, snapshotOf(t, l), 500, 0, 0)
	must(t, l.Settle("a", 500))
	wantTotals(t, snapshotOf(t, l), 0, 0, 500)
}

func TestSettleReplayIsIdempotentForSameAmountOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	l := openTest(t, path, testConfig())
	must(t, l.Reserve("a", 300))
	must(t, l.Settle("a", 120))
	before := readLedger(t, path)
	must(t, l.Settle("a", 120))
	if !bytes.Equal(before, readLedger(t, path)) {
		t.Fatal("replayed settle appended a record")
	}
	if err := l.Settle("a", 121); !errors.Is(err, ErrSettleConflict) {
		t.Fatalf("different amount after settle: %v", err)
	}
	if err := l.Settle("missing", 1); !errors.Is(err, ErrUnknownUnit) {
		t.Fatalf("settle of unknown unit: %v", err)
	}
	if err := l.MarkUncertain("a"); !errors.Is(err, ErrAlreadySettled) {
		t.Fatalf("mark settled unit uncertain: %v", err)
	}
	wantTotals(t, snapshotOf(t, l), 0, 0, 120)
}

func TestMarkUncertainRetainsReservationUntilSettled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	l := openTest(t, path, testConfig())
	must(t, l.Reserve("a", 400))
	must(t, l.MarkUncertain("a"))
	must(t, l.MarkUncertain("a"))
	s := snapshotOf(t, l)
	wantTotals(t, s, 0, 400, 0)
	if s.UncertainUnits != 1 {
		t.Fatalf("uncertain units = %d, want 1", s.UncertainUnits)
	}
	if err := l.MarkUncertain("missing"); !errors.Is(err, ErrUnknownUnit) {
		t.Fatalf("mark unknown unit: %v", err)
	}
	if err := l.Reserve("b", 601); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("reserve into uncertain budget: %v", err)
	}
	must(t, l.Reserve("b", 600))
	wantTotals(t, snapshotOf(t, l), 600, 400, 0)
	must(t, l.Settle("a", 0))
	wantTotals(t, snapshotOf(t, l), 600, 0, 0)
	// A fresh handle replays the uncertain mark and the zero settlement. The
	// reservation "b" was still open, so it is reported as uncertain after restart.
	must(t, l.Close())
	wantTotals(t, snapshotOf(t, openTest(t, path, testConfig())), 0, 600, 0)
}

func TestInvalidConfigIsRefusedWithoutCreatingFile(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		cfg  Config
	}{
		{"empty ledger id", Config{LedgerID: "", CapMicroUSD: testCap, MinPerUnitWorstCaseMicroUSD: testMin}},
		{"space in ledger id", Config{LedgerID: "bad id", CapMicroUSD: testCap, MinPerUnitWorstCaseMicroUSD: testMin}},
		{"zero cap", Config{LedgerID: "research-test", CapMicroUSD: 0, MinPerUnitWorstCaseMicroUSD: 1}},
		{"zero per-unit minimum", Config{LedgerID: "research-test", CapMicroUSD: testCap}},
		{"minimum above cap", Config{LedgerID: "research-test", CapMicroUSD: testCap, MinPerUnitWorstCaseMicroUSD: testCap + 1}},
		{"malformed expected head", Config{LedgerID: "research-test", CapMicroUSD: testCap, MinPerUnitWorstCaseMicroUSD: testMin, ExpectedHeadSHA256: "ABC"}},
	}
	for i, tc := range cases {
		path := filepath.Join(dir, fmt.Sprintf("bad-%d.jsonl", i))
		l, err := Open(path, tc.cfg)
		if err == nil {
			_ = l.Close()
			t.Fatalf("%s: Open accepted invalid config", tc.name)
		}
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%s: refused Open left a file behind (stat: %v)", tc.name, statErr)
		}
	}
}

func TestInvalidArgumentsAreRefused(t *testing.T) {
	l := openTest(t, filepath.Join(t.TempDir(), "research.jsonl"), testConfig())
	must(t, l.Reserve("r", 10))
	cases := []struct {
		name string
		run  func() error
		want error
	}{
		{"empty unit id", func() error { return l.Reserve("", 1) }, ErrInvalidID},
		{"unit id with space", func() error { return l.Reserve("a b", 1) }, ErrInvalidID},
		{"unit id with quote", func() error { return l.Reserve(`a"b`, 1) }, ErrInvalidID},
		{"overlong unit id", func() error { return l.Reserve(strings.Repeat("u", 129), 1) }, ErrInvalidID},
		{"zero worst case", func() error { return l.Reserve("z", 0) }, ErrInvalidAmount},
		{"negative worst case", func() error { return l.Reserve("n", -5) }, ErrInvalidAmount},
		{"negative actual", func() error { return l.Settle("r", -1) }, ErrInvalidAmount},
	}
	for _, tc := range cases {
		if err := tc.run(); !errors.Is(err, tc.want) {
			t.Fatalf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
	wantTotals(t, snapshotOf(t, l), 10, 0, 0)
}

func TestResearchAndEngineeringLedgersAreIsolated(t *testing.T) {
	dir := t.TempDir()
	researchPath := filepath.Join(dir, "research.jsonl")
	engineeringPath := filepath.Join(dir, "engineering.jsonl")
	researchCfg := Config{LedgerID: "research-openrouter-deepseek-v4.1-flash", CapMicroUSD: 5_000_000, MinPerUnitWorstCaseMicroUSD: 2_583, AllowGenesis: true}
	engineeringCfg := Config{LedgerID: "engineering-opencode-go", CapMicroUSD: 1_000_000, MinPerUnitWorstCaseMicroUSD: 1_000, AllowGenesis: true}
	research := openTest(t, researchPath, researchCfg)
	engineering := openTest(t, engineeringPath, engineeringCfg)

	must(t, research.Reserve("eval-0001", 2_000_000))
	must(t, engineering.Reserve("dev-0001", 999_000))
	wantTotals(t, snapshotOf(t, research), 2_000_000, 0, 0)
	wantTotals(t, snapshotOf(t, engineering), 999_000, 0, 0)

	if _, err := Open(researchPath, researchCfg); !errors.Is(err, ErrLocked) {
		t.Fatalf("second writer on research ledger: %v", err)
	}
	must(t, research.Close())
	if _, err := Open(researchPath, engineeringCfg); !errors.Is(err, ErrLedgerIDMismatch) {
		t.Fatalf("engineering ID opened research file: %v", err)
	}
	foreignCap := researchCfg
	foreignCap.CapMicroUSD = 10_000_000
	if _, err := Open(researchPath, foreignCap); !errors.Is(err, ErrCapMismatch) {
		t.Fatalf("foreign cap opened research file: %v", err)
	}
	reopened := openTest(t, researchPath, researchCfg)
	wantTotals(t, snapshotOf(t, reopened), 0, 2_000_000, 0)
}

func TestOpenDoesNotRewriteExistingLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	l := openTest(t, path, testConfig())
	must(t, l.Reserve("a", 300))
	must(t, l.Close())
	before := readLedger(t, path)
	for i := 0; i < 2; i++ {
		reopened, err := Open(path, testConfig())
		must(t, err)
		wantTotals(t, snapshotOf(t, reopened), 0, 300, 0)
		must(t, reopened.Close())
	}
	if !bytes.Equal(before, readLedger(t, path)) {
		t.Fatal("Open rewrote an existing ledger")
	}
}

func TestTornFinalRecordRefusesOpenWithoutRewriting(t *testing.T) {
	tails := map[string]string{
		"partial record":                  `{"seq":2,"prev":"abc`,
		"complete record without newline": `{"seq":2,"prev":"abc","kind":"settle","unit":"a","micro":5}`,
	}
	for name, tail := range tails {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "research.jsonl")
			l := openTest(t, path, testConfig())
			must(t, l.Reserve("a", 100))
			must(t, l.Close())
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
			must(t, err)
			_, err = file.WriteString(tail)
			must(t, err)
			must(t, file.Close())
			before := readLedger(t, path)
			if _, err := Open(path, testConfig()); !errors.Is(err, ErrTornRecord) || !errors.Is(err, ErrCorrupt) {
				t.Fatalf("torn final record: %v", err)
			}
			if !bytes.Equal(before, readLedger(t, path)) {
				t.Fatal("refused Open rewrote the torn ledger")
			}
		})
	}
}

func TestHashChainTamperIsRefused(t *testing.T) {
	build := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "research.jsonl")
		l := openTest(t, path, testConfig())
		must(t, l.Reserve("a", 600))
		must(t, l.Settle("a", 100))
		must(t, l.Reserve("b", 200))
		must(t, l.Close())
		return path
	}
	cases := []struct {
		name   string
		mutate func(lines []string) []string
		want   error
	}{
		{"edited reserve amount", func(l []string) []string {
			l[1] = strings.Replace(l[1], `"micro":600`, `"micro":100`, 1)
			return l
		}, ErrChainBroken},
		{"deleted middle record", func(l []string) []string {
			return append(l[:2:2], l[3:]...)
		}, ErrChainBroken},
		{"swapped records", func(l []string) []string {
			l[1], l[2] = l[2], l[1]
			return l
		}, ErrChainBroken},
		{"non-canonical whitespace", func(l []string) []string {
			l[2] = strings.Replace(l[2], `"kind":"settle"`, `"kind": "settle"`, 1)
			return l
		}, ErrCorrupt},
		{"edited genesis cap", func(l []string) []string {
			l[0] = strings.Replace(l[0], `"cap":1000`, `"cap":2000`, 1)
			return l
		}, ErrCapMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := build(t)
			writeLedger(t, path, tc.mutate(lineList(readLedger(t, path))))
			before := readLedger(t, path)
			if _, err := Open(path, testConfig()); !errors.Is(err, tc.want) {
				t.Fatalf("tampered ledger: got %v, want %v", err, tc.want)
			}
			if !bytes.Equal(before, readLedger(t, path)) {
				t.Fatal("refused Open rewrote the tampered ledger")
			}
		})
	}
}

func TestExpectedHeadAnchorDetectsTailEditAndBoundaryTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	l := openTest(t, path, testConfig())
	must(t, l.Reserve("a", 600))
	must(t, l.Settle("a", 120))
	head := snapshotOf(t, l).HeadSHA256
	must(t, l.Close())

	anchored := testConfig()
	anchored.ExpectedHeadSHA256 = head
	reopened, err := Open(path, anchored)
	must(t, err)
	must(t, reopened.Close())

	original := lineList(readLedger(t, path))
	tampered := append([]string(nil), original...)
	tampered[2] = strings.Replace(tampered[2], `"micro":120`, `"micro":100`, 1)
	writeLedger(t, path, tampered)
	if _, err := Open(path, anchored); !errors.Is(err, ErrHeadMismatch) {
		t.Fatalf("tail edit against anchor: %v", err)
	}
	writeLedger(t, path, original[:2])
	if _, err := Open(path, anchored); !errors.Is(err, ErrHeadMismatch) {
		t.Fatalf("boundary truncation against anchor: %v", err)
	}
}

func TestAnchoredOpenRefusesDeletedLedgerInsteadOfResetting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	anchored := testConfig()
	anchored.ExpectedHeadSHA256 = zeroHash
	if _, err := Open(path, anchored); !errors.Is(err, ErrHeadMismatch) {
		t.Fatalf("anchored open of absent ledger: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused anchored open left a ledger behind (stat: %v)", err)
	}
}

func TestMissingLedgerIsRefusedByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	cfg := testConfig()
	cfg.AllowGenesis = false
	if _, err := Open(path, cfg); !errors.Is(err, ErrMissingLedger) {
		t.Fatalf("absent ledger without AllowGenesis: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused Open created a ledger file (stat: %v)", err)
	}
}

func TestAllowGenesisCreatesFreshLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	l := openTest(t, path, testConfig())
	wantTotals(t, snapshotOf(t, l), 0, 0, 0)
	if n := len(lineList(readLedger(t, path))); n != 1 {
		t.Fatalf("genesis-only file has %d lines, want 1", n)
	}
}

func TestExpectedHeadRefusesMissingLedgerEvenWithAllowGenesis(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	anchored := testConfig()
	anchored.ExpectedHeadSHA256 = zeroHash
	if _, err := Open(path, anchored); !errors.Is(err, ErrHeadMismatch) {
		t.Fatalf("anchored absent ledger with AllowGenesis: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused anchored open created a ledger file (stat: %v)", err)
	}
}

// A file renamed into the path after this Open took its lock belongs to another
// inode. Writing through the locked descriptor would never reach the file that
// the path names, so Open must refuse.
func TestLedgerReplacedAfterLockIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "research.jsonl")
	l := openTest(t, path, testConfig())
	must(t, l.Reserve("a", 100))
	must(t, l.Close())

	impostor := filepath.Join(dir, "impostor.jsonl")
	impostorBytes := readLedger(t, path)
	must(t, os.WriteFile(impostor, impostorBytes, 0o600))
	withAfterFlockHook(t, func(p string) {
		if err := os.Rename(impostor, p); err != nil {
			t.Fatalf("rename impostor into place: %v", err)
		}
	})
	if _, err := Open(path, testConfig()); !errors.Is(err, ErrLedgerReplaced) {
		t.Fatalf("open after path was replaced: %v", err)
	}
	// The refused Open released its lock, and the renamed-in file was not written.
	if !bytes.Equal(impostorBytes, readLedger(t, path)) {
		t.Fatal("refused Open changed the renamed-in file")
	}
	afterFlockHook = nil
	wantTotals(t, snapshotOf(t, openTest(t, path, testConfig())), 0, 100, 0)
}

func TestReplayRejectsInvariantViolationsWithValidChain(t *testing.T) {
	genesis := record{Kind: genesisKind, Ledger: "research-test", Cap: testCap}
	cases := []struct {
		name string
		recs []record
	}{
		{"reserve above cap", []record{genesis, {Kind: reserveKind, Unit: "a", Micro: testCap + 1}}},
		{"zero reservation", []record{genesis, {Kind: reserveKind, Unit: "a"}}},
		{"duplicate reservation", []record{genesis, {Kind: reserveKind, Unit: "a", Micro: 100}, {Kind: reserveKind, Unit: "a", Micro: 100}}},
		{"settle above reservation", []record{genesis, {Kind: reserveKind, Unit: "a", Micro: 100}, {Kind: settleKind, Unit: "a", Micro: 101}}},
		{"settle unknown unit", []record{genesis, {Kind: settleKind, Unit: "a", Micro: 1}}},
		{"second settlement", []record{genesis, {Kind: reserveKind, Unit: "a", Micro: 100}, {Kind: settleKind, Unit: "a", Micro: 10}, {Kind: settleKind, Unit: "a", Micro: 10}}},
		{"uncertain after settlement", []record{genesis, {Kind: reserveKind, Unit: "a", Micro: 100}, {Kind: settleKind, Unit: "a", Micro: 10}, {Kind: uncertainKind, Unit: "a"}}},
		{"uncertain twice", []record{genesis, {Kind: reserveKind, Unit: "a", Micro: 100}, {Kind: uncertainKind, Unit: "a"}, {Kind: uncertainKind, Unit: "a"}}},
		{"reserve carrying genesis fields", []record{genesis, {Kind: reserveKind, Unit: "a", Micro: 1, Cap: 5}}},
		{"uncertain carrying an amount", []record{genesis, {Kind: reserveKind, Unit: "a", Micro: 1}, {Kind: uncertainKind, Unit: "a", Micro: 1}}},
		{"missing genesis", []record{{Kind: reserveKind, Unit: "a", Micro: 1}}},
		{"second genesis", []record{genesis, genesis}},
		{"overage not above reservation", []record{genesis, {Kind: reserveKind, Unit: "a", Micro: 100}, {Kind: overageKind, Unit: "a", Micro: 100}}},
		{"overage of unknown unit", []record{genesis, {Kind: overageKind, Unit: "a", Micro: 1}}},
		{"overage carrying genesis fields", []record{genesis, {Kind: reserveKind, Unit: "a", Micro: 100}, {Kind: overageKind, Unit: "a", Micro: 150, Cap: 5}}},
		{"second overage", []record{genesis, {Kind: reserveKind, Unit: "a", Micro: 100}, {Kind: overageKind, Unit: "a", Micro: 150}, {Kind: overageKind, Unit: "a", Micro: 200}}},
		{"reserve after overage", []record{genesis, {Kind: reserveKind, Unit: "a", Micro: 100}, {Kind: overageKind, Unit: "a", Micro: 150}, {Kind: reserveKind, Unit: "b", Micro: 1}}},
		{"settle above reservation written as settle", []record{genesis, {Kind: reserveKind, Unit: "a", Micro: 100}, {Kind: settleKind, Unit: "a", Micro: 150}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "research.jsonl")
			forgeLedger(t, path, tc.recs)
			if _, err := Open(path, testConfig()); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("forged ledger: got %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestEmptyExistingFileIsRefusedAsMissingGenesis(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	must(t, os.WriteFile(path, nil, 0o600))
	if _, err := Open(path, testConfig()); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("empty existing file: %v", err)
	}
}

func TestSymlinkLedgerPathIsRefused(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.jsonl")
	l := openTest(t, target, testConfig())
	must(t, l.Close())
	link := filepath.Join(dir, "link.jsonl")
	must(t, os.Symlink(target, link))
	if _, err := Open(link, testConfig()); !errors.Is(err, ErrInvalidLedgerFile) {
		t.Fatalf("symlink ledger path: %v", err)
	}
}

func TestWriteFailurePoisonsLedgerWithoutApplyingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	l := openTest(t, path, testConfig())
	must(t, l.Reserve("a", 100))
	before := readLedger(t, path)
	must(t, l.file.Close()) // simulate the descriptor failing under the writer
	if err := l.Reserve("b", 100); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("reserve after write failure: %v", err)
	}
	if err := l.Settle("a", 50); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("settle after write failure: %v", err)
	}
	if _, err := l.Snapshot(); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("snapshot after write failure: %v", err)
	}
	if !bytes.Equal(before, readLedger(t, path)) {
		t.Fatal("failed write changed the ledger")
	}
	// A fresh handle verifies the file and keeps the reservation counted.
	wantTotals(t, snapshotOf(t, openTest(t, path, testConfig())), 0, 100, 0)
}

func TestSyncFailureLandsRecordPoisonsHandleAndReopenVerifies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	l := openTest(t, path, testConfig())
	must(t, l.Reserve("a", 100))
	withSyncHook(t, func(*os.File) error { return errors.New("injected sync failure") })
	if err := l.Reserve("b", 100); !errors.Is(err, ErrPoisoned) || !strings.Contains(err.Error(), "injected sync failure") {
		t.Fatalf("reserve with failing sync: %v", err)
	}
	if err := l.Settle("a", 50); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("settle after sync failure: %v", err)
	}
	if _, err := l.Snapshot(); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("snapshot after sync failure: %v", err)
	}
	must(t, l.Close())
	// The record for "b" was written before its fsync failed, so the file holds it.
	// A fresh handle verifies the chain and counts both reservations as uncertain.
	s := snapshotOf(t, openTest(t, path, testConfig()))
	wantTotals(t, s, 0, 200, 0)
	if s.Records != 3 {
		t.Fatalf("records after landed write = %d, want genesis plus two reserves", s.Records)
	}
}

func TestFailedGenesisSyncRemovesNewLedgerFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	withSyncHook(t, func(*os.File) error { return errors.New("injected sync failure") })
	if _, err := Open(path, testConfig()); !errors.Is(err, ErrPoisoned) {
		t.Fatalf("create with failing genesis sync: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed create left a ledger file behind (stat: %v)", err)
	}
}

func TestOverageIsRecordedAboveCapAndBlocksFurtherReserves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	l := openTest(t, path, testConfig())
	must(t, l.Reserve("a", 400))
	must(t, l.Reserve("b", 600))
	if err := l.SettleOverage("a", 550); err != nil {
		t.Fatalf("overage settle: %v", err)
	}
	s := snapshotOf(t, l)
	// The actual 550 is committed in full, so committed is 1150 against a cap of 1000.
	wantTotals(t, s, 600, 0, 550)
	if s.RemainingMicroUSD != -150 || !s.StopRule || !s.Overspent {
		t.Fatalf("over-cap snapshot = remaining %d stop %v overspent %v", s.RemainingMicroUSD, s.StopRule, s.Overspent)
	}
	// Releasing the other reservation leaves headroom, but the ledger stays fail-closed.
	must(t, l.Settle("b", 0))
	wantTotals(t, snapshotOf(t, l), 0, 0, 550)
	if err := l.Reserve("c", 1); !errors.Is(err, ErrOverCap) {
		t.Fatalf("reserve after overage with headroom: %v", err)
	}
	if err := l.Reserve("c", 450); !errors.Is(err, ErrOverCap) {
		t.Fatalf("reserve filling remaining headroom after overage: %v", err)
	}
	wantTotals(t, snapshotOf(t, l), 0, 0, 550)
}

func TestOverageRecordsActualAmountNotReservation(t *testing.T) {
	l := openTest(t, filepath.Join(t.TempDir(), "research.jsonl"), testConfig())
	must(t, l.Reserve("a", 300))
	must(t, l.MarkUncertain("a"))
	if err := l.SettleOverage("a", 450); err != nil {
		t.Fatalf("overage settle of uncertain unit: %v", err)
	}
	wantTotals(t, snapshotOf(t, l), 0, 0, 450)
}

func TestSettleOverageRefusesAmountsNotAboveReservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	l := openTest(t, path, testConfig())
	must(t, l.Reserve("a", 500))
	before := readLedger(t, path)
	for _, tc := range []struct {
		name   string
		unit   string
		actual int64
		want   error
	}{
		{"equal to reservation", "a", 500, ErrNotOverage},
		{"below reservation", "a", 100, ErrNotOverage},
		{"negative", "a", -1, ErrInvalidAmount},
		{"unknown unit", "missing", 1, ErrUnknownUnit},
	} {
		if err := l.SettleOverage(tc.unit, tc.actual); !errors.Is(err, tc.want) {
			t.Fatalf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
	if !bytes.Equal(before, readLedger(t, path)) {
		t.Fatal("refused overage settle changed the ledger")
	}
	if snapshotOf(t, l).Overspent {
		t.Fatal("refused overage settle set Overspent")
	}
	// Settle keeps its old contract: an amount above the reservation is refused.
	if err := l.Settle("a", 501); !errors.Is(err, ErrOverSettle) {
		t.Fatalf("Settle above reservation: %v", err)
	}
	wantTotals(t, snapshotOf(t, l), 500, 0, 0)
}

func TestSettleOverageIsIdempotentForSameAmountOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	l := openTest(t, path, testConfig())
	must(t, l.Reserve("a", 100))
	must(t, l.SettleOverage("a", 150))
	before := readLedger(t, path)
	must(t, l.SettleOverage("a", 150))
	if !bytes.Equal(before, readLedger(t, path)) {
		t.Fatal("replayed overage settle appended a record")
	}
	if err := l.SettleOverage("a", 151); !errors.Is(err, ErrSettleConflict) {
		t.Fatalf("different overage after settle: %v", err)
	}
	if err := l.Settle("a", 100); !errors.Is(err, ErrSettleConflict) {
		t.Fatalf("different plain settle after overage: %v", err)
	}
	must(t, l.Settle("a", 150))
	wantTotals(t, snapshotOf(t, l), 0, 0, 150)
}

func TestSettleOverageOverflowIsRefusedWithoutChangingLedger(t *testing.T) {
	cfg := Config{LedgerID: "research-test", CapMicroUSD: math.MaxInt64, MinPerUnitWorstCaseMicroUSD: testMin, AllowGenesis: true}
	path := filepath.Join(t.TempDir(), "research.jsonl")
	l := openTest(t, path, cfg)
	must(t, l.Reserve("x", 10))
	must(t, l.Reserve("y", 10))
	before := readLedger(t, path)
	if err := l.SettleOverage("x", math.MaxInt64); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("overage that overflows committed: %v", err)
	}
	if !bytes.Equal(before, readLedger(t, path)) {
		t.Fatal("overflowing overage changed the ledger")
	}
	wantTotals(t, snapshotOf(t, l), 20, 0, 0)

	// The largest committed total that fits exactly is accepted.
	edge := openTest(t, filepath.Join(t.TempDir(), "edge.jsonl"), cfg)
	must(t, edge.Reserve("solo", 1))
	must(t, edge.SettleOverage("solo", math.MaxInt64))
	wantTotals(t, snapshotOf(t, edge), 0, 0, math.MaxInt64)
	if err := edge.Reserve("more", 1); !errors.Is(err, ErrOverCap) {
		t.Fatalf("reserve at MaxInt64 committed: %v", err)
	}
}

func TestCloseBlocksFurtherOperations(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "research.jsonl"), testConfig())
	must(t, err)
	must(t, l.Close())
	must(t, l.Close())
	if err := l.Reserve("a", 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("reserve after close: %v", err)
	}
	if _, err := l.Snapshot(); !errors.Is(err, ErrClosed) {
		t.Fatalf("snapshot after close: %v", err)
	}
}

func TestStopRuleTripsBelowPerUnitMinimum(t *testing.T) {
	cfg := Config{LedgerID: "research-test", CapMicroUSD: 1000, MinPerUnitWorstCaseMicroUSD: 300, AllowGenesis: true}
	l := openTest(t, filepath.Join(t.TempDir(), "research.jsonl"), cfg)
	must(t, l.Reserve("a", 700))
	if snapshotOf(t, l).StopRule {
		t.Fatal("stop rule set with exactly the minimum remaining")
	}
	must(t, l.Reserve("b", 1))
	if !snapshotOf(t, l).StopRule {
		t.Fatal("stop rule not set below the minimum")
	}
}

func TestConcurrentReservationsNeverExceedCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	l := openTest(t, path, testConfig())
	const attempts = 64
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- l.Reserve(fmt.Sprintf("unit-%02d", i), 100)
		}()
	}
	wg.Wait()
	close(errs)
	admitted := 0
	for err := range errs {
		switch {
		case err == nil:
			admitted++
		case errors.Is(err, ErrCapExceeded):
		default:
			t.Fatalf("unexpected reserve error: %v", err)
		}
	}
	if admitted != 10 {
		t.Fatalf("admitted %d reservations, want exactly 10", admitted)
	}
	wantTotals(t, snapshotOf(t, l), 1000, 0, 0)
	must(t, l.Close())
	wantTotals(t, snapshotOf(t, openTest(t, path, testConfig())), 0, 1000, 0)
}

func TestConcurrentDuplicateUnitAdmitsExactlyOne(t *testing.T) {
	l := openTest(t, filepath.Join(t.TempDir(), "research.jsonl"), testConfig())
	const attempts = 32
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- l.Reserve("same-unit", 100)
		}()
	}
	wg.Wait()
	close(errs)
	admitted, duplicates := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			admitted++
		case errors.Is(err, ErrUnitExists):
			duplicates++
		default:
			t.Fatalf("unexpected reserve error: %v", err)
		}
	}
	if admitted != 1 || duplicates != attempts-1 {
		t.Fatalf("admitted %d and refused %d duplicates, want 1 and %d", admitted, duplicates, attempts-1)
	}
	wantTotals(t, snapshotOf(t, l), 100, 0, 0)
}

// runFreshProcess re-executes this test binary and runs one helper step in a
// new process. The parent holds no state from the child.
func runFreshProcess(t *testing.T, mode, path string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestUSDLedgerHelper$")
	cmd.Env = append(os.Environ(), helperModeEnv+"="+mode, helperPathEnv+"="+path)
	return cmd.CombinedOutput()
}

func requireSIGKILL(t *testing.T, mode string, out []byte, err error) {
	t.Helper()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("%s: child did not exit by signal: %v\n%s", mode, err, out)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("%s: child was not killed by SIGKILL: %v\n%s", mode, err, out)
	}
}

func TestFreshProcessKillAfterReserveKeepsBudgetCounted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	out, err := runFreshProcess(t, "writer-reserve-kill", path)
	requireSIGKILL(t, "writer-reserve-kill", out, err)
	if out, err := runFreshProcess(t, "verify-reserve-kill", path); err != nil {
		t.Fatalf("verify-reserve-kill: %v\n%s", err, out)
	}
	// This handle reads only what the child processes left on disk.
	wantTotals(t, snapshotOf(t, openTest(t, path, helperConfig())), 0, 600, 150)
}

func TestFreshProcessKillAfterMixedOperationsRecoversEachState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	out, err := runFreshProcess(t, "writer-mixed-kill", path)
	requireSIGKILL(t, "writer-mixed-kill", out, err)
	if out, err := runFreshProcess(t, "verify-mixed-kill", path); err != nil {
		t.Fatalf("verify-mixed-kill: %v\n%s", err, out)
	}
	wantTotals(t, snapshotOf(t, openTest(t, path, helperConfig())), 0, 0, 620)
}

func TestFreshProcessOverageSurvivesRestartAndBlocksReserves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	out, err := runFreshProcess(t, "writer-overage-kill", path)
	requireSIGKILL(t, "writer-overage-kill", out, err)
	if out, err := runFreshProcess(t, "verify-overage-kill", path); err != nil {
		t.Fatalf("verify-overage-kill: %v\n%s", err, out)
	}
	// The overage is booked at its actual 450, not at the 300 reservation. The
	// verify child settled the other unit to zero, so nothing else is counted.
	l := openTest(t, path, helperConfig())
	s := snapshotOf(t, l)
	wantTotals(t, s, 0, 0, 450)
	if !s.Overspent {
		t.Fatal("overage did not survive restart")
	}
	if err := l.Reserve("parent", 1); !errors.Is(err, ErrOverCap) {
		t.Fatalf("reserve after restart with overage: %v", err)
	}
}

// killSelf terminates the process without deferred cleanup, as a crash would.
// The parent requires death by SIGKILL, so the os.Exit fallback is a failure.
func killSelf() {
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGKILL)
	os.Exit(3)
}

// TestUSDLedgerHelper is the child-process entry point for the fresh-process
// tests. Ordinary test runs leave the mode variable unset and return at once.
func TestUSDLedgerHelper(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		return
	}
	path := os.Getenv(helperPathEnv)
	switch mode {
	case "writer-reserve-kill":
		l, err := Open(path, helperConfig())
		must(t, err)
		must(t, l.Reserve("crash-a", 400))
		killSelf()
	case "verify-reserve-kill":
		l := openTest(t, path, helperConfig())
		wantTotals(t, snapshotOf(t, l), 0, 400, 0)
		if err := l.Reserve("crash-a", 1); !errors.Is(err, ErrUnitExists) {
			t.Fatalf("reuse of crashed unit: %v", err)
		}
		if err := l.Reserve("next", 601); !errors.Is(err, ErrCapExceeded) {
			t.Fatalf("reserve above recovered remainder: %v", err)
		}
		must(t, l.Reserve("next", 600))
		wantTotals(t, snapshotOf(t, l), 600, 400, 0)
		must(t, l.Settle("crash-a", 150))
		wantTotals(t, snapshotOf(t, l), 600, 0, 150)
		before := readLedger(t, path)
		must(t, l.Settle("crash-a", 150))
		if !bytes.Equal(before, readLedger(t, path)) {
			t.Fatal("replayed settle changed the ledger")
		}
	case "writer-mixed-kill":
		l, err := Open(path, helperConfig())
		must(t, err)
		must(t, l.Reserve("mix-a", 300))
		must(t, l.Settle("mix-a", 120))
		must(t, l.Reserve("mix-b", 500))
		killSelf()
	case "verify-mixed-kill":
		l := openTest(t, path, helperConfig())
		s := snapshotOf(t, l)
		wantTotals(t, s, 0, 500, 120)
		if !s.StopRule {
			t.Fatal("stop rule not set with 380 remaining against a 400 minimum")
		}
		before := readLedger(t, path)
		must(t, l.Settle("mix-a", 120))
		if !bytes.Equal(before, readLedger(t, path)) {
			t.Fatal("replayed settle changed the ledger")
		}
		if err := l.Settle("mix-a", 121); !errors.Is(err, ErrSettleConflict) {
			t.Fatalf("conflicting settle after restart: %v", err)
		}
		if err := l.Settle("mix-b", 501); !errors.Is(err, ErrOverSettle) {
			t.Fatalf("over-settle of recovered unit: %v", err)
		}
		must(t, l.Settle("mix-b", 500))
		wantTotals(t, snapshotOf(t, l), 0, 0, 620)
	case "writer-overage-kill":
		l, err := Open(path, helperConfig())
		must(t, err)
		must(t, l.Reserve("ov-a", 300))
		must(t, l.Reserve("ov-b", 600))
		must(t, l.SettleOverage("ov-a", 450))
		killSelf()
	case "verify-overage-kill":
		l := openTest(t, path, helperConfig())
		wantTotals(t, snapshotOf(t, l), 0, 600, 450)
		if !snapshotOf(t, l).Overspent {
			t.Fatal("overage not recovered")
		}
		if err := l.Reserve("next", 1); !errors.Is(err, ErrOverCap) {
			t.Fatalf("reserve after recovered overage: %v", err)
		}
		before := readLedger(t, path)
		must(t, l.SettleOverage("ov-a", 450))
		if !bytes.Equal(before, readLedger(t, path)) {
			t.Fatal("replayed overage settle changed the ledger")
		}
		// The chain verifies against an anchor taken from this process's view.
		head := snapshotOf(t, l).HeadSHA256
		must(t, l.Close())
		anchored := helperConfig()
		anchored.ExpectedHeadSHA256 = head
		reopened := openTest(t, path, anchored)
		must(t, reopened.Settle("ov-b", 0))
		wantTotals(t, snapshotOf(t, reopened), 0, 0, 450)
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

// Unit reports one reservation's lifecycle without changing the ledger. A reservation that was
// still open when the process died is uncertain in a fresh handle, never open.
func TestUnitReportsEachReservationState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.jsonl")
	l := openTest(t, path, testConfig())
	must(t, l.Reserve("open-unit", 100))
	must(t, l.Reserve("uncertain-unit", 200))
	must(t, l.MarkUncertain("uncertain-unit"))
	must(t, l.Reserve("settled-unit", 300))
	must(t, l.Settle("settled-unit", 120))
	for id, want := range map[string]UnitInfo{
		"open-unit":      {ID: "open-unit", State: UnitStateOpen, ReservedMicroUSD: 100},
		"uncertain-unit": {ID: "uncertain-unit", State: UnitStateUncertain, ReservedMicroUSD: 200},
		"settled-unit":   {ID: "settled-unit", State: UnitStateSettled, ReservedMicroUSD: 300, SettledMicroUSD: 120},
	} {
		got, found, err := l.Unit(id)
		if err != nil || !found || got != want {
			t.Fatalf("%s: got %+v found=%v err=%v, want %+v", id, got, found, err, want)
		}
	}
	if _, found, err := l.Unit("never-reserved"); err != nil || found {
		t.Fatalf("an unreserved unit was reported: found=%v err=%v", found, err)
	}
	before := readLedger(t, path)
	if _, _, err := l.Unit("open-unit"); err != nil {
		t.Fatal(err)
	}
	if after := readLedger(t, path); string(after) != string(before) {
		t.Fatal("Unit wrote to the ledger")
	}
	must(t, l.Close())
	fresh := openTest(t, path, testConfig())
	if got, found, err := fresh.Unit("open-unit"); err != nil || !found || got.State != UnitStateUncertain {
		t.Fatalf("a crash-open reservation is %+v found=%v err=%v, want uncertain", got, found, err)
	}
	if _, _, err := fresh.Unit(""); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("an invalid id was accepted: %v", err)
	}
	must(t, fresh.Close())
	if _, _, err := fresh.Unit("open-unit"); !errors.Is(err, ErrClosed) {
		t.Fatalf("a closed ledger answered: %v", err)
	}
}
