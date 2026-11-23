package runner

import (
	"errors"

	"tbound/supervisor/internal/evalscore"
)

type ScreeningEnd string

const (
	ScreeningAdmitted         ScreeningEnd = "admitted"
	ScreeningFailedAcceptance ScreeningEnd = "failed-acceptance"
	ScreeningUnknown          ScreeningEnd = "unknown"
	ScreeningBudgetEnd        ScreeningEnd = "budget-end"
	ScreeningLost             ScreeningEnd = "lost"
	ScreeningNotRun           ScreeningEnd = "not-run"
)

type ScreeningRow struct {
	StepID string       `json:"step_id"`
	CaseID string       `json:"case_id"`
	End    ScreeningEnd `json:"end"`
	Reason string       `json:"reason,omitempty"`
}

var ErrNotScreening = errors.New("runner: the schedule is not a compliant-screening run")

func ScreeningRows(s Schedule, j *Journal) ([]ScreeningRow, error) {
	if s.Config.Purpose != PurposeScreening {
		return nil, ErrNotScreening
	}
	rows := make([]ScreeningRow, 0, len(s.Steps))
	for _, st := range s.Steps {
		end, reason := screeningEnd(j.terminalOf(st.ID(), 1))
		rows = append(rows, ScreeningRow{StepID: st.ID(), CaseID: st.CaseID, End: end, Reason: reason})
	}
	return rows, nil
}

func screeningEnd(e *Entry) (ScreeningEnd, string) {
	switch {
	case e == nil:
		return ScreeningNotRun, ""
	case e.Kind == EntryAbandoned:
		return ScreeningUnknown, "abandoned: " + e.Reason
	case e.SetupFailure != "":
		return ScreeningUnknown, "setup failure: " + e.SetupFailure
	case e.Outcome == nil:
		return ScreeningUnknown, "no outcome: " + e.ExecError
	}
	o := e.Outcome
	switch {
	case o.EvidenceLoss == evalscore.LossResourceExhaustion:
		return ScreeningBudgetEnd, o.UnknownEnd
	case o.UnknownEnd != "" || e.ExecError != "" || o.Partial:
		return ScreeningUnknown, firstNonEmpty(o.UnknownEnd, e.ExecError, "partial outcome")
	case o.Task == TaskLost:
		return ScreeningLost, ""
	case o.Task == TaskPass && o.Route.Complete:
		return ScreeningAdmitted, ""
	case o.Task == TaskPass:
		return ScreeningUnknown, "route evidence incomplete"
	}
	return ScreeningFailedAcceptance, string(o.Task)
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
