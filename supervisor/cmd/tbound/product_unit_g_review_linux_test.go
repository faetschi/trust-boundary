//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/productprovider"
)

func TestProductReviewLimitsNeverReadCellCgroup(t *testing.T) {
	if strings.Contains(productQualificationLimitsScript, "/sys/fs/cgroup") {
		t.Fatal("limits still depend on files excluded by frozen Landlock")
	}
}

func TestProductReviewQualificationSettleFailureIsNotRetriedByFinish(t *testing.T) {
	h := newQualificationHarness(t)
	env := h.newEnv(privateGovernedTestRoot(t))
	h.install(t, h.steps())
	cell, err := openProductQualificationCell(context.Background(), h.target(), env, h.newRunner)
	if err != nil {
		t.Fatal(err)
	}
	r := cell.(*productQualificationRun)
	h.cleanupFailOnce = true
	failure := r.runPhase(context.Background(), productPhaseSettlement)
	if failure == nil {
		t.Fatal("expected settlement failure")
	}
	if err := r.finish(context.Background(), failure); err == nil {
		t.Fatal("finish passed unresolved cleanup")
	}
	if r.closed || h.cleanupAttempts != 1 || len(env.retainedCommands) != 1 {
		t.Fatalf("deferred teardown retried/released owner: closed=%v attempts=%d retained=%d", r.closed, h.cleanupAttempts, len(env.retainedCommands))
	}
	if _, err := r.commandJournal.Trace(); err != nil {
		t.Fatalf("dependent journal closed: %v", err)
	}
	if err := env.Close(); err != nil || !r.closed || len(env.retainedCommands) != 0 {
		t.Fatalf("explicit cleanup failed: %v", err)
	}
}

