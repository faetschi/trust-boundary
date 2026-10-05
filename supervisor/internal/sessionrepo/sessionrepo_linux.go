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
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/workspace"
)

const (
	maxReadBytes          = 1 << 20
	maxCommandOutputBytes = 1 << 20
	maxEditReplacements   = 1024
	maxRepositoryState    = 32 << 20
	repositoryStateName   = "repository-state.json"
)

// linuxOPath is Linux O_PATH, which is not exposed by syscall on all Go versions.
const linuxOPath = 0x200000

var (
	ErrInvalidOptions       = errors.New("invalid session repository options")
	ErrUntrustedRoot        = errors.New("session repository root is not private and supervisor-owned")
	ErrNotEmpty             = errors.New("session repository root is not empty")
	ErrClosed               = errors.New("session repository is closed")
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
	AuthorizeOperation   func(OperationRequest) error
	VerifyDecision       delta.DecisionVerifier
	VerifySettlement     func(settlement CommandSettlement, viewID, leaseID string) error
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
	Executable       string   `json:"executable"`
	Args             []string `json:"args"`
	WorkingDirectory string   `json:"working_directory,omitempty"`
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
	Generation *Generation      `json:"-"`
	Transition delta.Transition `json:"transition"`
	Command    *CommandResult   `json:"command,omitempty"`
	EffectID   string           `json:"effect_id"`
	Outcome    string           `json:"outcome"`
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
	mu                sync.Mutex
	root              *os.File
	genDir            *os.File
	viewDir           *os.File
	rootPath          string
	options           Options
	baseline          *Generation
	generations       []*Generation
	generationEffects []string
	transitions       []delta.Transition
	operations        []OperationEvidence
	usedDecisions     map[string]struct{}
	usedOperations    map[delta.OperationIdentity]struct{}
	usedViewIDs       map[string]struct{}
	views             map[string]*CommandView
	viewIDGenerator   func() (string, error)
	closed            bool
	quarantined       bool
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
	for _, item := range []struct {
		name string
		root *os.File
	}{{"generations", store.genDir}, {"views", store.viewDir}} {
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
	if err := s.checkUsableLocked(); err != nil {
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
		if err := syncTreeDirectories(stage); err != nil {
			return nil, fmt.Errorf("sync baseline directories: %w", err)
		}
		if err := renameNoReplace(s.genDir, stageName, "g0"); err != nil {
			return nil, fmt.Errorf("promote baseline generation: %w", err)
		}
		keepStage = true
		if err := syncDirectory(s.genDir); err != nil {
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
	if err := g.store.checkUsableLocked(); err != nil {
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
	return s.mutate(ctx, input, "edit", operation, decision, argumentDigest, func(root *os.File) error {
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
	return s.mutate(ctx, input, "write", operation, decision, argumentDigest, func(root *os.File) error {
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
	return s.mutate(ctx, input, "delete", operation, decision, argumentDigest, func(root *os.File) error {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkUsableLocked(); err != nil {
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
	if err := validateCommandSpec(spec); err != nil {
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
	intent, err := json.Marshal(struct {
		Request       OperationRequest `json:"request"`
		Command       CommandSpec      `json:"command"`
		RunnerProfile string           `json:"runner_profile"`
	}{request, spec, runnerProfile})
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
			settlement.ViewID != view.id || !settlement.ProcessScopeEmpty || !settlement.WritersStopped || !settlement.MountDetached {
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
		defer func() {
			_ = stage.Close()
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
		if err := syncTreeDirectories(stage); err != nil {
			return nil, fmt.Errorf("sync command generation directories: %w", err)
		}
		if err := renameNoReplace(s.genDir, stageName, outputID); err != nil {
			return nil, fmt.Errorf("promote command generation: %w", err)
		}
		keepStage = true
		if err := syncDirectory(s.genDir); err != nil {
			return nil, fmt.Errorf("sync command generation promotion: %w", err)
		}
		generation, err := s.openGenerationLocked(outputID, snapshot)
		if err != nil {
			return nil, err
		}
		committed = MutationResult{Generation: generation, Transition: transition, Command: cloneCommandResult(&commandResult), EffectID: effectID, Outcome: "success"}
		payload, err := json.Marshal(commandEffectResult{Transition: transition, Command: commandResult, Settlement: settlement, Spec: spec, RunnerProfile: runnerProfile})
		if err != nil {
			return nil, err
		}
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
		request.ExecutionContextDigest, runnerProfile, input, committed.Generation, resultBytes, committed.Command); err != nil {
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
	return s.validateChainLocked(s.generations[len(s.generations)-1].snapshot)
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
	chain, err := s.validateChainLocked(s.generations[len(s.generations)-1].snapshot)
	if err != nil {
		return EvidenceArtifact{}, err
	}
	trace, err := s.options.Journal.Trace()
	if err != nil {
		return EvidenceArtifact{}, fmt.Errorf("verify evidence journal: %w", err)
	}
	var auditHead string
	if len(trace.Records) > 0 {
		auditHead = trace.Records[len(trace.Records)-1].Hash
	}
	generations := make([]GenerationEvidence, 0, len(s.generations))
	for _, generation := range s.generations {
		generations = append(generations, GenerationEvidence{
			ID: generation.id, TreeDigest: generation.snapshot.TreeDigest,
			Manifest: cloneManifest(generation.snapshot.Manifest),
		})
	}
	artifact := EvidenceArtifact{
		SchemaVersion:        "tbound-sessionrepo-evidence/v1",
		RealProviderExchange: false, PiAdapterWired: false,
		CommandContainmentStatus: "not-established",
		AuditRecordCount:         uint64(len(trace.Records)), AuditHeadHash: auditHead,
		PolicyDigest: s.options.PolicyDigest, MetadataPolicyDigest: s.options.MetadataPolicyDigest,
		Baseline: generations[0], Generations: generations,
		ApprovedDeltaLedger: cloneTransitions(s.transitions), Operations: cloneOperations(s.operations),
		ValidatedChain: chain,
	}
	return artifact, nil
}

// Close releases the retained directory descriptors. The caller closes the
// audit journal separately. No pending command view is silently imported.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
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
	for _, file := range []*os.File{s.viewDir, s.genDir, s.root} {
		if file != nil {
			closeErrors = append(closeErrors, file.Close())
		}
	}
	return errors.Join(closeErrors...)
}

type commandEffectResult struct {
	Transition    delta.Transition  `json:"transition"`
	Command       CommandResult     `json:"command"`
	Settlement    CommandSettlement `json:"settlement"`
	Spec          CommandSpec       `json:"spec"`
	RunnerProfile string            `json:"runner_profile"`
}

type durableRepositoryState struct {
	SchemaVersion       string               `json:"schema_version"`
	HeadGeneration      string               `json:"head_generation"`
	HeadTreeDigest      string               `json:"head_tree_digest"`
	Generations         []GenerationEvidence `json:"generations"`
	GenerationEffects   []string             `json:"generation_effects"`
	ApprovedDeltaLedger []delta.Transition   `json:"approved_delta_ledger"`
}
