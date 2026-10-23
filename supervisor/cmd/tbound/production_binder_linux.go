//go:build linux

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/privategit"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
	"tbound/supervisor/internal/wsoracle"
)

// This file is the concrete production runtime binder owned by the CLI
// composition. It assembles the durable governed session (protected Store, D06
// private-Git source snapshot, provider conversation, durable executor), the
// sessionlaunch-owned sealed worker exposure and runtime assets, and the
// launcher-owned D09 Podman/crun launcher bound to a verified cgroup scope, then
// hands the CLI an opaque productionPiSession.
//
// It is intentionally an unexported type in package main: no piruntime function
// fabricates a main-owned type. Trusted operator inputs are resolved from
// protected root-owned defaults plus the bounded CLI flags; a missing trusted
// input is a refusal, never a synthesized success.

const (
	productionStoreRootDefault = "/var/lib/tbound/sessions"
	productionAuditRootDefault = "/var/lib/tbound/audit"
	productionAdminRootDefault = "/var/lib/tbound/private-admin"
	productionLiveRootDefault  = piruntime.D09ManagedPiLiveRootPath
	productionStateRootDefault = "/var/lib/tbound/publication-state"
	productionGitPathDefault   = "/usr/bin/git"
	productionGitApprovalPath  = "/etc/tbound/pi-git-approval.json"

	productionStatfsReadonly = 1

	productionSystemPrompt    = "You are the governed, offline-first TBound Pi worker. Use only the registered proxy tools."
	productionDeveloperPrompt = "Use only the registered read, write, edit, and bash proxy tools. Never invent effects."
)

// productionGitApproval is the protected, root-owned operator approval for the
// pinned Git executable. The digest is the content identity the trusted host
// verifier approves; it is never inferred from a Git-like version string.
type productionGitApproval struct {
	Path     string `json:"path"`
	Digest   string `json:"digest"`
	Boundary string `json:"boundary"`
}

// productionOperatorArtifacts are the resolved trusted operator inputs. Every
// field is a protected host path or a root-owned approval value; none is a
// caller-supplied trust boolean.
type productionOperatorArtifacts struct {
	StoreRoot            string
	AuditRoot            string
	AdminRoot            string
	SourceRoot           string
	CgroupRoot           string
	LiveRoot             string
	PublicationStateRoot string
	GitPath              string
	GitDigest            string
	GitBoundary          string
}

var ErrProductionSourceNotQuiescent = errors.New("production source is not a protected read-only filesystem snapshot")
var ErrProductionXattrVisibilityUnavailable = errors.New("registered D09 template xattr-visibility profile does not match a protected source filesystem and scanning privilege")

type productionCleanupQuarantineState struct {
	sync.Mutex
	holds [][]func() error
}

var productionCleanupQuarantine productionCleanupQuarantineState

// retainProductionCleanup keeps the exact held descriptors/owners reachable
// after an UNKNOWN teardown failure. It does not retry with broader authority or
// declare success; the durable cgroup/source identities remain available to a
// later recovery implementation while this process remains alive.
func retainProductionCleanup(closers []func() error) {
	productionCleanupQuarantine.retain(closers)
}

func (q *productionCleanupQuarantineState) retain(closers []func() error) {
	if len(closers) == 0 {
		return
	}
	q.Lock()
	q.holds = append(q.holds, append([]func() error(nil), closers...))
	q.Unlock()
}

func retainProductionCleanupIfUnsafe(
	quarantine *productionCleanupQuarantineState,
	evidence productionLifecycleSnapshot,
	runner *productionCommandRunner,
	closers []func() error,
) bool {
	if productionCleanupSafe(evidence, runner) {
		return false
	}
	quarantine.retain(closers)
	return true
}

// productionBinder is the concrete productionRuntimeBinder for a provisioned
// host. It holds no mutable session state between calls.
type productionBinder struct{}

func newProductionBinder() *productionBinder { return &productionBinder{} }

