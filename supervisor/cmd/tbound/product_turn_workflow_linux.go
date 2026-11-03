//go:build linux

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/privategit"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/publication"
	"tbound/supervisor/internal/sessionlaunch"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflow"
	"tbound/supervisor/internal/workflowrecovery"
	"tbound/supervisor/internal/workspace"
)

// Retained layout of one product turn under productTurnEnv.StateRoot/turns/<WorkflowID>. The catalog
// link records the three journal paths; the Store root is their sibling.
const (
	productTurnStoreDir        = "store"
	productTurnStoreJournal    = "store-audit.jsonl"
	productTurnProviderJournal = "provider-audit.jsonl"
	productTurnCommandJournal  = "command-audit.jsonl"
	// productStoreSeedGeneration is the generation sessionrepo.Store.Seed always creates. A project
	// baseline is the tree digest under this label, so it equals the next turn's Store baseline.
	productStoreSeedGeneration = "g0"
	// productCommandOutputBytes is the most of each Bash stream (stdout and stderr) a product turn shows the
	// model: its tail, as valid UTF-8. The text is the workspace command's own output, passed through as model
	// context, and it stays in the Store's retained command result in full, up to the Store's own bound.
	// Replay rebuilds the model-visible summary with the same bound, so changing it makes earlier Bash turns
	// unverifiable as predecessors: it is a format change.
	productCommandOutputBytes = 16 << 10
)

// errProductStaleBaseline marks a managed project whose tree differs from the admitted baseline.
var errProductStaleBaseline = errors.New("managed project tree differs from the admitted baseline")

// errProductBashRunnerUnavailable marks a project whose policy allows Bash on a service that has no
// command runner, so its turns cannot launch.
var errProductCommandUnsettled = errors.New("product command ownership is unresolved")

var errProductBashRunnerUnavailable = errors.New("the project policy allows Bash but the service has no command runner")

// productTurnCommandRunner is the trusted command runner of one Bash turn: the Store runs commands
// through it and the turn's authority registers each lease with it and re-verifies each settlement.
type productTurnCommandRunner interface {
	sessionrepo.CommandRunner
	governedCommandRunner
}

// productTurnCommandContext names the turn a command runner is built for. Journal is the turn's
// command journal, which a runner that retains settlement evidence appends to.
type productTurnCommandContext struct {
	Journal                                      *audit.Journal
	TurnDir                                      string
	WorkflowID, RunID, SessionID, ConversationID string
}

// productCommandHistoryOpener opens the read-only command-settlement verifier of a finished turn over
// its held command journal. The verifier has no runner and no lease authority.
type productCommandHistoryOpener func(journal *audit.Journal, link productsession.TurnLink) (governedRecoveryCommandVerifier, error)

// productPolicyAllowsBash reports whether the policy has an allow rule for bash.
func productPolicyAllowsBash(policy gate.Policy) bool {
	return slices.ContainsFunc(policy.Profile().Rules, func(r gate.Rule) bool { return r.Tool == "bash" && r.Effect == gate.EffectAllow })
}

// productTurnBashScope is the exact output scope the Bash commands of a turn declare, or nil when the
// turn's policy does not allow Bash. A product-scope turn declares none: its candidate is decided by
// the turn's own writable, read-only and protected lists, which ignore the declared paths. A turn
// under the exact-path rule keeps the registered paths.
func productTurnBashScope(turn productsession.Turn, policy gate.Policy) []string {
	if !productPolicyAllowsBash(policy) {
		return nil
	}
	if turn.Scope != nil {
		return []string{}
	}
	return append([]string{}, turn.BashOutputPaths...)
}

