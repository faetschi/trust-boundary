//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/privategit"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
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
	productionLiveRootDefault  = "/var/lib/tbound/live"
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

var productionCleanupQuarantine struct {
	sync.Mutex
	holds [][]func() error
}

// retainProductionCleanup keeps the exact held descriptors/owners reachable
// after an UNKNOWN teardown failure. It does not retry with broader authority or
// declare success; the durable cgroup/source identities remain available to a
// later recovery implementation while this process remains alive.
func retainProductionCleanup(closers []func() error) {
	if len(closers) == 0 {
		return
	}
	productionCleanupQuarantine.Lock()
	productionCleanupQuarantine.holds = append(productionCleanupQuarantine.holds, append([]func() error(nil), closers...))
	productionCleanupQuarantine.Unlock()
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

	runToken, err := productionToken()
	if err != nil {
		return nil, err
	}
	runID := "production-run-" + runToken
	workflowID := "tbound-workflow-" + runToken
	conversationID := "tbound-conversation-" + runToken
	publicationID := "production-publication-" + runToken
	storeRootPath, err := createPrivateRunDirectory(artifacts.StoreRoot, "session-"+runToken)
	if err != nil {
		return nil, fmt.Errorf("create unique protected session Store directory: %w", err)
	}
	adminRootPath, err := createPrivateRunDirectory(artifacts.AdminRoot, "admin-"+runToken)
	if err != nil {
		_ = os.Remove(storeRootPath)
		return nil, fmt.Errorf("create unique protected private-admin directory: %w", err)
	}
	session := &productionPiSession{
		Profile: profile, RunID: runID, SessionID: runID, WorkflowID: workflowID,
		ConversationID: conversationID, ProviderProfileID: profile.ProviderProfile,
		SystemPrompt: productionSystemPrompt, DeveloperPrompt: productionDeveloperPrompt,
		CallIssuer: governedCallIssuer, D09Template: template,
		Lifecycle: &productionLifecycleEvidence{},
	}
	closers := make([]func() error, 0, 24)
	storeCreated := false
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
	session.ManagedLiveRootLease = managedRootLease
	session.OwnerEpoch, session.PublicationID = ownerEpoch, publicationID
	if err := recoverManagedLiveRootBeforeCapture(ctx, managedRootLease, liveRoot, publicationStateRoot,
		publicationJournal, ownerEpoch); err != nil {
		return fail(fmt.Errorf("recover registered managed-root publications before source capture: %w", err))
	}

	policy, err := productionGatePolicy()
	if err != nil {
		return fail(err)
	}
	storePolicyDigest := digestBytes([]byte("tbound/session-policy/v1"))
	metadataPolicyDigest := digestBytes([]byte("tbound/metadata-policy/v1"))

	var commandLauncher *piruntime.D09PodmanCrunLauncher
	runner := newProductionCommandRunner(func(leaseID string) (sessionrepo.CommandRunner, error) {
		if commandLauncher == nil {
			return nil, ErrProductionBashRunnerUnavailable
		}
		inner, err := piruntime.NewD09CommandRunner(leaseID, commandLauncher)
		if err != nil {
			return nil, err
		}
		return newProductionD09BashRunner(leaseID, inner, commandJournal, runID, session.SessionID,
			session.WorkflowID, session.ConversationID, profile, template), nil
	})
	session.CommandRunner, session.ProductionCommandRunner = runner, runner
	authority, err := NewSessionOperationAuthority(storeJournal, providerJournal,
		workflowID, conversationID, profile.ProviderProfile, policy,
		storePolicyDigest, metadataPolicyDigest, runner)
	if err != nil {
		return fail(err)
	}
	// Production requires positive, evidenced Bash containment; a legacy/dev
	// "not-established" settlement can never be admitted or receipted as D09.
	authority.requireEvidencedBashContainment = true
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
	session.Workspace = workspace.Options{
		Generation: g0.ID(), MetadataPolicyDigest: metadataPolicyDigest, Limits: workspace.DefaultLimits(),
		XattrVisibility: xattrVisibility,
	}

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
	scope, scopePath, err := openProductionCgroupScope(ctx, template, profile)
	if err != nil {
		return fail(err)
	}
	removeScope := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return scope.Remove(cleanupCtx)
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
	closers = append(closers, closeLauncher, removeScope)
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
			if !productionCleanupSafe(session.Lifecycle.snapshot(), session.ProductionCommandRunner) {
				// Keep the held D09 cgroup, worker views, Store/Git/admin handles, and
				// command-scope receipts available to process-level recovery when the
				// worker/cell settlement is UNKNOWN. Process exit closes descriptors;
				// this function must not pretend it safely reclaimed a live scope.
				closeErr = ErrProductionLifecycleUnsettled
				retainProductionCleanup(closers)
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
	return session, nil
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
	rootPath := filepath.Clean(strings.TrimSpace(template.CgroupsPath))
	token, err := productionToken()
	if err != nil {
		return nil, "", err
	}
	rootHandle, err := os.Open(rootPath)
	if err != nil {
		return nil, "", fmt.Errorf("open delegated cgroup root: %w", err)
	}
	scope, err := piruntime.CreateLinuxCgroupScope(rootHandle, "pi-"+token, profile.ContainmentProfile, profile.SettlementProfile)
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
	return gate.NewPolicy(gate.Profile{Version: gate.ProfileVersion, Rules: []gate.Rule{
		{Tool: "read", Effect: gate.EffectAllow},
		{Tool: "edit", Effect: gate.EffectAllow},
		{Tool: "write", Effect: gate.EffectAllow},
		{Tool: "bash", Effect: gate.EffectAllow},
	}})
}

func productionToken() (string, error) {
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("mint production cgroup token: %w", err)
	}
	return hex.EncodeToString(token[:]), nil
}