// BindProductionPiSession composes one production session. It acquires every
// handle, and on any failure closes everything acquired so far before returning.
func (b *productionBinder) BindProductionPiSession(ctx context.Context, config productionServeConfig, profile piruntime.HostProfile) (*productionPiSession, error) {
	if ctx == nil {
		return nil, errors.New("production binder requires a context")
	}
	template, _, err := piruntime.LoadD09CellTemplate(ctx)
	if err != nil {
		return nil, fmt.Errorf("load registered D09 cell template for production composition: %w", err)
	}
	templateDigest, err := template.Digest()
	if err != nil || profile.Mode != piruntime.ModeProduction || profile.ContainmentProfile != piruntime.D09ContainmentProfile ||
		profile.ContainmentProfileDigest != templateDigest {
		return nil, errors.Join(errors.New("registered D09 template is not bound to the verified production profile"), err)
	}
	artifacts, err := loadProductionOperatorArtifacts(config, template)
	if err != nil {
		return nil, err
	}
	var workflowAdmission *productionWorkflowAdmission
	if config.ServiceMode {
		workflowAdmission = config.workflowAdmission
		if workflowAdmission == nil || workflowAdmission.Catalog == nil ||
			(workflowAdmission.Mode != "fresh" && workflowAdmission.Mode != "resume") {
			return nil, ErrPiWorkflowCatalog
		}
		if err := requireWorkflowCatalogPath(workflowAdmission.Entry, piServiceConfig{
			WorkflowOwnerStoreRoot: piServiceOwnerStoreRoot,
		}); err != nil {
			return nil, err
		}
		if workflowAdmission.Mode == "resume" {
			if err := openProductionWorkflowOwner(ctx, workflowAdmission, piServiceConfig{
				WorkflowRootDBPath: piServiceWorkflowRootDBPath, WorkflowOwnerStoreRoot: piServiceOwnerStoreRoot,
			}, template, profile); err != nil {
				return nil, fmt.Errorf("open host-fenced retained workflow owner before recovery inspection: %w", err)
			}
			recovered, err := bindProductionPiRecoverySession(ctx, config, profile, template, artifacts, workflowAdmission)
			if err != nil {
				_ = workflowAdmission.CloseOwner()
				return nil, err
			}
			return recovered, nil
		}
		if err := openProductionWorkflowOwner(ctx, workflowAdmission, piServiceConfig{
			WorkflowRootDBPath: piServiceWorkflowRootDBPath, WorkflowOwnerStoreRoot: piServiceOwnerStoreRoot,
		}, template, profile); err != nil {
			return nil, fmt.Errorf("open host-fenced fresh workflow owner before allocating production session state: %w", err)
		}
	}
	workflowOwnerAttached := false
	defer func() {
		if workflowAdmission != nil && !workflowOwnerAttached {
			_ = workflowAdmission.CloseOwner()
		}
	}()

	var runID, workflowID, conversationID, publicationID, storeRootPath, adminRootPath string
	if workflowAdmission != nil {
		entry := workflowAdmission.Entry
		runID, workflowID, conversationID = entry.RunID, entry.WorkflowID, entry.ConversationID
		publicationID = "production-publication-" + entry.WorkflowID
		storeRootPath, adminRootPath = entry.StoreRootPath, entry.AdminRootPath
		if err := requireProductionWorkflowFreshLayout(entry, artifacts); err != nil {
			return nil, err
		}
		storeRootPath, err = createPrivateRunDirectory(artifacts.StoreRoot, filepath.Base(storeRootPath))
		if err != nil {
			return nil, fmt.Errorf("create catalog-bound protected session Store directory: %w", err)
		}
		adminRootPath, err = createPrivateRunDirectory(artifacts.AdminRoot, filepath.Base(adminRootPath))
		if err != nil {
			_ = os.Remove(storeRootPath)
			return nil, fmt.Errorf("create catalog-bound protected private-admin directory: %w", err)
		}
	} else {
		runToken, tokenErr := productionToken()
		if tokenErr != nil {
			return nil, tokenErr
		}
		runID = "production-run-" + runToken
		workflowID = "tbound-workflow-" + runToken
		conversationID = "tbound-conversation-" + runToken
		publicationID = "production-publication-" + runToken
		storeRootPath, err = createPrivateRunDirectory(artifacts.StoreRoot, "session-"+runToken)
		if err != nil {
			return nil, fmt.Errorf("create unique protected session Store directory: %w", err)
		}
		adminRootPath, err = createPrivateRunDirectory(artifacts.AdminRoot, "admin-"+runToken)
		if err != nil {
			_ = os.Remove(storeRootPath)
			return nil, fmt.Errorf("create unique protected private-admin directory: %w", err)
		}
	}
	session := &productionPiSession{
		Profile: profile, RunID: runID, SessionID: runID, WorkflowID: workflowID,
		ConversationID: conversationID, ProviderProfileID: profile.ProviderProfile,
		SystemPrompt: productionSystemPrompt, DeveloperPrompt: productionDeveloperPrompt,
		CallIssuer: governedCallIssuer, D09Template: template,
		Lifecycle: &productionLifecycleEvidence{},
	}
	closers := make([]func() error, 0, 24)
	if workflowAdmission != nil {
		closers = append(closers, workflowAdmission.CloseOwner)
	}
	storeCreated := false
	ownerScopeBound := false
	closers = append(closers, func() error {
		if storeCreated {
			return nil
		}
		return os.Remove(storeRootPath)
	})
	adminParentOpened := false
	closers = append(closers, func() error {
		if adminParentOpened {
			return nil
		}
		return os.Remove(adminRootPath)
	})
	fail := func(err error) (*productionPiSession, error) {
		if session.Authority != nil {
			session.Authority.mu.Lock()
			conversation := session.Authority.conversation
			session.Authority.mu.Unlock()
			if conversation != nil {
				conversation.Close()
			}
		}
		if session.ManagedLiveRootLease != nil && !session.Lifecycle.snapshot().taskAdmitted {
			if abortErr := session.ManagedLiveRootLease.abortBeforeTask(session.Lifecycle.snapshot(), session.ProductionCommandRunner); abortErr != nil {
				err = errors.Join(err, abortErr)
			}
		}
		var cleanupErrors []error
		for index := len(closers) - 1; index >= 0; index-- {
			cleanupErr := closers[index]()
			if cleanupErr != nil {
				cleanupErrors = append(cleanupErrors, cleanupErr)
				retainProductionCleanup(closers[:index+1])
				break
			}
		}
		if workflowAdmission != nil && !ownerScopeBound && len(cleanupErrors) == 0 {
			if drainErr := workflowAdmission.Catalog.MarkOwnerUnexposedDrained(
				workflowAdmission.Entry.AdmissionRecordHash, workflowID,
				workflowAdmission.CandidateEpoch, workflowAdmission.OwnerIncarnation, filepath.Clean(template.CgroupsPath)); drainErr != nil {
				cleanupErrors = append(cleanupErrors, drainErr)
			}
		}
		return nil, errors.Join(append([]error{err}, cleanupErrors...)...)
	}

	// Durable governed session: protected journals, authority, Store, D06 source
	// snapshot, private Git, baseline binding.
	storeJournal, err := audit.Open(filepath.Join(artifacts.AuditRoot, runID+"-session-audit.jsonl"))
	if err != nil {
		return fail(fmt.Errorf("open protected session audit journal: %w", err))
	}
	closers = append(closers, storeJournal.Close)
	providerJournal, err := audit.Open(filepath.Join(artifacts.AuditRoot, runID+"-provider-audit.jsonl"))
	if err != nil {
		return fail(fmt.Errorf("open protected provider audit journal: %w", err))
	}
	closers = append(closers, providerJournal.Close)
	commandJournal, err := audit.Open(filepath.Join(artifacts.AuditRoot, runID+"-command-audit.jsonl"))
	if err != nil {
		return fail(fmt.Errorf("open protected production command-cell journal: %w", err))
	}
	closers = append(closers, commandJournal.Close)

	// Protected publication state and its exclusive lifecycle journal are opened
	// before acquiring the registered live-root authority. The journal lock only
	// serializes cooperating TBound sessions; only the fixed managed-root
	// registration establishes the trusted writer-domain precondition.
	publicationStateRoot, err := os.Open(artifacts.PublicationStateRoot)
	if err != nil {
		return fail(fmt.Errorf("open protected publication state root: %w", err))
	}
	closers = append(closers, publicationStateRoot.Close)
	// One shared lifecycle journal is required for RootLedger's cross-run,
	// cross-workflow live-root quarantine and recovery semantics.
	publicationJournal, err := audit.Open(filepath.Join(artifacts.AuditRoot, "publication-audit.jsonl"))
	if err != nil {
		return fail(fmt.Errorf("open protected publication lifecycle journal: %w", err))
	}
	closers = append(closers, publicationJournal.Close)
	candidateLiveRoot, err := os.Open(artifacts.LiveRoot)
	if err != nil {
		return fail(fmt.Errorf("open candidate registered live-root descriptor: %w", err))
	}
	ownerEpoch, err := nextManagedRootOwnerEpoch(publicationJournal, candidateLiveRoot)
	if err != nil {
		_ = candidateLiveRoot.Close()
		return fail(fmt.Errorf("allocate monotonically increasing managed-root run epoch: %w", err))
	}
	managedRootLease, err := processManagedLiveRootAuthority.acquire(ctx, candidateLiveRoot, template, publicationJournal,
		runID, workflowID, publicationID, ownerEpoch)
	if err != nil {
		_ = candidateLiveRoot.Close()
		return fail(fmt.Errorf("acquire fixed-registration managed live-root writer authority: %w", err))
	}
	closers = append(closers, managedRootLease.close)
	candidateCloseErr := candidateLiveRoot.Close()
	if candidateCloseErr != nil {
		return fail(fmt.Errorf("close candidate managed live-root descriptor after verified duplication: %w", candidateCloseErr))
	}
	liveRoot, err := managedRootLease.duplicateRoot()
	if err != nil {
		return fail(fmt.Errorf("duplicate held managed live-root descriptor for Finalizer: %w", err))
	}
	closers = append(closers, liveRoot.Close)
	// The live-root descriptor closes before its private authority duplicate;
	// both close before the shared journal lock is released.
	session.LiveRoot, session.PublicationStateRoot, session.PublicationJournal = liveRoot, publicationStateRoot, publicationJournal
	// Service-side E04 points (production_e04_points_linux.go). The store closes after its final-cleanup
	// closer has run, and both run before the live root and the writer lease are released.
	if workflowAdmission != nil {
		session.E04 = newProductionE04Recorder(artifacts.AuditRoot, workflowID, runID, artifacts.LiveRoot, liveRoot)
		closers = append(closers, session.E04.close, session.E04.finalCleanup)
	}
	session.ManagedLiveRootLease = managedRootLease
	session.OwnerEpoch, session.PublicationID = ownerEpoch, publicationID
	if err := recoverManagedLiveRootBeforeCapture(ctx, managedRootLease, liveRoot, publicationStateRoot,
		publicationJournal, ownerEpoch); err != nil {
		return fail(fmt.Errorf("recover registered managed-root publications before source capture: %w", err))
	}
	// E04 baseline: the writer lease is held and recovered, and nothing has been captured, so no
	// workspace or cell exists yet.
	session.recordE04(wsoracle.PointBaseline)

	policy, err := productionGatePolicy()
	if err != nil {
		return fail(err)
	}
	storePolicyDigest := digestBytes([]byte(gate.ProductionSessionPolicyLabel))
	metadataPolicyDigest := digestBytes([]byte(gate.ProductionMetadataPolicyLabel))

	var commandLauncher *piruntime.D09PodmanCrunLauncher
	// commandDelta is the command-cell delta of this run, set below from the admitted role and the
	// pinned manifest only; it stays the signed base cell for every run that is not a P replay.
	var commandDelta piruntime.D09CellDelta
	runner := newProductionCommandRunner(func(leaseID string) (sessionrepo.CommandRunner, error) {
		if commandLauncher == nil {
			return nil, ErrProductionBashRunnerUnavailable
		}
		inner, err := piruntime.NewD09CommandRunnerForDelta(leaseID, commandLauncher, commandDelta)
		if err != nil {
			return nil, err
		}
		bash := newProductionD09BashRunner(leaseID, inner, commandJournal, runID, session.SessionID,
			session.WorkflowID, session.ConversationID, profile, template)
		bash.delta = commandDelta
		return bash, nil
	})
	session.CommandRunner, session.ProductionCommandRunner = runner, runner
	authority, err := NewSessionOperationAuthority(storeJournal, providerJournal,
		workflowID, conversationID, profile.ProviderProfile, policy,
		storePolicyDigest, metadataPolicyDigest, runner, cloneDurableOutputScopePaths(config.BashOutputPaths))
	if err != nil {
		return fail(err)
	}
	// Production requires positive, evidenced Bash containment; a legacy/dev
	// "not-established" settlement can never be admitted or receipted as D09.
	authority.requireEvidencedBashContainment = true
	authority.BindProviderRoute(config.providerRoute)
	if workflowAdmission != nil {
		// The admitted role alone selects the provider (model or scripted) and the admission rule.
		corpusBound, bindErr := workflowAdmission.Catalog.bindingForEntry(workflowAdmission.Entry)
		if bindErr != nil {
			return fail(bindErr)
		}
		if corpusBound != nil {
			if commandDelta, err = commandCellDeltaFor(corpusBound.role, corpusBound.delta); err != nil {
				return fail(err)
			}
			runner.survivorsTolerated = commandDelta.NoReap()
			if err := authority.BindCorpusBinding(*corpusBound); err != nil {
				return fail(err)
			}
			session.Corpus = corpusBound
		}
	}
	session.Authority, session.StoreJournal, session.ProviderJournal = authority, storeJournal, providerJournal
	session.CommandJournal = commandJournal
	session.Policy, session.StorePolicyDigest, session.MetadataPolicyDigest = policy, storePolicyDigest, metadataPolicyDigest
	session.CommandRunner = runner
	session.ProductionCommandRunner = runner

	// Socket service mode seeds only from the current registered managed-RW
	// root after C's recovered lease and A's signed xattr qualification pass.
	// Ordinary mode retains the protected read-only source snapshot path.
	sourceRoot := liveRoot
	var sourceQualification *piruntime.D09ManagedLiveRootSnapshotQualification
	var xattrVisibility workspace.XattrVisibilityAttestation
	if config.ServiceMode {
		verified, ok := managedRootLease.verified.(*piruntime.VerifiedD09ManagedLiveRoot)
		if !ok {
			return fail(ErrManagedSourceCaptureUnavailable)
		}
		sourceQualification, err = piruntime.D09ManagedLiveRootSnapshotEvidenceForSeed(ctx,
			verified, sourceRoot, profile, template, managedRootLease)
		if err != nil {
			return fail(fmt.Errorf("qualify current managed-root source under the recovered D09 lease: %w", err))
		}
		xattrVisibility, err = sourceQualification.XattrVisibilityAttestation()
		if err != nil {
			return fail(fmt.Errorf("derive managed-root workspace xattr attestation from qualification: %w", err))
		}
	} else {
		sourceRoot, err = openProductionQuiescentSource(artifacts.SourceRoot)
		if err != nil {
			return fail(fmt.Errorf("open and attest protected read-only source root: %w", err))
		}
		closers = append(closers, sourceRoot.Close)
		xattrVisibility, err = productionXattrVisibility(profile, template, sourceRoot)
		if err != nil {
			return fail(err)
		}
	}

	storeRoot, err := os.Open(storeRootPath)
	if err != nil {
		return fail(fmt.Errorf("open protected session Store root: %w", err))
	}
	closers = append(closers, storeRoot.Close)
	var exposureBinder sessionrepo.Binder
	if !config.ServiceMode {
		exposureBinder = sessionrepo.LinuxReadOnlyBinder{}
	}
	managed, err := authority.CreateStore(storeRoot, workspace.DefaultLimits(), xattrVisibility, exposureBinder)
	if err != nil {
		return fail(fmt.Errorf("create protected session Store: %w", err))
	}
	storeCreated = true
	closers = append(closers, managed.Close)
	session.ManagedStore = managed
	session.Store = managed.Store()

	var g0 *sessionrepo.Generation
	if sourceQualification != nil {
		g0, err = seedQualifiedManagedRootSource(ctx, managed.Store(), sourceRoot, storeJournal, runID, sourceQualification)
	} else {
		g0, err = managed.Store().Seed(sourceRoot, sessionrepo.RootAttestation{Quiescent: true})
	}
	if err != nil {
		return fail(fmt.Errorf("seed baseline generation from qualified protected source: %w", err))
	}
	if err := authority.BindBaseline(managed.Store(), g0); err != nil {
		return fail(fmt.Errorf("bind verified baseline generation: %w", err))
	}
	session.Generation = g0
	session.Workspace = productionWorkspaceOptions(g0.ID(), metadataPolicyDigest, xattrVisibility)

	adminRoot, err := os.Open(adminRootPath)
	if err != nil {
		return fail(fmt.Errorf("open protected private-admin root: %w", err))
	}
	closers = append(closers, adminRoot.Close)
	admin, err := sessionlaunch.OpenAdminParent(adminRoot)
	if err != nil {
		return fail(fmt.Errorf("open supervisor-owned private Git admin parent: %w", err))
	}
	adminParentOpened = true
	closers = append(closers, admin.Close)

	git, gitProfile, err := pinProductionGit(artifacts)
	if err != nil {
		return fail(err)
	}
	closers = append(closers, git.Close)

	prepared, err := sessionlaunch.Prepare(ctx, managed, g0, admin, git, gitProfile)
	if err != nil {
		return fail(fmt.Errorf("prepare D06 private-Git source snapshot: %w", err))
	}
	closers = append(closers, prepared.Close)
	if err := authority.BindPreparedSession(prepared); err != nil {
		return fail(fmt.Errorf("bind durable D06 provenance before worker exposure: %w", err))
	}
	session.Prepared = prepared
	// E04 post-materialization: the workspace generation is seeded from the live root and the
	// private-Git snapshot is bound; the worker is not exposed to anything yet.
	session.recordE04(wsoracle.PointPostMaterialization)
	if workflowAdmission != nil {
		conversation, err := initializeProductionFreshWorkflow(ctx, config, workflowAdmission, profile, template,
			storePolicyDigest, metadataPolicyDigest, managed, storeRoot, adminRoot,
			storeJournal, providerJournal, commandJournal, prepared, g0,
			sessionrepo.Options{PolicyDigest: storePolicyDigest, MetadataPolicyDigest: metadataPolicyDigest,
				Limits: workspace.DefaultLimits(), XattrVisibility: xattrVisibility, Journal: storeJournal,
				AdmissionScope: authority.admissionScope},
			runner, authority, productionSystemPrompt, productionDeveloperPrompt)
		if err != nil {
			return fail(fmt.Errorf("initialize the host-authorized production workflow before worker exposure: %w", err))
		}
		session.recoveredProviderConversation = conversation
	}

	// Sealed worker exposure and read-only runtime assets.
	source, err := prepared.OpenVerifiedD09ExposureSource(ctx, g0)
	if err != nil {
		return fail(fmt.Errorf("open verified D09 sealed-source capability for the exact current generation: %w", err))
	}
	closers = append(closers, source.Close)
	bundleRoot, err := os.Open(profile.PiRuntimeBundleRoot)
	if err != nil {
		return fail(fmt.Errorf("open read-only Pi runtime bundle root: %w", err))
	}
	closers = append(closers, bundleRoot.Close)
	assets, err := sessionlaunch.OpenVerifiedD09RuntimeAssetSource(bundleRoot, sessionlaunch.RuntimeAssetSpec{
		BundleDigest: profile.PiRuntimeBundleDigest, NamespaceProfileDigest: profile.ContainmentProfileDigest,
	})
	if err != nil {
		return fail(fmt.Errorf("open strict verified D09 runtime-asset source: %w", err))
	}
	closers = append(closers, assets.Close)

	// Launcher-owned D09 cell: verified cgroup scope + rootless Podman/crun
	// launcher bound to it.
	var scope *piruntime.LinuxCgroupScope
	var scopePath string
	if workflowAdmission != nil {
		scope, scopePath, err = openProductionCgroupScopeNamed(ctx, template, profile,
			workflowOwnerScopeName(workflowAdmission.Entry.WorkflowID, workflowAdmission.CandidateEpoch))
	} else {
		scope, scopePath, err = openProductionCgroupScope(ctx, template, profile)
	}
	if err != nil {
		return fail(err)
	}
	scopeIdentity := scope.IdentityDigest()
	if !validDigestText(scopeIdentity) {
		return fail(errors.New("created owner cgroup has no stable host identity"))
	}
	removeScope := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := scope.Remove(cleanupCtx); err != nil {
			return err
		}
		if workflowAdmission != nil && ownerScopeBound {
			return workflowAdmission.Catalog.MarkOwnerScopeDrained(workflowAdmission.Entry.AdmissionRecordHash,
				workflowAdmission.Entry.WorkflowID, workflowAdmission.CandidateEpoch, workflowAdmission.OwnerIncarnation,
				filepath.Clean(template.CgroupsPath), "session_cleanup_complete")
		}
		return nil
	}
	closers = append(closers, removeScope)
	if workflowAdmission != nil {
		if err := workflowAdmission.Catalog.BindOwnerScope(workflowAdmission, workflowAdmission.CandidateEpoch,
			workflowAdmission.OwnerIncarnation, scopePath, scopeIdentity); err != nil {
			return fail(fmt.Errorf("bind exact owner cgroup identity to the host workflow catalog: %w", err))
		}
		ownerScopeBound = true
	}
	launcherConfig := template.Config()
	launcherConfig.CgroupsPath = scopePath
	launcher := piruntime.NewD09PodmanCrunLauncher(launcherConfig)
	closeLauncher := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return launcher.Close(cleanupCtx)
	}
	// Append in reverse execution order: C removes its own cgroup scope first,
	// then closes A's launcher-owned registration clone. The lifecycle guard runs
	// before this stack; a failed scope removal stops cleanup and retains both.
	// Insert the launcher after the scope remover in acquisition order so
	// reverse cleanup removes the cell scope before releasing the launcher's
	// registration clone.
	scopeCloserIndex := len(closers) - 1
	closers = append(closers, nil)
	copy(closers[scopeCloserIndex+1:], closers[scopeCloserIndex:])
	closers[scopeCloserIndex] = closeLauncher
	if err := managedRootLease.bindLauncher(launcher); err != nil {
		return fail(fmt.Errorf("bind fixed-registration managed live root into D09 launcher admission: %w", err))
	}
	if err := launcher.BindCgroupScope(scope); err != nil {
		return fail(fmt.Errorf("bind verified cgroup scope to D09 launcher: %w", err))
	}
	// Reverse cleanup is now channels, C-owned scope removal, then launcher.Close.
	// The lifecycle guard runs before this stack, and a failed scope removal stops
	// the stack before launcher.Close, retaining both owners for UNKNOWN recovery.
	commandLauncher = launcher

	// D09 source-stage channels export the Store capability exactly once. The
	// caller-owned source FD remains in these channels; the sessionlaunch wrapper
	// and Store capability stay live until after scope/cell cleanup.
	bindings, d09Channels, err := piruntime.BindProductionPiWorkerBindingsWithSource(ctx, profile,
		source, source.SourceCapability(), assets, launcher, scope)
	if err != nil {
		return fail(fmt.Errorf("assemble production Pi worker bindings: %w", err))
	}
	closers = append(closers, d09Channels.Close)
	session.Bindings, session.D09Channels, session.Channels = bindings, d09Channels, d09Channels.PiWorkerChannels()

	var closeOnce sync.Once
	var closeErr error
	session.Close = func() error {
		closeOnce.Do(func() {
			if retainProductionCleanupIfUnsafe(
				&productionCleanupQuarantine,
				session.Lifecycle.snapshot(),
				session.ProductionCommandRunner,
				closers,
			) {
				// Keep the held D09 cgroup, worker views, Store/Git/admin handles, and
				// command-scope receipts available to process-level recovery when the
				// worker/cell settlement is UNKNOWN. Process exit closes descriptors;
				// this function must not pretend it safely reclaimed a live scope.
				closeErr = ErrProductionLifecycleUnsettled
				return
			}
			var errs []error
			if session.Authority != nil {
				session.Authority.mu.Lock()
				conversation := session.Authority.conversation
				session.Authority.mu.Unlock()
				if conversation != nil {
					conversation.Close()
				}
			}
			for index := len(closers) - 1; index >= 0; index-- {
				if err := closers[index](); err != nil {
					errs = append(errs, err)
					retainProductionCleanup(closers[:index+1])
					break
				}
			}
			closeErr = errors.Join(errs...)
		})
		return closeErr
	}
	workflowOwnerAttached = workflowAdmission != nil
	return session, nil
}

