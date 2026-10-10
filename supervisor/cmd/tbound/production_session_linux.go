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
	"strings"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
)

// This file is the concrete production composition root for `tbound serve --pi`
// on a provisioned Linux host. It is owned by the CLI composition (cmd/tbound).
//
// It deliberately separates two things that must never be conflated:
//
//   - the CLI-side trust decision (protected root-owned signed profile, fixed
//     admin public key, registered D09 cell template binding) which lives here;
//   - the sealed runtime bindings (D06 worker exposure, read-only runtime-bundle
//     view, D09 descriptor-role/FD mapping and cgroup scope) which are produced
//     by the runtime/session composition owners and consumed here through the
//     narrow productionRuntimeBinder seam.
//
// The CLI never infers trust from a path, a boolean, a callback returning nil,
// a worker observation, or a self-signed operator profile. When the sealed
// bindings are absent it refuses with the exact typed prerequisite list rather
// than returning a generic composition-unavailable error or launching a
// half-wired worker.

const (
	productionTaskIDMaxBytes    = 256
	productionRequestMaxBytes   = 256
	productionPromptMaxBytes    = 1 << 16
	productionReceiptMaxBytes   = 64 << 10
	productionSessionMaxRuntime = 20 * time.Minute
)

// ErrProductionSealedRuntime marks the one remaining composition boundary. It is
// not a success path and never substitutes for a real launch.
var ErrProductionSealedRuntime = errors.New("production Pi sealed runtime bindings are not available")

// ProductionPrerequisiteError enumerates the exact trusted bindings a
// provisioned launch still requires. A missing binding is a refusal, never a
// fallback.
type ProductionPrerequisiteError struct {
	Missing []string
}

func (e *ProductionPrerequisiteError) Error() string {
	if e == nil || len(e.Missing) == 0 {
		return ErrProductionSealedRuntime.Error()
	}
	return ErrProductionSealedRuntime.Error() + ": missing " + strings.Join(e.Missing, "; ")
}

func (e *ProductionPrerequisiteError) Unwrap() error { return ErrProductionSealedRuntime }

// productionServeConfig is bound, plain CLI input. It carries no credential,
// endpoint, model, verification key, or profile-directory trust hint.
type productionServeConfig struct {
	ProfilePath   string
	Workspace     string
	Source        string
	TaskID        string
	TaskRequestID string
	TaskPrompt    string
}

func (c productionServeConfig) validate() error {
	if strings.TrimSpace(c.ProfilePath) != "" && !filepath.IsAbs(c.ProfilePath) {
		return errors.New("--native-host-profile must be an absolute protected path")
	}
	for name, path := range map[string]string{"--workspace": c.Workspace, "--source": c.Source} {
		if strings.TrimSpace(path) != "" && !filepath.IsAbs(path) {
			return fmt.Errorf("%s must be an absolute path", name)
		}
	}
	if c.TaskID != "" && !boundedProductionIdentity(c.TaskID, productionTaskIDMaxBytes) {
		return errors.New("--task-id must be bounded non-empty text")
	}
	if c.TaskRequestID != "" && !boundedProductionIdentity(c.TaskRequestID, productionRequestMaxBytes) {
		return errors.New("--task-request-id must be bounded non-empty text")
	}
	if c.TaskPrompt != "" && !boundedProductionPrompt(c.TaskPrompt) {
		return errors.New("--task must be bounded non-empty plain text")
	}
	return nil
}

// productionRuntimeBinder is the single seam owned by the trusted runtime and
// session composition owners (Runtime A: Podman/cgroup/entrypoint launcher and
// FD-role mapping; View B: sealed D06 exposure and runtime-bundle assets). It
// returns only already-verified structures; the CLI performs no trust inference
// over them and re-verifies the complete binding before launch.
type productionRuntimeBinder interface {
	BindProductionPiSession(ctx context.Context, config productionServeConfig, profile piruntime.HostProfile) (*productionPiSession, error)
}

