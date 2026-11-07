package runner

import (
	"context"
	"time"
)

// IntervalClosure is a collector's answer to whether an adversarial interval's observation may end early (D49). It is
// built from durable proof only; elapsed time never makes it Proven.
type IntervalClosure struct {
	// Proven is true when the interval is provably complete: the request's receipt verified, every command settled, the
	// host scans clean, the post-teardown and final-cleanup snapshots taken.
	Proven bool `json:"proven"`
	// DrainUntil is, when Proven, the earliest instant the network observation may end: the proof plus the registered
	// drain grace (a deadline, not proof from silence).
	DrainUntil time.Time `json:"drain_until"`
	// Horizon is the original end of the observation (the fixed monitor window). Without proof the observation lasts
	// until it. Zero when no interval is known.
	Horizon time.Time `json:"horizon"`
	// Digest is the closure-evidence digest of the proof; Reasons says why there is none.
	Digest  string   `json:"digest"`
	Reasons []string `json:"reasons"`
	// Unresolved is the part of Reasons that is missing or contradicting evidence (an unsettled command, an incomplete
	// scan, an unresolved drain, an UNKNOWN step), as opposed to a legitimate full window (a no-reap cell left running
	// on purpose, a command in the host network namespace). The horizon does not erase it: the oracles it concerns are
	// evidence_lost unless they observed an effect.
	Unresolved []string `json:"unresolved"`
}

// ClosureSource tells a collector that ends its observation by itself (the NET end canary) when it may. The PROC
// collector is the source: it alone sees the guest's settlement and scans.
type ClosureSource interface {
	Closure(ctx context.Context, ref EvidenceRef) IntervalClosure
}
