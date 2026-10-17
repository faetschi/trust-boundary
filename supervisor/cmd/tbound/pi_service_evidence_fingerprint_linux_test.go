//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// evidenceFixture gives every request an owner database and a store journal file, the kind of
// per-request host evidence a settlement verification reads, and a verifier that reads them.
type evidenceFixture struct {
	dir string
	h   *incrementalJournal
}

func (f *evidenceFixture) ownerDB(requestID string) string {
	return filepath.Join(f.dir, requestID+".sqlite")
}

func (f *evidenceFixture) storeJournal(requestID string) string {
	return filepath.Join(f.dir, requestID+"-store.jsonl")
}

func newEvidenceFixture(t *testing.T) *evidenceFixture {
	t.Helper()
	root := incrementalTestRoot(t)
	f := &evidenceFixture{dir: filepath.Join(root, "evidence"), h: openIncrementalJournal(t, filepath.Join(root, "requests.jsonl"))}
	if err := os.Mkdir(f.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 8; index++ {
		id := fmt.Sprintf("request-%04d", index)
		for _, path := range []string{f.ownerDB(id), f.storeJournal(id)} {
			if err := os.WriteFile(path, []byte("settled evidence of "+id+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.h.requests.setEvidenceFiles(func(state piServiceRequestState) []string {
		id := state.accepted.RequestID
		return []string{f.ownerDB(id), f.storeJournal(id)}
	})
	// The verifier reads the evidence the way the real one does and refuses a tampered file.
	f.h.requests.settlementVerifier = func(_ context.Context, prior piServiceRequestState) error {
		id := prior.accepted.RequestID
		f.h.calls[id]++
		f.h.total++
		for _, path := range []string{f.ownerDB(id), f.storeJournal(id)} {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !bytes.Equal(data, []byte("settled evidence of "+id+"\n")) {
				return errors.New("settlement evidence of an old request no longer verifies")
			}
		}
		return nil
	}
	return f
}

// A cached verification covers the evidence files it read: changing request A's owner database
// (or its store journal) after A was cached makes the next admission verify A again and refuse,
// instead of waiting for the next service start. Unchanged priors are still not read again.
func TestCachedSettlementIsReverifiedWhenItsEvidenceFileChanged(t *testing.T) {
	for name, tamper := range map[string]func(t *testing.T, f *evidenceFixture){
		"owner database rewritten with the same size and mtime": func(t *testing.T, f *evidenceFixture) {
			path := f.ownerDB("request-0001")
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, bytes.Repeat([]byte("x"), int(before.Size())), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
				t.Fatal(err)
			}
		},
		"owner database grown": func(t *testing.T, f *evidenceFixture) {
			file, err := os.OpenFile(f.ownerDB("request-0001"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, err := file.WriteString("extra\n"); err != nil {
				t.Fatal(err)
			}
		},
		"owner database replaced by another inode": func(t *testing.T, f *evidenceFixture) {
			path := f.ownerDB("request-0001")
			replacement := path + ".new"
			if err := os.WriteFile(replacement, []byte("a different database\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, path); err != nil {
				t.Fatal(err)
			}
		},
		"owner database deleted": func(t *testing.T, f *evidenceFixture) {
			if err := os.Remove(f.ownerDB("request-0001")); err != nil {
				t.Fatal(err)
			}
		},
		"store journal rewritten": func(t *testing.T, f *evidenceFixture) {
			if err := os.WriteFile(f.storeJournal("request-0001"), []byte("rewritten\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newEvidenceFixture(t)
			defer f.h.close(t)
			for index := 1; index <= 3; index++ {
				if err := f.h.admit(t, index, true); err != nil {
					t.Fatalf("seed %d: %v", index, err)
				}
			}
			f.h.total = 0
			if err := f.h.admit(t, 4, true); err != nil || f.h.total != 1 {
				t.Fatalf("steady admission verified %d priors (err=%v), want 1", f.h.total, err)
			}
			calls := f.h.calls["request-0001"]
			tamper(t, f)
			records := len(journalKinds(t, f.h.audit))
			if err := f.h.admit(t, 5, true); !errors.Is(err, ErrPiServiceUnresolvedPrior) {
				t.Fatalf("a cached prior whose evidence changed was not re-verified and refused: %v", err)
			}
			if f.h.calls["request-0001"] != calls+1 {
				t.Fatalf("the changed prior was verified %d more times, want 1", f.h.calls["request-0001"]-calls)
			}
			if got := len(journalKinds(t, f.h.audit)); got != records {
				t.Fatalf("a refused admission appended %d records", got-records)
			}
		})
	}
}

// Only a prior whose files changed is read again, and a prior that passes again is cached
// again, so the steady state stays at one verification per newly settled request.
func TestCachedSettlementOfUnchangedEvidenceIsNotReverified(t *testing.T) {
	f := newEvidenceFixture(t)
	defer f.h.close(t)
	for index := 1; index <= 3; index++ {
		if err := f.h.admit(t, index, true); err != nil {
			t.Fatal(err)
		}
	}
	// A benign change (the file is touched, its content is the same) is verified once more,
	// passes, and is cached under the new fingerprint.
	path := f.ownerDB("request-0002")
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	f.h.total = 0
	if err := f.h.admit(t, 4, true); err != nil {
		t.Fatal(err)
	}
	if f.h.calls["request-0002"] != 2 {
		t.Fatalf("a touched evidence file was not verified again: %v", f.h.calls)
	}
	f.h.total = 0
	if err := f.h.admit(t, 5, true); err != nil || f.h.total != 1 {
		t.Fatalf("steady admission after the touch verified %d priors (err=%v), want 1", f.h.total, err)
	}
}
