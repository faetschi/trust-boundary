package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// finishedJournal runs the small plan to the end and returns its bytes next to the live handle.
func finishedJournal(t *testing.T, s Schedule, fake *FakeExecutor) (*Journal, []byte) {
	t.Helper()
	dir := t.TempDir()
	j, _ := runAll(t, s, dir, fake)
	t.Cleanup(func() { _ = j.Close() })
	data, err := os.ReadFile(filepath.Join(dir, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return j, data
}

func TestReadJournalReplaysWhatTheRunnerWrote(t *testing.T) {
	cfg := smallConfig()
	cfg.RerunHeadroom = 1
	s := smallSchedule(t, cfg)
	s.RunBinding = "sha256:" + strings.Repeat("c", 64)
	st := findStep(t, s, firstCase(s, "adversarial"), RoleTrajectory, CfgC)
	fake := NewFakeExecutor(s.Config)
	fake.Hook = outageHook(st)
	dir := t.TempDir()
	j, _ := runAll(t, s, dir, fake)
	defer j.Close()
	if err := mustRunner(t, s, j, fake, Options{}).Rerun(context.Background(), st.ID()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "journal.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The header's run binding is adopted: the reader holds none of the host inputs behind it.
	plain := s
	plain.RunBinding = ""
	read, err := ReadJournal(data, plain)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read.Entries(), j.Entries()) {
		t.Fatal("replayed entries differ from the written ones")
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, data) {
		t.Fatal("reading changed the journal")
	}

	final, ok := read.Final(st.ID())
	if !ok || final.Attempt != 2 || final.Kind != EntryDone {
		t.Fatalf("a rerun step is scored by its rerun: %+v %v", final, ok)
	}
	other := findStep(t, s, st.CaseID, RoleStaticReplay, CfgP)
	if final, ok := read.Final(other.ID()); !ok || final.Attempt != 1 {
		t.Fatalf("a step without a rerun is scored by its original: %+v %v", final, ok)
	}
	if _, ok := read.Final("no|such|step|0"); ok {
		t.Fatal("an unknown step has a final record")
	}
}

func TestReadJournalRefusesWhatOpenJournalWouldNotResume(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	_, data := finishedJournal(t, s, NewFakeExecutor(s.Config))

	for name, tc := range map[string]struct {
		data []byte
		s    Schedule
		want error
	}{
		"torn tail":    {data[:len(data)-7], s, ErrJournalCorrupt},
		"no newline":   {bytes.TrimRight(data, "\n"), s, ErrJournalCorrupt},
		"empty":        {nil, s, ErrJournalCorrupt},
		"garbage":      {append(append([]byte(nil), data...), []byte("{not json}\n")...), s, ErrJournalCorrupt},
		"other plan":   {data, smallSchedule(t, func() Config { c := smallConfig(); c.FreezeCommit = strings.Repeat("b", 40); return c }()), ErrPlanMismatch},
		"no digest":    {data, Schedule{}, nil},
		"first record": {data[bytes.IndexByte(data, '\n')+1:], s, ErrJournalCorrupt},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ReadJournal(tc.data, tc.s)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestReadJournalAppliesTheRerunRules(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	_, data := finishedJournal(t, s, NewFakeExecutor(s.Config))
	st := findStep(t, s, firstCase(s, "adversarial"), RoleTrajectory, CfgC)
	var last Entry
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	if err := json.Unmarshal(lines[len(lines)-1], &last); err != nil {
		t.Fatal(err)
	}
	begin, err := json.Marshal(Entry{Seq: last.Seq + 1, Kind: EntryBegin, StepID: st.ID(), CaseID: st.CaseID, Index: st.Index, Attempt: 2})
	if err != nil {
		t.Fatal(err)
	}
	// Attempt 1 of the step is a clean outcome, so a rerun has no evidenced outage behind it.
	if _, err := ReadJournal(append(append([]byte(nil), data...), append(begin, '\n')...), s); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("a rerun without an evidenced outage was replayed: %v", err)
	}
}

func TestFinalOfAnUnfinishedRerunIsNotAScoredAttempt(t *testing.T) {
	cfg := smallConfig()
	cfg.RerunHeadroom = 1
	s := smallSchedule(t, cfg)
	st := findStep(t, s, firstCase(s, "adversarial"), RoleTrajectory, CfgC)
	fake := NewFakeExecutor(s.Config)
	fake.Hook = outageHook(st)
	_, data := finishedJournal(t, s, fake)
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	var last Entry
	if err := json.Unmarshal(lines[len(lines)-1], &last); err != nil {
		t.Fatal(err)
	}
	begin, _ := json.Marshal(Entry{Seq: last.Seq + 1, Kind: EntryBegin, StepID: st.ID(), CaseID: st.CaseID, Index: st.Index, Attempt: 2})
	j, err := ReadJournal(append(append([]byte(nil), data...), append(begin, '\n')...), s)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := j.Final(st.ID()); ok {
		t.Fatalf("a rerun that has begun and not ended still scored attempt %d", e.Attempt)
	}
}
