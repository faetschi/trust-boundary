package runner

import (
	"errors"
	"fmt"
	"strings"
)

// The adaptive attacker budget is cumulative across the attempts of a case and role (D32: the caps hold across
// retries and recovery generations). The service starts attempt 2 with the caps minus what attempt 1 spent
// and binds three members in the receipt: this attempt's usage, the cumulative usage, and the predecessor.
// The machine of attempt 2 restarts from its initial state (only the budget continues): the retry runs on a
// reset fixture, and the thesis fixes the budget, not the tried steps, as cumulative.

// AttackerUsage is the spend of an adaptive replay against the pinned caps: pivots issued, tool calls and
// wall-clock milliseconds and tokens (omitted from JSON when zero, as a scripted attacker spends none).
type AttackerUsage struct {
	Iterations int64 `json:"iterations"`
	ToolCalls  int64 `json:"tool_calls"`
	WallMS     int64 `json:"wall_ms"`
	InTokens   int64 `json:"in_tokens,omitempty"`
	OutTokens  int64 `json:"out_tokens,omitempty"`
}

func (u AttackerUsage) add(other AttackerUsage) AttackerUsage {
	return AttackerUsage{Iterations: u.Iterations + other.Iterations, ToolCalls: u.ToolCalls + other.ToolCalls, WallMS: u.WallMS + other.WallMS,
		InTokens: u.InTokens + other.InTokens, OutTokens: u.OutTokens + other.OutTokens}
}

func (u AttackerUsage) valid() bool {
	return u.Iterations >= 0 && u.ToolCalls >= 0 && u.WallMS >= 0 && u.InTokens >= 0 && u.OutTokens >= 0
}

// AttackerPredecessor is the receipt's reference to the earlier attempt whose budget the run continued.
type AttackerPredecessor struct {
	AdmissionID     string        `json:"admission_id"`
	StateDigest     string        `json:"state_digest"`
	CumulativeUsage AttackerUsage `json:"cumulative_usage"`
}

// ErrAdaptiveBudget means the adaptive attempts of a case do not share one budget within the pinned caps.
var ErrAdaptiveBudget = errors.New("runner: adaptive attacker budget is not cumulative within the pinned caps")

// checkAttackerUsage checks the arithmetic of one adaptive receipt: both usages are present and non-negative,
// and the cumulative usage is this attempt's usage, plus the predecessor's cumulative usage if there is one.
func checkAttackerUsage(r *ReceiptFacts) error {
	switch {
	case r.AttackerUsage == nil || r.AttackerCumulativeUsage == nil:
		return errors.New("the receipt lacks attacker_usage or attacker_cumulative_usage")
	case !r.AttackerUsage.valid() || !r.AttackerCumulativeUsage.valid():
		return errors.New("the receipt has a negative attacker usage")
	}
	want := *r.AttackerUsage
	if p := r.AttackerPredecessor; p != nil {
		if p.AdmissionID == "" || p.StateDigest == "" || !p.CumulativeUsage.valid() {
			return errors.New("the receipt's attacker predecessor is incomplete")
		}
		want = p.CumulativeUsage.add(want)
	}
	if *r.AttackerCumulativeUsage != want {
		return fmt.Errorf("the receipt's cumulative attacker usage %+v is not the predecessor's plus this attempt's, %+v", *r.AttackerCumulativeUsage, want)
	}
	return nil
}

// attackerAttempts is the number of pivots the case's adaptive run has spent in all attempts: the cumulative
// iterations of the receipt, or, for a receipt without them, the pivots it issued.
func attackerAttempts(r *ReceiptFacts) int {
	if r.AttackerCumulativeUsage != nil {
		return int(r.AttackerCumulativeUsage.Iterations)
	}
	return r.PivotsIssued
}

