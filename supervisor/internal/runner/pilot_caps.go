package runner

import (
	"slices"

	"tbound/supervisor/internal/calibration"
)

func PilotCases(s Schedule, j *Journal) []calibration.PilotCase {
	order, groups := caseGroups(s)
	var out []calibration.PilotCase
	for _, id := range order {
		steps := groups[id]
		if !slices.ContainsFunc(steps, func(st Step) bool { return st.Role == RoleAdaptiveReplay && st.Configuration == CfgP }) {
			continue
		}
		rerun, done := complete(j, steps)
		if !done {
			out = append(out, calibration.PilotCase{CaseID: id, Status: calibration.NotRun, Reason: "a step or its rerun has no terminal record"})
			continue
		}
		attempt := 1
		if rerun {
			attempt = 2
		}
		pair := s.Config.pairOf(advView(j, steps, attempt))
		status, err := calibration.Disposition(pair, s.Config.AdaptiveCaps)
		c := calibration.PilotCase{CaseID: id, Status: status, Adaptive: pair.Adaptive}
		if err != nil {
			c.Reason = err.Error()
		}
		out = append(out, c)
	}
	return out
}
