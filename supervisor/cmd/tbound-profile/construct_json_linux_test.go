//go:build linux

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/runner"
)

func TestConstructRejectsCaseFoldedEvidenceMembers(t *testing.T) {
	w := buildWorld(t, worldOpts{only: []string{constructWSCase, constructReadCase}})
	for _, tc := range []struct {
		name, file, old, replacement, caseID, check string
	}{
		{"accept route", "accept/" + constructReadCase + ".reference.json", `"route":"trial-accept-cell"`,
			`"route":"foreign","Route":"trial-accept-cell"`, constructReadCase, "acceptance-cell"},
		{"journal kind", "run/journal.jsonl", `"kind":"header"`,
			`"kind":"foreign","Kind":"header"`, constructWSCase, "calibration-p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := w.copyEvidence()
			editFile(t, filepath.Join(ev, tc.file), func(raw []byte) []byte {
				if !bytes.Contains(raw, []byte(tc.old)) {
					t.Fatal("test mutation target missing")
				}
				return bytes.Replace(raw, []byte(tc.old), []byte(tc.replacement), 1)
			})
			wantRow(t, rowOf(t, w.apply(ev, tc.caseID), tc.caseID, tc.check), "guest-only", "duplicate")
		})
	}
}

func TestConstructJournalRecordOverOneMiBReads(t *testing.T) {
	w := buildWorld(t, worldOpts{only: []string{constructWSCase}})
	path := filepath.Join(w.ev, "run", "journal.jsonl")
	longError := strings.Repeat("executor diagnostic ", (1<<20)/20+1)
	edited := false
	editJournal(t, path, func(e *runner.Entry) {
		if !edited && e.Kind == runner.EntryDone && e.Outcome != nil && e.StepID != staticPOf(constructWSCase) {
			e.ExecError, e.Outcome.Partial, edited = longError, true, true
		}
	})
	if !edited {
		t.Fatal("no terminal record to enlarge")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sched, err := constructSchedule(w.input())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ReadJournal(raw, sched); err != nil {
		t.Fatalf("runner refuses its otherwise valid large record: %v", err)
	}
	wantRow(t, rowOf(t, w.apply(w.ev, constructWSCase), constructWSCase, "calibration-p"), "pass", "")
}
