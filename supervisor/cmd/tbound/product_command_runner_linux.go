//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	ownerMu sync.Mutex
	cleanup func(context.Context) error
	closed  bool
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

type productD09ScopeOwner struct {
	Schema             string `json:"schema"`
	State              string `json:"state"`
	WorkflowID         string `json:"workflow_id"`
	RunID              string `json:"run_id"`
	RegistrationDigest string `json:"registration_digest"`
	ProfileDigest      string `json:"profile_digest"`
	RootDigest         string `json:"root_digest"`
	ScopePath          string `json:"scope_path"`
	ScopeIdentity      string `json:"scope_identity"`
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
		RegistrationDigest: host.Template().ProductRegistrationDigest, ProfileDigest: host.Profile().Digest, RootDigest: scope.RootDigest(), ScopePath: path, ScopeIdentity: scope.IdentityDigest()}
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
	launcher, err = piruntime.NewProductD09PodmanCrunLauncher(ctx, host, scope)
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
	owners := map[string]struct{}{}
	for _, retained := range retainedRoots {
		entries, err := os.ReadDir(retained)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
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
			err = reconcileProductCommandScope(ctx, root, journal, owners)
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

func reconcileProductCommandScope(ctx context.Context, root *os.File, journal *audit.Journal, owners map[string]struct{}) error {
	trace, err := journal.Trace()
	if err != nil || len(trace.Records) < 1 || len(trace.Records) > 2 {
		return errors.Join(piruntime.ErrUnknownSettlement, err)
	}
	var opened productD09ScopeOwner
	for i, record := range trace.Records {
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
			return nil
		}
	}
	name := filepath.Base(opened.ScopePath)
	if _, duplicate := owners[name]; duplicate {
		return piruntime.ErrUnknownSettlement
	}
	owners[name] = struct{}{}
	scope, err := piruntime.OpenLinuxCgroupScopeForDrain(root, name, piruntime.D09ContainmentProfile, piruntime.D09SettlementProfile, opened.ScopeIdentity)
	if err != nil {
		return err
	}
	if scope.RootDigest() != opened.RootDigest {
		_ = scope.Close()
		return piruntime.ErrCgroupScopeRefused
	}
	// Only a process that still owns the launcher can prove container, pidfd and
	// mount cleanup. G must reconcile those before marking an orphan drained.
	_ = scope.Close()
	return piruntime.ErrUnknownSettlement
}
