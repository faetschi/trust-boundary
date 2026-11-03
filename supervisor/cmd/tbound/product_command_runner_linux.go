//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
)

const productCommandOwnerJournal = "command-owner.jsonl"

type productCommandLifecycle interface {
	Pending() int
	Settle(context.Context) error
	Close() error
}

// productD09TurnRunner keeps the scope and journal alive until both receipt binding
// and physical cleanup succeed. A failed cleanup remains retryable by the host.
type productD09TurnRunner struct {
	*productionCommandRunner
	ownerMu  sync.Mutex
	cleanup  func(context.Context) error
	closed   bool
	limitsMu sync.Mutex
	limits   map[string]piruntime.D09ProductCommandLimits
}

func newProductD09CommandRunnerWithFactory(factory productionCommandRunnerFactory, cleanup func(context.Context) error) *productD09TurnRunner {
	return &productD09TurnRunner{productionCommandRunner: newProductionCommandRunner(factory), cleanup: cleanup}
}

func (r *productD09TurnRunner) RegisterLease(viewID, leaseID string) error {
	r.ownerMu.Lock()
	defer r.ownerMu.Unlock()
	if r.closed {
		return piruntime.ErrUnknownSettlement
	}
	return r.productionCommandRunner.RegisterLease(viewID, leaseID)
}

func (r *productD09TurnRunner) Pending() int {
	r.ownerMu.Lock()
	defer r.ownerMu.Unlock()
	n := r.productionCommandRunner.Pending()
	if !r.closed {
		n++
	}
	return n
}

func (r *productD09TurnRunner) Settle(ctx context.Context) error {
	r.ownerMu.Lock()
	defer r.ownerMu.Unlock()
	if ctx == nil {
		return piruntime.ErrUnknownSettlement
	}
	if r.closed {
		return nil
	}
	if r.productionCommandRunner.Pending() != 0 || r.cleanup == nil {
		return piruntime.ErrUnknownSettlement
	}
	if err := r.cleanup(ctx); err != nil {
		return err
	}
	r.closed = true
	return nil
}

func (r *productD09TurnRunner) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return r.Settle(ctx)
}

// An unlinked turn cannot become UNKNOWN in the catalog. Its admitted reservation
// stays held across retries and cancellation until cleanup succeeds.
func (e *productTurnEnv) settleRetainedProductCommands(turnID string) error {
	e.retainedMu.Lock()
	var retained []*productTurnWorkflow
	for w := range e.retainedCommands {
		if w.taskID == turnID {
			retained = append(retained, w)
		}
	}
	e.retainedMu.Unlock()
	for _, w := range retained {
		if err := w.close(); err != nil {
			return errors.Join(errProductCommandUnsettled, err)
		}
	}
	return nil
}

type productD09ScopeOwner struct {
	Schema                  string `json:"schema"`
	State                   string `json:"state"`
	WorkflowID              string `json:"workflow_id"`
	RunID                   string `json:"run_id"`
	RegistrationDigest      string `json:"registration_digest"`
	ProfileDigest           string `json:"profile_digest"`
	RootDigest              string `json:"root_digest"`
	ScopePath               string `json:"scope_path"`
	ScopeIdentity           string `json:"scope_identity"`
	CommandOwnershipVersion int    `json:"command_ownership_version,omitempty"`
}

func persistProductCommandScopeOwner(journal *audit.Journal, owner productD09ScopeOwner) error {
	if journal == nil || owner.Schema != "tbound-product-d09-scope-owner/v1" || (owner.State != "opened" && owner.State != "drained") ||
		owner.WorkflowID == "" || owner.RunID == "" || !validDigestText(owner.RegistrationDigest) || !validDigestText(owner.ProfileDigest) ||
		!validDigestText(owner.RootDigest) || !validDigestText(owner.ScopeIdentity) || filepath.Dir(owner.ScopePath) != "/sys/fs/cgroup/system.slice/tbound-product.service/cells" {
		return piruntime.ErrCgroupScopeRefused
	}
	data, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	_, err = journal.Append(audit.Event{Kind: "product_command_scope_" + owner.State, ID: owner.ScopeIdentity, Data: data})
	return err
}

