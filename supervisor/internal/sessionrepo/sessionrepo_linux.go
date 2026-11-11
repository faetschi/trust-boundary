//go:build linux

// Package sessionrepo owns ordinary-byte-copy session generations and their
// approved-delta ledger. Its APIs keep generation descriptors inside the
// trusted supervisor and return operation results only after audit durability.
// It does not establish a read-only Pi mount or protect against another
// process running with the supervisor's UID; those require the runtime layer.
package sessionrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/workspace"
)

const (
	OperationIntentSchemaVersion = "tbound-sessionrepo-operation-intent/v2"
	maxReadBytes                 = 1 << 20
	maxCommandOutputBytes        = 1 << 20
	maxEditReplacements          = 1024
	maxRepositoryState           = 32 << 20
	repositoryStateName          = "repository-state.json"
)

// linuxOPath is Linux O_PATH, which is not exposed by syscall on all Go versions.
const linuxOPath = 0x200000

var (
	ErrInvalidOptions       = errors.New("invalid session repository options")
	ErrUntrustedRoot        = errors.New("session repository root is not private and supervisor-owned")
	ErrNotEmpty             = errors.New("session repository root is not empty")
	ErrClosed               = errors.New("session repository is closed")
	ErrRecoveryPreview      = errors.New("session repository is a read-only recovery preview")
	ErrQuarantined          = errors.New("session repository is quarantined")
	ErrNoBaseline           = errors.New("session repository has no baseline generation")
	ErrBaselineExists       = errors.New("session repository baseline already exists")
	ErrStaleGeneration      = errors.New("operation does not use the current generation")
	ErrGenerationModified   = errors.New("sealed generation changed after promotion")
	ErrInvalidPath          = errors.New("invalid workspace-relative path")
	ErrUnsupportedTarget    = errors.New("operation target is not a supported regular file")
	ErrInvalidEdit          = errors.New("edit replacements must be unique and non-overlapping")
	ErrDenied               = errors.New("operation was denied by the trusted policy authorizer")
	ErrInvalidDecision      = errors.New("operation lacks a matching trusted allow decision")
	ErrOperationReplay      = errors.New("policy decision or broker operation was already used")
	ErrInvalidSettlement    = errors.New("command view is not fully settled")
	ErrViewConsumed         = errors.New("command view has already been consumed")
	ErrViewGeneration       = errors.New("command view is not based on the current generation")
	ErrAudit                = errors.New("durable audit recording failed")
	ErrOperationOutputLimit = errors.New("operation output exceeds its configured limit")
	ErrOutputScopeRequired  = errors.New("Bash operation requires an explicitly declared output scope")
	ErrInvalidOutputScope   = errors.New("operation output scope is invalid or non-canonical")
	ErrDeltaDenied          = errors.New("candidate transition was denied by output-scope admission")
)

// Options pins the policy and metadata identities used by every manifest.
// AuthorizeOperation must establish authority before the operation runs;
// VerifyDecision must authenticate the final exact delta binding. The
// settlement verifier must be backed by the later process/mount observers.
// Journal must be dedicated to this repository: Open treats every unresolved
// or unknown effect in that journal as a recovery failure.
type Options struct {
	PolicyDigest         string
	MetadataPolicyDigest string
	Limits               workspace.Limits
	XattrVisibility      workspace.XattrVisibilityAttestation
	Journal              *audit.Journal
	ExposureBinder       Binder
	AuthorizeOperation   func(OperationRequest) error
	VerifyDecision       delta.DecisionVerifier
	VerifySettlement     func(settlement CommandSettlement, viewID, leaseID string) error
	// AdmissionScope selects the delta-admission rule. The zero value is exact-path-output-scope/v1
	// without protected paths.
	AdmissionScope AdmissionScope
}

// RootAttestation is a caller assertion that a source tree has no active
// writers. Import also checks each object for stability, but cannot establish
// process quiescence by itself.
type RootAttestation struct {
	Quiescent bool
}

// OperationRequest is the data passed to the trusted policy authority before
// an operation can run. ArgumentDigest hashes this package's typed,
// normalized execution inputs; it is not the raw Pi proposal-JSON digest.
// Future provider integration must retain and bind that captured digest
// separately through its trusted argument mapping. ViewID and
// ExecutionContextDigest identify the supervisor's Bash view scope.
type OperationRequest struct {
	Tool                   string                  `json:"tool"`
	Operation              delta.OperationIdentity `json:"operation"`
	Decision               delta.PolicyDecision    `json:"decision"`
	InputGeneration        string                  `json:"input_generation"`
	InputTreeDigest        string                  `json:"input_tree_digest"`
	ArgumentDigest         string                  `json:"argument_digest"`
	ViewID                 string                  `json:"view_id,omitempty"`
	ExecutionContextDigest string                  `json:"execution_context_digest,omitempty"`
	EffectID               string                  `json:"effect_id"`
	OutputScopePaths       []string                `json:"output_scope_paths"`
}

