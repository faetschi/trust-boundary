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
	"tbound/supervisor/internal/corpusadmit"
	"tbound/supervisor/internal/evalscore"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workspace"
	"tbound/supervisor/internal/wsoracle"
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
	productionTaskIDMaxBytes    = corpusadmit.TaskIDMaxBytes
	productionRequestMaxBytes   = corpusadmit.RequestIDMaxBytes
	productionPromptMaxBytes    = corpusadmit.PromptMaxBytes
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
	ProfilePath         string
	Workspace           string
	Source              string
	TaskID              string
	TaskRequestID       string
	TaskPrompt          string
	ServiceMode         bool                         // set only by the fixed socket-activated service entrypoint
	ManagedLiveRootPath string                       // set only from A's verified signed registration in service mode
	BashOutputPaths     []string                     // operator-declared exact output scope; nil disables Bash
	workflowRecovery    *governedRecoveryRequest     // set only by a trusted host recovery loader; never client parsed
	workflowAdmission   *productionWorkflowAdmission // set only by the authenticated service + host catalog
	providerRoute       productionProviderRoute      // set only by the service from its fixed provider_route; zero is the engineering route
}

func (c productionServeConfig) validate() error {
	if c.ServiceMode && (c.ProfilePath != "" || c.Workspace != "" || c.Source != "" ||
		!filepath.IsAbs(c.ManagedLiveRootPath) || filepath.Clean(c.ManagedLiveRootPath) != c.ManagedLiveRootPath) {
		return errors.New("internal Pi service mode refuses client-selected profile/source/workspace")
	}
	if !c.ServiceMode && c.ManagedLiveRootPath != "" {
		return errors.New("managed live-root service path is internal and cannot be supplied by a client")
	}
	if c.BashOutputPaths != nil {
		if err := sessionrepo.ValidateOutputScopePaths(c.BashOutputPaths); err != nil {
			return fmt.Errorf("invalid production Bash output scope: %w", err)
		}
	}
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
	D09Channels *piruntime.D09PiWorkerChannels

	Authority    *SessionOperationAuthority
	Prepared     *sessionlaunch.PreparedSession
	Store        *sessionrepo.Store
	Generation   *sessionrepo.Generation
	ManagedStore *sessionlaunch.ManagedStore
	Lifecycle    *productionLifecycleEvidence
	// E04 records the protected-tree manifest at each registered E04 point (production_e04_points_linux.go).
	// It is nil outside the service workflow modes.
	E04 *productionE04Recorder
	// Corpus is the binding the service derived from the admitted role (provider mode, delta, admission
	// rule). It is nil outside corpus-mode admissions.
	Corpus *corpusBinding

	StoreJournal    *audit.Journal
	ProviderJournal *audit.Journal
	CommandJournal  *audit.Journal

	Policy                  gate.Policy
	CallIssuer              string
	StorePolicyDigest       string
	MetadataPolicyDigest    string
	CommandRunner           sessionrepo.CommandRunner
	ProductionCommandRunner *productionCommandRunner

	SessionID                     string
	RunID                         string
	WorkflowID                    string
	ConversationID                string
	ProviderProfileID             string
	SystemPrompt                  string
	DeveloperPrompt               string
	offlineProviderConversation   *providerbridge.Conversation // integration-test fixture only; never set by CLI parsing
	recoveredProviderConversation *providerbridge.Conversation
	recoveryPromptTaskID          string
	recoveryPromptRequestID       string
	recoveryPrompt                string

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
	HintChooser        string               `json:"hint_chooser,omitempty"`
	HintManifestDigest string               `json:"hint_manifest_digest,omitempty"`
	Hints              []evalscore.HintFact `json:"hints,omitempty"`

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
	// ProviderRetries counts failed, retryable provider attempts that were repeated (each its own reserved host call).
	ProviderRetries          int    `json:"provider_retries,omitempty"`
	SourceGeneration         string `json:"source_generation"`
	FinalGeneration          string `json:"final_generation"`
	GenerationCount          int    `json:"generation_count"`
	Settlement               string `json:"settlement"`
	ExitObserved             bool   `json:"exit_observed"`
	ExitCode                 int    `json:"exit_code"`
	DescendantsSettled       bool   `json:"descendants_settled"`
	CommandRunnerProfile     string `json:"command_runner_profile"`
	CommandCellSettlements   int    `json:"command_cell_settlement_count"`
	TCBProofDigest           string `json:"tcb_proof_digest,omitempty"`
	TCBProofJournalSequence  uint64 `json:"tcb_proof_journal_sequence,omitempty"`
	PublicationStatus        string `json:"publication_status"`
	Disposition              string `json:"disposition"`
	PublicationTransactionID string `json:"publication_transaction_id,omitempty"`
	// The publication anchor of a corpus-mode interval (see production_publication_anchor_linux.go).
	// The service fills it from its own publication journal and its host catalog entry, never from
	// a request field. PublicationIntervalRef is the runner's IntervalRef of the trial
	// (<case_id>|<role>|C|<repeat>#<attempt>). PublicationJournalHead is the head hash of the
	// interval journal: a runner_interval record for that ref, then the workflow's "publication*"
	// records of the shared publication journal, chained from sequence 1. PublicationJournalRecordCount
	// is how many of those records are not publication_owner_epoch. A receipt with an empty head
	// carries no anchor, and its count means nothing. The guest executor copies head and count into
	// runner.PublicationFree.AnchorHead and AnchorCount. The receipt digest of the terminal record
	// (piServiceTerminalDigest) covers all three, as it covers every receipt byte.
	PublicationIntervalRef        string `json:"publication_interval_ref,omitempty"`
	PublicationJournalHead        string `json:"publication_journal_head,omitempty"`
	PublicationJournalRecordCount int    `json:"publication_journal_record_count"`
	// The E04 evidence binding (see production_e04_points_linux.go): the head hash and record count of
	// the workflow's store of protected-tree manifests, one per registered E04 point, as the service
	// journaled them, and how many points of this run are a gap or could not be recorded. Like the
	// publication anchor it is filled by the service and covered by the receipt digest. A receipt with
	// an empty head carries no binding: the host then has no E04 evidence for the run.
	E04EvidenceHead        string `json:"e04_evidence_head,omitempty"`
	E04EvidenceRecordCount int    `json:"e04_evidence_record_count,omitempty"`
	// The provider, the delta and the admission rule of a corpus run, as the service derived them from the
	// admitted role and the pinned manifest (production_corpus_binding_linux.go). ProviderMode is "model" or
	// "scripted"; ScriptDigest is the digest of the frozen script a scripted run replayed; Delta is the
	// registered targeted configuration delta ("none" for every C role); AdmissionPolicyDigest commits to
	// the delta-admission rule (rule and protected paths); Proposals are the gate verdicts of the run's
	// tool proposals in order, the ATT-TOOL-01 source.
	ProviderMode          string                   `json:"provider_mode,omitempty"`
	ScriptDigest          string                   `json:"script_digest,omitempty"`
	Delta                 string                   `json:"delta,omitempty"`
	AdmissionPolicyDigest string                   `json:"admission_policy_digest,omitempty"`
	Proposals             []productionProposalFact `json:"proposals,omitempty"`
	// The adaptive replay roles report the attacker run (production_corpus_binding_linux.go): the pivots
	// the machine issued, why it stopped (success_pivot, cap, exhausted, invalid or open), the machine's
	// state-history digest, the digest of the pinned attacker artifact and of the case's pivot table.
	PivotsIssued        int    `json:"pivots_issued,omitempty"`
	AttackerStop        string `json:"attacker_stop,omitempty"`
	AttackerStateDigest string `json:"attacker_state_digest,omitempty"`
	AttackerDigest      string `json:"attacker_digest,omitempty"`
	PivotsDigest        string `json:"pivots_digest,omitempty"`
	// The attacker budget (production_corpus_adaptive_budget_linux.go): what this attempt spent, what the case
	// has spent in all (the budget is cumulative across attempts), and the earlier attempt whose budget it
	// continued (empty for attempt 1).
	AttackerUsage           *providerbridge.AdaptiveUsage  `json:"attacker_usage,omitempty"`
	AttackerCumulativeUsage *providerbridge.AdaptiveUsage  `json:"attacker_cumulative_usage,omitempty"`
	AttackerPredecessor     *productionAttackerPredecessor `json:"attacker_predecessor,omitempty"`
	E04GapCount             int                            `json:"e04_gap_count,omitempty"`
	Success                 bool                           `json:"success"`

	// DeltaTemplateDigest is the derived identity of the delta command-cell template (piruntime.D09CellDelta
	// TemplateDigest of ContainmentProfileDigest); empty for every run whose cell is the signed base cell.
	// CommandSurvivors counts the command cells a no-reap/v1 run left running: their descendants are
	// unsettled by design and the operator reset ends them.
	DeltaTemplateDigest string `json:"delta_template_digest,omitempty"`
	CommandSurvivors    int    `json:"command_survivors,omitempty"`
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
	if config.ServiceMode {
		if !filepath.IsAbs(config.ManagedLiveRootPath) || filepath.Clean(config.ManagedLiveRootPath) != config.ManagedLiveRootPath ||
			!boundedProductionIdentity(config.TaskID, productionTaskIDMaxBytes) ||
			!boundedProductionIdentity(config.TaskRequestID, productionRequestMaxBytes) ||
			!boundedProductionPrompt(config.TaskPrompt) {
			return errors.New("socket-activated production task lacks a bounded task id, request id, or prompt")
		}
		return nil
	}
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
	if session.Bindings.D09Source == nil || session.Bindings.D09SourceCapability == nil ||
		session.Bindings.D09RuntimeAssetSource == nil || session.D09Channels == nil || session.Channels == nil ||
		session.Authority == nil || session.Store == nil || session.Generation == nil || session.LiveRoot == nil ||
		session.ManagedLiveRootLease == nil {
		return &ProductionPrerequisiteError{Missing: productionMissingBindings()}
	}
	// An adaptive attempt that ends in an error has spent budget too: record it (the first record wins).
	defer func() { _ = recordAttackerUsage(config.workflowAdmission, session.Corpus) }()

	verifier, err := piruntime.OpenLinuxHostProfileVerifier(session.Bindings)
	if err != nil {
		return fmt.Errorf("open trusted production host-profile verifier: %w", err)
	}
	capability, err := piruntime.AdmitProduction(ctx, session.Profile, verifier)
	if err != nil {
		return fmt.Errorf("admit production Pi host profile: %w", err)
	}
	durable, err := NewDurableExecutor(DurableExecutorConfig{
		Store: session.Store, Tip: session.Generation, CallIssuer: session.CallIssuer,
		PolicyDigest: session.StorePolicyDigest, MetadataPolicyDigest: session.MetadataPolicyDigest,
		CommandRunner: session.CommandRunner, BashOutputPaths: cloneDurableOutputScopePaths(config.BashOutputPaths),
		AfterDurableOperation: session.Authority.SettleDurableOperation,
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
		if err := productionSessionCloseIfUnsafe(
			session.Lifecycle.snapshot(), session.ProductionCommandRunner, previousClose); err != nil {
			return err
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
		if err := session.Publication.Close(); err != nil {
			return err
		}
		if previousClose != nil {
			return previousClose()
		}
		return nil
	}
	releaseSessionAdmission, err := publicationRuntime.BeginSession(ctx)
	if err != nil {
		return fmt.Errorf("recover publication lifecycle and activate session admission before D09 launch: %w", err)
	}
	var settlement piruntime.Settlement
	var workerCloseErr error
	workerClosed := false
	var workerLaunchErr error
	sessionAdmissionHeld := true
	releaseAdmission := func() {
		if sessionAdmissionHeld {
			sessionAdmissionHeld = false
			releaseSessionAdmission()
		}
	}
	defer func() {
		releaseProductionSessionAdmissionIfSafe(
			releaseAdmission, workerLaunchErr, workerClosed, settlement, workerCloseErr)
	}()

	bootstrap := piruntime.PiWorkerBootstrap{
		SessionID:      session.SessionID,
		WorkflowID:     session.WorkflowID,
		ConversationID: session.ConversationID,
	}
	journalLaunchProof := func(launcherPID int, proof piruntime.D09TCBProof) error {
		return journalProductionLaunchProof(session.StoreJournal, session.Lifecycle, session.RunID,
			session.SessionID, session.WorkflowID, session.ConversationID, session.Generation.ID(),
			session.Prepared.Provenance().BindingDigest, session.Profile, session.D09Template, launcherPID, proof)
	}
	session.Lifecycle.markWorkerLaunchAttempted()
	worker, err := piruntime.LaunchD09PiCLIWorker(ctx, capability, session.D09Channels, bootstrap)
	if err != nil {
		workerLaunchErr = err
		session.Lifecycle.markLaunchFailed(err)
		return productionLaunchFailure(err, journalLaunchProof)
	}
	session.Lifecycle.markWorkerLaunched(worker.PID())
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
	if err := journalLaunchProof(worker.PID(), launchProof); err != nil {
		settlement = closeWorker()
		return errors.Join(fmt.Errorf("journal production D09 launch proof before task release: %w", err), workerCloseErr)
	}

	conversation := session.offlineProviderConversation
	// A scripted replay makes no provider exchange: its turns come from the frozen script.
	providerExchange := conversation == nil && (session.Corpus == nil || !session.Corpus.scripted())
	if conversation == nil && session.recoveredProviderConversation != nil {
		conversation = session.recoveredProviderConversation
		session.Authority.mu.Lock()
		ownedConversation := session.Authority.conversation
		session.Authority.mu.Unlock()
		if ownedConversation != conversation {
			return errors.New("recovered provider conversation is not bound to the fresh workflow authority")
		}
	} else if conversation == nil {
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
	channelFailed := make(chan error, 1) // a loop failure, seen by waitTurn before the worker ends the turn
	session.Lifecycle.markChannelsStarted()
	go func() {
		err := RunGovernedChannels(runCtx, supervisor, session.Channels.ProviderChannel(), conversation)
		if err != nil {
			channelFailed <- err
		}
		channelsDone <- err
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
	taskID, taskRequestID, taskPrompt := config.TaskID, config.TaskRequestID, config.TaskPrompt
	if session.recoveryPrompt != "" {
		taskID, taskRequestID, taskPrompt = session.recoveryPromptTaskID, session.recoveryPromptRequestID, session.recoveryPrompt
	}
	if err := worker.SendAdmittedPrompt(ctx, promptAdmission, taskID, taskRequestID, taskPrompt); err != nil {
		cancelRun()
		return fmt.Errorf("admit and forward registered production prompt to actual Pi: %w", err)
	}
	session.Lifecycle.markTaskAdmitted()
	session.ManagedLiveRootLease.markTaskAdmitted()
	ops := productionLifecycleOps{
		waitTurn: func() error {
			if err := waitTurnOrChannelFailure(ctx, func(turnCtx context.Context) error {
				return waitProductionTurnEnd(turnCtx, worker, config.TaskRequestID)
			}, channelFailed); err != nil {
				cancelRun()
				return err
			}
			return nil
		},
		closeWorker: func() (piruntime.Settlement, error) {
			settlement = closeWorker()
			return settlement, workerCloseErr
		},
		joinChannels: func() error {
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
			return nil
		},
		releaseAdmission: releaseAdmission,
		evidenceBundle:   session.Store.EvidenceBundle,
		recordTerminal: func(ctx context.Context, status publication.Status, transactionID string) error {
			return session.ManagedLiveRootLease.recordDurableTerminal(ctx, session.PublicationJournal, status, transactionID)
		},
	}
	if session.Publication != nil {
		ops.closePublication, ops.finalize = session.Publication.Close, session.Publication.Finalize
	}
	if session.ManagedLiveRootLease != nil {
		ops.markNoPublication = func() error {
			return session.ManagedLiveRootLease.markNoPublication(session.Workspace, session.Generation)
		}
	}
	outcome, err := session.settleProductionTurn(ctx, ops)
	if err != nil {
		return err
	}
	publicationStatus, disposition, publicationTx := outcome.publicationStatus, outcome.disposition, outcome.publicationTx
	publishErr, bundle, bundleErr := outcome.publishErr, outcome.bundle, outcome.bundleErr
	noChangeReleased, settled := outcome.noChangeReleased, outcome.settled
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
			(disposition == "PUBLISHED" && publicationStatus == string(publication.StatusSucceeded) ||
				noChangeReleased && disposition == "NO_DIFF" && publicationStatus == "NO_DIFF"),
	}
	// TraceCommitted, not Trace: a journal cut or emptied while this handle is open is still a valid
	// hash chain, and anchoring it could give a false zero-publication head. Rollback across a
	// reopening is not detected here (privileged interference or operational rollback).
	if session.PublicationJournal == nil {
		if config.workflowAdmission != nil && config.workflowAdmission.Entry.CorpusManifestDigest != "" {
			publishErr = errors.Join(publishErr, errors.New("anchor production publication interval: no publication journal"))
		}
	} else if publicationTrace, traceErr := session.PublicationJournal.TraceCommitted(); traceErr != nil {
		publishErr = errors.Join(publishErr, fmt.Errorf("read production publication journal for the interval anchor: %w", traceErr))
	} else if anchorErr := applyPublicationAnchor(&receipt, config.workflowAdmission, session.WorkflowID, publicationTrace); anchorErr != nil {
		publishErr = errors.Join(publishErr, fmt.Errorf("anchor production publication interval: %w", anchorErr))
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
			if record.Event.Kind == "provider_exchange_retryable_failure" {
				receipt.ProviderRetries++
				continue
			}
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
	if err := applyCorpusReceipt(&receipt, session.Corpus, providerExchange, session.StorePolicyDigest,
		session.MetadataPolicyDigest, providerTrace, providerTraceErr); err != nil {
		publishErr = errors.Join(publishErr, err)
	}
	// The spend of an adaptive attempt is recorded durably before its receipt is released: a rerun reads it.
	if err := recordAttackerUsage(config.workflowAdmission, session.Corpus); err != nil {
		publishErr = errors.Join(publishErr, err)
	}
	if err := applyCellDeltaReceipt(&receipt, session.Corpus, session.CommandRunner); err != nil {
		publishErr = errors.Join(publishErr, err)
	}
	cleanupErr := session.close()
	if cleanupErr != nil {
		publishErr = errors.Join(publishErr, fmt.Errorf("close settled production session before receipt: %w", cleanupErr))
	}
	// The final-cleanup point was recorded by the store's closer inside session.close(); the
	// binding is the store as it stood when it was sealed. A recorder that cannot name its store
	// leaves no binding, and the host has no E04 evidence for the run.
	applyE04Binding(&receipt, session.E04)
	if publishErr != nil || workerCloseErr != nil || cleanupErr != nil {
		receipt.Success = false
	}
	return errors.Join(writeProductionPiReceipt(output, receipt), publishErr, workerCloseErr, cleanupErr)
}

// productionLifecycleOps are the effects of the session between the end of the admitted prompt and
// the publication outcome. runComposedProductionPiSession binds them to the real worker, governed
// loops, store, publication runtime and managed-root lease; a test binds fakes, which is the seam
// that lets it observe the order of the E04 points against the lifecycle events. A nil closePublication,
// markNoPublication or finalize means the session has no such component.
type productionLifecycleOps struct {
	waitTurn          func() error
	closeWorker       func() (piruntime.Settlement, error)
	joinChannels      func() error
	releaseAdmission  func()
	evidenceBundle    func() (sessionrepo.EvidenceBundle, error)
	closePublication  func() error
	markNoPublication func() error
	finalize          func(context.Context) (publication.Result, error)
	recordTerminal    func(context.Context, publication.Status, string) error
}

// productionSettlementOutcome is what settleProductionTurn leaves for the receipt.
type productionSettlementOutcome struct {
	settled           bool
	publicationStatus string
	disposition       string
	publicationTx     string
	publishErr        error
	bundle            sessionrepo.EvidenceBundle
	bundleErr         error
	noChangeReleased  bool
}

// settleProductionTurn is the lifecycle from the end of the turn to the publication outcome, and the
// four E04 points taken in it: post-operation-settlement (turn ended, worker not torn down),
// post-teardown-import (worker settled, loops joined), pre-publication, and post-publication. A
// returned error is a turn, teardown or channel failure: the points after it are not taken.
func (session *productionPiSession) settleProductionTurn(ctx context.Context, ops productionLifecycleOps) (productionSettlementOutcome, error) {
	var out productionSettlementOutcome
	if err := ops.waitTurn(); err != nil {
		return out, err
	}
	session.Lifecycle.markTurnCompleted()
	// E04 post-operation-settlement: the turn has ended, so its operations are settled; the
	// worker is not torn down yet.
	session.recordE04(wsoracle.PointPostSettlement)

	settlement, workerCloseErr := ops.closeWorker()
	if err := ops.joinChannels(); err != nil {
		return out, err
	}
	out.publicationStatus, out.disposition = "UNAVAILABLE", "UNKNOWN"
	e04LedgerSet := false
	out.settled = productionWorkerSettlementAllowsPublication(settlement, workerCloseErr)
	publicationAttemptErr := runProductionPublicationAfterWorkerSettlement(settlement, workerCloseErr, func() error {
		ops.releaseAdmission()
		out.bundle, out.bundleErr = ops.evidenceBundle()
		// E04 post-teardown-import: the worker is stopped and settled (the gate above), the loops
		// are joined, and the supervisor has imported the workspace result.
		session.recordE04(wsoracle.PointPostTeardown)
		if out.bundleErr != nil {
			out.publishErr = fmt.Errorf("read production session generation/tool receipt: %w", out.bundleErr)
			out.publicationStatus, out.disposition = "UNKNOWN", "UNKNOWN"
		} else if bundleHasNoChange(out.bundle) {
			out.publicationStatus, out.disposition = "NO_DIFF", "NO_DIFF"
			session.recordE04(wsoracle.PointPrePublication) // the no-diff release is the publication step
			if ops.closePublication != nil {
				if err := ops.closePublication(); err != nil {
					out.publishErr = fmt.Errorf("close no-diff publication session: %w", err)
				}
			}
			if ops.markNoPublication != nil {
				if err := ops.markNoPublication(); err != nil {
					out.publishErr = errors.Join(out.publishErr, fmt.Errorf("release no-diff managed live-root authority: %w", err))
					out.publicationStatus, out.disposition = "UNKNOWN", "UNKNOWN"
				} else {
					out.noChangeReleased = true
				}
			}
		} else if ops.finalize == nil {
			out.publishErr = errors.New("production publication runtime is unavailable")
			out.publicationStatus, out.disposition = "UNAVAILABLE", "UNKNOWN"
		} else {
			session.recordE04(wsoracle.PointPrePublication) // the step that may write the live root
			result, finalizeErr := ops.finalize(ctx)
			out.publicationStatus = string(result.Status)
			if out.publicationStatus == "" {
				out.publicationStatus = "UNKNOWN"
			}
			out.publicationTx = result.TransactionID
			if result.Status == publication.StatusSucceeded || result.Status == publication.StatusFailed {
				if err := ops.recordTerminal(ctx, result.Status, result.TransactionID); err != nil {
					finalizeErr = errors.Join(finalizeErr, fmt.Errorf("verify durable managed-root terminal lifecycle record: %w", err))
					out.publicationStatus, out.disposition = "UNKNOWN", "UNKNOWN"
				}
			}
			// The live-root ledger is what the publication itself applied (result.Ledger), taken
			// only when the outcome says exactly what happened; otherwise it stays unavailable.
			session.E04.setLedger(productionE04LedgerFromResult(result, finalizeErr))
			// What publication held besides the ledger, for EFF-WS-05/06: carried by the post-publication record.
			session.E04.setPublication(productionE04PublicationFromResult(result))
			e04LedgerSet = true
			if finalizeErr == nil && result.Status == publication.StatusSucceeded && len(result.Objects) == 0 {
				out.publicationStatus, out.disposition = "NO_DIFF", "NO_DIFF"
			} else if finalizeErr == nil && result.Status == publication.StatusSucceeded {
				out.disposition = "PUBLISHED"
			} else {
				out.disposition = "PUBLICATION_" + string(result.Status)
				if finalizeErr == nil {
					finalizeErr = fmt.Errorf("production publication did not succeed: %s", out.publicationStatus)
				}
			}
			if finalizeErr != nil {
				out.publishErr = fmt.Errorf("finalize registered production session (%s): %w", out.publicationStatus, finalizeErr)
			}
		}
		// E04 post-publication. The approved deltas reach the live root only here. Finalize set the
		// ledger from what the publication applied (see production_e04_points_linux.go); any other
		// path, where no publication outcome exists, records the empty prefix only for a run known
		// to have published nothing.
		if !e04LedgerSet && (out.disposition != "NO_DIFF" || out.publishErr != nil) {
			session.E04.setLedger(productionE04LedgerUnavailable())
		}
		session.recordE04(wsoracle.PointPostPublication)
		return nil
	})
	if publicationAttemptErr != nil {
		out.bundleErr = publicationAttemptErr
		out.publishErr = errors.Join(out.publishErr, publicationAttemptErr)
		out.publicationStatus, out.disposition = "UNAVAILABLE", "UNKNOWN"
	}
	return out, nil
}

func bundleHasChanges(bundle sessionrepo.EvidenceBundle) bool {
	for _, transition := range bundle.ApprovedDeltaLedger {
		if len(transition.Changes) != 0 {
			return true
		}
	}
	return false
}

// bundleHasNoChange reports a run that publication would find nothing to write:
// no approved transition carries a change, or the verified sealed tree equals
// the verified baseline tree. Publication composes net changes per path, so
// equal trees compose to an empty delta. Such a run never reaches Finalize, and
// a run that does reach Finalize has at least one composed object.
func bundleHasNoChange(bundle sessionrepo.EvidenceBundle) bool {
	chain := bundle.ValidatedChain
	return !bundleHasChanges(bundle) ||
		validManagedTreeDigest(chain.BaselineTreeDigest) && chain.SealedTreeDigest == chain.BaselineTreeDigest
}

var ErrProductionWorkerSettlementIncomplete = errors.New("production publication requires a fully observed STOPPED worker settlement")

func productionWorkerSettlementAllowsPublication(settlement piruntime.Settlement, closeErr error) bool {
	return settlement.State == piruntime.SettlementStopped && settlement.ExitObserved && settlement.DescendantsSettled && closeErr == nil
}

func incompleteProductionLaunchFailure(err error) bool {
	if err == nil {
		return false
	}
	switch current := err.(type) {
	case *piruntime.PiWorkerLaunchError:
		if current == nil {
			return false
		}
		if !productionWorkerSettlementAllowsPublication(current.Settlement, current.CleanupErr) {
			return true
		}
		return incompleteProductionLaunchFailure(current.Cause)
	case interface{ Unwrap() []error }:
		for _, nested := range current.Unwrap() {
			if incompleteProductionLaunchFailure(nested) {
				return true
			}
		}
	case interface{ Unwrap() error }:
		return incompleteProductionLaunchFailure(current.Unwrap())
	}
	return false
}

// A complete typed launch settlement resolves its own UNKNOWN cause. A
// separately joined UNKNOWN branch still requires admission retention.
func unresolvedProductionLaunchUnknown(err error, suppressCauseUnknown bool) bool {
	if err == nil {
		return false
	}
	switch current := err.(type) {
	case *piruntime.PiWorkerLaunchError:
		if current == nil {
			return false
		}
		if productionWorkerSettlementAllowsPublication(current.Settlement, current.CleanupErr) {
			return unresolvedProductionLaunchUnknown(current.Cause, true)
		}
		return false // Incomplete typed settlements are handled above.
	case interface{ Unwrap() []error }:
		for _, nested := range current.Unwrap() {
			if unresolvedProductionLaunchUnknown(nested, false) {
				return true
			}
		}
	case interface{ Unwrap() error }:
		return unresolvedProductionLaunchUnknown(current.Unwrap(), suppressCauseUnknown)
	default:
		return !suppressCauseUnknown && errors.Is(err, piruntime.ErrUnknownSettlement)
	}
	return false
}

// releaseProductionSessionAdmissionIfSafe retains admission if launch or worker
// teardown did not prove complete settlement. A plain prelaunch error remains
// safe to release; typed launch failures must carry the same complete evidence
// required by the publication gate.
func releaseProductionSessionAdmissionIfSafe(
	release func(),
	launchErr error,
	workerClosed bool,
	settlement piruntime.Settlement,
	closeErr error,
) bool {
	if incompleteProductionLaunchFailure(launchErr) ||
		unresolvedProductionLaunchUnknown(launchErr, false) {
		return false
	}
	if workerClosed && !productionWorkerSettlementAllowsPublication(settlement, closeErr) {
		return false
	}
	if release != nil {
		release()
	}
	return true
}

// productionLaunchFailure is the error of a launch that yielded no worker. A route-probe cell never
// yields one: it ran its probe and did not start Pi. Its validated launch proof is journaled as the
// run's production_tcb_proof, the record the guest-route-direct-runtime producer reads, and the run
// still fails.
func productionLaunchFailure(err error, journalLaunchProof func(launcherPID int, proof piruntime.D09TCBProof) error) error {
	var launchErr *piruntime.PiWorkerLaunchError
	if !errors.As(err, &launchErr) || launchErr.RouteProbe == nil {
		return fmt.Errorf("launch admitted production Pi worker: %w", err)
	}
	if journalErr := journalLaunchProof(launchErr.RouteProbe.LauncherPID, launchErr.RouteProbe.Proof); journalErr != nil {
		return errors.Join(fmt.Errorf("launch route-probe cell: %w", err),
			fmt.Errorf("journal route-probe launch proof: %w", journalErr))
	}
	return fmt.Errorf("route-probe launch proof journaled, Pi not started: %w", err)
}

// productionSessionCloseIfUnsafe lets the binder's own Close guard run when
// the session lifecycle is unsafe, so it can retain its owners in quarantine.
func productionSessionCloseIfUnsafe(
	evidence productionLifecycleSnapshot,
	runner *productionCommandRunner,
	previousClose func() error,
) error {
	if productionCleanupSafe(evidence, runner) {
		return nil
	}
	if previousClose != nil {
		return errors.Join(ErrProductionLifecycleUnsettled, previousClose())
	}
	return ErrProductionLifecycleUnsettled
}

// runProductionPublicationAfterWorkerSettlement is the single gate around the
// production evidence/publication path. Callers cannot inspect the mutable tip,
// release publication admission, mark no-diff, or finalize until direct-child
// exit and descendant settlement have both been observed without close error.
func runProductionPublicationAfterWorkerSettlement(
	settlement piruntime.Settlement,
	closeErr error,
	attempt func() error,
) error {
	if attempt == nil {
		return errors.New("production publication attempt is unavailable")
	}
	if !productionWorkerSettlementAllowsPublication(settlement, closeErr) {
		return fmt.Errorf("%w: state=%s exit_observed=%t descendants_settled=%t close_error=%t",
			ErrProductionWorkerSettlementIncomplete, settlement.State, settlement.ExitObserved, settlement.DescendantsSettled, closeErr != nil)
	}
	return attempt()
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

func waitProductionTurnEnd(ctx context.Context, worker *piruntime.PiCLIWorker, requestID string) error {
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
	return corpusadmit.BoundedIdentity(value, maximum)
}

func boundedProductionPrompt(value string) bool {
	return corpusadmit.BoundedPrompt(value)
}

// waitTurnOrChannelFailure waits for the end of the turn, but returns as soon as the governed channel
// loops fail: a worker whose provider or tool channel is gone may never send a terminal event, and the
// caller's teardown must not wait for the 20-minute task context. The channel error is kept in the
// result; the caller cancels the run and enters its bounded teardown.
func waitTurnOrChannelFailure(ctx context.Context, turn func(context.Context) error, channelFailed <-chan error) error {
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	turnDone := make(chan error, 1)
	go func() { turnDone <- turn(turnCtx) }()
	select {
	case err := <-turnDone:
		return err
	case err := <-channelFailed:
		cancel()
		<-turnDone // the turn wait honors its context, so this is prompt
		return fmt.Errorf("governed production channels failed before the turn ended: %w", err)
	}
}