// productionPiSession is the fully composed, already-verified production
// session. Every handle here is owned by the trusting runtime composition; the
// CLI merely consumes it, re-checks it through piruntime admission, and drives
// the governed loops.
type productionPiSession struct {
	Profile     piruntime.HostProfile
	D09Template piruntime.D09CellTemplate
	Bindings    piruntime.HostRuntimeBindings
	Channels    *piruntime.PiWorkerChannels

	Authority    *SessionOperationAuthority
	Prepared     *sessionlaunch.PreparedSession
	Store        *sessionrepo.Store
	Generation   *sessionrepo.Generation
	ManagedStore *sessionlaunch.ManagedStore
	Lifecycle    *productionLifecycleEvidence

	StoreJournal    *audit.Journal
	ProviderJournal *audit.Journal
	CommandJournal  *audit.Journal

	Policy                  gate.Policy
	CallIssuer              string
	StorePolicyDigest       string
	MetadataPolicyDigest    string
	CommandRunner           sessionrepo.CommandRunner
	ProductionCommandRunner *productionCommandRunner

	SessionID                   string
	RunID                       string
	WorkflowID                  string
	ConversationID              string
	ProviderProfileID           string
	SystemPrompt                string
	DeveloperPrompt             string
	offlineProviderConversation *providerbridge.Conversation // integration-test fixture only; never set by CLI parsing

	// Publication finalizes the registered session after settlement. The binder
	// supplies the protected live root, publication state root/journal, owner
	// epoch, and workspace so the CLI can construct the real Finalizer; when the
	// publication outcome is not a verified SUCCEEDED the receipt must not claim
	// publication.
	Publication          *PublicationRuntime
	LiveRoot             *os.File
	ManagedLiveRootLease *managedLiveRootLease
	PublicationStateRoot *os.File
	PublicationJournal   *audit.Journal
	OwnerEpoch           uint64
	PublicationID        string
	Workspace            workspace.Options

	// Close releases every handle in reverse acquisition order. It must be
	// idempotent.
	Close func() error
}

func (s *productionPiSession) close() error {
	if s == nil || s.Close == nil {
		return nil
	}
	return s.Close()
}

type authorityPromptAdmission struct {
	authority    *SessionOperationAuthority
	conversation *providerbridge.Conversation
	durable      *DurableExecutor
}

func (a authorityPromptAdmission) AdmitPrompt(taskID, prompt string) error {
	if a.authority == nil || a.conversation == nil || a.durable == nil {
		return ErrGovernedSessionEvidence
	}
	return a.authority.AdmitPrompt(a.conversation, a.durable, taskID, prompt)
}

func (a authorityPromptAdmission) Close() {
	if a.conversation != nil {
		a.conversation.Close()
	}
}

// productionPiReceipt is the bounded, non-secret disposition of one production
// launch. It records only observed launch/lifecycle facts; success is never
// claimed before a verified STOPPED settlement.
type productionPiReceipt struct {
	SchemaVersion            string   `json:"schema_version"`
	Mode                     string   `json:"mode"`
	RunID                    string   `json:"run_id"`
	ProfileID                string   `json:"profile_id"`
	ProfileHash              string   `json:"profile_hash"`
	ContainmentProfile       string   `json:"containment_profile"`
	ContainmentProfileDigest string   `json:"containment_profile_digest"`
	FrozenSourceDigest       string   `json:"frozen_source_digest"`
	ProviderProfile          string   `json:"provider_profile"`
	ProviderExchange         bool     `json:"provider_exchange"`
	ClaimBearing             bool     `json:"claim_bearing"`
	WorkerReady              bool     `json:"worker_ready"`
	TaskAdmitted             bool     `json:"task_admitted"`
	TurnCompleted            bool     `json:"turn_completed"`
	ProposalCount            uint64   `json:"proposal_count"`
	ToolSequence             []string `json:"tool_sequence"`
	ProviderTurns            int      `json:"provider_turns"`
	SourceGeneration         string   `json:"source_generation"`
	FinalGeneration          string   `json:"final_generation"`
	GenerationCount          int      `json:"generation_count"`
	Settlement               string   `json:"settlement"`
	ExitObserved             bool     `json:"exit_observed"`
	ExitCode                 int      `json:"exit_code"`
	DescendantsSettled       bool     `json:"descendants_settled"`
	CommandRunnerProfile     string   `json:"command_runner_profile"`
	CommandCellSettlements   int      `json:"command_cell_settlement_count"`
	TCBProofDigest           string   `json:"tcb_proof_digest,omitempty"`
	TCBProofJournalSequence  uint64   `json:"tcb_proof_journal_sequence,omitempty"`
	PublicationStatus        string   `json:"publication_status"`
	Disposition              string   `json:"disposition"`
	PublicationTransactionID string   `json:"publication_transaction_id,omitempty"`
	Success                  bool     `json:"success"`
}