// Replacement is one exact text replacement against the original file bytes.
type Replacement struct {
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

// CommandSettlement is a receipt from the trusted command runner. It is only
// an integration boundary here: a caller-provided boolean is not proof of
// cgroup emptiness or mount detachment.
type CommandSettlement struct {
	LeaseID           string `json:"lease_id"`
	ViewID            string `json:"view_id"`
	ProcessScopeEmpty bool   `json:"process_scope_empty"`
	WritersStopped    bool   `json:"writers_stopped"`
	MountDetached     bool   `json:"mount_detached"`
	ExitObserved      bool   `json:"exit_observed"`
	ExitCode          int    `json:"exit_code"`
	EvidenceClass     string `json:"evidence_class"`
	// SurvivorsTolerated is set only by the no-reap/v1 command cell: the command exited but a
	// descendant and the cell init were left running, so the three postcondition booleans above are
	// false. A consumer that accepts it must have bound the delta itself (VerifySettlement of the
	// production runner does); a runner cannot turn an unsettled command into a settled one by
	// setting it, because every other runner and the replay and recovery paths still require the
	// three booleans.
	SurvivorsTolerated bool `json:"survivors_tolerated,omitempty"`
}

// Closed reports the postconditions of a fully settled command: its process scope is empty, its
// writers are stopped and its mount is detached. A tolerated-survivors settlement is not closed.
func (s CommandSettlement) Closed() bool {
	return s.ProcessScopeEmpty && s.WritersStopped && s.MountDetached
}

// Admitted reports whether the live command path may take this settlement: closed, or left with
// tolerated survivors by the delta the production runner verifies.
func (s CommandSettlement) Admitted() bool {
	if s.SurvivorsTolerated {
		return !s.ProcessScopeEmpty && !s.WritersStopped && !s.MountDetached
	}
	return s.Closed()
}

// CommandResult is bounded output and terminal status returned by a trusted
// runner. A nonzero exit code is still a known command result.
type CommandResult struct {
	ExitCode     int    `json:"exit_code"`
	ExitObserved bool   `json:"exit_observed"`
	Stdout       []byte `json:"stdout,omitempty"`
	Stderr       []byte `json:"stderr,omitempty"`
}

// CommandRunner must start the command in the later registered containment
// profile, stop/reap its complete process scope, detach its mount, and return
// only after those postconditions have been independently observed. Direct
// process launch in package E2E tests checks data flow only.
type CommandRunner interface {
	Run(context.Context, *CommandView, CommandSpec) (CommandResult, CommandSettlement, error)
}

// CommandRunnerProfile is a descriptive label for evidence. It is not proof
// of containment; VerifySettlement remains the authority for postconditions.
type CommandRunnerProfile interface {
	Profile() string
}

// CommandSpec is the exact executable, argument vector, and optional relative
// working directory authorized for one command lease.
type CommandSpec struct {
	Executable          string   `json:"executable"`
	Args                []string `json:"args"`
	WorkingDirectory    string   `json:"working_directory,omitempty"`
	DeclaredOutputPaths []string `json:"-"`
}

// CommandRunnerFunc adapts a function to CommandRunner.
type CommandRunnerFunc func(context.Context, *CommandView, CommandSpec) (CommandResult, CommandSettlement, error)

func (run CommandRunnerFunc) Run(ctx context.Context, view *CommandView, spec CommandSpec) (CommandResult, CommandSettlement, error) {
	return run(ctx, view, spec)
}

func (CommandRunnerFunc) Profile() string { return "direct-uncontained-fixture" }

// MutationResult binds an output generation to its exact approved transition.
// Command is set for a Bash operation.
type MutationResult struct {
	Generation *Generation         `json:"-"`
	Transition delta.Transition    `json:"transition"`
	Command    *CommandResult      `json:"command,omitempty"`
	EffectID   string              `json:"effect_id"`
	Outcome    string              `json:"outcome"`
	Admission  *AdmissionReference `json:"admission,omitempty"`
	// DenyReason is the stable reason code of a denied candidate (DenyReason* constants). It is
	// computed from the admission scope and is empty unless Outcome is denied_after_effect.
	DenyReason string `json:"-"`
}

// OperationEvidence records a durable released result. ResultDigest commits to
// the exact bytes stored as the audit effect result.
type OperationEvidence struct {
	AuditSequence            uint64                  `json:"audit_sequence"`
	Tool                     string                  `json:"tool"`
	EffectID                 string                  `json:"effect_id"`
	Operation                delta.OperationIdentity `json:"operation"`
	DecisionID               string                  `json:"decision_id"`
	ArgumentDigest           string                  `json:"argument_digest"`
	ViewID                   string                  `json:"view_id,omitempty"`
	ExecutionContextDigest   string                  `json:"execution_context_digest,omitempty"`
	InputGeneration          string                  `json:"input_generation"`
	InputTreeDigest          string                  `json:"input_tree_digest"`
	OutputGeneration         string                  `json:"output_generation,omitempty"`
	OutputTreeDigest         string                  `json:"output_tree_digest,omitempty"`
	ResultDigest             string                  `json:"result_digest"`
	Outcome                  string                  `json:"outcome"`
	Admission                *AdmissionReference     `json:"admission,omitempty"`
	ExitCode                 *int                    `json:"exit_code,omitempty"`
	CommandContainmentStatus string                  `json:"command_containment_status,omitempty"`
	CommandRunnerProfile     string                  `json:"command_runner_profile,omitempty"`
}

// GenerationEvidence contains the complete manifest needed to reconstruct
// tree identity, plus its digest. TreeDigest is computed over the canonical
// metadata-policy representation, excluding timestamps.
type GenerationEvidence struct {
	ID         string             `json:"id"`
	TreeDigest string             `json:"tree_digest"`
	Manifest   delta.TreeManifest `json:"manifest"`
}

// EvidenceArtifact is a reproducible local generation report. It makes no
// claim about a real provider exchange, Pi wiring, a read-only Pi mount, or
// kernel-level command containment.
type EvidenceArtifact struct {
	SchemaVersion            string               `json:"schema_version"`
	RealProviderExchange     bool                 `json:"real_provider_exchange"`
	PiAdapterWired           bool                 `json:"pi_adapter_wired"`
	PolicyDigest             string               `json:"policy_digest"`
	MetadataPolicyDigest     string               `json:"metadata_policy_digest"`
	Baseline                 GenerationEvidence   `json:"baseline"`
	Generations              []GenerationEvidence `json:"generations"`
	ApprovedDeltaLedger      []delta.Transition   `json:"approved_delta_ledger"`
	Operations               []OperationEvidence  `json:"operations"`
	ValidatedChain           delta.Result         `json:"validated_chain"`
	CommandContainmentStatus string               `json:"command_containment_status"`
	AuditRecordCount         uint64               `json:"audit_record_count"`
	AuditHeadHash            string               `json:"audit_head_hash"`
}

// Store owns one fresh session repository. The root, generations, and views
// remain private to the supervisor; untrusted workers receive only mount
// sources selected by the trusted runtime layer.
type Store struct {
	mu                           sync.Mutex
	root                         *os.File
	genDir                       *os.File
	viewDir                      *os.File
	exposedDir                   *os.File
	admissionJournal             *audit.Journal
	rootPath                     string
	options                      Options
	baseline                     *Generation
	generations                  []*Generation
	generationEffects            []string
	transitions                  []delta.Transition
	operations                   []OperationEvidence
	usedDecisions                map[string]struct{}
	usedOperations               map[delta.OperationIdentity]struct{}
	usedViewIDs                  map[string]struct{}
	historicalAuthorizations     map[string]historicalAuthorizationEvidence
	usedHistoricalAuthorizations map[string]struct{}
	views                        map[string]*CommandView
	exposedViews                 map[string]*ExposedView
	sealedSources                map[*VerifiedSealedSource]struct{}
	viewIDGenerator              func() (string, error)
	recoveryPreview              bool
	previewJournalHead           string
	previewJournalCount          uint64
	previewAdmissionHead         string
	previewAdmissionCount        uint64
	previewState                 durableRepositoryState
	previewStateExists           bool
	closed                       bool
	quarantined                  bool
}

// Generation is an immutable logical snapshot held by a Store. Its directory
// descriptor is unexported; callers cannot obtain a writable generation path.
type Generation struct {
	store    *Store
	id       string
	root     *os.File
	snapshot workspace.Snapshot
}

// CommandView is a private writable copy of one sealed input generation. Its
// path and descriptor are available only to the trusted CommandRunner.
type CommandView struct {
	store    *Store
	id       string
	input    *Generation
	root     *os.File
	path     string
	consumed bool
	// readOnlyMounts is computed before authorization, so a failure to list it discards the view
	// before any effect instead of failing inside the runner.
	readOnlyMounts []string
}

// Create initializes an empty, exact-mode private repository root. Existing
// content is rejected so stale generations cannot be mistaken for this
// session. The caller retains ownership of root and journal handles.
func Create(root *os.File, options Options) (*Store, error) {
	if err := validateOptions(options); err != nil {
		return nil, err
	}
	if err := validateRepositoryRoot(root); err != nil {
		return nil, err
	}
	workspaceOptions := options.workspaceOptions("store", true)
	empty, err := workspace.Scan(root, workspaceOptions)
	if err != nil {
		return nil, fmt.Errorf("validate repository root: %w", err)
	}
	if len(empty.Manifest.Objects) != 0 {
		return nil, ErrNotEmpty
	}
	rootCopy, err := duplicateDirectory(root, "session-repository-root")
	if err != nil {
		return nil, err
	}
	store := &Store{
		root: rootCopy, rootPath: root.Name(), options: options,
		viewIDGenerator: newRandomViewID,
		views:           make(map[string]*CommandView),
		exposedViews:    make(map[string]*ExposedView),
		sealedSources:   make(map[*VerifiedSealedSource]struct{}),
		usedDecisions:   make(map[string]struct{}), usedOperations: make(map[delta.OperationIdentity]struct{}), usedViewIDs: make(map[string]struct{}),
	}
	if err := syscall.Mkdirat(int(rootCopy.Fd()), "generations", 0o700); err != nil {
		_ = rootCopy.Close()
		return nil, fmt.Errorf("create generation directory: %w", err)
	}
	if err := syscall.Mkdirat(int(rootCopy.Fd()), "views", 0o700); err != nil {
		_ = removeTreeAt(int(rootCopy.Fd()), "generations")
		_ = rootCopy.Close()
		return nil, fmt.Errorf("create command-view directory: %w", err)
	}
	if err := syscall.Mkdirat(int(rootCopy.Fd()), "exposed", 0o700); err != nil {
		_ = removeTreeAt(int(rootCopy.Fd()), "views")
		_ = removeTreeAt(int(rootCopy.Fd()), "generations")
		_ = rootCopy.Close()
		return nil, fmt.Errorf("create exposed-view directory: %w", err)
	}
	store.genDir, err = openDirectoryAt(rootCopy, "generations")
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("open generation directory: %w", err)
	}
	store.viewDir, err = openDirectoryAt(rootCopy, "views")
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("open command-view directory: %w", err)
	}
	store.exposedDir, err = openDirectoryAt(rootCopy, "exposed")
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("open exposed-view directory: %w", err)
	}
	for _, item := range []struct {
		name string
		root *os.File
	}{{"generations", store.genDir}, {"views", store.viewDir}, {"exposed", store.exposedDir}} {
		if err := validatePrivateDirectory(item.root); err != nil {
			_ = store.Close()
			return nil, fmt.Errorf("validate private %s directory: %w", item.name, err)
		}
		snapshot, err := workspace.Scan(item.root, options.workspaceOptions("empty-"+item.name, true))
		if err != nil || len(snapshot.Manifest.Objects) != 0 {
			_ = store.Close()
			if err != nil {
				return nil, fmt.Errorf("validate private %s directory: %w", item.name, err)
			}
			return nil, fmt.Errorf("private %s directory is not empty", item.name)
		}
	}
	if err := createAdmissionJournalAt(rootCopy); err != nil {
		_ = store.Close()
		return nil, err
	}
	store.admissionJournal, err = audit.Open(filepath.Join(store.rootPath, admissionJournalName))
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("open session admission journal: %w", err)
	}
	if err := syncDirectory(rootCopy); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("sync repository root: %w", err)
	}
	return store, nil
}