// productionCommandRunner is the CLI's lease/settlement wrapper for a signed
// D09 command-cell runner. It validates the concrete Runtime A runner type and
// proof-verifier interface, registers the authenticated view/lease, and retains
// one-use settlement receipts. It never falls back to the legacy Alpine
// internal/podman runner or the development sandbox. A missing or incompatible
// runtime API fails at lease registration before a Bash effect can start.
var ErrProductionBashRunnerUnavailable = errors.New("no registered signed D09 Bash command-cell runner is configured")

type productionCommandRunnerFactory func(leaseID string) (sessionrepo.CommandRunner, error)

type productionD09RunEvidence struct {
	leaseID         string
	viewID          string
	receipt         piruntime.D09CommandReceipt
	receiptSnapshot []byte
	receiptDigest   string
	operation       sessionrepo.OperationEvidence
	transition      delta.Transition
	providerCall    delta.OperationIdentity
	binding         bool
	journalSeq      uint64
	journalHash     string
	eventID         string
}

type productionD09CommandSettledEvent struct {
	SchemaVersion  string                        `json:"schema_version"`
	RunID          string                        `json:"run_id"`
	SessionID      string                        `json:"session_id"`
	WorkflowID     string                        `json:"workflow_id"`
	ConversationID string                        `json:"conversation_id"`
	ProfileID      string                        `json:"profile_id"`
	ProfileDigest  string                        `json:"profile_digest"`
	ProviderCall   delta.OperationIdentity       `json:"provider_call"`
	Operation      sessionrepo.OperationEvidence `json:"operation_evidence"`
	Transition     delta.Transition              `json:"approved_transition"`
	ReceiptDigest  string                        `json:"receipt_digest"`
	Receipt        json.RawMessage               `json:"receipt"`
}

type productionD09CommandEffectResult struct {
	Transition    delta.Transition              `json:"transition"`
	Command       sessionrepo.CommandResult     `json:"command"`
	Settlement    sessionrepo.CommandSettlement `json:"settlement"`
	Spec          sessionrepo.CommandSpec       `json:"spec"`
	RunnerProfile string                        `json:"runner_profile"`
}

type productionD09CommandInvoker interface {
	Run(context.Context, *sessionrepo.CommandView, sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error)
	TakeCommandReceipt(*sessionrepo.CommandView, sessionrepo.CommandSpec, sessionrepo.CommandResult,
		sessionrepo.CommandSettlement) (piruntime.D09CommandReceipt, error)
}

// productionD09BashRunner binds one Runtime A D09 runner to one authenticated
// lease. It consumes the runtime's one-use TCB receipt immediately after Run,
// then binds it to the Store's actual OperationEvidence and delta in one
// command-journal event before DurableExecutor releases any result.
type productionD09BashRunner struct {
	leaseID        string
	inner          productionD09CommandInvoker
	journal        *audit.Journal
	runID          string
	sessionID      string
	workflowID     string
	conversationID string
	profile        piruntime.HostProfile
	template       piruntime.D09CellTemplate
	mu             sync.Mutex
	evidence       map[string]productionD09RunEvidence
}

func newProductionD09BashRunner(leaseID string, inner *piruntime.D09CommandRunner, journal *audit.Journal,
	runID, sessionID, workflowID, conversationID string, profile piruntime.HostProfile, template piruntime.D09CellTemplate) *productionD09BashRunner {
	return newProductionD09BashRunnerWithInvoker(leaseID, inner, journal, runID, sessionID, workflowID,
		conversationID, profile, template)
}

func newProductionD09BashRunnerWithInvoker(leaseID string, inner productionD09CommandInvoker, journal *audit.Journal,
	runID, sessionID, workflowID, conversationID string, profile piruntime.HostProfile, template piruntime.D09CellTemplate) *productionD09BashRunner {
	return &productionD09BashRunner{leaseID: leaseID, inner: inner, journal: journal, runID: runID,
		sessionID: sessionID, workflowID: workflowID, conversationID: conversationID, profile: profile, template: template,
		evidence: make(map[string]productionD09RunEvidence)}
}