func requireProductionWorkflowFreshLayout(entry piServiceWorkflowEntry, artifacts productionOperatorArtifacts) error {
	if entry.RunID == "" || entry.WorkflowID == "" || entry.SessionID != entry.RunID || entry.ConversationID == "" ||
		entry.OwnerDBPath != filepath.Join(piServiceOwnerStoreRoot, entry.WorkflowID+".sqlite") ||
		entry.StoreRootPath != filepath.Join(artifacts.StoreRoot, filepath.Base(entry.StoreRootPath)) ||
		entry.AdminRootPath != filepath.Join(artifacts.AdminRoot, filepath.Base(entry.AdminRootPath)) ||
		entry.StoreJournalPath != filepath.Join(artifacts.AuditRoot, entry.RunID+"-session-audit.jsonl") ||
		entry.ProviderJournalPath != filepath.Join(artifacts.AuditRoot, entry.RunID+"-provider-audit.jsonl") ||
		entry.CommandJournalPath != filepath.Join(artifacts.AuditRoot, entry.RunID+"-command-audit.jsonl") {
		return ErrPiWorkflowCatalog
	}
	return nil
}

// productionXattrVisibility turns the root-owned, profile-bound D09 template's
// measured xattr visibility identity into the sessionrepo attestation only after
// checking the exact profile/template binding and the actual source descriptor's
// filesystem and scanning uid/gid. A constant digest, caller boolean, writable
// checkout, or successful listxattr syscall cannot produce this result.
func productionXattrVisibility(profile piruntime.HostProfile, template piruntime.D09CellTemplate, source *os.File) (workspace.XattrVisibilityAttestation, error) {
	digest, err := template.Digest()
	if err != nil || source == nil || profile.Mode != piruntime.ModeProduction ||
		profile.ContainmentProfileDigest != digest || profile.ContainmentProfile != piruntime.D09ContainmentProfile ||
		!validDigestText(template.XattrVisibilityProfileDigest) {
		return workspace.XattrVisibilityAttestation{}, errors.Join(ErrProductionXattrVisibilityUnavailable, err)
	}
	config := template.Config()
	if config.TemplateDigest != profile.ContainmentProfileDigest ||
		config.XattrVisibilityProfileDigest != template.XattrVisibilityProfileDigest {
		return workspace.XattrVisibilityAttestation{}, ErrProductionXattrVisibilityUnavailable
	}
	attestation, err := piruntime.VerifyD09XattrVisibility(source, profile, template)
	if err != nil {
		return workspace.XattrVisibilityAttestation{}, errors.Join(ErrProductionXattrVisibilityUnavailable, err)
	}
	return attestation, nil
}