// Seed imports an independently owned, quiescent source as sealed generation
// g0. Import is a normalized ordinary byte copy; live Git administration and
// object stores are never linked or mounted into this repository.
func (s *Store) Seed(source *os.File, attestation RootAttestation) (*Generation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkMutableLocked(); err != nil {
		return nil, err
	}
	if s.baseline != nil {
		return nil, ErrBaselineExists
	}
	if !attestation.Quiescent {
		return nil, workspace.ErrNotQuiescent
	}
	effectID, err := newEffectID()
	if err != nil {
		return nil, err
	}
	intent, err := json.Marshal(struct {
		Action                  string `json:"action"`
		OutputGeneration        string `json:"output_generation"`
		SourceAttestedQuiescent bool   `json:"source_attested_quiescent"`
	}{"seed", "g0", attestation.Quiescent})
	if err != nil {
		return nil, err
	}
	var generation *Generation
	result, err := s.options.Journal.RunEffect(effectID, intent, func() ([]byte, error) {
		stageName := ".stage-g0"
		stage, err := s.createStageLocked(stageName)
		if err != nil {
			return nil, err
		}
		keepStage := false
		defer func() {
			_ = stage.Close()
			if !keepStage {
				_ = removeTreeAt(int(s.genDir.Fd()), stageName)
			}
		}()
		snapshot, err := workspace.Import(source, stage, s.options.workspaceOptions("g0", true))
		if err != nil {
			return nil, fmt.Errorf("import baseline: %w", err)
		}
		if err := syncStageTree(stage); err != nil {
			return nil, fmt.Errorf("sync baseline directories: %w", err)
		}
		if err := promoteStage(s.genDir, stageName, "g0"); err != nil {
			return nil, fmt.Errorf("promote baseline generation: %w", err)
		}
		keepStage = true
		if err := syncPromotion(s.genDir); err != nil {
			return nil, fmt.Errorf("sync baseline promotion: %w", err)
		}
		generation, err = s.openGenerationLocked("g0", snapshot)
		if err != nil {
			return nil, err
		}
		evidence := GenerationEvidence{ID: "g0", TreeDigest: snapshot.TreeDigest, Manifest: cloneManifest(snapshot.Manifest)}
		return json.Marshal(evidence)
	})
	if err != nil {
		s.quarantined = true
		if generation != nil && generation.root != nil {
			_ = generation.root.Close()
		}
		return nil, fmt.Errorf("%w: baseline: %w", ErrAudit, err)
	}
	if generation == nil || len(result) == 0 {
		s.quarantined = true
		return nil, fmt.Errorf("%w: empty durable baseline result", ErrAudit)
	}
	candidateGenerations := []*Generation{generation}
	candidateEffects := []string{effectID}
	if err := s.persistCandidateStateLocked(candidateGenerations, nil, candidateEffects); err != nil {
		s.quarantined = true
		_ = generation.root.Close()
		return nil, fmt.Errorf("persist baseline manifest and head: %w", err)
	}
	s.baseline = generation
	s.generations = candidateGenerations
	s.generationEffects = candidateEffects
	return generation, nil
}