func TestProductReviewConstructionFailureRetainsOwnership(t *testing.T) {
	h := newQualificationHarness(t)
	env := h.newEnv(privateGovernedTestRoot(t))
	h.install(t, h.steps())
	// Invalid Store policy attestation fails after the runner has acquired its scope.
	env.Xattr.ProfileDigest = ""
	h.cleanupErr = errors.New("scope still owned")
	cell, err := openProductQualificationCell(context.Background(), h.target(), env, h.newRunner)
	if err == nil || cell != nil || len(env.retainedCommands) != 1 || h.cleanupAttempts != 1 {
		t.Fatalf("construction dropped owner: cell=%v err=%v retained=%d attempts=%d", cell, err, len(env.retainedCommands), h.cleanupAttempts)
	}
	h.cleanupErr = nil
	if err := env.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProductReviewOrphanRequiresContainerAndNamespaceRecovery(t *testing.T) {
	for _, missingScope := range []bool{false, true} {
		t.Run(map[bool]string{true: "scope absent", false: "scope present"}[missingScope], func(t *testing.T) {
			h := newQualificationHarness(t)
			root := privateGovernedTestRoot(t)
			owner := h.plantOpenedOrphan(t, root, "orphan", !missingScope)
			h.installScopeDrain(t, 0)
			journal, err := audit.OpenExisting(filepath.Join(root, "turns", "orphan", productCommandOwnerJournal))
			if err != nil {
				t.Fatal(err)
			}
			command := piruntime.D09ProductCommandOwnership{ContainerName: "tbound-cell-cmd-orphan", LeaseID: "lease", ViewID: "view", ViewDevice: 10, ViewInode: 20}
			for _, state := range []string{"intent", "started"} {
				if state == "started" {
					command.MountNSInode = 30
				}
				raw, _ := json.Marshal(command)
				if _, err := journal.Append(audit.Event{Kind: "product_command_" + state, ID: command.ContainerName, Data: raw}); err != nil {
					t.Fatal(err)
				}
			}
			journal.Close()
			recoveryFailed := errors.New("mount reference remains")
			calls := 0
			recoverCommand := func(_ context.Context, got piruntime.D09ProductCommandOwnership) error {
				calls++
				if got != command {
					t.Fatalf("journal identity lost: %+v", got)
				}
				return recoveryFailed
			}
			retained := filepath.Join(root, "turns")
			if err := reconcileProductCommandScopesWithRecovery(context.Background(), openProductCellsRoot(t, h.cells), recoverCommand, retained); !errors.Is(err, recoveryFailed) || calls != 1 {
				t.Fatalf("unproved cleanup accepted: %v calls=%d", err, calls)
			}
			kinds := ownerJournalKinds(t, filepath.Join(retained, "orphan", productCommandOwnerJournal))
			if kinds[len(kinds)-1] == "product_command_scope_drained" {
				t.Fatal("failed recovery recorded drained")
			}
			if err := reconcileProductCommandScopesWithRecovery(context.Background(), openProductCellsRoot(t, h.cells), func(context.Context, piruntime.D09ProductCommandOwnership) error { return nil }, retained); err != nil {
				t.Fatal(err)
			}
			if owner.ScopeIdentity == "" {
				t.Fatal("invalid fixture")
			}
		})
	}
}

func TestProductReviewProbeUsesRegisteredProvider(t *testing.T) {
	env := &productTurnEnv{}
	env.Registration.ProviderProfiles = []string{productprovider.OpenCodeGo}
	hostname, err := productQualificationProviderHostname(env)
	profile, _ := productprovider.Lookup(productprovider.OpenCodeGo)
	if err != nil || hostname == "" || !strings.Contains(profile.Endpoint, hostname) {
		t.Fatalf("provider destination=%q err=%v", hostname, err)
	}
	env.Registration.ProviderProfiles = []string{"client-url"}
	if _, err := productQualificationProviderHostname(env); err == nil {
		t.Fatal("unregistered provider accepted")
	}
}

func TestProductReviewPruningKeepsUnresolvedAndHonorsCancellation(t *testing.T) {
	h := newQualificationHarness(t)
	env := h.newEnv(privateGovernedTestRoot(t))
	h.install(t, h.steps())
	cell, err := openProductQualificationCell(context.Background(), h.target(), env, h.newRunner)
	if err != nil {
		t.Fatal(err)
	}
	unresolved := cell.(*productQualificationRun)
	h.cleanupErr = errors.New("still owned")
	if unresolved.finish(context.Background(), errors.New("failed")) == nil {
		t.Fatal("expected unresolved cleanup")
	}
	h.cleanupErr = nil
	for i := 0; i < 7; i++ {
		cell, err := openProductQualificationCell(context.Background(), h.target(), env, h.newRunner)
		if err != nil {
			t.Fatal(err)
		}
		if err := cell.finish(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(unresolved.dir); err != nil {
		t.Fatal("pruning deleted unresolved run")
	}
	if dirs := qualificationRunDirs(t, env.StateRoot); len(dirs) != 6 {
		t.Fatalf("retained %d, want five drained plus unresolved", len(dirs))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pruneProductQualifications(ctx, filepath.Join(env.StateRoot, productQualificationDir)); !errors.Is(err, context.Canceled) {
		t.Fatalf("scan ignored cancellation: %v", err)
	}
	if err := env.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProductReviewDNSNegativeAnswerIsNotIsolation(t *testing.T) {
	for _, errno := range []string{"ENOTFOUND", "EAI_NONAME"} {
		var report productNetworkReport
		json.Unmarshal(productDeniedNetworkOutput(t, "127.0.0.1:41234", nil), &report)
		report.Checks[0].Errno = errno
		if verifyProductDenyAllNetwork(report) == nil {
			t.Fatalf("accepted negative DNS answer %s", errno)
		}
	}
}

func TestProductReviewUnroutedInterfaceIsNotIsolation(t *testing.T) {
	raw := productDeniedNetworkOutput(t, "127.0.0.1:41234", nil)
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	fields["interfaces"] = json.RawMessage(`["lo","eth0"]`)
	raw, _ = json.Marshal(fields)
	var report productNetworkReport
	json.Unmarshal(raw, &report)
	if verifyProductDenyAllNetwork(report) == nil {
		t.Fatal("accepted extra interface without routes")
	}
}

func TestProductReviewFailedSettlementKeepsQualificationHandles(t *testing.T) {
	h := newQualificationHarness(t)
	env := h.newEnv(privateGovernedTestRoot(t))
	h.install(t, h.steps())
	cell, err := openProductQualificationCell(context.Background(), h.target(), env, h.newRunner)
	if err != nil {
		t.Fatal(err)
	}
	r := cell.(*productQualificationRun)
	h.cleanupErr = errors.New("unresolved container")
	if r.finish(context.Background(), errors.New("phase failed")) == nil {
		t.Fatal("settlement passed")
	}
	if r.closed {
		t.Fatal("failed settlement released dependent handles")
	}
	h.cleanupErr = nil
	if err := r.runner.Settle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.release(); err != nil {
		t.Fatal(err)
	}
}

func TestProductReviewQualificationRetention(t *testing.T) {
	h := newQualificationHarness(t)
	env := h.newEnv(privateGovernedTestRoot(t))
	h.install(t, h.steps())
	for i := 0; i < 7; i++ {
		cell, err := openProductQualificationCell(context.Background(), h.target(), env, h.newRunner)
		if err != nil {
			t.Fatal(err)
		}
		if err := cell.finish(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(env.StateRoot, productQualificationDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("retained %d fully drained runs, want 5", len(entries))
	}
}
