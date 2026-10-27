package audit

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// OpenExisting is for a reader that must never bring a journal into being: a missing file, even one that
// vanished after the caller looked, is an error and leaves no file behind.
func TestOpenExistingNeverCreatesTheJournal(t *testing.T) {
	requireLinuxJournalOpen(t)
	dir := privateJournalTestDir(t)
	path := filepath.Join(dir, "missing.jsonl")
	if journal, err := OpenExisting(path); !errors.Is(err, fs.ErrNotExist) {
		if journal != nil {
			_ = journal.Close()
		}
		t.Fatalf("OpenExisting of a missing journal = %v, want a not-exist error", err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("OpenExisting left %d entries behind (err %v)", len(entries), err)
	}
}

// Apart from creation it is Open: the same exclusive lock, the same verified trace, the same checks.
func TestOpenExistingLocksAndVerifiesLikeOpen(t *testing.T) {
	requireLinuxJournalOpen(t)
	path := filepath.Join(privateJournalTestDir(t), "journal.jsonl")
	created, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := created.Append(Event{Kind: "fact", ID: "one", Data: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExisting(path); !errors.Is(err, ErrAlreadyOpen) {
		t.Fatalf("OpenExisting while Open holds the journal = %v, want ErrAlreadyOpen", err)
	}
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}
	existing, err := OpenExisting(path)
	if err != nil {
		t.Fatalf("OpenExisting of an existing journal: %v", err)
	}
	trace, err := existing.Trace()
	if err != nil || len(trace.Records) != 1 || trace.Records[0].Event.ID != "one" {
		t.Fatalf("trace = %+v, %v; want the one record Open wrote", trace.Records, err)
	}
	if _, err := Open(path); !errors.Is(err, ErrAlreadyOpen) {
		t.Fatalf("Open while OpenExisting holds the journal = %v, want ErrAlreadyOpen", err)
	}
	if err := existing.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExisting(path); !errors.Is(err, ErrInsecurePermissions) {
		t.Fatalf("OpenExisting of a 0644 journal = %v, want ErrInsecurePermissions", err)
	}
}