// ID returns the supervisor-assigned generation label.
func (g *Generation) ID() string { return g.id }

// TreeDigest returns the canonical complete-tree digest.
func (g *Generation) TreeDigest() string { return g.snapshot.TreeDigest }

// Manifest returns a defensive copy of the complete generation manifest.
func (g *Generation) Manifest() delta.TreeManifest { return cloneManifest(g.snapshot.Manifest) }

// Read returns bounded file bytes from a verified sealed generation. The
// caller's correlated identity and policy receipt are durably journaled before
// the bytes are released.
func (g *Generation) Read(ctx context.Context, operation delta.OperationIdentity, decision delta.PolicyDecision, path string, maxBytes int64) ([]byte, error) {
	if g == nil || g.store == nil {
		return nil, ErrNoBaseline
	}
	return g.store.read(ctx, g, operation, decision, path, maxBytes)
}

// ReadOnlyMountSource returns an O_PATH directory descriptor for a later
// trusted bind-mount setup. It is not itself a read-only view: the runtime must
// bind it read-only into Pi and verify that effective mount before session use.
// Never pass this descriptor directly to untrusted Pi code.
func (g *Generation) ReadOnlyMountSource() (*os.File, error) {
	if g == nil || g.store == nil {
		return nil, ErrNoBaseline
	}
	g.store.mu.Lock()
	defer g.store.mu.Unlock()
	if err := g.store.checkMutableLocked(); err != nil {
		return nil, err
	}
	if err := g.store.verifyGenerationLocked(g); err != nil {
		return nil, err
	}
	fd, err := syscall.Openat(int(g.store.genDir.Fd()), g.id, linuxOPath|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open sealed generation mount source: %w", err)
	}
	return os.NewFile(uintptr(fd), "sealed-generation-mount-source"), nil
}

// IsRecoveryPreview reports whether this Store has been reconstructed for
// read-only validation but has not yet been activated by the trusted recovery
// composition. It conveys no authority by itself.
func (s *Store) IsRecoveryPreview() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recoveryPreview
}