func (r *productionD09BashRunner) Profile() string {
	if r == nil || r.inner == nil || r.leaseID == "" {
		return "production-d09-bash-runner-unavailable"
	}
	return piruntime.D09CommandRunnerProfile
}

func (r *productionD09BashRunner) Run(ctx context.Context, view *sessionrepo.CommandView, spec sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	if r == nil || r.inner == nil || r.journal == nil || ctx == nil || view == nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, ErrProductionBashRunnerUnavailable
	}
	result, settlement, err := r.inner.Run(ctx, view, spec)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	// Consume the exact runtime-issued proof receipt before any further work or
	// result release. TakeCommandReceipt is one-use and validates this view,
	// canonical spec, actual result, and actual settlement against the invocation.
	runtimeReceipt, err := r.inner.TakeCommandReceipt(view, spec, result, settlement)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.Join(ErrProductionCommandEvidence, err)
	}
	if err := runtimeReceipt.Validate(); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.Join(ErrProductionCommandEvidence, err)
	}
	receiptDigest, err := runtimeReceipt.Digest()
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.Join(ErrProductionCommandEvidence, err)
	}
	receiptSnapshot, err := json.Marshal(runtimeReceipt)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.Join(ErrProductionCommandEvidence, err)
	}
	if runtimeReceipt.ReceiptDigest != receiptDigest || runtimeReceipt.LeaseID != r.leaseID ||
		runtimeReceipt.ViewID != view.ID() || runtimeReceipt.Settlement != settlement ||
		runtimeReceipt.ExitCode != result.ExitCode || !result.ExitObserved {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, ErrProductionCommandEvidence
	}
	if err := validateProductionD09ReceiptInvocation(view, spec, result, settlement, runtimeReceipt, r.profile, r.template); err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.Join(ErrProductionCommandEvidence, err)
	}
	if settlement.LeaseID != r.leaseID || settlement.ViewID != view.ID() || !settlement.ExitObserved ||
		!settlement.ProcessScopeEmpty || !settlement.WritersStopped || !settlement.MountDetached ||
		!strings.HasPrefix(settlement.EvidenceClass, piruntime.D09CommandRunnerProfile+":") {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, ErrProductionCommandEvidence
	}
	receipt := productionD09RunEvidence{leaseID: r.leaseID, viewID: view.ID(), receipt: runtimeReceipt,
		receiptSnapshot: append([]byte(nil), receiptSnapshot...), receiptDigest: receiptDigest}
	r.mu.Lock()
	if _, duplicate := r.evidence[view.ID()]; duplicate {
		r.mu.Unlock()
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.New("D09 command view already has a retained proof receipt")
	}
	r.evidence[view.ID()] = receipt
	r.mu.Unlock()
	return result, settlement, nil
}