func loadProductionOperatorArtifacts(config productionServeConfig, template piruntime.D09CellTemplate) (productionOperatorArtifacts, error) {
	source := strings.TrimSpace(config.Source)
	liveRoot := productionLiveRootDefault
	if config.ServiceMode {
		liveRoot = config.ManagedLiveRootPath
	} else if strings.TrimSpace(config.Workspace) != "" {
		liveRoot = strings.TrimSpace(config.Workspace)
	}
	if !filepath.IsAbs(liveRoot) || filepath.Clean(liveRoot) != liveRoot ||
		(!config.ServiceMode && (source == "" || !filepath.IsAbs(source) || filepath.Clean(source) == filepath.Clean(liveRoot))) ||
		(config.ServiceMode && (source != "" || config.Workspace != "")) {
		return productionOperatorArtifacts{}, errors.New("--workspace must be a canonical absolute live root distinct from --source")
	}
	cgroupRoot := strings.TrimSpace(template.CgroupsPath)
	if !filepath.IsAbs(cgroupRoot) {
		return productionOperatorArtifacts{}, errors.New("registered D09 template has no absolute cgroups_path")
	}
	approval, err := readProtectedGitApproval(productionGitApprovalPath)
	if err != nil {
		return productionOperatorArtifacts{}, err
	}
	gitPath := strings.TrimSpace(approval.Path)
	if gitPath == "" {
		gitPath = productionGitPathDefault
	}
	if !filepath.IsAbs(gitPath) {
		return productionOperatorArtifacts{}, errors.New("protected Git approval path must be absolute")
	}
	if !validDigestText(approval.Digest) || !validIdentityText(approval.Boundary) {
		return productionOperatorArtifacts{}, errors.New("protected Git approval is missing a valid digest or boundary")
	}
	return productionOperatorArtifacts{
		StoreRoot: productionStoreRootDefault, AuditRoot: productionAuditRootDefault,
		AdminRoot: productionAdminRootDefault, SourceRoot: filepath.Clean(source),
		CgroupRoot: filepath.Clean(cgroupRoot), LiveRoot: filepath.Clean(liveRoot),
		PublicationStateRoot: productionStateRootDefault,
		GitPath:              filepath.Clean(gitPath),
		GitDigest:            approval.Digest, GitBoundary: approval.Boundary,
	}, nil
}

