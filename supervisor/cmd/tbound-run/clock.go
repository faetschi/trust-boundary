package main

import (
	"context"
	"fmt"
	"time"

	"tbound/supervisor/internal/evalcorpus"
	"tbound/supervisor/internal/procoracle"
	"tbound/supervisor/internal/runner"
)

// PROC starts an interval on the host clock and accepts the guest monitor's first poll, stamped on the
// guest clock, at most procoracle.GuestMaxGap (1 s) later; the ssh launch of the monitor spends part of
// that second. A guest clock 0.65 s ahead of the host lost every PROC begin, and with it NET-01, in F29.
// maxClockSkew is the offset the run refuses, well inside the budget.
const (
	maxClockSkew = 250 * time.Millisecond
	clockSamples = 3
)

// clockSample is one reading of the guest clock: the host read sent before it launched proc-capture and
// received when the document came back, and the guest stamped guest in between. Whatever the transport
// took, the guest clock is at least ahead() ahead of the host clock and at least behind() behind it.
type clockSample struct{ sent, guest, received time.Time }

func (s clockSample) ahead() time.Duration  { return s.guest.Sub(s.received) }
func (s clockSample) behind() time.Duration { return s.sent.Sub(s.guest) }

// sampleGuestClock reads the guest clock with proc-cell and proc-capture, the read-only evidence
// commands PROC itself uses: the capture's snapshot carries the guest's observation time.
func sampleGuestClock(ctx context.Context, ev runner.EvidenceClient, cellUID, cellGID uint32) (clockSample, error) {
	var cell procoracle.CellDoc
	if err := ev.CallDoc(ctx, &cell, runner.EvidenceProcCell); err != nil {
		return clockSample{}, err
	}
	if cell.CellUID != cellUID || cell.CellGID != cellGID {
		return clockSample{}, fmt.Errorf("the guest's registered cell %d:%d is not %d:%d", cell.CellUID, cell.CellGID, cellUID, cellGID)
	}
	sent := time.Now()
	reg := procoracle.GuestRegistration(cell, sent, sent)
	var doc procoracle.CaptureDoc
	err := ev.CallDoc(ctx, &doc, runner.EvidenceProcCapture, "--registration", reg.Digest(),
		"--interval-start", procoracle.InstantArg(sent), "--deadline", procoracle.InstantArg(sent), "--point", string(procoracle.PointPostTeardown))
	received := time.Now()
	if err != nil {
		return clockSample{}, err
	}
	if doc.RegistrationDigest != reg.Digest() || doc.Snapshot.ObservedAt.IsZero() {
		return clockSample{}, fmt.Errorf("the capture document does not answer the clock reading")
	}
	return clockSample{sent: sent, guest: doc.Snapshot.ObservedAt, received: received}, nil
}

// checkClock refuses when any sample proves the guest clock more than maxClockSkew off the host clock,
// or when the clock cannot be read.
func checkClock(ctx context.Context, sample func(context.Context) (clockSample, error)) error {
	var ahead, behind time.Duration
	for range clockSamples {
		s, err := sample(ctx)
		if err != nil {
			return fmt.Errorf("the guest clock cannot be read: %w", err)
		}
		ahead, behind = max(ahead, s.ahead()), max(behind, s.behind())
	}
	off, way := ahead, "ahead of"
	if behind > ahead {
		off, way = behind, "behind"
	}
	if off <= maxClockSkew {
		return nil
	}
	return fmt.Errorf("the guest clock is at least %s %s the host clock, more than %s, and PROC compares the two (H1-04): "+
		"resync the Windows host (w32tm /resync /force) and the guest (chronyc makestep), then run again", off.Round(time.Millisecond), way, maxClockSkew)
}

// clockGuard checks the guest clock before every adversarial step, the steps PROC observes. A refusal
// comes from Prepare, so it voids the begin: no unit is spent and the run stops.
type clockGuard struct {
	runner.Executor
	sample func(context.Context) (clockSample, error)
}

func (g clockGuard) Prepare(ctx context.Context, s runner.Step) error {
	if s.Kind == evalcorpus.KindAdversarial {
		if err := checkClock(ctx, g.sample); err != nil {
			return fmt.Errorf("%w: %v", runner.ErrSetupRefused, err)
		}
	}
	return g.Executor.Prepare(ctx, s)
}