// productTurnEnv holds the trusted inputs of the product turn driver. Production fills it from the
// host profile; tests fill it from fixtures. Nothing in it comes from a client request.
type productTurnEnv struct {
	Catalog      *productsession.Catalog
	Registration productsession.Registration
	// StateRoot is a private directory that holds every turn's retained evidence and each project's
	// publication lifecycle journal.
	StateRoot string
	// ProjectRoots maps a registered project ID to its managed directory. The directory identity must
	// equal the registration's root_identity_digest.
	ProjectRoots map[string]string
	// GatePolicies maps a registered project ID to its installed gate policy. Its digest must equal
	// the registration's policy_digest.
	GatePolicies         map[string]gate.Policy
	StorePolicyDigest    string
	MetadataPolicyDigest string
	Xattr                workspace.XattrVisibilityAttestation
	SystemPrompt         string
	DeveloperPrompt      string
	// PinGit pins the private Git executable for one turn; the turn closes it.
	PinGit func() (privategit.PinnedGit, sessionlaunch.GitProfile, error)
	// NewConversation constructs the provider conversation over the turn's provider journal.
	NewConversation func(providerbridge.Config, *audit.Journal) (*providerbridge.Conversation, error)
	// PublicationTrust is the live-root trust of an auto-publish turn, bound to the turn's authority.
	PublicationTrust func(*SessionOperationAuthority) workflow.Trust
	// NewCommandRunner builds the trusted Bash command runner of one turn over its command journal.
	// Production fills it with the D09 command-cell runner of a qualified host; tests use the
	// development runner. A turn whose project policy allows Bash refuses to launch without it.
	NewCommandRunner func(productTurnCommandContext) (productTurnCommandRunner, error)
	// OpenCommandHistory opens the read-only verifier of the command settlements a finished Bash turn
	// retained. Without it a retained Store that holds a Bash effect cannot be reopened.
	OpenCommandHistory productCommandHistoryOpener
	// MaxWorkerSteps bounds the provider exchanges of one turn.
	MaxWorkerSteps int
	// publicationFaults is a trusted-host test seam for publication boundaries; production leaves it nil.
	publicationFaults publication.FaultInjector
	// crashAt is a trusted-host test seam called at named driver boundaries; production leaves it nil.
	crashAt func(point string)

	retainedMu       sync.Mutex
	retainedCommands map[*productTurnWorkflow]struct{}
	mu               sync.Mutex
	// running holds the turns RunTurn drives in this process. pending holds closed review turns whose
	// workflow and finalizer stay open until a resolution, cancellation or expiry takes them.
	running map[string]bool
	pending map[string]*productPendingReview
	// liveProvider holds the open provider journal of every turn whose workflow this process still holds, so
	// the service reads a running or pending-review turn's journal without a second writer lock. It has its
	// own mutex because workflow close runs under mu.
	liveMu       sync.Mutex
	liveProvider map[string]*audit.Journal
}

func (e *productTurnEnv) crash(point string) {
	if e.crashAt != nil {
		e.crashAt(point)
	}
}

// productTurnWorkflow is one fresh governed workflow composed for an admitted turn.
type productTurnWorkflow struct {
	workflowID, runID, sessionID, conversationID string
	taskID                                       string
	turnDir                                      string
	storeJournalPath                             string
	providerJournalPath                          string
	commandJournalPath                           string
	projectRoot                                  *os.File
	authority                                    *SessionOperationAuthority
	ledger                                       *workflowrecovery.Ledger
	durable                                      *DurableExecutor
	policy                                       gate.Policy
	store                                        *sessionrepo.Store
	baseline                                     *sessionrepo.Generation
	storeJournal                                 *audit.Journal
	providerJournal                              *audit.Journal
	commandJournal                               *audit.Journal
	bash                                         bool // the turn composed Bash, so its publication trust must resolve a Bash lease origin
	config                                       providerbridge.Config
	started                                      time.Time
	closers                                      []func() error
	commandLifecycle                             productCommandLifecycle
	env                                          *productTurnEnv
	closeMu                                      sync.Mutex
}

func (w *productTurnWorkflow) settleCommands() error {
	if w.commandLifecycle == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return w.commandLifecycle.Settle(ctx)
}

