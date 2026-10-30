package runner

import (
	"context"
	"encoding/hex"

	"tbound/supervisor/internal/attacker"
)

// Machine is the part of attacker.Machine that the runner drives. Next takes
// only the projected Feedback (reason code and capability text): an oracle
// verdict never reaches it as feedback.
type Machine interface {
	Start() (attacker.State, attacker.Action)
	Next(attacker.State, attacker.Feedback) (attacker.State, attacker.Action)
	Charge(attacker.State, attacker.Usage) attacker.State
}

// ManifestMarker is the runner-side terminal call for an oracle verdict that
// the effect manifested. It matches attacker.Machine.MarkManifested, which
// package attacker provides since commit be53c0e. It is a separate optional
// interface so that test doubles need not implement it: if the machine has
// it, DriveAdaptive uses it; if not, DriveAdaptive ends the run itself with a
// manifested stop and the machine state as it stood.
type ManifestMarker interface {
	MarkManifested(attacker.State) (attacker.State, attacker.Action)
}

var (
	_ Machine        = attacker.Machine{}
	_ ManifestMarker = attacker.Machine{} // the real API (commit be53c0e)
)

// PivotResult is what an executor reports for one pivot. Feedback is what the
// enforcement surface showed the attacker. Manifested is the independent
// oracle's verdict for the pivot and is kept apart from Feedback on purpose.
type PivotResult struct {
	Feedback   attacker.Feedback
	Usage      attacker.Usage
	Manifested bool
}

// PivotFunc executes one pivot action against the target and reports the result.
type PivotFunc func(ctx context.Context, a attacker.Action) (PivotResult, error)

// DriveAdaptive runs the adaptive attacker to a terminal state and returns it.
// Attempts counts the pivots executed, so for a manifested run it is the
// attempts-to-manifest input of calibration.PrefixCurve. The machine's caps end
// the loop; the context and a pivot error end it early with the error.
func DriveAdaptive(ctx context.Context, m Machine, pivot PivotFunc) (AttackerTerminal, error) {
	st, act := m.Start()
	attempts := 0
	for act.Kind == attacker.ActionPivot {
		if err := ctx.Err(); err != nil {
			return AttackerTerminal{}, err
		}
		res, err := pivot(ctx, act)
		if err != nil {
			return AttackerTerminal{}, err
		}
		attempts++
		st = m.Charge(st, res.Usage)
		if res.Manifested {
			if mk, ok := m.(ManifestMarker); ok {
				st, act = mk.MarkManifested(st)
			} else {
				act = attacker.Action{Kind: attacker.ActionStop, Stop: attacker.StopManifested}
			}
			break
		}
		st, act = m.Next(st, res.Feedback)
	}
	stop := st.Stop()
	if act.Kind == attacker.ActionStop {
		stop = act.Stop
	}
	d := st.Digest()
	return AttackerTerminal{
		Stop:        stop.String(),
		Attempts:    attempts,
		Iterations:  st.Iterations(),
		StateDigest: hex.EncodeToString(d[:]),
	}, nil
}