func createPrivateRunDirectory(parent, name string) (string, error) {
	if !filepath.IsAbs(parent) || filepath.Clean(parent) != parent || name == "" ||
		strings.ContainsAny(name, "/\\\x00") || name == "." || name == ".." {
		return "", errors.New("production run directory parent/name is invalid")
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm() != 0o700 {
		return "", errors.Join(errors.New("production run-directory parent must be an exact mode-0700 non-symlink directory"), err)
	}
	parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok || parentStat.Uid != uint32(os.Geteuid()) || parentStat.Gid != uint32(os.Getegid()) {
		return "", errors.New("production run-directory parent must be owned by the launching uid/gid")
	}
	path := filepath.Join(parent, name)
	if err := os.Mkdir(path, 0o700); err != nil {
		return "", fmt.Errorf("create unique production run directory: %w", err)
	}
	cleanup := func(err error) (string, error) {
		_ = os.Remove(path)
		return "", err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return cleanup(err)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return cleanup(fmt.Errorf("open unique production run directory without following links: %w", err))
	}
	opened := os.NewFile(uintptr(fd), "production-run-directory")
	defer opened.Close()
	openedInfo, err := opened.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil {
		return cleanup(errors.Join(errors.New("production run directory identity changed during creation"), err, pathErr))
	}
	openedStat, openedOK := openedInfo.Sys().(*syscall.Stat_t)
	if !openedInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, pathInfo) ||
		openedInfo.Mode().Perm() != 0o700 || !openedOK || openedStat.Uid != uint32(os.Geteuid()) || openedStat.Gid != uint32(os.Getegid()) {
		return cleanup(errors.Join(errors.New("production run directory identity/ownership changed during creation"), err, pathErr))
	}
	return path, nil
}