// Edit applies exact non-overlapping replacements against the original file
// bytes in a fresh private copy, then promotes and journals the sealed result.
func (s *Store) Edit(ctx context.Context, input *Generation, operation delta.OperationIdentity, decision delta.PolicyDecision, path string, edits []Replacement) (MutationResult, error) {
	if err := validateRelativePath(path); err != nil {
		return MutationResult{}, err
	}
	if len(edits) == 0 || len(edits) > maxEditReplacements {
		return MutationResult{}, ErrInvalidEdit
	}
	// The receipt digest and the later file change must use the same snapshot
	// even if the caller reuses the input slice while authorization runs.
	edits = append([]Replacement(nil), edits...)
	for _, edit := range edits {
		if edit.OldText == "" || !utf8.ValidString(edit.OldText) || !utf8.ValidString(edit.NewText) {
			return MutationResult{}, ErrInvalidEdit
		}
	}
	argumentDigest, err := argumentsDigest(struct {
		Path  string        `json:"path"`
		Edits []Replacement `json:"edits"`
	}{path, edits})
	if err != nil {
		return MutationResult{}, err
	}
	return s.mutate(ctx, input, "edit", operation, decision, argumentDigest, path, func(root *os.File) error {
		original, mode, err := readRegularAt(root, path, s.options.Limits.MaxFileBytes)
		if err != nil {
			return err
		}
		updated, err := applyReplacements(original, edits)
		if err != nil {
			return err
		}
		return replaceRegularAt(root, path, updated, mode)
	})
}

// Write creates or replaces one regular file in a fresh private copy. It does
// not create missing parent directories; those must already be part of the
// approved workspace tree.
func (s *Store) Write(ctx context.Context, input *Generation, operation delta.OperationIdentity, decision delta.PolicyDecision, path string, content []byte) (MutationResult, error) {
	if err := validateRelativePath(path); err != nil {
		return MutationResult{}, err
	}
	if int64(len(content)) > s.options.Limits.MaxFileBytes {
		return MutationResult{}, workspace.ErrLimit
	}
	copyContent := append([]byte(nil), content...)
	argumentDigest, err := argumentsDigest(struct {
		Path          string `json:"path"`
		ContentDigest string `json:"content_digest"`
		ContentBytes  int    `json:"content_bytes"`
	}{path, digestBytes(copyContent), len(copyContent)})
	if err != nil {
		return MutationResult{}, err
	}
	return s.mutate(ctx, input, "write", operation, decision, argumentDigest, path, func(root *os.File) error {
		executableBits := uint32(0)
		if existingExecutableBits, exists, err := regularModeAt(root, path); err != nil {
			return err
		} else if exists {
			executableBits = existingExecutableBits
		}
		return replaceRegularAt(root, path, copyContent, executableBits)
	})
}

// Delete removes one regular file from a fresh private copy of the input
// generation and promotes the sealed result, with the same durable intent,
// ordered transition, approved-delta ledger, and recovery semantics as Write
// and Edit. Deleting a path that is absent, non-regular, linked, or substituted
// is rejected; the sealed input generation is never modified. Parent
// directories are not removed, so create/delete-and-revert fixtures are
// representable as their own recorded transitions (process §5.4).
func (s *Store) Delete(ctx context.Context, input *Generation, operation delta.OperationIdentity, decision delta.PolicyDecision, path string) (MutationResult, error) {
	if err := validateRelativePath(path); err != nil {
		return MutationResult{}, err
	}
	argumentDigest, err := argumentsDigest(struct {
		Path string `json:"path"`
	}{path})
	if err != nil {
		return MutationResult{}, err
	}
	return s.mutate(ctx, input, "delete", operation, decision, argumentDigest, path, func(root *os.File) error {
		return removeRegularAt(root, path)
	})
}

