package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"tbound/supervisor/internal/evalcorpus"
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
//
// A resumed step (Step.Resumed) is not reset when its dispatch record exists: the
// request may have reached the guest, and a reset would wipe the tree the run left for
// the acceptance check. Without the record the earlier process died before the reset
// had ended, so no request was sent, and the step is reset and sent as a new one.
type ResetExecutor struct {
	Inner Executor
	// Transport carries the trial command to the guest. Use a transport of its own (its
	// own SSH user and key) whose launcher is DefaultTrialCommand.
	Transport Transport
	// DispatchDir holds one record per attempt whose reset ended, written before the
	// inner executor runs. Empty means no records: a resumed step is then refused.
	DispatchDir string
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
	if s.Resumed {
		if r.DispatchDir == "" {
			return Outcome{}, fmt.Errorf("%w: a resumed step needs a dispatch record directory", ErrSetupRefused)
		}
		_, err := os.Stat(r.dispatchPath(s))
		switch {
		case err == nil:
			return r.Inner.Execute(ctx, s)
		case !errors.Is(err, fs.ErrNotExist):
			return Outcome{}, fmt.Errorf("%w: dispatch record: %v", ErrSetupRefused, err)
		case s.Kind == evalcorpus.KindAdversarial:
			// Nothing was sent, so there is nothing to settle, and an unobserved run must not start.
			return Outcome{}, fmt.Errorf("%w: no request was sent", ErrSetupRefused)
		}
		s.Resumed = false
	}
	if err := r.reset(ctx, s, false); err != nil {
		return Outcome{}, err
	}
	if r.DispatchDir != "" {
		if err := writeDispatchRecord(r.DispatchDir, r.dispatchPath(s), IntervalRef(s)); err != nil {
			return Outcome{}, fmt.Errorf("%w: dispatch record: %v", ErrSetupRefused, err)
		}
	}
	return r.Inner.Execute(ctx, s)
}

func (r ResetExecutor) dispatchPath(s Step) string {
	sum := sha256.Sum256([]byte(IntervalRef(s)))
	return filepath.Join(r.DispatchDir, hex.EncodeToString(sum[:]))
}

func writeDispatchRecord(dir, path, interval string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(interval + "\n")
	if err := errors.Join(werr, f.Sync(), f.Close()); err != nil {
		return err
	}
	syncDir(dir)
	return nil
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