// openProductionQuiescentSource opens each path component descriptor-relatively
// without following symlinks and admits a source snapshot only when the root is
// root-owned, non-writable, and the containing filesystem is mounted read-only.
// The read-only mount is the concrete seed-writer exclusion; a caller boolean
// or a path string alone never sets RootAttestation.Quiescent.
func openProductionQuiescentSource(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.Join(ErrProductionSourceNotQuiescent, errors.New("source path must be a canonical absolute path"))
	}
	fd, err := syscall.Open(string(os.PathSeparator), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.Join(ErrProductionSourceNotQuiescent, err)
	}
	current := os.NewFile(uintptr(fd), "production-source-path-root")
	for _, component := range strings.Split(strings.TrimPrefix(path, string(os.PathSeparator)), string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		nextFD, openErr := syscall.Openat(int(current.Fd()), component,
			syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if openErr != nil {
			_ = current.Close()
			return nil, errors.Join(ErrProductionSourceNotQuiescent, openErr)
		}
		next := os.NewFile(uintptr(nextFD), "production-source-component")
		_ = current.Close()
		current = next
	}
	var stat syscall.Stat_t
	var fs syscall.Statfs_t
	statErr := syscall.Fstat(int(current.Fd()), &stat)
	fsErr := syscall.Fstatfs(int(current.Fd()), &fs)
	if statErr != nil || fsErr != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFDIR || stat.Uid != 0 ||
		stat.Mode&0o222 != 0 || fs.Flags&productionStatfsReadonly == 0 {
		_ = current.Close()
		return nil, errors.Join(ErrProductionSourceNotQuiescent, statErr, fsErr)
	}
	return current, nil
}

