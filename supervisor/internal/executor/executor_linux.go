//go:build linux

package executor

import (
	"context"
	"fmt"
	"strings"

	"tbound/supervisor/internal/sandbox"
	"tbound/supervisor/internal/sessionrepo"
)

// profile is the descriptive runner label recorded for evidence. It is
// deliberately distinct from the direct uncontained test fixture and from any
// claim-bearing profile.
const profile = "wsl-dev-sandbox-non-claim-bearing"

// Runner adapts sandbox.Launch to sessionrepo.CommandRunner. It is a thin
// mapping layer: the sandbox owns the cell lifecycle and settlement.
type Runner struct {
	// LeaseID is the authorized command lease this runner may settle. The
	// CommandRunner seam does not expose the lease, so it is supplied here.
	LeaseID string
}

// New returns a runner bound to one authorized command lease.
func New(leaseID string) *Runner {
	return &Runner{LeaseID: leaseID}
}

// Profile returns the descriptive, non-claim-bearing runner profile label.
func (runner *Runner) Profile() string { return profile }

// Run launches the authorized command inside a fresh sandbox cell rooted at a
// duplicate of the private command view. It returns only a fully settled
// result; any missing mechanism or unproven postcondition is an error.
func (runner *Runner) Run(ctx context.Context, view *sessionrepo.CommandView, spec sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	if view == nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("executor: nil command view")
	}
	source, err := view.MountSource()
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("executor: obtain cell root: %w", err)
	}
	defer func() { _ = source.Close() }()

	argv := make([]string, 0, len(spec.Args)+1)
	argv = append(argv, spec.Executable)
	argv = append(argv, spec.Args...)
	result, settlement, err := sandbox.Launch(ctx, sandbox.Request{
		Root: source,
		Argv: argv,
		Env:  defaultEnvironment(),
		Dir:  spec.WorkingDirectory,
		// Default bounds are requested; when cgroup v2 is not delegated the
		// sandbox records them as unestablished rather than claiming them.
		Limits: sandbox.Limits{MemoryBytes: 512 << 20, PidsMax: 256, CPUWeight: 100},
	})
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, fmt.Errorf("executor: %w", err)
	}
	commandResult, commandSettlement := mapOutcome(result, settlement, view.ID(), runner.LeaseID)
	return commandResult, commandSettlement, nil
}

// mapOutcome performs the pure mapping from sandbox evidence into the
// supervisor's runner receipt. ViewID and LeaseID come from the trusted view
// and the authorized lease; ExitObserved and ExitCode come from the real
// result. It is exported only within the package for direct testing.
func mapOutcome(result sandbox.Result, settlement sandbox.Settlement, viewID, leaseID string) (sessionrepo.CommandResult, sessionrepo.CommandSettlement) {
	commandResult := sessionrepo.CommandResult{
		ExitCode:     result.ExitCode,
		ExitObserved: settlement.ExitObserved,
		Stdout:       result.Stdout,
		Stderr:       result.Stderr,
	}
	commandSettlement := sessionrepo.CommandSettlement{
		LeaseID:           leaseID,
		ViewID:            viewID,
		ProcessScopeEmpty: settlement.ProcessScopeEmpty,
		WritersStopped:    settlement.WritersStopped,
		MountDetached:     settlement.MountDetached,
		ExitObserved:      settlement.ExitObserved,
		ExitCode:          settlement.ExitCode,
		EvidenceClass:     settlement.EvidenceClass,
	}
	return commandResult, commandSettlement
}

// defaultEnvironment is a sanitized command environment: no provider
// credentials and no live-workspace paths are inherited.
func defaultEnvironment() []string {
	return []string{
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=/tmp",
		"TMPDIR=/tmp",
		"LANG=C",
	}
}

// IsNonClaimBearing reports whether an evidence class is one of this runner's
// honest, non-claim-bearing labels.
func IsNonClaimBearing(evidenceClass string) bool {
	return strings.Contains(evidenceClass, "non-claim-bearing")
}