func openProductCommandCgroupScope(ctx context.Context, host *piruntime.D09ProductCommandHost, c productTurnCommandContext) (*piruntime.LinuxCgroupScope, error) {
	root, err := piruntime.PrepareProductD09CgroupRoot(ctx, host)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// Only trusted workflow/run identities choose the child; no model argument reaches the name.
	name := "turn-" + strings.TrimPrefix(digestBytes([]byte(c.WorkflowID+"\n"+c.RunID)), "sha256:")
	return piruntime.CreateLinuxCgroupScope(root, name, piruntime.D09ContainmentProfile, piruntime.D09SettlementProfile)
}

func newProductD09CommandRunner(ctx context.Context, host *piruntime.D09ProductCommandHost, c productTurnCommandContext) (*productD09TurnRunner, error) {
	if c.Journal == nil || c.TurnDir == "" || c.WorkflowID == "" || c.RunID == "" || c.SessionID == "" || c.ConversationID == "" {
		return nil, errProductBashRunnerUnavailable
	}
	if err := host.Revalidate(ctx); err != nil {
		return nil, err
	}
	journal, err := audit.Open(filepath.Join(c.TurnDir, productCommandOwnerJournal))
	if err != nil {
		return nil, err
	}
	scope, err := openProductCommandCgroupScope(ctx, host, c)
	if err != nil {
		return nil, errors.Join(err, journal.Close())
	}
	path, pathErr := scope.Path()
	owner := productD09ScopeOwner{Schema: "tbound-product-d09-scope-owner/v1", State: "opened", WorkflowID: c.WorkflowID, RunID: c.RunID,
		RegistrationDigest: host.Template().ProductRegistrationDigest, ProfileDigest: host.Profile().Digest, RootDigest: scope.RootDigest(), ScopePath: path, ScopeIdentity: scope.IdentityDigest(), CommandOwnershipVersion: 1}
	var launcher *piruntime.D09PodmanCrunLauncher
	removed, recorded, drained := false, false, false
	runner := newProductD09CommandRunnerWithFactory(func(lease string) (sessionrepo.CommandRunner, error) {
		if launcher == nil {
			return nil, errProductBashRunnerUnavailable
		}
		inner, err := piruntime.NewD09CommandRunner(lease, launcher)
		if err != nil {
			return nil, err
		}
		return newProductionD09BashRunner(lease, inner, c.Journal, c.RunID, c.SessionID, c.WorkflowID, c.ConversationID, host.Profile(), host.Template()), nil
	}, func(ctx context.Context) error {
		if !removed {
			if err := scope.Remove(ctx); err != nil {
				return err
			}
			removed = true
		}
		if launcher != nil {
			if err := launcher.Close(ctx); err != nil {
				return err
			}
		}
		if recorded && !drained {
			owner.State = "drained"
			if err := persistProductCommandScopeOwner(journal, owner); err != nil {
				return err
			}
			drained = true
		}
		return journal.Close()
	})
	if pathErr != nil {
		return runner, pathErr
	}
	if err := persistProductCommandScopeOwner(journal, owner); err != nil {
		return runner, err
	}
	recorded = true
	launcher, err = piruntime.NewProductD09PodmanCrunLauncher(ctx, host, scope, func(command piruntime.D09ProductCommandOwnership) error {
		data, err := json.Marshal(command)
		if err != nil {
			return err
		}
		state := "intent"
		if command.MountNSInode != 0 {
			state = "started"
		}
		if _, err := journal.Append(audit.Event{Kind: "product_command_" + state, ID: command.ContainerName, Data: data}); err != nil {
			return err
		}
		if command.MountNSInode != 0 {
			limits, err := command.ReadLimits()
			if err != nil {
				return err
			}
			runner.limitsMu.Lock()
			defer runner.limitsMu.Unlock()
			if runner.limits == nil {
				runner.limits = map[string]piruntime.D09ProductCommandLimits{}
			}
			runner.limits[command.LeaseID] = limits
		}
		return nil
	})
	return runner, err
}

