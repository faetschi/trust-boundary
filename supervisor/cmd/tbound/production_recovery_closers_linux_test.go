//go:build linux

package main

import (
	"reflect"
	"testing"
)

// After Activate the binder replaces the recovery candidate's closer with the activated
// handles' closers. Everything registered after the candidate (the live root among it) stays.
func TestTransferCandidateClosersRemovesTheCandidateAndKeepsTheLiveRoot(t *testing.T) {
	var ran []string
	closer := func(name string) func() error {
		return func() error { ran = append(ran, name); return nil }
	}
	owner, store := closer("owner"), closer("store")
	candidate := closer("candidate")
	publicationRoot, publicationJournal := closer("publication-root"), closer("publication-journal")
	lease, liveRoot := closer("lease"), closer("live-root")
	managed, admin, prepared := closer("managed"), closer("admin"), closer("prepared")

	stack := []func() error{owner, store, candidate, publicationRoot, publicationJournal, lease, liveRoot}
	stack, ok := transferCandidateClosers(stack, candidate, managed, admin, prepared)
	if !ok {
		t.Fatal("the candidate closer was not found on the stack")
	}
	if len(stack) != 9 {
		t.Fatalf("stack has %d closers, want 9 (7 - candidate + 3 activated)", len(stack))
	}
	for index := len(stack) - 1; index >= 0; index-- {
		if err := stack[index](); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"prepared", "admin", "managed", "live-root", "lease", "publication-journal", "publication-root", "store", "owner"}
	if !reflect.DeepEqual(ran, want) {
		t.Fatalf("closers ran as %v, want %v: the live root must still be closed and the candidate must be gone", ran, want)
	}
}

// A stack that does not hold the candidate closer is reported, and the activated handles' closers
// are on the returned stack anyway, so a failure path still closes them.
func TestTransferCandidateClosersReportsAStackWithoutTheCandidate(t *testing.T) {
	a, b, candidate, activated := func() error { return nil }, func() error { return nil }, func() error { return nil }, func() error { return nil }
	out, ok := transferCandidateClosers([]func() error{a, b}, candidate, activated)
	if ok || len(out) != 3 {
		t.Fatalf("ok=%v len=%d, want false and 3 (both kept closers and the activated one)", ok, len(out))
	}
}