// runProductionPiServe performs the CLI-side production trust decision and then
// drives the composed session. It refuses, with the exact missing trusted
// binding names, when the sealed runtime composition is not provided. It never
// starts a worker, reads credentials, or reaches the network before every
// protected profile, template, and binding check has passed.
func runProductionPiServe(ctx context.Context, output io.Writer, config productionServeConfig, binder productionRuntimeBinder) (resultErr error) {
	if ctx == nil {
		return errors.New("production Pi serve requires a context")
	}
	ctx, cancel := context.WithTimeout(ctx, productionSessionMaxRuntime)
	defer cancel()
	if err := config.validate(); err != nil {
		return err
	}

	// 1. Registered D09 cell template selects the frozen image/entrypoint/limits.
	//    The conservative default only surfaces the missing prerequisites.
	launcherConfig, templateDigest, templateErr := loadProductionD09Config(ctx)
	launcher := piruntime.NewD09PodmanCrunLauncher(launcherConfig)
	if _, err := launcher.Probe(ctx); err != nil {
		return fmt.Errorf("governed Pi D09 prerequisites are not satisfied: %w", err)
	}
	if templateErr != nil {
		return fmt.Errorf("governed Pi admission refused: %w", templateErr)
	}

	// 2. Protected signed host profile. The verification key is always read from
	//    the fixed root-owned trust root, so --native-host-profile cannot choose
	//    its own trust root and a self-signed user profile is refused.
	profile, err := piruntime.LoadLinuxSignedHostProfile(config.ProfilePath)
	if err != nil {
		return fmt.Errorf("load and verify signed native Pi host profile: %w", err)
	}

	// 3. Bind the registered template digest into the signed profile identity so
	//    no D09 identity can be switched after verification.
	if profile.ContainmentProfile != piruntime.D09ContainmentProfile || profile.ContainmentProfileDigest != templateDigest {
		return fmt.Errorf("governed Pi admission refused: registered D09 cell template digest does not match the signed profile: %w", piruntime.ErrProductionLauncherUnavailable)
	}

	// 4. The concrete production binder is always supplied by the CLI entry
	//    point; a nil binder is a programming error, never a production refusal.
	if binder == nil {
		return errors.New("production Pi serve requires a concrete runtime binder")
	}
	if err := requireProductionLaunchInputs(config); err != nil {
		return err
	}

	session, err := binder.BindProductionPiSession(ctx, config, profile)
	if err != nil {
		return fmt.Errorf("compose production Pi session: %w", err)
	}
	if session == nil {
		return errors.New("production runtime binder returned no composed session")
	}
	defer func() {
		if closeErr := session.close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close composed production session: %w", closeErr))
		}
	}()
	if session.Profile != profile {
		return fmt.Errorf("%w: composed session profile differs from the verified signed profile", piruntime.ErrHostProfileMismatch)
	}

	return runComposedProductionPiSession(ctx, output, config, session)
}

// loadProductionD09Config returns the launcher configuration plus the canonical
// registered template digest. A missing/invalid registered template is returned
// as templateErr while the conservative default is still used to surface host
// prerequisites.
func loadProductionD09Config(ctx context.Context) (piruntime.D09Config, string, error) {
	config := piruntime.DefaultD09Config(piruntime.LinuxHostProfileProtectedPublicKeyPath)
	template, digest, err := piruntime.LoadD09CellTemplate(ctx)
	if err == nil {
		return template.Config(), digest, nil
	}
	return config, "", err
}

func productionMissingBindings() []string {
	return []string{
		"sealed D06 worker exposure adapter (sessionlaunch/GovComposition owner)",
		"read-only Pi runtime-bundle asset view (runtime bundle owner)",
		"verified delegated cgroup-v2 scope and D09 descriptor-role/FD mapping (Podman/containment owner)",
	}
}