// reconcileProductCommandScopes grants cleanup authority only to exact recorded
// identities. A missing scope cannot prove container/mount cleanup and blocks.
func reconcileProductCommandScopes(ctx context.Context, host *piruntime.D09ProductCommandHost, retainedRoots ...string) error {
	root, err := piruntime.PrepareProductD09CgroupRoot(ctx, host)
	if err != nil {
		return err
	}
	defer root.Close()
	return reconcileProductCommandScopesWithRecovery(ctx, root, func(ctx context.Context, command piruntime.D09ProductCommandOwnership) error {
		return piruntime.ReconcileProductD09CommandResidue(ctx, host, command)
	}, retainedRoots...)
}

// reconcileProductCommandScopesUnder is reconcileProductCommandScopes over a cells root that is already
// prepared, so the journal and direct-child checks need no host.
func reconcileProductCommandScopesUnder(ctx context.Context, root *os.File, retainedRoots ...string) error {
	return reconcileProductCommandScopesWithRecovery(ctx, root, nil, retainedRoots...)
}

func reconcileProductCommandScopesWithRecovery(ctx context.Context, root *os.File, recoverCommand func(context.Context, piruntime.D09ProductCommandOwnership) error, retainedRoots ...string) error {
	owners := map[string]struct{}{}
	for _, retained := range retainedRoots {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := os.ReadDir(retained)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(retained, entry.Name(), productCommandOwnerJournal)
			journal, err := openExistingRecoveryJournal(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			err = reconcileProductCommandScope(ctx, root, journal, owners, recoverCommand)
			closeErr := journal.Close()
			if err != nil || closeErr != nil {
				return errors.Join(err, closeErr)
			}
		}
	}
	entries, err := root.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if _, known := owners[entry.Name()]; !known {
				return fmt.Errorf("unrecorded product command scope %s: %w", entry.Name(), piruntime.ErrUnknownSettlement)
			}
		}
	}
	return nil
}

func reconcileProductCommandScope(ctx context.Context, root *os.File, journal *audit.Journal, owners map[string]struct{}, recoverCommand func(context.Context, piruntime.D09ProductCommandOwnership) error) error {
	trace, err := journal.Trace()
	if err != nil {
		return errors.Join(piruntime.ErrUnknownSettlement, err)
	}
	// An empty journal is a scope allocation that failed before its owner record. A child it left
	// behind has no owner, and reconcileProductCommandScopesUnder's direct-child scan still rejects it.
	if len(trace.Records) == 0 {
		return nil
	}
	commands := map[string]piruntime.D09ProductCommandOwnership{}
	drained := false
	var opened productD09ScopeOwner
	for i, record := range trace.Records {
		if err := ctx.Err(); err != nil {
			return err
		}
		if i > 0 && (record.Event.Kind == "product_command_intent" || record.Event.Kind == "product_command_started") {
			if drained {
				return piruntime.ErrUnknownSettlement
			}
			var command piruntime.D09ProductCommandOwnership
			if json.Unmarshal(record.Event.Data, &command) != nil || command.ContainerName != record.Event.ID || command.ViewInode == 0 || command.LeaseID == "" || command.ViewID == "" {
				return piruntime.ErrUnknownSettlement
			}
			prior, exists := commands[command.ContainerName]
			if record.Event.Kind == "product_command_intent" {
				if exists || command.MountNSInode != 0 {
					return piruntime.ErrUnknownSettlement
				}
			} else {
				started := command
				started.MountNSInode = 0
				if !exists || prior.MountNSInode != 0 || started != prior || command.MountNSInode == 0 {
					return piruntime.ErrUnknownSettlement
				}
			}
			commands[command.ContainerName] = command
			continue
		}
		var owner productD09ScopeOwner
		if json.Unmarshal(record.Event.Data, &owner) != nil || owner.Schema != "tbound-product-d09-scope-owner/v1" || record.Event.ID != owner.ScopeIdentity ||
			!validDigestText(owner.RegistrationDigest) || !validDigestText(owner.ProfileDigest) || !validDigestText(owner.RootDigest) || !validDigestText(owner.ScopeIdentity) || owner.WorkflowID == "" || owner.RunID == "" ||
			filepath.Dir(owner.ScopePath) != "/sys/fs/cgroup/system.slice/tbound-product.service/cells" || record.Event.Kind != "product_command_scope_"+owner.State {
			return piruntime.ErrUnknownSettlement
		}
		if i == 0 {
			if owner.State != "opened" {
				return piruntime.ErrUnknownSettlement
			}
			opened = owner
		} else {
			owner.State = "opened"
			if owner != opened || !strings.HasSuffix(record.Event.Kind, "drained") {
				return piruntime.ErrUnknownSettlement
			}
			drained = true
		}
	}
	if drained {
		return nil
	}
	if opened.CommandOwnershipVersion != 1 {
		return piruntime.ErrUnknownSettlement
	}
	name := filepath.Base(opened.ScopePath)
	if _, duplicate := owners[name]; duplicate {
		return piruntime.ErrUnknownSettlement
	}
	owners[name] = struct{}{}
	for _, command := range commands {
		if recoverCommand == nil {
			return piruntime.ErrUnknownSettlement
		}
		if err := recoverCommand(ctx, command); err != nil {
			return err
		}
	}
	// The drain revokes the scope and removes it. It never writes a command receipt: a killed command has no result
	// to certify, so a linked turn that depended on it stays UNKNOWN.
	scope, err := openProductScopeForDrain(root, name, opened.ScopeIdentity)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// A systemd unit stop removes its cgroups. A cgroup holds no process when it is removed, so a gone scope
		// leaves no writer unless a process still reports the recorded path.
		writers, countErr := productProcessesInScope(opened.ScopePath)
		if countErr != nil || writers != 0 {
			return errors.Join(piruntime.ErrUnknownSettlement, countErr, fmt.Errorf("%d processes still report the absent scope %s", writers, opened.ScopePath))
		}
	case err != nil:
		return err
	default:
		if scope.RootDigest() != opened.RootDigest {
			_ = scope.Close()
			return piruntime.ErrCgroupScopeRefused
		}
		if err := scope.Remove(ctx); err != nil {
			_ = scope.Close()
			return errors.Join(piruntime.ErrUnknownSettlement, err)
		}
	}
	opened.State = "drained"
	return persistProductCommandScopeOwner(journal, opened)
}