// AdaptiveBudgetFlag names a case whose C and P adaptive halves started their scored attempt with different
// cumulative budgets already spent. D32 gives C the identical attacker machine and cumulative budget, so the
// pair is not a valid adaptive P/C pair: Export marks its scored record with pc_evidence_complete false.
type AdaptiveBudgetFlag struct {
	CaseID string        `json:"case_id"`
	C      AttackerUsage `json:"c_started_with"`
	P      AttackerUsage `json:"p_started_with"`
}

func (f AdaptiveBudgetFlag) String() string {
	return fmt.Sprintf("%s: the scored adaptive attempts of C and P started with different cumulative budgets already spent (C %+v, P %+v)", f.CaseID, f.C, f.P)
}

// checkAdaptiveBudgets verifies, for every adaptive replay step of the journal, that its attempts share one
// budget: each receipt's usage adds up, the cumulative usage stays within the pinned caps, attempt 1 has no
// predecessor, and attempt 2 names attempt 1 (its state digest and cumulative usage) as its predecessor. It
// also compares the budget the scored adaptive attempts of the C and the P half started with and returns the
// cases where they differ as flags: when only one half was rerun, that half had less budget left.
func checkAdaptiveBudgets(s Schedule, j *Journal) ([]AdaptiveBudgetFlag, error) {
	flags := []AdaptiveBudgetFlag{}
	caps := s.Config.AdaptiveCaps
	order, groups := caseGroups(s)
	for _, id := range order {
		start := map[Configuration]AttackerUsage{}
		for _, st := range groups[id] {
			if st.Role != RoleAdaptiveReplay {
				continue
			}
			var first *ReceiptFacts
			for attempt := 1; attempt <= 2; attempt++ {
				t := j.terminalOf(st.ID(), attempt)
				if t == nil || t.Outcome == nil || t.Outcome.Receipt == nil || t.Outcome.Receipt.AttackerCumulativeUsage == nil {
					continue // no usable receipt: Attest refused the outcome and it is exported as partial
				}
				r := t.Outcome.Receipt
				if err := checkAttackerUsage(r); err != nil {
					return nil, fmt.Errorf("%w: %s attempt %d: %v", ErrAdaptiveBudget, st.ID(), attempt, err)
				}
				cumulative := *r.AttackerCumulativeUsage
				if (caps.Iterations > 0 && cumulative.Iterations > int64(caps.Iterations)) ||
					(caps.ToolCalls > 0 && cumulative.ToolCalls > int64(caps.ToolCalls)) ||
					(caps.WallMS > 0 && cumulative.WallMS > int64(caps.WallMS)) {
					return nil, fmt.Errorf("%w: %s attempt %d spent %+v in all, over the caps (iterations %d, tool calls %d, wall %d ms)",
						ErrAdaptiveBudget, st.ID(), attempt, cumulative, caps.Iterations, caps.ToolCalls, caps.WallMS)
				}
				p := r.AttackerPredecessor
				switch {
				case attempt == 1 && p != nil:
					return nil, fmt.Errorf("%w: %s attempt 1 names a predecessor", ErrAdaptiveBudget, st.ID())
				case attempt == 2 && (first == nil || p == nil || p.StateDigest != first.AttackerStateDigest ||
					p.CumulativeUsage != *first.AttackerCumulativeUsage || !strings.HasSuffix(p.AdmissionID, "/1")):
					return nil, fmt.Errorf("%w: %s attempt 2 does not continue the budget of attempt 1 (a rerun with a fresh budget, or a predecessor that is not the original)",
						ErrAdaptiveBudget, st.ID())
				}
				start[st.Configuration] = AttackerUsage{}
				if p != nil {
					start[st.Configuration] = p.CumulativeUsage
				}
				if attempt == 1 {
					first = r
				}
			}
		}
		c, hasC := start[CfgC]
		p, hasP := start[CfgP]
		if hasC && hasP && c != p {
			flags = append(flags, AdaptiveBudgetFlag{CaseID: id, C: c, P: p})
		}
	}
	return flags, nil
}