func validateProductionD09ReceiptInvocation(view *sessionrepo.CommandView, spec sessionrepo.CommandSpec,
	result sessionrepo.CommandResult, settlement sessionrepo.CommandSettlement, receipt piruntime.D09CommandReceipt,
	profile piruntime.HostProfile, template piruntime.D09CellTemplate) error {
	if view == nil || !result.ExitObserved || settlement.LeaseID != receipt.LeaseID || settlement.ViewID != view.ID() ||
		receipt.ViewID != view.ID() || receipt.Settlement != settlement || receipt.ExitCode != result.ExitCode ||
		profile.Mode != piruntime.ModeProduction || profile.ContainmentProfile != piruntime.D09ContainmentProfile ||
		profile.SettlementProfile != piruntime.D09SettlementProfile {
		return ErrProductionCommandEvidence
	}
	templateDigest, err := template.Digest()
	if err != nil || templateDigest != profile.ContainmentProfileDigest || receipt.TemplateDigest != templateDigest ||
		receipt.ImageDigest != template.ExpectedImageDigest || receipt.ImageRef != registeredD09ImageDigestRef(template) ||
		template.CommandExecutable != spec.Executable || receipt.Proof.CommandDigest != template.CommandExecutableDigest || receipt.Proof.CommandPath != spec.Executable ||
		receipt.Proof.CommandWorkdir != filepath.Clean(spec.WorkingDirectory) || receipt.Proof.UID != template.CellUID ||
		receipt.Proof.GID != template.CellGID || receipt.Proof.CapEff != "0000000000000000" || !receipt.Proof.NoNewPrivs ||
		receipt.Proof.Seccomp != 2 || !receipt.Proof.CgroupV2 || !receipt.Proof.LandlockTSYNC ||
		receipt.Proof.LandlockABI < template.LandlockABI || receipt.Proof.LandlockRulesetDigest != template.LandlockRulesetDigest ||
		!receipt.Proof.ConsumerMountsVerified || !receipt.Proof.RawSourceFDsClosed || !receipt.Proof.NoExternalRoutes ||
		receipt.Proof.WorkerMountID == 0 || receipt.Proof.WorkerMountID != receipt.Proof.RuntimeMountID {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	encodedSpec, err := json.Marshal(spec)
	if err != nil || digestBytes(encodedSpec) != receipt.CommandSpecDigest {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	encodedResult, err := json.Marshal(result)
	if err != nil || digestBytes(encodedResult) != receipt.ResultDigest {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	encodedSettlement, err := json.Marshal(settlement)
	if err != nil || digestBytes(encodedSettlement) != receipt.SettlementDigest {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	source, err := view.MountSource()
	if err != nil {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.IsDir() {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Dev) != receipt.Proof.WorkerDevice || stat.Ino != receipt.Proof.WorkerInode ||
		uint64(stat.Dev) != receipt.Proof.RuntimeDevice || stat.Ino != receipt.Proof.RuntimeInode {
		return ErrProductionCommandEvidence
	}
	mountID, err := productionDescriptorMountID(source.Fd())
	if err != nil || mountID != receipt.Proof.WorkerMountID || mountID != receipt.Proof.RuntimeMountID {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	return nil
}

func registeredD09ImageDigestRef(template piruntime.D09CellTemplate) string {
	image := template.Image
	if colon := strings.LastIndex(image, ":"); colon > strings.LastIndex(image, "/") {
		image = image[:colon]
	}
	return image + "@" + template.ExpectedImageDigest
}

func productionDescriptorMountID(fd uintptr) (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", fd))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "mnt_id:" {
			mountID, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil || mountID == 0 {
				return 0, errors.New("command-view descriptor mount ID is invalid")
			}
			return mountID, nil
		}
	}
	return 0, errors.New("command-view descriptor fdinfo omitted mount ID")
}

func (r *productionD09BashRunner) BindProductionBashEvidence(ctx context.Context, providerCall delta.OperationIdentity,
	bundle sessionrepo.EvidenceBundle, transition delta.Transition, operation sessionrepo.OperationEvidence) error {
	if r == nil || r.inner == nil || r.journal == nil || ctx == nil || operation.ViewID == "" {
		return ErrProductionCommandEvidence
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	receipt, exists := r.evidence[operation.ViewID]
	if !exists || receipt.journalSeq != 0 || receipt.binding {
		r.mu.Unlock()
		return ErrProductionCommandEvidence
	}
	receipt.binding = true
	r.evidence[operation.ViewID] = receipt
	r.mu.Unlock()
	if err := validateProductionD09OperationBundle(bundle, transition, operation, receipt.receipt); err != nil {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	if providerCall.Kind != delta.OperationProposalCall || providerCall.ProposalID == "" ||
		providerCall.CallIssuer == "" || providerCall.CallID == "" || transition.Operation.Kind != delta.OperationLease ||
		transition.Operation.LeaseID != receipt.leaseID || operation.Operation != transition.Operation {
		return ErrProductionCommandEvidence
	}
	eventID := "production-d09-command:" + r.runID + ":" + receipt.leaseID + ":" + receipt.viewID + ":" + operation.EffectID
	data, err := marshalProductionD09CommandEvent(r, providerCall, transition, operation, receipt)
	if err != nil {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	before, err := r.journal.Trace()
	if err != nil {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	for _, record := range before.Records {
		if record.Event.Kind == "production_d09_command_settled" && record.Event.ID == eventID {
			return ErrProductionCommandEvidence
		}
	}
	journalRecord, err := r.journal.Append(audit.Event{Kind: "production_d09_command_settled", ID: eventID, Data: data})
	if err != nil {
		return fmt.Errorf("durably bind D09 receipt to actual Store operation before result release: %w", err)
	}
	receipt.operation, receipt.transition, receipt.providerCall = operation, transition, providerCall
	receipt.journalSeq, receipt.journalHash, receipt.eventID = journalRecord.Sequence, journalRecord.Hash, eventID
	if err := r.verifyProductionBashEvent(ctx, receipt, operation, transition, providerCall, bundle, journalRecord); err != nil {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	r.mu.Lock()
	r.evidence[operation.ViewID] = receipt
	r.mu.Unlock()
	return nil
}

func marshalProductionD09CommandEvent(r *productionD09BashRunner, providerCall delta.OperationIdentity,
	transition delta.Transition, operation sessionrepo.OperationEvidence, receipt productionD09RunEvidence) ([]byte, error) {
	return json.Marshal(productionD09CommandSettledEvent{
		SchemaVersion: "tbound-production-d09-command-settled/v2",
		RunID:         r.runID, SessionID: r.sessionID, WorkflowID: r.workflowID, ConversationID: r.conversationID,
		ProfileID: r.profile.ID, ProfileDigest: r.profile.Digest,
		ProviderCall: providerCall, Operation: operation, Transition: transition,
		ReceiptDigest: receipt.receiptDigest, Receipt: append(json.RawMessage(nil), receipt.receiptSnapshot...),
	})
}

func validateProductionD09OperationBundle(bundle sessionrepo.EvidenceBundle, transition delta.Transition,
	operation sessionrepo.OperationEvidence, receipt piruntime.D09CommandReceipt) error {
	if operation.Tool != "bash" || operation.Operation.Kind != delta.OperationLease || operation.Operation.LeaseID != receipt.LeaseID ||
		operation.ViewID != receipt.ViewID || operation.ArgumentDigest != receipt.CommandSpecDigest || operation.ExitCode == nil ||
		*operation.ExitCode != receipt.ExitCode || operation.CommandContainmentStatus != "not-established" ||
		operation.CommandRunnerProfile != piruntime.D09CommandRunnerProfile || operation.Outcome != "success" ||
		operation.EffectID == "" || operation.AuditSequence == 0 || !validDigestText(operation.ResultDigest) ||
		!validManagedTreeDigest(operation.InputTreeDigest) || !validManagedTreeDigest(operation.OutputTreeDigest) ||
		operation.InputGeneration != transition.InputGeneration || operation.InputTreeDigest != transition.InputTreeDigest ||
		operation.OutputGeneration != transition.OutputGeneration || operation.OutputTreeDigest != transition.OutputTreeDigest ||
		operation.Operation != transition.Operation || operation.DecisionID != transition.Decision.ID ||
		operation.ArgumentDigest != transition.ArgumentDigest ||
		operation.ViewID != transition.ViewID || operation.ExecutionContextDigest != transition.ExecutionContextDigest ||
		operation.ExecutionContextDigest != governedExecutionContextDigest(operation.ViewID, operation.InputTreeDigest, receipt.LeaseID) ||
		transition.Tool != "bash" || transition.ID == "" || transition.Sequence == 0 ||
		transition.OutputGeneration == transition.InputGeneration || len(bundle.AuditJournal) == 0 {
		return ErrProductionCommandEvidence
	}
	trace, err := audit.Verify(bytes.NewReader(bundle.AuditJournal))
	if err != nil || uint64(len(trace.Records)) != bundle.AuditRecordCount || auditHeadHash(trace.Records) != bundle.AuditHeadHash {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	var effect *audit.EffectTrace
	for index := range trace.Effects {
		candidate := &trace.Effects[index]
		if candidate.ID != operation.EffectID {
			continue
		}
		if effect != nil {
			return ErrProductionCommandEvidence
		}
		effect = candidate
	}
	if effect == nil || effect.Unresolved || effect.Outcome != "success" || effect.OutcomeSequence != operation.AuditSequence ||
		digestBytes(effect.Result) != operation.ResultDigest {
		return ErrProductionCommandEvidence
	}
	var commandResult productionD09CommandEffectResult
	if err := unmarshalGovernedJSON(effect.Result, &commandResult); err != nil {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	commandBytes, commandErr := json.Marshal(commandResult.Command)
	settlementBytes, settlementErr := json.Marshal(commandResult.Settlement)
	specBytes, specErr := json.Marshal(commandResult.Spec)
	if commandErr != nil || settlementErr != nil || specErr != nil ||
		!reflect.DeepEqual(commandResult.Transition, transition) || commandResult.RunnerProfile != piruntime.D09CommandRunnerProfile ||
		commandResult.Command.ExitCode != receipt.ExitCode || !commandResult.Command.ExitObserved ||
		digestBytes(commandBytes) != receipt.ResultDigest || digestBytes(settlementBytes) != receipt.SettlementDigest ||
		digestBytes(specBytes) != receipt.CommandSpecDigest || commandResult.Settlement != receipt.Settlement {
		return errors.Join(ErrProductionCommandEvidence, errors.Join(commandErr, settlementErr, specErr))
	}
	matchedOperation := 0
	for _, candidate := range bundle.Operations {
		if candidate.EffectID == operation.EffectID {
			if !reflect.DeepEqual(candidate, operation) {
				return ErrProductionCommandEvidence
			}
			matchedOperation++
		}
	}
	matchedTransition := 0
	for _, candidate := range bundle.ApprovedDeltaLedger {
		if candidate.ID == transition.ID {
			if !reflect.DeepEqual(candidate, transition) {
				return ErrProductionCommandEvidence
			}
			matchedTransition++
		}
	}
	if matchedOperation != 1 || matchedTransition != 1 {
		return ErrProductionCommandEvidence
	}
	return nil
}

func (r *productionD09BashRunner) VerifyProductionBashEvidence(ctx context.Context, operation sessionrepo.OperationEvidence) error {
	if r == nil || ctx == nil || operation.ViewID == "" {
		return ErrProductionCommandEvidence
	}
	r.mu.Lock()
	retained, exists := r.evidence[operation.ViewID]
	r.mu.Unlock()
	if !exists {
		return ErrProductionCommandEvidence
	}
	return r.verifyProductionBashEvent(ctx, retained, operation, retained.transition, retained.providerCall, sessionrepo.EvidenceBundle{}, audit.Record{})
}

func (r *productionD09BashRunner) VerifyProductionBashEvidenceWithOrigin(ctx context.Context,
	bundle sessionrepo.EvidenceBundle, providerCall delta.OperationIdentity, transition delta.Transition,
	operation sessionrepo.OperationEvidence) error {
	if r == nil || ctx == nil || operation.ViewID == "" || len(bundle.AuditJournal) == 0 {
		return ErrProductionCommandEvidence
	}
	r.mu.Lock()
	retained, exists := r.evidence[operation.ViewID]
	r.mu.Unlock()
	if !exists {
		return ErrProductionCommandEvidence
	}
	return r.verifyProductionBashEvent(ctx, retained, operation, transition, providerCall, bundle, audit.Record{})
}

func (r *productionD09BashRunner) verifyProductionBashEvent(ctx context.Context, retained productionD09RunEvidence,
	operation sessionrepo.OperationEvidence, transition delta.Transition, providerCall delta.OperationIdentity,
	bundle sessionrepo.EvidenceBundle, expected audit.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if retained.journalSeq == 0 || retained.eventID == "" || retained.receiptDigest == "" ||
		!reflect.DeepEqual(operation, retained.operation) || !reflect.DeepEqual(transition, retained.transition) ||
		!reflect.DeepEqual(providerCall, retained.providerCall) || operation.ViewID != retained.viewID ||
		operation.Operation.Kind != delta.OperationLease || operation.Operation.LeaseID != retained.leaseID ||
		operation.ArgumentDigest != retained.receipt.CommandSpecDigest || operation.ExitCode == nil ||
		*operation.ExitCode != retained.receipt.ExitCode || operation.CommandContainmentStatus != "not-established" ||
		operation.CommandRunnerProfile != piruntime.D09CommandRunnerProfile || retained.receipt.LeaseID != retained.leaseID ||
		retained.receipt.ViewID != retained.viewID || retained.receipt.ReceiptDigest != retained.receiptDigest {
		return ErrProductionCommandEvidence
	}
	if err := retained.receipt.Validate(); err != nil {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	encodedReceipt, err := json.Marshal(retained.receipt)
	if err != nil || !bytes.Equal(encodedReceipt, retained.receiptSnapshot) {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	var snapshot piruntime.D09CommandReceipt
	if err := unmarshalGovernedJSON(retained.receiptSnapshot, &snapshot); err != nil {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	if err := snapshot.ValidateSnapshot(); err != nil || snapshot.ReceiptDigest != retained.receiptDigest {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	if bundle.AuditJournal != nil {
		if err := validateProductionD09OperationBundle(bundle, transition, operation, retained.receipt); err != nil {
			return err
		}
	}
	trace, err := r.journal.Trace()
	if err != nil {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	var matched *audit.Record
	for index := range trace.Records {
		candidate := &trace.Records[index]
		if candidate.Event.Kind != "production_d09_command_settled" || candidate.Event.ID != retained.eventID {
			continue
		}
		if matched != nil {
			return ErrProductionCommandEvidence
		}
		matched = candidate
	}
	if err := validateProductionD09JournalRecord(matched, retained, expected); err != nil {
		return ErrProductionCommandEvidence
	}
	var event productionD09CommandSettledEvent
	if err := unmarshalGovernedJSON(matched.Event.Data, &event); err != nil {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	if err := compareProductionD09CommandEvent(r, event, retained, providerCall, operation, transition); err != nil {
		return ErrProductionCommandEvidence
	}
	return nil
}

func validateProductionD09JournalRecord(record *audit.Record, retained productionD09RunEvidence, expected audit.Record) error {
	if record == nil || record.Event.Kind != "production_d09_command_settled" || record.Event.ID != retained.eventID ||
		record.Sequence == 0 || record.Sequence != retained.journalSeq || record.Hash == "" || record.Hash != retained.journalHash ||
		expected.Sequence != 0 && (record.Sequence != expected.Sequence || record.Hash != expected.Hash) {
		return ErrProductionCommandEvidence
	}
	return nil
}

// compareProductionD09CommandEvent compares a decoded journal payload to
// already-live validated receipt/operation identities. It is deliberately not
// an authority on its own; production calls it only after receipt.Validate and
// the Store-bundle audit checks succeed.
func compareProductionD09CommandEvent(r *productionD09BashRunner, event productionD09CommandSettledEvent,
	retained productionD09RunEvidence, providerCall delta.OperationIdentity,
	operation sessionrepo.OperationEvidence, transition delta.Transition) error {
	if r == nil || event.SchemaVersion != "tbound-production-d09-command-settled/v2" || event.RunID != r.runID ||
		event.SessionID != r.sessionID || event.WorkflowID != r.workflowID || event.ConversationID != r.conversationID ||
		event.ProfileID != r.profile.ID || event.ProfileDigest != r.profile.Digest ||
		!reflect.DeepEqual(event.ProviderCall, providerCall) || !reflect.DeepEqual(event.Operation, operation) ||
		!reflect.DeepEqual(event.Transition, transition) || event.ReceiptDigest != retained.receiptDigest ||
		!bytes.Equal(event.Receipt, retained.receiptSnapshot) {
		return ErrProductionCommandEvidence
	}
	return nil
}

type productionCommandLease struct {
	leaseID string
	runner  sessionrepo.CommandRunner
	running bool
}

type productionCommandRunner struct {
	mu              sync.Mutex
	factory         productionCommandRunnerFactory
	leases          map[string]productionCommandLease
	receipts        map[string]sessionrepo.CommandSettlement
	settledReceipts map[string]sessionrepo.CommandSettlement
	evidenceRunners map[string]sessionrepo.CommandRunner
	settled         int
}

// newProductionCommandRunner accepts only a per-lease factory. RegisterLease
// constructs a fresh signed D09CommandRunner for the exact authenticated lease;
// a missing factory or a profile-only/legacy runner fails before command launch.
func newProductionCommandRunner(factory productionCommandRunnerFactory) *productionCommandRunner {
	return &productionCommandRunner{
		factory: factory, leases: make(map[string]productionCommandLease),
		receipts:        make(map[string]sessionrepo.CommandSettlement),
		settledReceipts: make(map[string]sessionrepo.CommandSettlement),
		evidenceRunners: make(map[string]sessionrepo.CommandRunner),
	}
}

func (r *productionCommandRunner) Profile() string {
	if r == nil || r.factory == nil {
		return "production-d09-bash-runner-unavailable"
	}
	return piruntime.D09CommandRunnerProfile
}

func (r *productionCommandRunner) RegisterLease(viewID, leaseID string) error {
	if r == nil || r.factory == nil {
		return ErrProductionBashRunnerUnavailable
	}
	if viewID == "" || leaseID == "" {
		return errors.New("production command runner requires a view and lease identity")
	}
	r.mu.Lock()
	if _, duplicate := r.leases[viewID]; duplicate {
		r.mu.Unlock()
		return errors.New("production command runner already registered this view")
	}
	r.mu.Unlock()
	signed, err := r.factory(leaseID)
	if err != nil {
		return errors.Join(ErrProductionBashRunnerUnavailable, err)
	}
	if signed == nil {
		return ErrProductionBashRunnerUnavailable
	}
	value := reflect.ValueOf(signed)
	if (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) && value.IsNil() {
		return ErrProductionBashRunnerUnavailable
	}
	profile, hasProfile := signed.(sessionrepo.CommandRunnerProfile)
	_, hasVerifier := signed.(productionBashEvidenceVerifier)
	if signed == nil || !hasProfile || !hasVerifier || profile.Profile() != piruntime.D09CommandRunnerProfile {
		return ErrProductionBashRunnerUnavailable
	}
	if _, exact := signed.(*productionD09BashRunner); !exact {
		return ErrProductionBashRunnerUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, duplicate := r.leases[viewID]; duplicate {
		return errors.New("production command runner raced a duplicate view registration")
	}
	r.leases[viewID] = productionCommandLease{leaseID: leaseID, runner: signed}
	return nil
}

func (r *productionCommandRunner) Run(ctx context.Context, view *sessionrepo.CommandView, spec sessionrepo.CommandSpec) (sessionrepo.CommandResult, sessionrepo.CommandSettlement, error) {
	if r == nil || r.factory == nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, ErrProductionBashRunnerUnavailable
	}
	if view == nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.New("production command runner requires a command view")
	}
	r.mu.Lock()
	lease, exists := r.leases[view.ID()]
	if !exists || lease.running {
		r.mu.Unlock()
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.New("production command runner has no idle registered lease for the view")
	}
	lease.running = true
	r.leases[view.ID()] = lease
	r.mu.Unlock()
	result, settlement, err := lease.runner.Run(ctx, view, spec)
	if err != nil {
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, err
	}
	r.mu.Lock()
	if _, duplicate := r.receipts[view.ID()]; duplicate {
		r.mu.Unlock()
		return sessionrepo.CommandResult{}, sessionrepo.CommandSettlement{}, errors.New("production command runner produced a duplicate settlement")
	}
	r.receipts[view.ID()] = settlement
	r.evidenceRunners[view.ID()] = lease.runner
	r.mu.Unlock()
	return result, settlement, nil
}

// Settlements returns the number of one-use command-cell settlements observed.
func (r *productionCommandRunner) Settlements() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.settled
}

// Pending returns the number of registered leases that do not yet have a
// verified one-use settlement plus any retained-but-unverified receipts. A
// non-zero value means command writers are not proven stopped.
func (r *productionCommandRunner) Pending() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.leases) + len(r.receipts) + len(r.settledReceipts)
}

func (r *productionCommandRunner) VerifySettlement(candidate sessionrepo.CommandSettlement, viewID, leaseID string) error {
	if r == nil {
		return errors.New("production command runner is not configured")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	actual, exists := r.receipts[viewID]
	lease, leaseExists := r.leases[viewID]
	if !exists || !leaseExists || lease.leaseID != leaseID || actual.LeaseID != leaseID || actual.ViewID != viewID ||
		!actual.ExitObserved || !actual.ProcessScopeEmpty || !actual.WritersStopped || !actual.MountDetached ||
		!strings.HasPrefix(actual.EvidenceClass, piruntime.D09CommandRunnerProfile+":") || !settlementMatches(actual, candidate) {
		return errors.New("production command settlement does not match the retained one-use receipt")
	}
	delete(r.receipts, viewID)
	r.settledReceipts[viewID] = actual
	return nil
}

func (r *productionCommandRunner) BindProductionBashEvidence(ctx context.Context, providerCall delta.OperationIdentity,
	bundle sessionrepo.EvidenceBundle, transition delta.Transition, operation sessionrepo.OperationEvidence) error {
	if r == nil || ctx == nil || operation.ViewID == "" || operation.Operation.Kind != delta.OperationLease {
		return ErrProductionCommandEvidence
	}
	r.mu.Lock()
	lease, leaseExists := r.leases[operation.ViewID]
	settlement, settlementExists := r.settledReceipts[operation.ViewID]
	signed := r.evidenceRunners[operation.ViewID]
	if !leaseExists || !settlementExists || signed == nil || lease.leaseID != operation.Operation.LeaseID ||
		settlement.LeaseID != lease.leaseID || settlement.ViewID != operation.ViewID {
		r.mu.Unlock()
		return ErrProductionCommandEvidence
	}
	r.mu.Unlock()
	binder, ok := signed.(productionBashEvidenceBinder)
	if !ok {
		return ErrProductionCommandEvidence
	}
	if err := binder.BindProductionBashEvidence(ctx, providerCall, bundle, transition, operation); err != nil {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	currentLease, leaseStillExists := r.leases[operation.ViewID]
	currentSettlement, settlementStillExists := r.settledReceipts[operation.ViewID]
	if !leaseStillExists || !settlementStillExists || currentLease.leaseID != lease.leaseID ||
		!settlementMatches(currentSettlement, settlement) || r.evidenceRunners[operation.ViewID] != signed {
		return ErrProductionCommandEvidence
	}
	delete(r.leases, operation.ViewID)
	delete(r.settledReceipts, operation.ViewID)
	r.settled++
	return nil
}

// VerifyProductionBashEvidence re-verifies a completed D09 per-command proof
// against the exact OperationEvidence produced after the Store's durable result.
// The schema's generic containment marker remains not-established; only the
// registered signed D09 runner's captured proof can authenticate the command.
func (r *productionCommandRunner) VerifyProductionBashEvidence(ctx context.Context, operation sessionrepo.OperationEvidence) error {
	if r == nil || ctx == nil || operation.Tool != "bash" || operation.Operation.Kind != delta.OperationLease ||
		operation.Operation.LeaseID == "" || operation.ViewID == "" || operation.ExitCode == nil ||
		operation.CommandContainmentStatus != "not-established" ||
		operation.CommandRunnerProfile != piruntime.D09CommandRunnerProfile {
		return ErrProductionCommandEvidence
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	signed := r.evidenceRunners[operation.ViewID]
	r.mu.Unlock()
	if signed == nil {
		return ErrProductionCommandEvidence
	}
	verifier, ok := signed.(productionBashEvidenceVerifier)
	if !ok {
		return ErrProductionCommandEvidence
	}
	if err := verifier.VerifyProductionBashEvidence(ctx, operation); err != nil {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	return nil
}

func (r *productionCommandRunner) VerifyProductionBashEvidenceWithOrigin(ctx context.Context,
	bundle sessionrepo.EvidenceBundle, providerCall delta.OperationIdentity, transition delta.Transition,
	operation sessionrepo.OperationEvidence) error {
	if err := r.VerifyProductionBashEvidence(ctx, operation); err != nil {
		return err
	}
	r.mu.Lock()
	signed := r.evidenceRunners[operation.ViewID]
	r.mu.Unlock()
	verifier, ok := signed.(interface {
		VerifyProductionBashEvidenceWithOrigin(context.Context, sessionrepo.EvidenceBundle, delta.OperationIdentity,
			delta.Transition, sessionrepo.OperationEvidence) error
	})
	if !ok {
		return ErrProductionCommandEvidence
	}
	if err := verifier.VerifyProductionBashEvidenceWithOrigin(ctx, bundle, providerCall, transition, operation); err != nil {
		return errors.Join(ErrProductionCommandEvidence, err)
	}
	return nil
}

func settlementMatches(actual, candidate sessionrepo.CommandSettlement) bool {
	return actual.LeaseID == candidate.LeaseID && actual.ViewID == candidate.ViewID &&
		actual.ExitObserved == candidate.ExitObserved && actual.ExitCode == candidate.ExitCode &&
		actual.ProcessScopeEmpty == candidate.ProcessScopeEmpty && actual.WritersStopped == candidate.WritersStopped &&
		actual.MountDetached == candidate.MountDetached && actual.EvidenceClass == candidate.EvidenceClass
}

func validDigestText(value string) bool {
	const prefix = "sha256:"
	if len(value) != len(prefix)+sha256.Size*2 || !strings.HasPrefix(value, prefix) {
		return false
	}
	encoded := value[len(prefix):]
	if encoded != strings.ToLower(encoded) {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}
