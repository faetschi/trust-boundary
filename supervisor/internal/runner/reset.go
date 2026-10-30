package runner

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// DefaultTrialCommand is the guest-side launcher of the trial subcommand, reached with
// the trial key (its own SSH key, restricted by the guest to `tbound trial ...`). Like the
// evidence command it has no setpriv and no client identity.
func DefaultTrialCommand() []string { return []string{EvidenceBinaryPath, "trial"} }

// ResetExecutor makes the fixture of a case pristine around an inner Executor. The guest
// half is `tbound trial reset <case_id> [--check]`.
//
//   - Prepare runs the inner Prepare, then `reset <case> --check`, the read-only
//     precondition check. A refusal voids the begin and spends no unit.
//   - Execute runs `reset <case>` first and only then the inner Execute. A failed reset is
//     a setup error (ErrSetupRefused with an empty Outcome): the runner has journaled the
//     start, so the unit is spent, and the inner executor never runs.
//
// Use it for the configurations that run in the fixture workspace (C, P, B). V resets
// nothing.
type ResetExecutor struct {
	Inner Executor
	// Transport carries the trial command to the guest. Use a transport of its own (its
	// own SSH user and key) whose launcher is DefaultTrialCommand.
	Transport Transport
}

// Validate checks the trial transport without touching the network.
func (r ResetExecutor) Validate() error {
	if r.Transport == nil {
		return errors.New("trial transport is not wired")
	}
	if v, ok := r.Transport.(interface{ Validate() error }); ok {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("trial transport: %w", err)
		}
	}
	if t, ok := r.Transport.(interface{ launcher() []string }); ok && !slices.Equal(t.launcher(), DefaultTrialCommand()) {
		return errors.New("trial transport must launch exactly the trial command (SSHTransport.ClientCommand = DefaultTrialCommand())")
	}
	return nil
}

// Prepare implements Executor.
func (r ResetExecutor) Prepare(ctx context.Context, s Step) error {
	if r.Inner == nil {
		return ErrNotWired
	}
	if err := r.Inner.Prepare(ctx, s); err != nil {
		return err
	}
	if r.Transport == nil {
		return ErrNotWired
	}
	if err := r.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrSetupRefused, err)
	}
	return r.reset(ctx, s, true)
}

// Execute implements Executor.
func (r ResetExecutor) Execute(ctx context.Context, s Step) (Outcome, error) {
	if r.Inner == nil {
		return Outcome{}, ErrNotWired
	}
	if r.Transport == nil {
		return Outcome{}, ErrNotWired
	}
	if err := r.reset(ctx, s, false); err != nil {
		return Outcome{}, err
	}
	return r.Inner.Execute(ctx, s)
}

func (r ResetExecutor) reset(ctx context.Context, s Step, check bool) error {
	argv := []string{"reset", s.CaseID}
	what := "reset"
	if check {
		argv, what = append(argv, "--check"), "reset check"
	}
	_, stderr, exit, err := r.Transport.Run(ctx, argv)
	switch {
	case err != nil:
		return fmt.Errorf("%w: trial %s of %s: %v", ErrSetupRefused, what, s.CaseID, err)
	case exit != 0:
		return fmt.Errorf("%w: trial %s of %s exit %d: %s", ErrSetupRefused, what, s.CaseID, exit, clip(stderr))
	}
	return nil
}