func requireProductionLaunchInputs(config productionServeConfig) error {
	if strings.TrimSpace(config.Source) == "" || !filepath.IsAbs(config.Source) {
		return errors.New("production launch requires an absolute --source protected read-only snapshot")
	}
	if strings.TrimSpace(config.TaskID) == "" {
		return errors.New("production launch requires --task-id (registered, host-admitted task)")
	}
	if !boundedProductionIdentity(config.TaskRequestID, productionRequestMaxBytes) {
		return errors.New("production launch requires a bounded non-empty --task-request-id")
	}
	if !boundedProductionPrompt(config.TaskPrompt) {
		return errors.New("production launch requires a bounded non-empty --task prompt")
	}
	return nil
}

// runComposedProductionPiSession re-verifies the composed session through the
// opaque production admission path, launches the admitted real Pi worker, and
// drives the governed provider/supervisor loops to a settled disposition.
func runComposedProductionPiSession(ctx context.Context, output io.Writer, config productionServeConfig, session *productionPiSession) (resultErr error) {
	if session.Bindings.WorkerExposure == nil || session.Bindings.WorkerRuntimeAssets == nil || session.Channels == nil ||
		session.Authority == nil || session.Store == nil || session.Generation == nil || session.LiveRoot == nil ||
		session.ManagedLiveRootLease == nil {
		return &ProductionPrerequisiteError{Missing: productionMissingBindings()}
	}

	verifier, err := piruntime.OpenLinuxHostProfileVerifier(session.Bindings)
	if err != nil {
		return fmt.Errorf("open trusted production host-profile verifier: %w", err)
	}
	capability, err := piruntime.AdmitProduction(ctx, session.Profile, verifier)
	if err != nil {
		return fmt.Errorf("admit production Pi host profile: %w", err)
	}

	bootstrap := piruntime.PiWorkerBootstrap{
		SessionID:      session.SessionID,
		WorkflowID:     session.WorkflowID,
		ConversationID: session.ConversationID,
	}
	session.Lifecycle.markWorkerLaunchAttempted()
	worker, err := piruntime.LaunchPiSDKWorker(ctx, capability, session.Bindings.WorkerExposure,
		session.Bindings.WorkerRuntimeAssets, session.Channels, bootstrap)
	if err != nil {
		return fmt.Errorf("launch admitted production Pi worker: %w", err)
	}
	session.Lifecycle.markWorkerLaunched(worker.PID())
	var settlement piruntime.Settlement
	var workerCloseErr error
	workerClosed := false
	closeWorker := func() piruntime.Settlement {
		if workerClosed {
			return settlement
		}
		workerClosed = true
		closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		settlement, workerCloseErr = worker.Close(closeCtx)
		session.Lifecycle.markWorkerClosed(settlement, workerCloseErr)
		return settlement
	}
	defer func() { _ = closeWorker() }()

	// Journal the actual entrypoint proof for this exact worker before task
	// admission/control fd0 and therefore before any provider or tool effect.
	launchProof, proofOK := worker.D09LaunchProof()
	if !proofOK {
		settlement = closeWorker()
		return errors.Join(errors.New("production D09 worker has no captured launch TCB proof"), workerCloseErr)
	}
	if err := journalProductionLaunchProof(session.StoreJournal, session.Lifecycle, session.RunID,
		session.SessionID, session.WorkflowID, session.ConversationID, session.Generation.ID(),
		session.Prepared.Provenance().BindingDigest, session.Profile, session.D09Template, worker.PID(), launchProof); err != nil {
		settlement = closeWorker()
		return errors.Join(fmt.Errorf("journal production D09 launch proof before task release: %w", err), workerCloseErr)
	}

	conversation := session.offlineProviderConversation
	providerExchange := conversation == nil
	if conversation == nil {
		conversation, err = session.Authority.NewProviderConversation(providerbridge.Config{
			ConversationID:  session.ConversationID,
			WorkflowID:      session.WorkflowID,
			ProfileID:       session.ProviderProfileID,
			SystemPrompt:    session.SystemPrompt,
			DeveloperPrompt: session.DeveloperPrompt,
		})
		if err != nil {
			return fmt.Errorf("construct production provider conversation: %w", err)
		}
	} else {
		session.Authority.mu.Lock()
		ownedConversation := session.Authority.conversation
		session.Authority.mu.Unlock()
		if ownedConversation != conversation {
			return errors.New("offline test conversation is not bound to the production session authority")
		}
	}
	durable, err := NewDurableExecutor(DurableExecutorConfig{
		Store: session.Store, Tip: session.Generation, CallIssuer: session.CallIssuer,
		PolicyDigest: session.StorePolicyDigest, MetadataPolicyDigest: session.MetadataPolicyDigest,
		CommandRunner: session.CommandRunner,
	})
	if err != nil {
		return fmt.Errorf("construct production durable executor: %w", err)
	}
	publicationRuntime, err := session.buildPublicationRuntime(durable)
	if err != nil {
		return fmt.Errorf("construct production publication runtime: %w", err)
	}
	session.Publication = publicationRuntime
	previousClose := session.Close
	session.Close = func() error {
		if !productionCleanupSafe(session.Lifecycle.snapshot(), session.ProductionCommandRunner) {
			// Do not release the publication live-root owner or any session/cgroup
			// handles while a child, loop, or command scope may still be active.
			return ErrProductionLifecycleUnsettled
		}
		lifecycle := session.Lifecycle.snapshot()
		if !lifecycle.taskAdmitted && session.ManagedLiveRootLease != nil {
			if err := session.ManagedLiveRootLease.abortBeforeTask(lifecycle, session.ProductionCommandRunner); err != nil {
				return err
			}
		}
		if session.ManagedLiveRootLease == nil || !session.ManagedLiveRootLease.canRelease() {
			return ErrManagedLiveRootRetained
		}
		var publicationCloseErr error
		if session.Publication != nil {
			publicationCloseErr = session.Publication.Close()
		}
		if publicationCloseErr != nil {
			return publicationCloseErr
		}
		if previousClose != nil {
			return previousClose()
		}
		return publicationCloseErr
	}
	releaseSessionAdmission, err := publicationRuntime.BeginSession(ctx)
	if err != nil {
		return fmt.Errorf("recover publication lifecycle and activate session admission before Pi task: %w", err)
	}
	defer releaseSessionAdmission()
	if err := session.ManagedLiveRootLease.completeAdmissionRecovery(ctx); err != nil {
		return fmt.Errorf("reconcile prior managed-root owner epochs after RootLedger recovery and before Pi task: %w", err)
	}
	supervisor, err := NewGovernedSupervisor(session.Channels.IPCServer(), conversation, session.Authority,
		session.Policy, durable)
	if err != nil {
		return fmt.Errorf("construct governed production supervisor: %w", err)
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	// The registered task is durably admitted in Go BEFORE any control byte
	// reaches the worker's control descriptor (fd0): SendAdmittedPrompt calls the
	// owner Conversation's AdmitPrompt (which appends canonical history) and only
	// then writes the control frame. The SessionOperationAuthority.AdmitPrompt
	// host-control variant must NOT also be applied here, because
	// Conversation.AdmitPrompt is single-use and would refuse a double admission.
	channelsDone := make(chan error, 1)
	session.Lifecycle.markChannelsStarted()
	go func() {
		channelsDone <- RunGovernedChannels(runCtx, supervisor, session.Channels.ProviderChannel(), conversation)
	}()
	channelsJoined := false
	defer func() {
		if channelsJoined {
			return
		}
		cancelRun()
		_ = closeWorker()
		if workerCloseErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("settle production Pi worker during cancellation: %w", workerCloseErr))
		}
		_ = session.Channels.ProviderChannel().Close()
		select {
		case loopErr := <-channelsDone:
			channelsJoined = true
			session.Lifecycle.markChannelsJoined(loopErr)
			if loopErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("join canceled governed production loops: %w", loopErr))
			}
		case <-time.After(30 * time.Second):
			resultErr = errors.Join(resultErr, errors.New("canceled governed production loops did not join within the cleanup deadline"))
		}
	}()

	promptAdmission := authorityPromptAdmission{authority: session.Authority, conversation: conversation, durable: durable}
	if err := worker.SendAdmittedPrompt(ctx, promptAdmission, config.TaskID, config.TaskRequestID, config.TaskPrompt); err != nil {
		cancelRun()
		return fmt.Errorf("admit and forward registered production prompt to actual Pi: %w", err)
	}
	session.Lifecycle.markTaskAdmitted()
	if err := waitProductionTurnEnd(ctx, worker, config.TaskRequestID); err != nil {
		cancelRun()
		return err
	}
	session.Lifecycle.markTurnCompleted()

	settlement = closeWorker()
	select {
	case err := <-channelsDone:
		channelsJoined = true
		session.Lifecycle.markChannelsJoined(err)
		if err != nil {
			return fmt.Errorf("governed production channels did not settle cleanly: %w", err)
		}
	case <-time.After(60 * time.Second):
		cancelRun()
		return errors.New("governed production channels did not settle after the worker exited")
	}
	releaseSessionAdmission()

	publicationStatus := "UNAVAILABLE"
	disposition := "UNKNOWN"
	publicationTx := ""
	var publishErr error
	bundle, bundleErr := session.Store.EvidenceBundle()
	if bundleErr != nil {
		publishErr = fmt.Errorf("read production session generation/tool receipt: %w", bundleErr)
		publicationStatus, disposition = "UNKNOWN", "UNKNOWN"
	} else if !bundleHasChanges(bundle) {
		publicationStatus, disposition = "NO_DIFF", "NO_DIFF"
		if session.Publication != nil {
			if err := session.Publication.Close(); err != nil {
				publishErr = fmt.Errorf("close no-diff publication session: %w", err)
			}
		}
		if session.ManagedLiveRootLease != nil {
			if err := session.ManagedLiveRootLease.markNoPublication(); err != nil {
				publishErr = errors.Join(publishErr, fmt.Errorf("release no-diff managed live-root authority: %w", err))
				publicationStatus, disposition = "UNKNOWN", "UNKNOWN"
			}
		}
	} else if session.Publication == nil {
		publishErr = errors.New("production publication runtime is unavailable")
		publicationStatus, disposition = "UNAVAILABLE", "UNKNOWN"
	} else {
		result, finalizeErr := session.Publication.Finalize(ctx)
		publicationStatus = string(result.Status)
		if publicationStatus == "" {
			publicationStatus = "UNKNOWN"
		}
		publicationTx = result.TransactionID
		if result.Status == publication.StatusSucceeded || result.Status == publication.StatusFailed {
			if err := session.ManagedLiveRootLease.recordDurableTerminal(ctx, session.PublicationJournal, result.Status, result.TransactionID); err != nil {
				finalizeErr = errors.Join(finalizeErr, fmt.Errorf("verify durable managed-root terminal lifecycle record: %w", err))
				publicationStatus, disposition = "UNKNOWN", "UNKNOWN"
			}
		}
		if finalizeErr == nil && result.Status == publication.StatusSucceeded && len(result.Objects) == 0 {
			publicationStatus, disposition = "NO_DIFF", "NO_DIFF"
		} else if finalizeErr == nil && result.Status == publication.StatusSucceeded {
			disposition = "PUBLISHED"
		} else {
			disposition = "PUBLICATION_" + string(result.Status)
			if finalizeErr == nil {
				finalizeErr = fmt.Errorf("production publication did not succeed: %s", publicationStatus)
			}
		}
		if finalizeErr != nil {
			publishErr = fmt.Errorf("finalize registered production session (%s): %w", publicationStatus, finalizeErr)
		}
	}

	settled := settlement.State == piruntime.SettlementStopped && settlement.ExitObserved && settlement.DescendantsSettled
	lifecycle := session.Lifecycle.snapshot()
	lifecycleOK := validateProductionLifecycle(lifecycle, session.RunID, session.ProductionCommandRunner) == nil &&
		verifyProductionLaunchProofJournal(session.StoreJournal, lifecycle, session.Profile, session.D09Template, session.RunID,
			session.SessionID, session.WorkflowID, session.ConversationID, session.Generation.ID(),
			session.Prepared.Provenance().BindingDigest) == nil
	providerTrace, providerTraceErr := session.ProviderJournal.Trace()
	if providerTraceErr != nil {
		publishErr = errors.Join(publishErr, fmt.Errorf("read production provider lineage receipt: %w", providerTraceErr))
	}
	receipt := productionPiReceipt{
		SchemaVersion: "tbound-production-pi-receipt/v1", Mode: "production", RunID: session.RunID,
		ProfileID: session.Profile.ID, ProfileHash: session.Profile.Digest,
		ContainmentProfile:       session.Profile.ContainmentProfile,
		ContainmentProfileDigest: session.Profile.ContainmentProfileDigest,
		FrozenSourceDigest:       session.Profile.SourceProvenanceDigest, ProviderProfile: session.Profile.ProviderProfile,
		ProviderExchange: providerExchange, ClaimBearing: false,
		WorkerReady: true, TaskAdmitted: true, TurnCompleted: true,
		ProposalCount: supervisor.HandledProposals(),
		Settlement:    string(settlement.State), ExitObserved: settlement.ExitObserved, ExitCode: settlement.ExitCode,
		DescendantsSettled:     settlement.DescendantsSettled,
		CommandRunnerProfile:   productionCommandRunnerProfile(session.CommandRunner),
		CommandCellSettlements: productionCommandRunnerSettlements(session.CommandRunner),
		TCBProofDigest:         lifecycle.proofDigest, TCBProofJournalSequence: lifecycle.proofSeq,
		PublicationStatus: publicationStatus, Disposition: disposition, PublicationTransactionID: publicationTx,
		Success: settled && lifecycleOK && publishErr == nil && workerCloseErr == nil &&
			disposition == "PUBLISHED" && publicationStatus == string(publication.StatusSucceeded),
	}
	if bundleErr == nil {
		receipt.GenerationCount = len(bundle.Generations)
		if len(bundle.Generations) != 0 {
			receipt.SourceGeneration = bundle.Generations[0].ID
			receipt.FinalGeneration = bundle.Generations[len(bundle.Generations)-1].ID
		}
	}
	if providerTraceErr == nil {
		for _, record := range providerTrace.Records {
			if record.Event.Kind != "provider_exchange" {
				continue
			}
			var exchange providerExchangeEvidence
			if err := unmarshalGovernedJSON(record.Event.Data, &exchange); err != nil {
				publishErr = errors.Join(publishErr, fmt.Errorf("decode production provider lineage: %w", err))
				continue
			}
			receipt.ProviderTurns++
			if exchange.ToolCall != nil && len(receipt.ToolSequence) < 32 {
				receipt.ToolSequence = append(receipt.ToolSequence, exchange.ToolCall.Name)
			}
		}
	}
	cleanupErr := session.close()
	if cleanupErr != nil {
		publishErr = errors.Join(publishErr, fmt.Errorf("close settled production session before receipt: %w", cleanupErr))
	}
	if publishErr != nil || workerCloseErr != nil || cleanupErr != nil {
		receipt.Success = false
	}
	return errors.Join(writeProductionPiReceipt(output, receipt), publishErr, workerCloseErr, cleanupErr)
}