func readProtectedGitApproval(path string) (productionGitApproval, error) {
	parent := filepath.Dir(path)
	directoryInfo, err := os.Lstat(parent)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 || directoryInfo.Mode().Perm()&0o022 != 0 {
		return productionGitApproval{}, fmt.Errorf("protected Git approval directory %s is missing or writable", parent)
	}
	directoryStat, ok := directoryInfo.Sys().(*syscall.Stat_t)
	if !ok || directoryStat.Uid != 0 {
		return productionGitApproval{}, fmt.Errorf("protected Git approval directory %s must be root-owned", parent)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 || info.Size() <= 0 || info.Size() > 64<<10 {
		return productionGitApproval{}, fmt.Errorf("protected Git approval %s is missing or not a root-owned non-writable file", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return productionGitApproval{}, fmt.Errorf("protected Git approval %s must be root-owned", path)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return productionGitApproval{}, fmt.Errorf("open protected Git approval %s without following links: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return productionGitApproval{}, errors.New("create protected Git approval descriptor")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return productionGitApproval{}, errors.Join(errors.New("protected Git approval changed while opening"), err)
	}
	raw, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil || int64(len(raw)) != info.Size() || len(raw) > 64<<10 {
		return productionGitApproval{}, errors.Join(errors.New("protected Git approval size changed while reading"), err)
	}
	if err := protocol.ValidateStrictJSON(raw); err != nil {
		return productionGitApproval{}, fmt.Errorf("protected Git approval is not strict JSON: %w", err)
	}
	var approval productionGitApproval
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&approval); err != nil {
		return productionGitApproval{}, fmt.Errorf("decode protected Git approval %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return productionGitApproval{}, errors.New("protected Git approval contains trailing JSON")
	}
	return approval, nil
}

func pinProductionGit(artifacts productionOperatorArtifacts) (privategit.PinnedGit, sessionlaunch.GitProfile, error) {
	digest, boundary := artifacts.GitDigest, artifacts.GitBoundary
	git, err := privategit.PinGitExecutable(artifacts.GitPath, privategit.GitExecutableApproval{
		ExpectedDigest: digest, Boundary: boundary,
		Verifier: func(actual privategit.GitExecutableIdentity) error {
			if actual.Digest != digest || actual.Mode&0o022 != 0 {
				return errors.New("pinned Git executable bytes or mode are not the approved identity")
			}
			return nil
		},
	})
	if err != nil {
		return privategit.PinnedGit{}, sessionlaunch.GitProfile{}, fmt.Errorf("pin approved Git executable: %w", err)
	}
	return git, sessionlaunch.GitProfile{ExecutableDigest: digest, ExecutionBoundary: boundary}, nil
}

// openProductionCgroupScope delegates unique-child creation/open to A's
// descriptor-relative API. The returned path comes from the held scope handle
// and is only the launcher's --cgroup-parent; teardown uses scope.Remove(ctx),
// never os.Remove or a caller-derived unlink path.
func openProductionCgroupScope(ctx context.Context, template piruntime.D09CellTemplate, profile piruntime.HostProfile) (*piruntime.LinuxCgroupScope, string, error) {
	if ctx == nil {
		return nil, "", errors.New("production cgroup scope requires a context")
	}
	token, err := productionToken()
	if err != nil {
		return nil, "", err
	}
	return openProductionCgroupScopeNamed(ctx, template, profile, "pi-"+token)
}

func openProductionCgroupScopeNamed(ctx context.Context, template piruntime.D09CellTemplate,
	profile piruntime.HostProfile, childName string) (*piruntime.LinuxCgroupScope, string, error) {
	if ctx == nil || childName == "" || strings.ContainsAny(childName, "/\\\x00") || childName == "." || childName == ".." {
		return nil, "", errors.New("production cgroup scope requires a context and one direct-child name")
	}
	rootPath := filepath.Clean(strings.TrimSpace(template.CgroupsPath))
	rootHandle, err := os.Open(rootPath)
	if err != nil {
		return nil, "", fmt.Errorf("open delegated cgroup root: %w", err)
	}
	scope, err := piruntime.CreateLinuxCgroupScope(rootHandle, childName, profile.ContainmentProfile, profile.SettlementProfile)
	rootCloseErr := rootHandle.Close()
	if err != nil {
		return nil, "", errors.Join(fmt.Errorf("create and verify descriptor-relative delegated cgroup child: %w", err), rootCloseErr)
	}
	scopePath, err := scope.Path()
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cleanupErr := scope.Remove(cleanupCtx)
		cancel()
		if cleanupErr != nil {
			retainProductionCleanup([]func() error{func() error {
				retryCtx, cancelRetry := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancelRetry()
				return scope.Remove(retryCtx)
			}})
		}
		return nil, "", errors.Join(err, cleanupErr, rootCloseErr)
	}
	if rootCloseErr != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cleanupErr := scope.Remove(cleanupCtx)
		cancel()
		if cleanupErr != nil {
			retainProductionCleanup([]func() error{func() error {
				retryCtx, cancelRetry := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancelRetry()
				return scope.Remove(retryCtx)
			}})
		}
		return nil, "", errors.Join(rootCloseErr, cleanupErr)
	}
	return scope, scopePath, nil
}

func productionGatePolicy() (gate.Policy, error) {
	return gate.NewPolicy(gate.ProductionProfile())
}

func productionToken() (string, error) {
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("mint production cgroup token: %w", err)
	}
	return hex.EncodeToString(token[:]), nil
}

// productionWorkspaceOptions is the workspace binding of a production session: the root ledger
// records it and the publication repository scans the live and sealed roots with it. Those scans
// run only after productionTrust.AttestQuiescent issued the one-use permit over the managed root
// (sole writer domain, STOPPED worker, joined loops), so the binding carries that attestation;
// without it workspace.Scan refuses every publication scan.
func productionWorkspaceOptions(generation, metadataPolicyDigest string, xattr workspace.XattrVisibilityAttestation) workspace.Options {
	return workspace.Options{Generation: generation, MetadataPolicyDigest: metadataPolicyDigest,
		Limits: workspace.DefaultLimits(), QuiescentRoot: true, XattrVisibility: xattr}
}