// RunBash supplies an independent ordinary byte-copy view to a trusted
// CommandRunner. Intent is durable before Run is called, and no command result
// is released until the imported gN transition and terminal audit result are
// durable. Runtime containment and settlement evidence remain runner duties.
func (s *Store) RunBash(ctx context.Context, input *Generation, operation delta.OperationIdentity, decision delta.PolicyDecision, spec CommandSpec, runner CommandRunner) (MutationResult, error) {
	if runner == nil {
		return MutationResult{}, ErrInvalidOptions
	}
	// Keep the authorized command immutable even if the caller reuses or
	// modifies its argument slice after this call begins.
	spec = cloneCommandSpec(spec)
	if err := validateCommandSpec(spec); err != nil {
		return MutationResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkMutableLocked(); err != nil {
		return MutationResult{}, err
	}
	if err := s.requireTipLocked(input); err != nil {
		return MutationResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return MutationResult{}, err
	}
	if operation.Kind != delta.OperationLease || strings.TrimSpace(operation.LeaseID) == "" {
		return MutationResult{}, ErrInvalidOptions
	}
	if err := checkAdmissionRecordFits(input.snapshot.Manifest, spec.DeclaredOutputPaths); err != nil {
		return MutationResult{}, err
	}
	view, err := s.newViewLocked(input)
	if err != nil {
		return MutationResult{}, err
	}
	outputID := fmt.Sprintf("g%d", len(s.generations))
	viewSnapshot, err := workspace.Scan(view.root, s.options.workspaceOptions(input.id, true))
	if err != nil {
		return MutationResult{}, s.discardUnstartedViewLocked(view, fmt.Errorf("verify command view before authorization: %w", err))
	}
	if view.readOnlyMounts, err = s.readOnlyMountPathsLocked(view); err != nil {
		return MutationResult{}, s.discardUnstartedViewLocked(view, err)
	}
	argumentDigest, err := commandArgumentsDigest(spec)
	if err != nil {
		return MutationResult{}, s.discardUnstartedViewLocked(view, err)
	}
	effectID, err := newEffectID()
	if err != nil {
		return MutationResult{}, s.discardUnstartedViewLocked(view, err)
	}
	contextDigest, err := commandExecutionContextDigest(view.id, viewSnapshot.TreeDigest, operation.LeaseID)
	if err != nil {
		return MutationResult{}, s.discardUnstartedViewLocked(view, err)
	}
	request := OperationRequest{Tool: "bash", Operation: operation, Decision: decision, InputGeneration: input.id,
		InputTreeDigest: input.snapshot.TreeDigest, ArgumentDigest: argumentDigest, ViewID: view.id,
		OutputScopePaths:       cloneOutputScopePaths(spec.DeclaredOutputPaths),
		ExecutionContextDigest: contextDigest, EffectID: effectID}
	if err := s.authorizeLocked(request); err != nil {
		return MutationResult{}, s.discardUnstartedViewLocked(view, err)
	}
	runnerProfile := "unclassified"
	if profiled, ok := runner.(CommandRunnerProfile); ok {
		profile := profiled.Profile()
		if validIdentity(profile) {
			runnerProfile = profile
		}
	}
	intent, err := json.Marshal(readIntent{SchemaVersion: OperationIntentSchemaVersion,
		Request: request, Command: spec, RunnerProfile: runnerProfile})
	if err != nil {
		return MutationResult{}, s.discardUnstartedViewLocked(view, err)
	}
	var committed MutationResult
	resultBytes, effectErr := s.options.Journal.RunEffect(effectID, intent, func() ([]byte, error) {
		runnerSpec := cloneCommandSpec(spec)
		commandResult, settlement, runErr := runner.Run(ctx, view, runnerSpec)
		if runErr != nil {
			return nil, fmt.Errorf("command runner failed: %w", runErr)
		}
		if !sameCommandSpec(runnerSpec, spec) {
			return nil, errors.New("command runner modified the authorized command spec")
		}
		if int64(len(commandResult.Stdout)) > maxCommandOutputBytes || int64(len(commandResult.Stderr)) > maxCommandOutputBytes || !commandResult.ExitObserved {
			return nil, ErrOperationOutputLimit
		}
		if settlement.ExitCode != commandResult.ExitCode || !settlement.ExitObserved || settlement.LeaseID != operation.LeaseID ||
			settlement.ViewID != view.id || !settlement.Admitted() {
			return nil, ErrInvalidSettlement
		}
		if strings.TrimSpace(settlement.EvidenceClass) == "" {
			return nil, ErrInvalidSettlement
		}
		if err := s.options.VerifySettlement(settlement, view.id, operation.LeaseID); err != nil {
			return nil, fmt.Errorf("verify command settlement: %w", err)
		}
		if err := s.verifyGenerationLocked(input); err != nil {
			return nil, err
		}
		stageName := ".stage-" + outputID
		stage, err := s.createStageLocked(stageName)
		if err != nil {
			return nil, err
		}
		keepStage := false
		stageClosed := false
		defer func() {
			if !stageClosed {
				_ = stage.Close()
			}
			if !keepStage {
				_ = removeTreeAt(int(s.genDir.Fd()), stageName)
			}
		}()
		snapshot, err := workspace.Import(view.root, stage, s.options.workspaceOptions(outputID, true))
		if err != nil {
			return nil, fmt.Errorf("import settled command view: %w", err)
		}
		transition, _, err := s.transitionLocked(input, outputID, request, snapshot)
		if err != nil {
			return nil, err
		}
		reference, err := AppendAdmissionScoped(s.admissionJournal, request, intent,
			s.options.PolicyDigest, s.options.MetadataPolicyDigest,
			input.snapshot.Manifest, snapshot.Manifest, transition, s.options.AdmissionScope)
		if err != nil {
			return nil, err
		}
		committed = MutationResult{
			Transition: transition, Command: cloneCommandResult(&commandResult),
			EffectID: effectID, Outcome: "success", Admission: &reference,
		}
		payload, err := json.Marshal(commandEffectResult{
			Transition: transition, Admission: reference, Command: commandResult,
			Settlement: settlement, Spec: spec, RunnerProfile: runnerProfile,
		})
		if err != nil {
			return nil, err
		}
		if reference.Outcome == "deny" {
			committed.Outcome = "denied_after_effect"
			_, committed.DenyReason = s.options.AdmissionScope.Decide(request.OutputScopePaths, transition.Changes)
			stageClosed = true
			keepStage = true
			if err := s.cleanDeniedStage(stage, stageName); err != nil {
				return nil, err
			}
			return payload, nil
		}
		if err := syncStageTree(stage); err != nil {
			return nil, fmt.Errorf("sync command generation directories: %w", err)
		}
		if err := promoteStage(s.genDir, stageName, outputID); err != nil {
			return nil, fmt.Errorf("promote command generation: %w", err)
		}
		keepStage = true
		if err := syncPromotion(s.genDir); err != nil {
			return nil, fmt.Errorf("sync command generation promotion: %w", err)
		}
		generation, err := s.openGenerationLocked(outputID, snapshot)
		if err != nil {
			return nil, err
		}
		committed.Generation = generation
		return payload, nil
	})
	if effectErr != nil {
		s.quarantined = true
		if committed.Generation != nil && committed.Generation.root != nil {
			_ = committed.Generation.root.Close()
		}
		return MutationResult{}, fmt.Errorf("%w: %w", ErrAudit, effectErr)
	}
	if len(resultBytes) == 0 {
		s.quarantined = true
		return MutationResult{}, fmt.Errorf("%w: empty durable command result", ErrAudit)
	}
	if committed.Admission != nil && committed.Admission.Outcome == "deny" {
		if err := s.discardViewLocked(view); err != nil {
			s.quarantined = true
			return MutationResult{}, fmt.Errorf("discard denied command view: %w", err)
		}
		if err := s.recordOperationLocked("bash", effectID, operation, decision,
			request.ArgumentDigest, request.ViewID, request.ExecutionContextDigest,
			runnerProfile, input, nil, resultBytes, committed.Command, *committed.Admission); err != nil {
			s.quarantined = true
			return MutationResult{}, fmt.Errorf("%w: record denied command: %w", ErrAudit, err)
		}
		// Retain command output in audit evidence, but withhold it from the caller.
		committed.Command = nil
		return committed, ErrDeltaDenied
	}
	if committed.Generation == nil {
		s.quarantined = true
		return MutationResult{}, fmt.Errorf("%w: command transition missing from durable result", ErrAudit)
	}
	if err := s.discardViewLocked(view); err != nil {
		s.quarantined = true
		_ = committed.Generation.root.Close()
		return MutationResult{}, fmt.Errorf("discard settled command view: %w", err)
	}
	candidateTransitions := append(cloneTransitions(s.transitions), committed.Transition)
	candidateGenerations := append(append([]*Generation(nil), s.generations...), committed.Generation)
	candidateEffects := append(append([]string(nil), s.generationEffects...), effectID)
	if err := s.persistCandidateStateLocked(candidateGenerations, candidateTransitions, candidateEffects); err != nil {
		s.quarantined = true
		_ = committed.Generation.root.Close()
		return MutationResult{}, fmt.Errorf("persist command transition and head: %w", err)
	}
	s.transitions = candidateTransitions
	s.generations = candidateGenerations
	s.generationEffects = candidateEffects
	if err := s.recordOperationLocked("bash", effectID, operation, decision, request.ArgumentDigest, request.ViewID,
		request.ExecutionContextDigest, runnerProfile, input, committed.Generation, resultBytes, committed.Command, *committed.Admission); err != nil {
		s.quarantined = true
		return MutationResult{}, fmt.Errorf("%w: record Bash outcome evidence: %w", ErrAudit, err)
	}
	return committed, nil
}

// Verify rescans every retained generation and validates that every ledger
// object reconciles from g0 to the current tip using the trusted decision
// verifier installed at Store creation.
func (s *Store) Verify() (delta.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkUsableLocked(); err != nil {
		return delta.Result{}, err
	}
	if s.baseline == nil || len(s.generations) == 0 {
		return delta.Result{}, ErrNoBaseline
	}
	for _, generation := range s.generations {
		if err := s.verifyGenerationLocked(generation); err != nil {
			return delta.Result{}, err
		}
	}
	if err := s.validateAdmissionReferencesLocked(); err != nil {
		return delta.Result{}, err
	}
	return s.validateChainLocked(s.generations[len(s.generations)-1].snapshot)
}

// BaselineGeneration returns the retained immutable generation that anchors
// this Store's verified history. The returned handle grants only the existing
// sealed-generation read APIs; it does not expose the Store root or any
// writable path.
func (s *Store) BaselineGeneration() *Generation {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.quarantined {
		return nil
	}
	return s.baseline
}

// TipGeneration returns the current immutable generation after a successful
// Store create/open. Callers must still call Verify before using the handle as
// recovery authority; this accessor only exposes the Store's reconstructed tip.
func (s *Store) TipGeneration() *Generation {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.quarantined || len(s.generations) == 0 {
		return nil
	}
	return s.generations[len(s.generations)-1]
}

// Evidence returns defensive copies of all manifests, transitions, operation
// digests, and the independently recomputed chain result.
func (s *Store) Evidence() (EvidenceArtifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkUsableLocked(); err != nil {
		return EvidenceArtifact{}, err
	}
	if s.baseline == nil || len(s.generations) == 0 {
		return EvidenceArtifact{}, ErrNoBaseline
	}
	for _, generation := range s.generations {
		if err := s.verifyGenerationLocked(generation); err != nil {
			return EvidenceArtifact{}, err
		}
	}
	if err := s.validateAdmissionReferencesLocked(); err != nil {
		return EvidenceArtifact{}, err
	}
	chain, err := s.validateChainLocked(s.generations[len(s.generations)-1].snapshot)
	if err != nil {
		return EvidenceArtifact{}, err
	}
	trace, err := s.options.Journal.Trace()
	if err != nil {
		return EvidenceArtifact{}, fmt.Errorf("verify evidence journal: %w", err)
	}
	return s.artifactLocked(chain, trace), nil
}

// VerifiedEvidence holds the chain, artifact, and bundle reports from one
// VerifyEvidence pass. Each report owns its slices, so mutating one cannot change
// another report or the Store.
type VerifiedEvidence struct {
	Chain    delta.Result
	Artifact EvidenceArtifact
	Bundle   EvidenceBundle
}

// VerifyEvidence returns the chain, artifact, and bundle reports from one locked
// verification pass. Trusted decision verifiers run before the single generation
// sweep, so a file, content, or directory-name change they make is caught by this
// same call. Any failure returns zero reports. A quarantined store is refused here;
// the forensic EvidenceBundle still reconstructs it.
func (s *Store) VerifyEvidence() (VerifiedEvidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkUsableLocked(); err != nil {
		return VerifiedEvidence{}, err
	}
	if s.baseline == nil || len(s.generations) == 0 {
		return VerifiedEvidence{}, ErrNoBaseline
	}
	// A chain error must not skip the sweep: a verifier may have changed a retained
	// generation, and preexisting tampering must quarantine the Store and win the error.
	chain, chainErr := s.validateChainLocked(s.generations[len(s.generations)-1].snapshot)
	for _, generation := range s.generations {
		if err := s.verifyGenerationLocked(generation); err != nil {
			return VerifiedEvidence{}, err
		}
	}
	if chainErr != nil {
		return VerifiedEvidence{}, chainErr
	}
	primary, admission, err := s.traceEvidenceJournalsLocked()
	if err != nil {
		return VerifiedEvidence{}, err
	}
	bundle, err := s.bundleLocked(chain, primary, admission)
	if err != nil {
		return VerifiedEvidence{}, err
	}
	return VerifiedEvidence{Chain: cloneChainResult(chain), Artifact: s.artifactLocked(chain, primary), Bundle: bundle}, nil
}

// traceEvidenceJournalsLocked reads both retained journals once for one evidence
// report. The caller must hold Store.mu.
func (s *Store) traceEvidenceJournalsLocked() (audit.Trace, audit.Trace, error) {
	primary, err := s.options.Journal.Trace()
	if err != nil {
		return audit.Trace{}, audit.Trace{}, fmt.Errorf("verify evidence journal: %w", err)
	}
	admission, err := s.admissionJournal.Trace()
	if err != nil {
		return audit.Trace{}, audit.Trace{}, fmt.Errorf("verify evidence admission journal: %w", err)
	}
	return primary, admission, nil
}

// artifactLocked builds the standalone report from an already validated chain. It
// never serializes the journals, so it carries no bundle size limit. The caller
// must hold Store.mu.
func (s *Store) artifactLocked(chain delta.Result, primary audit.Trace) EvidenceArtifact {
	generations := s.generationEvidenceLocked()
	var auditHead string
	if len(primary.Records) > 0 {
		auditHead = primary.Records[len(primary.Records)-1].Hash
	}
	return EvidenceArtifact{
		SchemaVersion:        "tbound-sessionrepo-evidence/v2",
		RealProviderExchange: false, PiAdapterWired: false,
		CommandContainmentStatus: "not-established",
		AuditRecordCount:         uint64(len(primary.Records)), AuditHeadHash: auditHead,
		PolicyDigest: s.options.PolicyDigest, MetadataPolicyDigest: s.options.MetadataPolicyDigest,
		Baseline: cloneGenerationEvidence(generations[0]), Generations: generations,
		ApprovedDeltaLedger: cloneTransitions(s.transitions), Operations: cloneOperations(s.operations),
		ValidatedChain: cloneChainResult(chain),
	}
}

// generationEvidenceLocked returns defensive copies of every retained generation.
// The caller must hold Store.mu.
func (s *Store) generationEvidenceLocked() []GenerationEvidence {
	generations := make([]GenerationEvidence, 0, len(s.generations))
	for _, generation := range s.generations {
		generations = append(generations, GenerationEvidence{
			ID: generation.id, TreeDigest: generation.snapshot.TreeDigest,
			Manifest: cloneManifest(generation.snapshot.Manifest),
		})
	}
	return generations
}

// cloneChainResult copies every slice-backed field. Nil and empty slices stay
// distinct so the copy compares equal to the validated original.
func cloneChainResult(result delta.Result) delta.Result {
	result.FinalManifest.Objects = cloneSliceKeepingNil(result.FinalManifest.Objects)
	result.ComposedChanges = cloneSliceKeepingNil(result.ComposedChanges)
	result.TouchedPaths = cloneSliceKeepingNil(result.TouchedPaths)
	return result
}

func cloneSliceKeepingNil[T any](values []T) []T {
	if values == nil {
		return nil
	}
	return append(make([]T, 0, len(values)), values...)
}

// Close releases the retained directory descriptors. The caller closes the
// audit journal separately. No pending command view is silently imported.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if len(s.sealedSources) != 0 {
		return ErrSealedSourceOutstanding
	}
	s.closed = true
	var closeErrors []error
	for _, view := range s.views {
		if view.root != nil {
			closeErrors = append(closeErrors, view.root.Close())
		}
	}
	for _, generation := range s.generations {
		if generation.root != nil {
			closeErrors = append(closeErrors, generation.root.Close())
		}
	}
	for _, view := range s.exposedViews {
		if err := view.closeLocked(); err != nil {
			closeErrors = append(closeErrors, err)
			// Store.Close cannot leave an open descriptor that suggests a later
			// retry is possible. Any unremoved entry remains in exposed/ and
			// causes Open to fail closed.
			if view.root != nil {
				closeErrors = append(closeErrors, view.root.Close())
				view.root = nil
			}
			view.closed = true
			view.selected = nil
			view.observed = GenerationEvidence{}
			delete(s.exposedViews, view.name)
		}
	}
	if s.admissionJournal != nil {
		closeErrors = append(closeErrors, s.admissionJournal.Close())
	}
	for _, file := range []*os.File{s.exposedDir, s.viewDir, s.genDir, s.root} {
		if file != nil {
			closeErrors = append(closeErrors, file.Close())
		}
	}
	return errors.Join(closeErrors...)
}