func (w *productTurnWorkflow) close() error {
	w.closeMu.Lock()
	defer w.closeMu.Unlock()
	if w.commandLifecycle != nil {
		if err := w.commandLifecycle.Close(); err != nil {
			if w.env != nil {
				w.env.retainedMu.Lock()
				if w.env.retainedCommands == nil {
					w.env.retainedCommands = map[*productTurnWorkflow]struct{}{}
				}
				w.env.retainedCommands[w] = struct{}{}
				w.env.retainedMu.Unlock()
			}
			return err
		}
	}
	if w.env != nil {
		w.env.retainedMu.Lock()
		delete(w.env.retainedCommands, w)
		w.env.retainedMu.Unlock()
	}
	var errs []error
	for index := len(w.closers) - 1; index >= 0; index-- {
		errs = append(errs, w.closers[index]())
	}
	w.closers = nil
	return errors.Join(errs...)
}

func (w *productTurnWorkflow) link(predecessor providerbridge.PredecessorContext) productsession.TurnLink {
	return productsession.TurnLink{WorkflowID: w.workflowID, RunID: w.runID, SessionID: w.sessionID,
		ConversationID: w.conversationID, ProviderJournalPath: w.providerJournalPath, StoreJournalPath: w.storeJournalPath,
		CommandJournalPath: w.commandJournalPath, ImportedPrefixDigest: predecessor.Digest()}
}

// productTurnPathsFromLink derives the retained evidence paths of a linked turn.
func productTurnPathsFromLink(link productsession.TurnLink) (productTurnPaths, error) {
	dir := filepath.Dir(link.StoreJournalPath)
	if filepath.Dir(link.ProviderJournalPath) != dir || filepath.Dir(link.CommandJournalPath) != dir ||
		filepath.Base(link.StoreJournalPath) != productTurnStoreJournal || filepath.Base(link.ProviderJournalPath) != productTurnProviderJournal ||
		filepath.Base(link.CommandJournalPath) != productTurnCommandJournal {
		return productTurnPaths{}, errProductTurnEvidence
	}
	return productTurnPaths{StoreRoot: filepath.Join(dir, productTurnStoreDir), StoreJournalPath: link.StoreJournalPath,
		ProviderJournalPath: link.ProviderJournalPath, CommandJournalPath: link.CommandJournalPath, Link: link}, nil
}

