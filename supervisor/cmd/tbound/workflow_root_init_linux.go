//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"tbound/supervisor/internal/workflowrecovery"
)

const workflowRootInitUsage = "usage: tbound workflow-root-init\n" +
	"  Creates the host-only workflow recovery root once, at provisioning. Run it as the service identity\n" +
	"  (uid/gid of tbound-pi, no supplementary groups, NoNewPrivs), not as root. It takes no arguments:\n" +
	"  the root database and owner-store directories are the fixed service paths.\n"

var errWorkflowRootInitDenied = errors.New("workflow-root-init boundary permits only namespace creation")

// workflowRootBootstrapBoundary is the HostBoundary of the one-time bootstrap. Creating the root
// namespace consults no boundary method (it admits no workflow, owner, store or drain), so every
// method denies: nothing this process holds can authorize a workflow or vouch for an owner.
type workflowRootBootstrapBoundary struct{}

func (workflowRootBootstrapBoundary) VerifyOwnerStore(context.Context, string, workflowrecovery.OwnerStoreIdentity) error {
	return errWorkflowRootInitDenied
}

func (workflowRootBootstrapBoundary) VerifyOwnerIncarnation(context.Context, string, workflowrecovery.OwnerIncarnation) error {
	return errWorkflowRootInitDenied
}

func (workflowRootBootstrapBoundary) VerifyOwnersDrainedWithEvidence(context.Context, string, []workflowrecovery.OwnerEpochIdentity) ([]string, error) {
	return nil, errWorkflowRootInitDenied
}

func (workflowRootBootstrapBoundary) AuthorizeFreshWorkflow(context.Context, string, uint64, string,
	workflowrecovery.OwnerIncarnation, time.Time, string, workflowrecovery.OwnerStoreIdentity) error {
	return errWorkflowRootInitDenied
}

// workflowRootInitResult is the single JSON line the command prints.
type workflowRootInitResult struct {
	Path         string `json:"path"`
	OwnerStore   string `json:"owner_store_root"`
	RootIdentity string `json:"root_identity"`
}

func runWorkflowRootInit(args []string, stdout, stderr io.Writer) error {
	if len(args) != 0 {
		fmt.Fprint(stderr, workflowRootInitUsage)
		return errors.New("workflow-root-init takes no arguments")
	}
	if os.Geteuid() == 0 {
		return errors.New("workflow-root-init must run as the service identity, not root: the root database must be service-owned")
	}
	return initializeWorkflowRoot(piServiceWorkflowRootDBPath, piServiceOwnerStoreRoot, stdout)
}

// initializeWorkflowRoot creates the (absent) root database and its two leaf directories below
// existing parents, closes the root, and prints one JSON line. An existing root database is never
// opened or replaced.
func initializeWorkflowRoot(rootDB, ownerStoreRoot string, stdout io.Writer) error {
	if _, err := os.Lstat(rootDB); err == nil {
		return fmt.Errorf("workflow root %s already exists: refusing to replace it", rootDB)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect workflow root %s: %w", rootDB, err)
	}
	for _, directory := range []string{filepath.Dir(rootDB), ownerStoreRoot} {
		if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create %s (its parent must already exist): %w", directory, err)
		}
	}
	root, err := workflowrecovery.CreateFreshProtectedRoot(rootDB, ownerStoreRoot, workflowRootBootstrapBoundary{})
	if err != nil {
		return fmt.Errorf("create workflow root: %w", err)
	}
	if err := root.Close(); err != nil {
		return fmt.Errorf("close workflow root: %w", err)
	}
	// Reopen it the way the service will, so the printed identity is the one the service computes
	// and a root that cannot be opened is found now.
	opened, err := workflowrecovery.OpenExistingProtectedRoot(rootDB, ownerStoreRoot, workflowRootBootstrapBoundary{})
	if err != nil {
		return fmt.Errorf("reopen workflow root: %w", err)
	}
	identity := opened.TrustedRootIdentityDigest()
	if err := opened.Close(); err != nil {
		return fmt.Errorf("close reopened workflow root: %w", err)
	}
	line, err := json.Marshal(workflowRootInitResult{Path: rootDB, OwnerStore: ownerStoreRoot, RootIdentity: identity})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s\n", line)
	return err
}