type commandEffectResult struct {
	Transition    delta.Transition   `json:"transition"`
	Admission     AdmissionReference `json:"admission"`
	Command       CommandResult      `json:"command"`
	Settlement    CommandSettlement  `json:"settlement"`
	Spec          CommandSpec        `json:"spec"`
	RunnerProfile string             `json:"runner_profile"`
}

type durableRepositoryState struct {
	SchemaVersion       string               `json:"schema_version"`
	HeadGeneration      string               `json:"head_generation"`
	HeadTreeDigest      string               `json:"head_tree_digest"`
	Generations         []GenerationEvidence `json:"generations"`
	GenerationEffects   []string             `json:"generation_effects"`
	ApprovedDeltaLedger []delta.Transition   `json:"approved_delta_ledger"`
}

// RequireCanonicalSource proves that the source root a generation was seeded from is already in
// the canonical form later steps demand of it: regular files 0644/0755, owned by the supervisor,
// and the same tree digest as the generation. Seed normalizes whatever it imports, so a source
// with other modes (for example 0600) is admitted and seeded, but the no-change release rescan and
// the publication baseline scan of that same root require the canonical form and would refuse it
// only after the whole run. Calling this right after Seed refuses such a root before any work.
func (s *Store) RequireCanonicalSource(source *os.File, generation *Generation) error {
	if s == nil || source == nil || generation == nil {
		return workspace.ErrUntrustedRoot
	}
	s.mu.Lock()
	options := s.options.workspaceOptions(generation.id, true)
	s.mu.Unlock()
	snapshot, err := workspace.Scan(source, options)
	if err != nil {
		return fmt.Errorf("source root is not in canonical form: %w", err)
	}
	if snapshot.TreeDigest != generation.snapshot.TreeDigest {
		return errors.New("source root differs from the seeded generation")
	}
	return nil
}
