//go:build linux

package publication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"tbound/supervisor/internal/wsoracle"
)

// The publication's own approved-delta ledger of the live root: one wsoracle.LedgerEntry per
// object the publication applied, with the before and after state read from the live root at
// apply time, bound to the transaction. These tests feed it to wsoracle.EvaluateE04.

func sha256Of(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// e04Over builds the EvaluateE04 input for a run whose protected root was sampled at the seven
// points: the four before publication and pre-publication are the baseline, post-publication and
// final-cleanup are the root as it is now. ledger is the ledger the two later points claim.
func e04Over(livePath string, baseline wsoracle.Manifest, ledger []wsoracle.LedgerEntry) wsoracle.EvaluateE04Input {
	in := wsoracle.EvaluateE04Input{ThreatClass: wsoracle.ClassTMC, Baseline: baseline, Ledger: ledger,
		Scope: wsoracle.E04Scope{Whole: true}}
	for _, name := range []string{wsoracle.PointPostMaterialization, wsoracle.PointPostSettlement, wsoracle.PointPostTeardown, wsoracle.PointPrePublication} {
		m := baseline
		m.Label = name
		in.Points = append(in.Points, wsoracle.E04Point{Name: name, Manifest: m, LedgerPrefixDigest: wsoracle.LedgerPrefixDigest(nil, 0)})
	}
	for _, name := range []string{wsoracle.PointPostPublication, wsoracle.PointFinalCleanup} {
		in.Points = append(in.Points, wsoracle.E04Point{Name: name, Manifest: wsoracle.Snapshot(livePath, name),
			LedgerApplied: len(ledger), LedgerPrefixDigest: wsoracle.LedgerPrefixDigest(ledger, len(ledger))})
	}
	return in
}

func requireVerdicts(t *testing.T, report wsoracle.E04Report, want string) {
	t.Helper()
	for _, p := range []string{wsoracle.PredicateWS01, wsoracle.PredicateWS02, wsoracle.PredicateWS03, wsoracle.PredicateWS04} {
		if got := report.VerdictOf(p); got != want {
			t.Errorf("%s = %q, want %q (gaps=%+v findings=%+v unattributed=%+v)", p, got, want, report.Gaps, report.Findings, report.Unattributed)
		}
	}
}

func publishTwoFiles(t *testing.T) (*fixture, *Repository, Result, wsoracle.Manifest) {
	t.Helper()
	base := map[string]string{"a.txt": "before-a", "b.txt": "before-b", "keep.txt": "keep"}
	middle := map[string]string{"a.txt": "middle-a", "b.txt": "before-b", "keep.txt": "keep"}
	final := map[string]string{"a.txt": "final-a", "b.txt": "final-b", "keep.txt": "keep"}
	f := newFixture(t, base, base, middle, final, nil, nil, nil)
	baseline := wsoracle.Snapshot(filepath.Join(f.root, "live"), wsoracle.PointBaseline)
	repo := f.acquire(1)
	result, err := repo.Publish(context.Background(), f.request, allowFor("two files"), nil)
	if err != nil || result.Status != StatusSucceeded {
		t.Fatalf("Publish: result=%+v err=%v", result, err)
	}
	return f, repo, result, baseline
}

// A publication that applies two files records two approved entries, bound to the transaction,
// with the before and after state of each file read from the live root; a clean run is no_effect.
func TestPublicationRecordsOneApprovedLedgerEntryPerAppliedObject(t *testing.T) {
	f, repo, result, baseline := publishTwoFiles(t)
	defer repo.Close()
	if len(result.Ledger) != 2 || len(result.Objects) != 2 {
		t.Fatalf("ledger = %+v for objects %+v", result.Ledger, result.Objects)
	}
	want := map[string][2]string{"a.txt": {"before-a", "final-a"}, "b.txt": {"before-b", "final-b"}}
	for i, entry := range result.Ledger {
		if entry.Sequence != uint64(i+1) || !strings.HasPrefix(entry.ID, result.TransactionID+"/") || len(entry.Changes) != 1 {
			t.Fatalf("entry %d = %+v, want sequence %d bound to %s", i, entry, i+1, result.TransactionID)
		}
		c := entry.Changes[0]
		contents, ok := want[c.Path]
		if !ok || c.Before == nil || c.After == nil {
			t.Fatalf("entry %d change = %+v", i, c)
		}
		if c.Before.Type != wsoracle.TypeFile || c.Before.SHA256 != sha256Of(contents[0]) || c.Before.Size != int64(len(contents[0])) ||
			c.Before.Nlink != 1 || c.Before.Mode != 0o644 || c.Before.Path != c.Path {
			t.Errorf("%s before = %+v", c.Path, *c.Before)
		}
		if c.After.Type != wsoracle.TypeFile || c.After.SHA256 != sha256Of(contents[1]) || c.After.Size != int64(len(contents[1])) ||
			c.After.Nlink != 1 || c.After.Mode != 0o644 || c.After.Path != c.Path || c.After.Ino == 0 {
			t.Errorf("%s after = %+v", c.Path, *c.After)
		}
		delete(want, c.Path)
	}
	live := filepath.Join(f.root, "live")
	report := wsoracle.EvaluateE04(e04Over(live, baseline, result.Ledger))
	requireVerdicts(t, report, wsoracle.VerdictNoEffect)
	if len(report.Gaps) != 0 {
		t.Fatalf("gaps = %+v", report.Gaps)
	}
	// The same ledger is available again from the durable journal, to a later observer.
	again, err := repo.Recover(f.live, result.TransactionID, nil)
	if err != nil || !reflect.DeepEqual(again.Ledger, result.Ledger) {
		t.Fatalf("recovered ledger = %+v err=%v, want %+v", again.Ledger, err, result.Ledger)
	}
}

// The publication ledger holds create, overwrite, delete, rename (delete plus create) and directory
// creation, each with the state it really had, and the whole thing composes against the baseline.
func TestPublicationLedgerCoversEveryOperationAndComposesAgainstTheBaseline(t *testing.T) {
	base := map[string]string{"over.txt": "old-value", "gone.txt": "remove-me", "move.txt": "rename-me"}
	middle := map[string]string{"over.txt": "middle-value", "gone.txt": "remove-me", "move.txt": "rename-me", "ephemeral.txt": "transient"}
	final := map[string]string{"over.txt": "final-value", "newdir/moved.txt": "rename-me", "created.txt": "new-value"}
	f := newFixture(t, base, base, middle, final, nil, nil, []string{"newdir"})
	live := filepath.Join(f.root, "live")
	baseline := wsoracle.Snapshot(live, wsoracle.PointBaseline)
	repo := f.acquire(1)
	defer repo.Close()
	result, err := repo.Publish(context.Background(), f.request, allowFor("all operations"), nil)
	if err != nil || result.Status != StatusSucceeded || len(result.Objects) != 6 {
		t.Fatalf("Publish: %+v err=%v", result, err)
	}
	if len(result.Ledger) != 6 {
		t.Fatalf("ledger has %d entries for 6 applied objects: %+v", len(result.Ledger), result.Ledger)
	}
	byPath := map[string]wsoracle.LedgerChange{}
	for _, e := range result.Ledger {
		byPath[e.Changes[0].Path] = e.Changes[0]
	}
	if c := byPath["gone.txt"]; c.Before == nil || c.After != nil {
		t.Errorf("delete = %+v", c)
	}
	if c := byPath["created.txt"]; c.Before != nil || c.After == nil || c.After.SHA256 != sha256Of("new-value") {
		t.Errorf("create = %+v", c)
	}
	if c := byPath["newdir"]; c.Before != nil || c.After == nil || c.After.Type != wsoracle.TypeDir || c.After.Mode != 0o755 {
		t.Errorf("directory create = %+v", c)
	}
	if c := byPath["newdir/moved.txt"]; c.Before != nil || c.After == nil || c.After.SHA256 != sha256Of("rename-me") {
		t.Errorf("rename target = %+v", c)
	}
	report := wsoracle.EvaluateE04(e04Over(live, baseline, result.Ledger))
	requireVerdicts(t, report, wsoracle.VerdictNoEffect)
}

// An unapproved change made beside the publication, after the ledger was written, is an effect.
func TestPublicationLedgerDoesNotApproveAChangeMadeBesideIt(t *testing.T) {
	f, repo, result, baseline := publishTwoFiles(t)
	defer repo.Close()
	live := filepath.Join(f.root, "live")
	if err := os.WriteFile(filepath.Join(live, "keep.txt"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := wsoracle.EvaluateE04(e04Over(live, baseline, result.Ledger))
	if got := report.VerdictOf(wsoracle.PredicateWS01); got != wsoracle.VerdictEffectPresent {
		t.Fatalf("EFF-WS-01 = %q, want effect_present (gaps=%+v findings=%+v)", got, report.Gaps, report.Findings)
	}
	// A published file changed again afterwards is no better: its after state is what was written.
	if err := os.WriteFile(filepath.Join(live, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "a.txt"), []byte("rewritten after publication"), 0o644); err != nil {
		t.Fatal(err)
	}
	report = wsoracle.EvaluateE04(e04Over(live, baseline, result.Ledger))
	if got := report.VerdictOf(wsoracle.PredicateWS01); got != wsoracle.VerdictEffectPresent {
		t.Fatalf("a published file rewritten afterwards: EFF-WS-01 = %q (gaps=%+v)", got, report.Gaps)
	}
}

// A ledger entry whose after state does not match what the root holds is not special-cased: it is
// the ordinary E04 comparison, an effect.
func TestPublicationLedgerWithAWrongAfterStateIsAnOrdinaryE04Effect(t *testing.T) {
	f, repo, result, baseline := publishTwoFiles(t)
	defer repo.Close()
	forged := append([]wsoracle.LedgerEntry(nil), result.Ledger...)
	change := forged[0].Changes[0]
	after := *change.After
	after.SHA256 = sha256Of("something else")
	change.After = &after
	forged[0] = wsoracle.LedgerEntry{ID: forged[0].ID, Sequence: 1, Changes: []wsoracle.LedgerChange{change}}
	report := wsoracle.EvaluateE04(e04Over(filepath.Join(f.root, "live"), baseline, forged))
	if got := report.VerdictOf(wsoracle.PredicateWS01); got != wsoracle.VerdictEffectPresent {
		t.Fatalf("EFF-WS-01 = %q, want effect_present (gaps=%+v)", got, report.Gaps)
	}
}

// A publication that never applied anything (the commit authority refused) has an empty ledger
// and leaves the root as it was: no approval is invented for what did not happen.
func TestPublicationRefusedByTheAuthorityHasNoLedgerAndNoEffect(t *testing.T) {
	base := map[string]string{"a.txt": "before-a", "b.txt": "before-b"}
	final := map[string]string{"a.txt": "final-a", "b.txt": "final-b"}
	f := newFixture(t, base, base, base, final, nil, nil, nil)
	live := filepath.Join(f.root, "live")
	baseline := wsoracle.Snapshot(live, wsoracle.PointBaseline)
	repo := f.acquire(1)
	defer repo.Close()
	result, err := repo.Publish(context.Background(), f.request, func(CommitBinding) (Authorization, error) {
		return Authorization{}, errors.New("refused")
	}, nil)
	if err == nil || len(result.Ledger) != 0 {
		t.Fatalf("refused publication: result=%+v err=%v", result, err)
	}
	report := wsoracle.EvaluateE04(e04Over(live, baseline, nil))
	requireVerdicts(t, report, wsoracle.VerdictNoEffect)
}

// An interrupted publication: the ledger names only the object that was applied and checkpointed.
// The object whose rename is uncertain and the object never started are not approved.
func TestPublicationLedgerOfAnInterruptedPublicationNamesOnlyWhatWasApplied(t *testing.T) {
	base := map[string]string{"a.txt": "before-a", "b.txt": "before-b", "c.txt": "before-c"}
	middle := map[string]string{"a.txt": "middle-a", "b.txt": "middle-b", "c.txt": "middle-c"}
	final := map[string]string{"a.txt": "final-a", "b.txt": "final-b", "c.txt": "final-c"}
	f := newFixture(t, base, base, middle, final, nil, nil, nil)
	f.request.PublicationID = "interrupted-ledger-effect"
	first := f.acquire(1)
	result, err := first.Publish(context.Background(), f.request, allowFor("interrupted ledger"), func(point string, index int) error {
		if point == "after-rename" && index == 1 {
			return errors.New("interrupt after the second rename")
		}
		return nil
	})
	if !errors.Is(err, ErrInjectedFault) || len(result.Ledger) != 0 {
		t.Fatalf("interrupted: result=%+v err=%v (a result that is UNKNOWN claims no ledger)", result, err)
	}
	_ = first.Close()
	second := f.acquire(2)
	defer second.Close()
	recovered, recoverErr := second.Recover(f.live, result.TransactionID, nil)
	if recovered.Status != StatusUnknown || !errors.Is(recoverErr, ErrRecoveryUnknown) {
		t.Fatalf("recovery = %+v err=%v", recovered, recoverErr)
	}
	if len(recovered.Ledger) != 1 || recovered.Ledger[0].Changes[0].Path != "a.txt" || recovered.Ledger[0].Sequence != 1 ||
		!strings.HasPrefix(recovered.Ledger[0].ID, result.TransactionID+"/") {
		t.Fatalf("recovered ledger = %+v, want only a.txt", recovered.Ledger)
	}
}

// A publication whose token was consumed but which applied nothing is FAILED with an empty ledger.
func TestPublicationFailedBeforeAnyObjectHasAnEmptyLedger(t *testing.T) {
	base := map[string]string{"file.txt": "before"}
	f := newFixture(t, base, base, map[string]string{"file.txt": "middle"}, map[string]string{"file.txt": "after"}, nil, nil, nil)
	f.request.PublicationID = "token-then-fault"
	first := f.acquire(1)
	result, err := first.Publish(context.Background(), f.request, allowFor("token then fault"), func(point string, _ int) error {
		if point == "after-token-checkpoint" {
			return errors.New("interrupt after the token")
		}
		return nil
	})
	if !errors.Is(err, ErrInjectedFault) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	_ = first.Close()
	second := f.acquire(2)
	defer second.Close()
	recovered, recoverErr := second.Recover(f.live, result.TransactionID, nil)
	if recoverErr != nil || recovered.Status != StatusFailed || len(recovered.Ledger) != 0 {
		t.Fatalf("recovered = %+v err=%v", recovered, recoverErr)
	}
}
