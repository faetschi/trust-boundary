package runner

import (
	"errors"
	"path/filepath"
	"testing"
)

// The journal header records the run binding; another binding (or none) is refused.
func TestJournalRefusesAChangedRunBinding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.jsonl")
	s := smallSchedule(t, smallConfig())
	s.RunBinding = "sha256:" + hexOf('a')
	j, err := OpenJournal(path, s)
	if err != nil {
		t.Fatal(err)
	}
	_ = j.Close()

	for name, binding := range map[string]string{"other": "sha256:" + hexOf('b'), "none": ""} {
		other := s
		other.RunBinding = binding
		if _, err := OpenJournal(path, other); !errors.Is(err, ErrPlanMismatch) {
			t.Fatalf("%s binding: got %v, want ErrPlanMismatch", name, err)
		}
	}
	j2, err := OpenJournal(path, s)
	if err != nil {
		t.Fatalf("the original binding must resume: %v", err)
	}
	if _, err := NewRunner(func() Schedule { o := s; o.RunBinding = "sha256:" + hexOf('c'); return o }(), j2, NewFakeExecutor(s.Config), Options{}); !errors.Is(err, ErrPlanMismatch) {
		t.Fatalf("NewRunner with another binding: %v", err)
	}
	_ = j2.Close()

	// A journal made without a binding stays openable without one and is refused with one.
	path2 := filepath.Join(dir, "plain.jsonl")
	plain := smallSchedule(t, smallConfig())
	j3, err := OpenJournal(path2, plain)
	if err != nil {
		t.Fatal(err)
	}
	_ = j3.Close()
	if _, err := OpenJournal(path2, s); !errors.Is(err, ErrPlanMismatch) {
		t.Fatalf("binding on an unbound journal: %v", err)
	}
	if j4, err := OpenJournal(path2, plain); err != nil {
		t.Fatal(err)
	} else {
		_ = j4.Close()
	}
}