func bundleHasChanges(bundle sessionrepo.EvidenceBundle) bool {
	for _, transition := range bundle.ApprovedDeltaLedger {
		if len(transition.Changes) != 0 {
			return true
		}
	}
	return false
}

func productionCommandRunnerProfile(runner sessionrepo.CommandRunner) string {
	if profile, ok := runner.(sessionrepo.CommandRunnerProfile); ok {
		return profile.Profile()
	}
	return ""
}

func productionCommandRunnerSettlements(runner sessionrepo.CommandRunner) int {
	if production, ok := runner.(*productionCommandRunner); ok {
		return production.Settlements()
	}
	return 0
}

func waitProductionTurnEnd(ctx context.Context, worker *piruntime.PiSDKWorker, requestID string) error {
	for {
		event, err := worker.NextEvent(ctx)
		if err != nil {
			return fmt.Errorf("wait for production Pi turn: %w", err)
		}
		switch event.Type {
		case "worker_error":
			return fmt.Errorf("actual production Pi worker failed with bounded code %s", event.Code)
		case "turn_end":
			if event.RequestID != requestID {
				return fmt.Errorf("production Pi turn ended for unexpected request %q", event.RequestID)
			}
			return nil
		}
	}
}

func writeProductionPiReceipt(output io.Writer, receipt productionPiReceipt) error {
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode production Pi receipt: %w", err)
	}
	if len(encoded) == 0 || len(encoded) > productionReceiptMaxBytes {
		return errors.New("production Pi receipt exceeds its bound")
	}
	if output == nil {
		return nil
	}
	if _, err := output.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("write production Pi receipt: %w", err)
	}
	return nil
}

func boundedProductionIdentity(value string, maximum int) bool {
	return value != "" && strings.TrimSpace(value) == value && len([]byte(value)) <= maximum && !strings.ContainsAny(value, "\x00\r\n")
}

func boundedProductionPrompt(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && len([]byte(value)) <= productionPromptMaxBytes && !strings.ContainsRune(value, 0)
}
