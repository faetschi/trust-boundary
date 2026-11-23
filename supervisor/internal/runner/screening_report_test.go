package runner

import (
	"context"
	"errors"
	"testing"

	"tbound/supervisor/internal/evalscore"
)

func TestScreeningRowsClassifyEachEnd(t *testing.T) {
	s := screeningSchedule(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake := NewFakeExecutor(s.Config)
	fake.Hook = func(st Step, o *Outcome) error {
		switch st.Index {
		case 1:
			o.Task = TaskFail
		case 2:
			o.Task = TaskLost
		case 3:
			o.EvidenceLoss, o.UnknownEnd = evalscore.LossResourceExhaustion, "resource_exhaustion"
		case 4:
			o.UnknownEnd = "task-outcome-unknown"
		case 5:
			o.Route.Complete = false
		case 6:
			cancel()
		}
		return nil
	}
	j := openJournal(t, t.TempDir(), s)
	defer j.Close()
	_, _ = mustRunner(t, s, j, fake, Options{}).Run(ctx)
	rows, err := ScreeningRows(s, j)
	if err != nil {
		t.Fatal(err)
	}
	want := []ScreeningEnd{ScreeningAdmitted, ScreeningFailedAcceptance, ScreeningLost, ScreeningBudgetEnd, ScreeningUnknown, ScreeningUnknown}
	if len(rows) != len(s.Steps) {
		t.Fatalf("%d rows for %d steps", len(rows), len(s.Steps))
	}
	for i, w := range want {
		if rows[i].End != w || rows[i].CaseID != s.Steps[i].CaseID {
			t.Errorf("row %d = %+v, want end %s", i, rows[i], w)
		}
	}
	if rows[6].End != ScreeningAdmitted {
		t.Errorf("the step that stopped the run finished: %+v, want admitted", rows[6])
	}
	for _, r := range rows[7:] {
		if r.End != ScreeningNotRun {
			t.Errorf("a step after the stop is %+v, want not-run", r)
		}
	}
}

func TestScreeningRowsRefuseAStudySchedule(t *testing.T) {
	s := smallSchedule(t, smallConfig())
	j := openJournal(t, t.TempDir(), s)
	defer j.Close()
	if _, err := ScreeningRows(s, j); !errors.Is(err, ErrNotScreening) {
		t.Fatalf("got %v, want ErrNotScreening", err)
	}
}
