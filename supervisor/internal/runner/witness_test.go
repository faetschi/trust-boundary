package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/evalcorpus"
)

// witnessSchedule plans the small synthetic corpus as a construct-witness run. Its study plan is
// smallSchedule(t, smallConfig()): the same manifest and allocation.
func witnessSchedule(t testing.TB) Schedule {
	t.Helper()
	src, alloc := synth(t, smallQuotas())
	cfg := smallConfig()
	cfg.Purpose = PurposeConstructWitness
	s, err := Plan(src, alloc, cfg)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return s
}

// The witness journal is an ordinary journal of the witness plan, and its static P records are what corpus-check
// imports as P construct evidence.
func TestConstructWitnessJournalImportsAsPEvidence(t *testing.T) {
	s := witnessSchedule(t)
	_, data := finishedJournal(t, s, NewFakeExecutor(s.Config))
	read, err := ReadJournal(data, s)
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	for _, st := range s.Steps {
		e, ok := read.Final(st.ID())
		if !ok || e.Kind != EntryDone || e.Outcome == nil || e.Outcome.Calibration == nil || e.Outcome.Interval != IntervalRef(st) {
			t.Fatalf("%s: no complete P record: %+v", st.ID(), e)
		}
	}
}

// The purpose is part of the plan digest, so neither journal can stand in for the other: a witness journal cannot be
// resumed under the study plan, and a study journal cannot satisfy a witness import.
func TestConstructWitnessJournalCannotBeMixedWithTheStudyPlan(t *testing.T) {
	w := witnessSchedule(t)
	study := smallSchedule(t, smallConfig())
	witnessDir := t.TempDir()
	j, _ := runAll(t, w, witnessDir, NewFakeExecutor(w.Config))
	j.Close()
	if _, err := OpenJournal(filepath.Join(witnessDir, "journal.jsonl"), study); !errors.Is(err, ErrPlanMismatch) {
		t.Fatalf("the study plan resumed a witness journal: %v", err)
	}
	studyDir := t.TempDir()
	j, _ = runAll(t, study, studyDir, NewFakeExecutor(study.Config))
	j.Close()
	data, err := os.ReadFile(filepath.Join(studyDir, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadJournal(data, w); !errors.Is(err, ErrPlanMismatch) {
		t.Fatalf("a study journal satisfied the witness import: %v", err)
	}
}

// An import refuses a witness journal that holds a step the witness plan does not hold, even though its header is the
// witness plan's.
func TestConstructWitnessJournalRefusesAnInjectedStep(t *testing.T) {
	w := witnessSchedule(t)
	_, data := finishedJournal(t, w, NewFakeExecutor(w.Config))
	c := findStep(t, smallSchedule(t, smallConfig()), firstCase(w, "adversarial"), RoleTrajectory, CfgC)
	seq := bytes.Count(data, []byte("\n")) // a record's seq is the count of records before it
	extra, err := json.Marshal(Entry{Seq: seq, Kind: EntryBegin, StepID: c.ID(), CaseID: c.CaseID, Index: c.Index, Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadJournal(append(data, append(extra, '\n')...), w); !errors.Is(err, ErrJournalCorrupt) || !strings.Contains(err.Error(), "unknown step") {
		t.Fatalf("an injected C trajectory was not refused as an unknown step: %v", err)
	}
}

// Export and the calibration summary have no confirmatory outcomes to build from a witness run, so they refuse it
// rather than report the missing C outcomes as failures.
func TestConstructWitnessRefusesExportAndCalibrations(t *testing.T) {
	w := witnessSchedule(t)
	j, _ := finishedJournal(t, w, NewFakeExecutor(w.Config))
	if _, err := Export(w, j, ExportOptions{}); !errors.Is(err, ErrNonConfirmatoryPurpose) {
		t.Fatalf("Export: %v", err)
	}
	if _, err := Calibrations(w, j); !errors.Is(err, ErrNonConfirmatoryPurpose) {
		t.Fatalf("Calibrations: %v", err)
	}
}

// NewRunner refuses a witness schedule that holds any step but a static P replay, so no unit of another configuration
// or role is dispatched.
func TestConstructWitnessRunnerRefusesOtherSteps(t *testing.T) {
	w := witnessSchedule(t)
	adv := w.Steps[0]
	const comp = "cmp-read-001"
	for name, injected := range map[string]Step{
		"C trajectory":      {CaseID: adv.CaseID, Kind: adv.Kind, Configuration: CfgC, Role: RoleTrajectory},
		"C static replay":   {CaseID: adv.CaseID, Kind: adv.Kind, Configuration: CfgC, Role: RoleStaticReplay},
		"P adaptive replay": {CaseID: adv.CaseID, Kind: adv.Kind, Configuration: CfgP, Role: RoleAdaptiveReplay},
		"B compliant":       {CaseID: comp, Kind: evalcorpus.KindCompliant, Configuration: CfgB, Role: RoleCompliant},
		"V compliant":       {CaseID: comp, Kind: evalcorpus.KindCompliant, Configuration: CfgV, Role: RoleCompliant},
	} {
		bad := w
		bad.Steps = append(slices.Clone(w.Steps), injected)
		bad.Steps[len(bad.Steps)-1].Index = len(w.Steps)
		j := openJournal(t, t.TempDir(), w)
		fake := NewFakeExecutor(w.Config)
		if _, err := NewRunner(bad, j, fake, Options{}); err == nil {
			t.Errorf("%s: NewRunner accepted a witness schedule that holds it", name)
		}
		if n := len(fake.Calls()); n != 0 {
			t.Errorf("%s: %d steps dispatched", name, n)
		}
		j.Close()
	}
	j := openJournal(t, t.TempDir(), w)
	defer j.Close()
	if _, err := NewRunner(w, j, NewFakeExecutor(w.Config), Options{}); err != nil {
		t.Fatalf("NewRunner on the witness plan: %v", err)
	}
}