// productDrainableScope is the exact orphan scope a restart reopens from its owner record.
type productDrainableScope interface {
	RootDigest() string
	Remove(context.Context) error
	Close() error
}

// openProductScopeForDrain reopens the direct child the owner record names, with its recorded identity. A test of the
// restart protocol replaces it, since an unprivileged test host has no delegated cgroup.
var openProductScopeForDrain = func(root *os.File, name, identity string) (productDrainableScope, error) {
	scope, err := piruntime.OpenLinuxCgroupScopeForDrain(root, name, piruntime.D09ContainmentProfile, piruntime.D09SettlementProfile, identity)
	if err != nil {
		return nil, err
	}
	return scope, nil
}

// productProcessesInScope counts the processes whose unified cgroup is the recorded scope or below it.
var productProcessesInScope = func(scopePath string) (int, error) {
	const mount = "/sys/fs/cgroup"
	if !strings.HasPrefix(scopePath, mount+"/") {
		return 0, fmt.Errorf("scope %s is not below %s", scopePath, mount)
	}
	scope := strings.TrimPrefix(scopePath, mount)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	writers := 0
	for _, entry := range entries {
		if _, numeric := strconv.Atoi(entry.Name()); numeric != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cgroup"))
		if errors.Is(err, os.ErrNotExist) {
			continue // the process exited
		}
		if err != nil {
			return 0, err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if member, found := strings.CutPrefix(line, "0::"); found && (member == scope || strings.HasPrefix(member, scope+"/")) {
				writers++
			}
		}
	}
	return writers, nil
}

func (r *productD09TurnRunner) productCommandLimits(lease string) (piruntime.D09ProductCommandLimits, error) {
	r.limitsMu.Lock()
	defer r.limitsMu.Unlock()
	limits, ok := r.limits[lease]
	if !ok {
		return limits, errors.New("no supervisor observation of the live command cgroup")
	}
	return limits, nil
}