func randomProductIdentity(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("product identity entropy: %w", err)
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

func (e *productTurnEnv) registeredProject(projectID string) (productsession.Project, bool) {
	for _, project := range e.Registration.Projects {
		if project.ProjectID == projectID {
			return project, true
		}
	}
	return productsession.Project{}, false
}

// openProjectRoot opens the managed directory of a registered project and checks its identity.
func (e *productTurnEnv) openProjectRoot(projectID string) (*os.File, error) {
	project, ok := e.registeredProject(projectID)
	path := e.ProjectRoots[projectID]
	if !ok || path == "" {
		return nil, fmt.Errorf("project %q has no registered managed directory", projectID)
	}
	root, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	identity, err := recoveryDirectoryIdentityDigest(root)
	if err != nil || sha256DigestFromBarePin(identity) != project.RootIdentityDigest {
		_ = root.Close()
		return nil, errors.Join(fmt.Errorf("managed directory of project %q is not its registered root", projectID), err)
	}
	return root, nil
}

func (e *productTurnEnv) projectWorkspace() workspace.Options {
	return productionWorkspaceOptions(productStoreSeedGeneration, e.MetadataPolicyDigest, e.Xattr)
}

// MeasureProjectBaseline is the catalog baseline of a managed project now: "sha256:" and the Store
// tree digest of the directory scanned as a seed generation. The composition passes it to
// CreateSession and SubmitTurn and checks it again before a turn runs.
func (e *productTurnEnv) MeasureProjectBaseline(projectID string) (string, error) {
	root, err := e.openProjectRoot(projectID)
	if err != nil {
		return "", err
	}
	defer root.Close()
	snapshot, err := workspace.Scan(root, e.projectWorkspace())
	if err != nil {
		return "", fmt.Errorf("scan managed project %q: %w", projectID, err)
	}
	return sha256DigestFromBarePin(bareStoreTreeDigest(snapshot.TreeDigest)), nil
}

func (e *productTurnEnv) gatePolicy(projectID, policyDigest string) (gate.Policy, error) {
	policy, ok := e.GatePolicies[projectID]
	if !ok || productRegisteredPolicyDigest(policy) != policyDigest {
		return gate.Policy{}, fmt.Errorf("installed gate policy of project %q differs from its registration", projectID)
	}
	return policy, nil
}

// storeOptions are the options a turn's Store was created under. The admission scope is the one the catalog
// recorded when it admitted that turn, not the registration now in force.
func (e *productTurnEnv) storeOptions(turn productsession.Turn) sessionrepo.Options {
	return sessionrepo.Options{PolicyDigest: e.StorePolicyDigest, MetadataPolicyDigest: e.MetadataPolicyDigest,
		Limits: workspace.DefaultLimits(), XattrVisibility: e.Xattr, AdmissionScope: turn.Scope.AdmissionScope()}
}

// composeProductTurnWorkflow builds one fresh governed workflow for an admitted turn, up to but not
// including its provider conversation: fresh identities and per-turn directory, a Store seeded from
// the managed project, the private Git source binding, a fresh protected owner and checkpoint whose
// limits are the turn's reservation and whose pins carry the imported prefix, and the bound ledger.
// It returns errProductStaleBaseline when the seeded tree differs from the admitted baseline.
func (e *productTurnEnv) composeProductTurnWorkflow(ctx context.Context, turn productsession.Turn,
	providerProfile string, predecessor providerbridge.PredecessorContext) (_ *productTurnWorkflow, err error) {
	w := &productTurnWorkflow{taskID: turn.ID, started: time.Now(), env: e}
	defer func() {
		if err != nil {
			if cleanupErr := w.close(); cleanupErr != nil {
				err = errors.Join(err, errProductCommandUnsettled, cleanupErr)
			}
		}
	}()
	for _, identity := range []struct {
		target *string
		prefix string
	}{{&w.workflowID, "pwf-"}, {&w.runID, "prun-"}, {&w.sessionID, "prs-"}, {&w.conversationID, "pconv-"}} {
		if *identity.target, err = randomProductIdentity(identity.prefix); err != nil {
			return nil, err
		}
	}
	policy, err := e.gatePolicy(turn.ProjectID, turn.PolicyDigest)
	if err != nil {
		return nil, err
	}
	w.policy = policy
	bashScope := productTurnBashScope(turn, policy)
	w.bash = bashScope != nil
	if bashScope != nil && e.NewCommandRunner == nil {
		return nil, errProductBashRunnerUnavailable
	}

	turnsDir := filepath.Join(e.StateRoot, "turns")
	if err := os.MkdirAll(turnsDir, 0o700); err != nil {
		return nil, err
	}
	w.turnDir = filepath.Join(turnsDir, w.workflowID)
	storePath, adminPath := filepath.Join(w.turnDir, productTurnStoreDir), filepath.Join(w.turnDir, "admin")
	recoveryRootDir, ownerStoreDir := filepath.Join(w.turnDir, "recovery-root"), filepath.Join(w.turnDir, "owner-store")
	for _, path := range []string{w.turnDir, storePath, adminPath, recoveryRootDir, ownerStoreDir} {
		if err := os.Mkdir(path, 0o700); err != nil {
			return nil, err
		}
	}
	openJournal := func(name string) (*audit.Journal, string, error) {
		path := filepath.Join(w.turnDir, name)
		journal, err := audit.Open(path)
		if err != nil {
			return nil, "", err
		}
		w.closers = append(w.closers, journal.Close)
		return journal, path, nil
	}
	if w.storeJournal, w.storeJournalPath, err = openJournal(productTurnStoreJournal); err != nil {
		return nil, err
	}
	if w.providerJournal, w.providerJournalPath, err = openJournal(productTurnProviderJournal); err != nil {
		return nil, err
	}
	if w.commandJournal, w.commandJournalPath, err = openJournal(productTurnCommandJournal); err != nil {
		return nil, err
	}

	var runner productTurnCommandRunner
	if bashScope != nil {
		runner, err = e.NewCommandRunner(productTurnCommandContext{Journal: w.commandJournal, TurnDir: w.turnDir, WorkflowID: w.workflowID,
			RunID: w.runID, SessionID: w.sessionID, ConversationID: w.conversationID})
		if value := reflect.ValueOf(runner); value.IsValid() && value.Kind() == reflect.Pointer && value.IsNil() {
			runner = nil
		}
		w.commandLifecycle, _ = runner.(productCommandLifecycle)
		if err != nil {
			return nil, err
		}
		if runner == nil {
			return nil, errProductBashRunnerUnavailable
		}
	}
	authority, err := NewSessionOperationAuthority(w.storeJournal, w.providerJournal, w.workflowID, w.conversationID,
		providerProfile, policy, e.StorePolicyDigest, e.MetadataPolicyDigest, runner, bashScope)
	if err != nil {
		return nil, err
	}
	w.authority = authority
	authority.requireEvidencedBashContainment = runner != nil && runner.Profile() == piruntime.D09CommandRunnerProfile
	authority.commandOutputBytes = productCommandOutputBytes
	if err := authority.BindAdmissionScope(turn.Scope.AdmissionScope()); err != nil {
		return nil, err
	}
	storeRoot, err := os.Open(storePath)
	if err != nil {
		return nil, err
	}
	managed, err := authority.CreateStore(storeRoot, workspace.DefaultLimits(), e.Xattr, nil)
	_ = storeRoot.Close()
	if err != nil {
		return nil, err
	}
	w.closers = append(w.closers, managed.Close)
	w.store = managed.Store()
	if w.projectRoot, err = e.openProjectRoot(turn.ProjectID); err != nil {
		return nil, err
	}
	w.closers = append(w.closers, w.projectRoot.Close)
	baseline, err := w.store.Seed(w.projectRoot, sessionrepo.RootAttestation{Quiescent: true})
	if err != nil {
		return nil, err
	}
	w.baseline = baseline
	if err := w.store.RequireCanonicalSource(w.projectRoot, baseline); err != nil {
		return nil, err
	}
	if baseline.ID() != productStoreSeedGeneration ||
		sha256DigestFromBarePin(bareStoreTreeDigest(baseline.TreeDigest())) != turn.BaselineDigest {
		return nil, errProductStaleBaseline
	}
	if err := authority.BindBaseline(w.store, baseline); err != nil {
		return nil, err
	}
	adminRoot, err := os.Open(adminPath)
	if err != nil {
		return nil, err
	}
	admin, err := sessionlaunch.OpenAdminParent(adminRoot)
	_ = adminRoot.Close()
	if err != nil {
		return nil, err
	}
	w.closers = append(w.closers, admin.Close)
	if e.PinGit == nil {
		return nil, errors.New("product turn has no private Git pin")
	}
	git, gitProfile, err := e.PinGit()
	if err != nil {
		return nil, err
	}
	w.closers = append(w.closers, git.Close)
	prepared, err := sessionlaunch.Prepare(ctx, managed, baseline, admin, git, gitProfile)
	if err != nil {
		return nil, err
	}
	w.closers = append(w.closers, prepared.Close)
	if err := authority.BindPreparedSession(prepared); err != nil {
		return nil, err
	}
	if w.durable, err = NewDurableExecutor(DurableExecutorConfig{
		Store: w.store, Tip: baseline, CallIssuer: governedCallIssuer,
		PolicyDigest: e.StorePolicyDigest, MetadataPolicyDigest: e.MetadataPolicyDigest,
		BashOutputPaths: bashScope, CommandRunner: runner,
		CommandOutputBytes:    productCommandOutputBytes,
		AfterDurableOperation: authority.SettleDurableOperation,
	}); err != nil {
		return nil, err
	}

	deadline := w.started.UTC().Add(time.Duration(turn.Reservation.DurationSeconds) * time.Second).Round(0)
	fresh := &productTurnFreshPolicy{workflow: w.workflowID, deadline: deadline}
	boundary, err := workflowrecovery.NewLinuxHostBoundary(ownerStoreDir, uint32(os.Geteuid()), productTurnNoPriorOwner{}, fresh)
	if err != nil {
		return nil, err
	}
	protectedRoot, err := workflowrecovery.CreateFreshProtectedRoot(filepath.Join(recoveryRootDir, "workflow-root.sqlite"), ownerStoreDir, boundary)
	if err != nil {
		return nil, err
	}
	w.closers = append(w.closers, protectedRoot.Close)
	owner, err := workflowrecovery.CreateFreshSQLiteOwner(filepath.Join(ownerStoreDir, "workflow.sqlite"), protectedRoot)
	if err != nil {
		return nil, err
	}
	w.closers = append(w.closers, owner.Close)
	ownerStore, err := owner.OwnerStoreIdentity(ctx, w.workflowID)
	if err != nil {
		return nil, err
	}
	fresh.root, fresh.incarnation, fresh.store = owner.TrustedRootIdentityDigest(), owner.OwnerIncarnation(), ownerStore

	directoryIdentity := func(path string) (string, error) {
		root, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer root.Close()
		return recoveryDirectoryIdentityDigest(root)
	}
	storeIdentity, err := directoryIdentity(storePath)
	if err != nil {
		return nil, err
	}
	adminIdentity, err := directoryIdentity(adminPath)
	if err != nil {
		return nil, err
	}
	commandIdentity, err := recoveryFileIdentityDigest(w.commandJournalPath, false)
	if err != nil {
		return nil, err
	}
	providerIdentity, err := recoveryFileIdentityDigest(w.providerJournalPath, false)
	if err != nil {
		return nil, err
	}
	admissionIdentity, err := recoveryFileIdentityDigest(filepath.Join(storePath, "delta-admission.jsonl"), false)
	if err != nil {
		return nil, err
	}
	d06Digest, gitIdentityDigest, _, err := prepared.RecoveryIdentity()
	if err != nil {
		return nil, err
	}
	bundle, err := w.store.EvidenceBundle()
	if err != nil {
		return nil, err
	}
	storeTrace, err := w.storeJournal.Trace()
	if err != nil {
		return nil, err
	}
	admissionTrace, err := audit.Verify(strings.NewReader(string(bundle.AdmissionJournal)))
	if err != nil {
		return nil, err
	}
	taskDigest := digestBytes([]byte("tbound-product-task/v1\n" + turn.ID + "\n" + turn.PromptDigest))
	limits := workflowrecovery.Limits{MaxPreEffectRetries: 2, MaxCleanRestarts: 2,
		MaxHostCalls: uint64(turn.Reservation.HostCalls), MaxTokens: uint64(turn.Reservation.Tokens), Deadline: deadline}
	pins := workflowrecovery.Pins{
		WorkflowID: w.workflowID, SessionID: w.sessionID, RecoveryGeneration: 1, OwnerEpoch: 1,
		OwnerIncarnationDigest: owner.OwnerIncarnationDigest(), RootIdentityDigest: owner.TrustedRootIdentityDigest(),
		StoreIdentityDigest: storeIdentity, AdmissionIdentityDigest: admissionIdentity,
		CommandIdentityDigest: commandIdentity, AdminIdentityDigest: adminIdentity,
		TaskDigest: bareWorkflowDigest(taskDigest), PromptDigest: bareWorkflowDigest(turn.PromptDigest),
		ImportedPrefixDigest:         bareWorkflowDigest(predecessor.Digest()),
		PredecessorTurnReceiptDigest: bareWorkflowDigest(predecessor.PredecessorReceiptDigest()),
		PolicyDigest:                 barePolicyDigest(policy.Digest()), ProfileDigest: bareWorkflowDigest(digestBytes([]byte(providerProfile))),
		// These are registration declarations, not measurements; every product receipt labels them unverified.
		ImageDigest: bareWorkflowDigest(e.Registration.ImageDigest), RuntimeDigest: bareWorkflowDigest(e.Registration.RuntimeDigest),
		D06ProvenanceDigest: bareWorkflowDigest(d06Digest), PrivateGitIdentityDigest: bareWorkflowDigest(gitIdentityDigest),
		StoreBaselineGenerationID: baseline.ID(), StoreBaselineTreeDigest: bareStoreTreeDigest(baseline.TreeDigest()),
		StoreTipGenerationID: baseline.ID(), StoreTipTreeDigest: bareStoreTreeDigest(baseline.TreeDigest()),
		StoreJournalHead: auditHeadHash(storeTrace.Records), StoreJournalCount: uint64(len(storeTrace.Records)),
		AdmissionJournalHead: auditHeadHash(admissionTrace.Records), AdmissionJournalCount: uint64(len(admissionTrace.Records)),
		ProviderProjectionDigest: workflowrecovery.TranscriptPinsDigest(nil),
		ProviderDisposition:      workflowrecovery.DispositionNone, PendingCallDisposition: workflowrecovery.PendingCallNone,
	}
	pins.RecoveryLineageDigest = workflowrecovery.ComputeRecoveryLineageDigest("", workflowrecovery.Pins{}, pins)
	if _, err := protectedRoot.NextOwnerEpoch(ctx, w.workflowID); err != nil {
		return nil, err
	}
	if _, err := workflowrecovery.InitializeFresh(ctx, owner, pins, limits); err != nil {
		return nil, fmt.Errorf("initialize the fresh product turn checkpoint: %w", err)
	}
	if w.ledger, err = workflowrecovery.Open(ctx, owner, workflowrecovery.OpenOptions{
		WorkflowID: w.workflowID, OwnerEpoch: 1, RetainedPins: pins, Limits: limits,
	}); err != nil {
		return nil, err
	}
	transcript := workflowrecovery.TranscriptPin{Ordinal: 1, ConversationID: w.conversationID,
		JournalIdentityDigest: providerIdentity, Disposition: workflowrecovery.DispositionAwaitingHostPrompt}
	if _, err := w.ledger.RegisterInitialTranscript(ctx, productTurnInitialTranscript{pins: pins, transcript: transcript,
		store: w.store, storeJournal: w.storeJournal, providerJournal: w.providerJournal, baseline: baseline}, transcript); err != nil {
		return nil, fmt.Errorf("register the empty provider transcript of the product turn: %w", err)
	}
	checkpoint, _ := w.ledger.Snapshot()
	if err := authority.BindWorkflowLedger(w.ledger, checkpoint.Pins, productionProviderTokenUpperBound(limits)); err != nil {
		return nil, err
	}
	if err := authority.BindInitialWorkflowPrompt(w.taskID, turn.Prompt, taskDigest); err != nil {
		return nil, err
	}
	w.config = providerbridge.Config{ProductSoftRefusals: true, WorkflowID: w.workflowID, ConversationID: w.conversationID, ProfileID: providerProfile,
		InitialGenerationID: baseline.ID(), InitialTreeDigest: baseline.TreeDigest(),
		SystemPrompt: e.SystemPrompt, DeveloperPrompt: e.DeveloperPrompt, Predecessor: predecessor}
	return w, nil
}

// productTurnFreshPolicy authorizes exactly one fresh workflow, once, with the composed owner,
// deadline and store.
type productTurnFreshPolicy struct {
	workflow    string
	deadline    time.Time
	root        string
	incarnation workflowrecovery.OwnerIncarnation
	store       workflowrecovery.OwnerStoreIdentity
	calls       int
}

func (p *productTurnFreshPolicy) AuthorizeFreshWorkflow(ctx context.Context, workflow string, epoch uint64,
	initialDigest string, incarnation workflowrecovery.OwnerIncarnation, deadline time.Time,
	root string, store workflowrecovery.OwnerStoreIdentity) error {
	if p == nil || ctx == nil || ctx.Err() != nil || p.calls != 0 || workflow != p.workflow || epoch != 1 ||
		!validBareWorkflowDigest(initialDigest) || !reflect.DeepEqual(incarnation, p.incarnation) ||
		!deadline.Equal(p.deadline) || deadline.Location() != time.UTC || root != p.root || store != p.store {
		return workflowrecovery.ErrFreshAuthorization
	}
	p.calls++
	return nil
}

// productTurnNoPriorOwner is the drain verifier of a fresh product workflow: it has no superseded
// owner, so it refuses any owner it is asked about.
type productTurnNoPriorOwner struct{}

func (productTurnNoPriorOwner) VerifyOwnerCellsDrainedWithProof(_ context.Context, _ string,
	owners []workflowrecovery.OwnerEpochIdentity) ([]string, error) {
	if len(owners) != 0 {
		return nil, errors.New("a fresh product turn workflow has no prior owner epochs to drain")
	}
	return nil, nil
}

// productTurnInitialTranscript verifies the first checkpoint progress of a fresh product turn: only
// the empty provider transcript is added, over an unchanged seeded Store and an empty provider journal.
type productTurnInitialTranscript struct {
	pins            workflowrecovery.Pins
	transcript      workflowrecovery.TranscriptPin
	store           *sessionrepo.Store
	storeJournal    *audit.Journal
	providerJournal *audit.Journal
	baseline        *sessionrepo.Generation
}

func (v productTurnInitialTranscript) VerifyProgress(_ context.Context, previous workflowrecovery.Checkpoint,
	next workflowrecovery.Pins) error {
	if v.store == nil || v.storeJournal == nil || v.providerJournal == nil || v.baseline == nil ||
		!reflect.DeepEqual(previous.Pins, v.pins) {
		return workflowrecovery.ErrObservation
	}
	want := cloneWorkflowPins(v.pins)
	want.ProviderTranscripts = []workflowrecovery.TranscriptPin{v.transcript}
	want.ProviderProjectionDigest = workflowrecovery.TranscriptPinsDigest(want.ProviderTranscripts)
	want.ProviderDisposition = workflowrecovery.DispositionAwaitingHostPrompt
	if !reflect.DeepEqual(want, next) {
		return workflowrecovery.ErrObservation
	}
	providerTrace, err := v.providerJournal.Trace()
	if err != nil || len(providerTrace.Records) != 0 || len(providerTrace.Effects) != 0 {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	bundle, err := v.store.EvidenceBundle()
	if err != nil {
		return err
	}
	storeTrace, err := v.storeJournal.Trace()
	if err != nil || uint64(len(storeTrace.Records)) != next.StoreJournalCount ||
		auditHeadHash(storeTrace.Records) != next.StoreJournalHead ||
		bundle.Baseline.ID != v.baseline.ID() || bundle.Baseline.TreeDigest != v.baseline.TreeDigest() {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	disposition, err := sessionrepo.Reconstruct(bundle)
	if err != nil || disposition.Quarantined || disposition.Unknowns != 0 || len(disposition.Operations) != 0 ||
		disposition.Sealed != v.baseline.ID() {
		return errors.Join(workflowrecovery.ErrObservation, err)
	}
	return nil
}
