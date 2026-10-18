package audit

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func committedTestJournal(t *testing.T, records int) (*Journal, string) {
	t.Helper()
	requireLinuxJournalOpen(t)
	path := filepath.Join(privateJournalTestDir(t), "journal.jsonl")
	journal, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	for i := 0; i < records; i++ {
		if _, err := journal.Append(Event{Kind: "decision", ID: string(rune('a' + i))}); err != nil {
			t.Fatal(err)
		}
	}
	return journal, path
}

func TestTraceCommittedAcceptsTheJournalAsCommitted(t *testing.T) {
	journal, _ := committedTestJournal(t, 3)
	committed, err := journal.TraceCommitted()
	if err != nil {
		t.Fatal(err)
	}
	plain, err := journal.Trace()
	if err != nil || len(committed.Records) != 3 || len(plain.Records) != 3 ||
		committed.Records[2].Hash != plain.Records[2].Hash {
		t.Fatalf("committed %d records, plain %d, err=%v", len(committed.Records), len(plain.Records), err)
	}
	// a journal that has committed nothing is committed as empty
	empty, _ := committedTestJournal(t, 0)
	if trace, err := empty.TraceCommitted(); err != nil || len(trace.Records) != 0 {
		t.Fatalf("empty journal: %d records err=%v", len(trace.Records), err)
	}
	// and it keeps accepting appends afterwards
	if _, err := journal.Append(Event{Kind: "decision", ID: "after"}); err != nil {
		t.Fatal(err)
	}
	if trace, err := journal.TraceCommitted(); err != nil || len(trace.Records) != 4 {
		t.Fatalf("after append: %d records err=%v", len(trace.Records), err)
	}
}

// A shortened file is still a valid hash chain, so Trace verifies it; TraceCommitted must not.
func TestTraceCommittedRefusesAJournalShorterThanWhatWasCommitted(t *testing.T) {
	journal, path := committedTestJournal(t, 3)
	whole, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	cut := 0
	for i, b := range whole {
		if b == '\n' {
			lines++
			if lines == 2 {
				cut = i + 1
			}
		}
	}
	if err := os.Truncate(path, int64(cut)); err != nil {
		t.Fatal(err)
	}
	if trace, err := journal.Trace(); err != nil || len(trace.Records) != 2 {
		t.Fatalf("Trace must keep verifying the valid prefix: %d records err=%v", len(trace.Records), err)
	}
	if trace, err := journal.TraceCommitted(); err == nil || !errors.Is(err, ErrCorrupt) || len(trace.Records) != 0 {
		t.Fatalf("truncation after commit was accepted: %d records err=%v", len(trace.Records), err)
	}
}

func TestTraceCommittedRefusesAnEmptyFileAfterCommit(t *testing.T) {
	journal, path := committedTestJournal(t, 2)
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	if trace, err := journal.Trace(); err != nil || len(trace.Records) != 0 {
		t.Fatalf("Trace must keep verifying an empty file: %d records err=%v", len(trace.Records), err)
	}
	if trace, err := journal.TraceCommitted(); err == nil || !errors.Is(err, ErrCorrupt) || len(trace.Records) != 0 {
		t.Fatalf("an emptied journal was accepted: %d records err=%v", len(trace.Records), err)
	}
}

func TestTraceCommittedFollowsTheStateOfTheHandle(t *testing.T) {
	journal, _ := committedTestJournal(t, 1)
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.TraceCommitted(); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed journal: %v", err)
	}
}
