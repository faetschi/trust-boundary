//go:build linux

package sessionrepo

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
	"sort"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/workspace"
)

func validateOptions(options Options) error {
	if !validSHA256(options.PolicyDigest) || !validSHA256(options.MetadataPolicyDigest) ||
		options.Journal == nil || options.AuthorizeOperation == nil || options.VerifyDecision == nil || options.VerifySettlement == nil {
		return ErrInvalidOptions
	}
	if options.Limits.MaxObjects <= 0 || options.Limits.MaxDepth <= 0 ||
		options.Limits.MaxFileBytes <= 0 || options.Limits.MaxTotalBytes <= 0 ||
		options.Limits.MaxFileBytes > options.Limits.MaxTotalBytes ||
		!options.XattrVisibility.Complete || !validSHA256(options.XattrVisibility.ProfileDigest) {
		return ErrInvalidOptions
	}
	return nil
}

func (options Options) workspaceOptions(generation string, quiescent bool) workspace.Options {
	return workspace.Options{
		Generation: generation, MetadataPolicyDigest: options.MetadataPolicyDigest,
		Limits: options.Limits, QuiescentRoot: quiescent,
		XattrVisibility: options.XattrVisibility,
	}
}

func validateRepositoryRoot(root *os.File) error {
	if root == nil {
		return ErrUntrustedRoot
	}
	var info syscall.Stat_t
	if err := syscall.Fstat(int(root.Fd()), &info); err != nil {
		return fmt.Errorf("stat repository root descriptor: %w", err)
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFDIR || info.Uid != uint32(syscall.Geteuid()) ||
		info.Gid != uint32(syscall.Getegid()) || info.Mode&0o7777 != 0o700 {
		return ErrUntrustedRoot
	}
	return nil
}

func (s *Store) checkUsableLocked() error {
	if s.closed {
		return ErrClosed
	}
	if s.quarantined {
		return ErrQuarantined
	}
	return nil
}

func (s *Store) createStageLocked(name string) (*os.File, error) {
	if !validInternalName(name) {
		return nil, ErrInvalidOptions
	}
	if err := syscall.Mkdirat(int(s.genDir.Fd()), name, 0o700); err != nil {
		return nil, fmt.Errorf("create private generation staging directory: %w", err)
	}
	stage, err := openDirectoryAt(s.genDir, name)
	if err != nil {
		_ = removeTreeAt(int(s.genDir.Fd()), name)
		return nil, fmt.Errorf("open private generation staging directory: %w", err)
	}
	return stage, nil
}

func (s *Store) openGenerationLocked(id string, snapshot workspace.Snapshot) (*Generation, error) {
	if !validGenerationID(id) {
		return nil, ErrInvalidOptions
	}
	root, err := openDirectoryAt(s.genDir, id)
	if err != nil {
		return nil, fmt.Errorf("open promoted generation %s: %w", id, err)
	}
	actual, err := workspace.Scan(root, s.options.workspaceOptions(id, true))
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("verify promoted generation %s: %w", id, err)
	}
	if actual.TreeDigest != snapshot.TreeDigest || !sameManifest(actual.Manifest, snapshot.Manifest) {
		_ = root.Close()
		return nil, ErrGenerationModified
	}
	return &Generation{store: s, id: id, root: root, snapshot: cloneSnapshot(snapshot)}, nil
}

func (s *Store) requireTipLocked(generation *Generation) error {
	if s.baseline == nil {
		return ErrNoBaseline
	}
	if generation == nil || generation.store != s || len(s.generations) == 0 ||
		generation != s.generations[len(s.generations)-1] {
		return ErrStaleGeneration
	}
	return s.verifyGenerationLocked(generation)
}

func (s *Store) verifyGenerationLocked(generation *Generation) error {
	if generation == nil || generation.store != s || generation.root == nil {
		return ErrGenerationModified
	}
	var held, named syscall.Stat_t
	if err := syscall.Fstat(int(generation.root.Fd()), &held); err != nil {
		s.quarantined = true
		return fmt.Errorf("stat held generation descriptor: %w", err)
	}
	namedFD, err := syscall.Openat(int(s.genDir.Fd()), generation.id, linuxOPath|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		s.quarantined = true
		return fmt.Errorf("reopen sealed generation name: %w", err)
	}
	err = syscall.Fstat(namedFD, &named)
	_ = syscall.Close(namedFD)
	if err != nil || !sameNode(held, named) {
		s.quarantined = true
		return ErrGenerationModified
	}
	actual, err := workspace.Scan(generation.root, s.options.workspaceOptions(generation.id, true))
	if err != nil {
		s.quarantined = true
		return fmt.Errorf("rescan sealed generation %s: %w", generation.id, err)
	}
	if actual.TreeDigest != generation.snapshot.TreeDigest || !sameManifest(actual.Manifest, generation.snapshot.Manifest) {
		s.quarantined = true
		return ErrGenerationModified
	}
	return nil
}

func (s *Store) read(ctx context.Context, generation *Generation, operation delta.OperationIdentity, decision delta.PolicyDecision, path string, maxBytes int64) ([]byte, error) {
	if err := validateRelativePath(path); err != nil {
		return nil, err
	}
	if maxBytes <= 0 || maxBytes > maxReadBytes {
		return nil, ErrOperationOutputLimit
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkUsableLocked(); err != nil {
		return nil, err
	}
	if err := s.verifyGenerationLocked(generation); err != nil {
		return nil, err
	}
	argumentDigest, err := argumentsDigest(struct {
		Path     string `json:"path"`
		MaxBytes int64  `json:"max_bytes"`
	}{path, maxBytes})
	if err != nil {
		return nil, err
	}
	effectID, err := newEffectID()
	if err != nil {
		return nil, err
	}
	request := OperationRequest{
		Tool: "read", Operation: operation, Decision: decision,
		InputGeneration: generation.id, InputTreeDigest: generation.snapshot.TreeDigest,
		ArgumentDigest: argumentDigest, EffectID: effectID,
	}
	if err := s.authorizeLocked(request); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	intent, err := json.Marshal(struct {
		Request  OperationRequest `json:"request"`
		Path     string           `json:"path"`
		MaxBytes int64            `json:"max_bytes"`
	}{request, path, maxBytes})
	if err != nil {
		return nil, err
	}
	var output []byte
	result, err := s.options.Journal.RunEffect(effectID, intent, func() ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := s.verifyGenerationLocked(generation); err != nil {
			return nil, err
		}
		data, _, err := readRegularAt(generation.root, path, maxBytes)
		if err != nil {
			return nil, err
		}
		if err := s.verifyGenerationLocked(generation); err != nil {
			return nil, err
		}
		output = data
		return data, nil
	})
	if err != nil {
		s.quarantined = true
		return nil, fmt.Errorf("%w: read: %w", ErrAudit, err)
	}
	if !bytes.Equal(result, output) {
		s.quarantined = true
		return nil, fmt.Errorf("%w: durable read result changed", ErrAudit)
	}
	if err := s.recordOperationLocked("read", effectID, operation, decision, request.ArgumentDigest, "", "", "", generation, generation, result, nil); err != nil {
		s.quarantined = true
		return nil, fmt.Errorf("%w: record read outcome evidence: %w", ErrAudit, err)
	}
	return append([]byte(nil), result...), nil
}

func (s *Store) mutate(ctx context.Context, input *Generation, tool string, operation delta.OperationIdentity, decision delta.PolicyDecision, argumentDigest string, apply func(*os.File) error) (MutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkUsableLocked(); err != nil {
		return MutationResult{}, err
	}
	if err := s.requireTipLocked(input); err != nil {
		return MutationResult{}, err
	}
	if operation.Kind != delta.OperationProposalCall {
		return MutationResult{}, ErrInvalidOptions
	}
	effectID, err := newEffectID()
	if err != nil {
		return MutationResult{}, err
	}
	request := OperationRequest{Tool: tool, Operation: operation, Decision: decision, InputGeneration: input.id, InputTreeDigest: input.snapshot.TreeDigest, ArgumentDigest: argumentDigest, EffectID: effectID}
	if err := s.authorizeLocked(request); err != nil {
		return MutationResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return MutationResult{}, err
	}
	outputID := fmt.Sprintf("g%d", len(s.generations))
	intent, err := json.Marshal(request)
	if err != nil {
		return MutationResult{}, err
	}
	var committed MutationResult
	resultBytes, err := s.options.Journal.RunEffect(effectID, intent, func() ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
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
		if _, err := workspace.Import(input.root, stage, s.options.workspaceOptions(input.id, true)); err != nil {
			return nil, fmt.Errorf("copy sealed input generation: %w", err)
		}
		if err := apply(stage); err != nil {
			return nil, err
		}
		snapshot, err := workspace.Scan(stage, s.options.workspaceOptions(outputID, true))
		if err != nil {
			return nil, fmt.Errorf("scan mutation output: %w", err)
		}
		transition, _, err := s.transitionLocked(input, outputID, request, snapshot)
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(transition)
		if err != nil {
			return nil, err
		}
		if err := syncTreeDirectories(stage); err != nil {
			return nil, fmt.Errorf("sync mutation generation directories: %w", err)
		}
		if err := renameNoReplace(s.genDir, stageName, outputID); err != nil {
			return nil, fmt.Errorf("promote mutation generation: %w", err)
		}
		keepStage = true
		if err := syncDirectory(s.genDir); err != nil {
			return nil, fmt.Errorf("sync mutation generation promotion: %w", err)
		}
		generation, err := s.openGenerationLocked(outputID, snapshot)
		if err != nil {
			return nil, err
		}
		committed = MutationResult{Generation: generation, Transition: transition, EffectID: effectID, Outcome: "success"}
		return payload, nil
	})
	if err != nil {
		s.quarantined = true
		if committed.Generation != nil && committed.Generation.root != nil {
			_ = committed.Generation.root.Close()
		}
		return MutationResult{}, fmt.Errorf("%w: %s: %w", ErrAudit, tool, err)
	}
	if len(resultBytes) == 0 {
		s.quarantined = true
		return MutationResult{}, fmt.Errorf("%w: empty durable transition", ErrAudit)
	}
	if committed.Generation == nil {
		s.quarantined = true
		return MutationResult{}, fmt.Errorf("%w: transition missing from durable result", ErrAudit)
	}
	candidateTransitions := append(cloneTransitions(s.transitions), committed.Transition)
	candidateGenerations := append(append([]*Generation(nil), s.generations...), committed.Generation)
	candidateEffects := append(append([]string(nil), s.generationEffects...), effectID)
	if err := s.persistCandidateStateLocked(candidateGenerations, candidateTransitions, candidateEffects); err != nil {
		s.quarantined = true
		_ = committed.Generation.root.Close()
		return MutationResult{}, fmt.Errorf("persist approved transition and head: %w", err)
	}
	s.transitions = candidateTransitions
	s.generations = candidateGenerations
	s.generationEffects = candidateEffects
	if err := s.recordOperationLocked(tool, effectID, operation, decision, request.ArgumentDigest, "", "", "", input, committed.Generation, resultBytes, nil); err != nil {
		s.quarantined = true
		return MutationResult{}, fmt.Errorf("%w: record mutation outcome evidence: %w", ErrAudit, err)
	}
	return committed, nil
}

func (s *Store) authorizeLocked(request OperationRequest) error {
	if err := validateOperation(request.Operation); err != nil {
		return err
	}
	if !validSupervisorEffectID(request.EffectID) || !validSHA256(request.ArgumentDigest) ||
		request.Decision.Outcome != delta.PolicyAllow || request.Decision.PolicyDigest != s.options.PolicyDigest ||
		request.Decision.MetadataPolicyDigest != s.options.MetadataPolicyDigest || !validIdentity(request.Decision.ID) {
		return s.denyLocked(request, ErrInvalidDecision)
	}
	if _, exists := s.usedDecisions[request.Decision.ID]; exists {
		return s.denyLocked(request, ErrOperationReplay)
	}
	if _, exists := s.usedOperations[request.Operation]; exists {
		return s.denyLocked(request, ErrOperationReplay)
	}
	if err := s.options.AuthorizeOperation(request); err != nil {
		return s.denyLocked(request, err)
	}
	s.usedDecisions[request.Decision.ID] = struct{}{}
	s.usedOperations[request.Operation] = struct{}{}
	return nil
}

func (s *Store) denyLocked(request OperationRequest, cause error) error {
	if !validSupervisorEffectID(request.EffectID) {
		return ErrInvalidOptions
	}
	data, err := json.Marshal(struct {
		Request OperationRequest `json:"request"`
		Reason  string           `json:"reason"`
	}{request, cause.Error()})
	if err != nil {
		return err
	}
	record, err := s.options.Journal.Append(audit.Event{Kind: "operation_denied", ID: "denied-" + request.EffectID, Data: data})
	if err != nil {
		s.quarantined = true
		return fmt.Errorf("%w: denial: %w", ErrAudit, err)
	}
	s.operations = append(s.operations, OperationEvidence{
		AuditSequence: record.Sequence, Tool: request.Tool, EffectID: request.EffectID, Operation: request.Operation,
		DecisionID: request.Decision.ID, InputGeneration: request.InputGeneration,
		InputTreeDigest: request.InputTreeDigest, ArgumentDigest: request.ArgumentDigest,
		ViewID: request.ViewID, ExecutionContextDigest: request.ExecutionContextDigest,
		ResultDigest: digestBytes(data), Outcome: "denied",
	})
	return fmt.Errorf("%w: %v", ErrDenied, cause)
}

func (s *Store) transitionLocked(input *Generation, outputID string, request OperationRequest, output workspace.Snapshot) (delta.Transition, delta.Result, error) {
	commandContextValid := request.Tool == "bash" && validViewID(request.ViewID) && validSHA256(request.ExecutionContextDigest)
	noCommandContext := request.ViewID == "" && request.ExecutionContextDigest == ""
	if input == nil || request.InputGeneration != input.id || request.InputTreeDigest != input.snapshot.TreeDigest ||
		!validSHA256(request.ArgumentDigest) || validateOperation(request.Operation) != nil ||
		(request.Tool != "edit" && request.Tool != "write" && request.Tool != "delete" && request.Tool != "bash") ||
		(request.Tool == "bash" && !commandContextValid) || (request.Tool != "bash" && !noCommandContext) ||
		output.Manifest.Generation != outputID || output.Manifest.MetadataPolicyDigest != s.options.MetadataPolicyDigest {
		return delta.Transition{}, delta.Result{}, ErrInvalidOptions
	}
	changes := diffManifests(input.snapshot.Manifest, output.Manifest)
	transition := delta.Transition{
		ID: fmt.Sprintf("transition-%06d", len(s.transitions)+1), Sequence: uint64(len(s.transitions) + 1),
		Tool: request.Tool, ArgumentDigest: request.ArgumentDigest,
		ViewID: request.ViewID, ExecutionContextDigest: request.ExecutionContextDigest,
		InputGeneration: input.id, OutputGeneration: outputID,
		InputTreeDigest: input.snapshot.TreeDigest, OutputTreeDigest: output.TreeDigest,
		Operation: request.Operation, Changes: changes, Decision: request.Decision,
	}
	candidate := append(cloneTransitions(s.transitions), transition)
	chain, err := s.validateChainForLocked(output, candidate)
	if err != nil {
		return delta.Transition{}, delta.Result{}, err
	}
	return transition, chain, nil
}

func (s *Store) validateChainLocked(sealed workspace.Snapshot) (delta.Result, error) {
	return s.validateChainForLocked(sealed, s.transitions)
}

func (s *Store) validateChainForLocked(sealed workspace.Snapshot, transitions []delta.Transition) (delta.Result, error) {
	if s.baseline == nil {
		return delta.Result{}, ErrNoBaseline
	}
	spec := delta.ChainSpec{
		Baseline: cloneManifest(s.baseline.snapshot.Manifest), Sealed: cloneManifest(sealed.Manifest),
		ExpectedBaselineTreeDigest: s.baseline.snapshot.TreeDigest,
		ExpectedSealedTreeDigest:   sealed.TreeDigest,
		PolicyDigest:               s.options.PolicyDigest, MetadataPolicyDigest: s.options.MetadataPolicyDigest,
	}
	return delta.ValidateChain(spec, transitions, s.options.VerifyDecision)
}

func (s *Store) newViewLocked(input *Generation) (*CommandView, error) {
	var name string
	for attempt := 0; attempt < 8; attempt++ {
		generator := s.viewIDGenerator
		if generator == nil {
			generator = newRandomViewID
		}
		candidate, err := generator()
		if err != nil {
			return nil, fmt.Errorf("generate private command-view ID: %w", err)
		}
		if !validViewID(candidate) {
			return nil, ErrInvalidOptions
		}
		if _, seen := s.usedViewIDs[candidate]; seen {
			continue
		}
		err = syscall.Mkdirat(int(s.viewDir.Fd()), candidate, 0o700)
		if errors.Is(err, syscall.EEXIST) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("create private command view: %w", err)
		}
		name = candidate
		s.usedViewIDs[candidate] = struct{}{}
		break
	}
	if name == "" {
		return nil, errors.New("could not allocate a unique private command-view ID")
	}
	root, err := openDirectoryAt(s.viewDir, name)
	if err != nil {
		_ = removeTreeAt(int(s.viewDir.Fd()), name)
		return nil, fmt.Errorf("open private command view: %w", err)
	}
	snapshot, err := workspace.Import(input.root, root, s.options.workspaceOptions(input.id, true))
	if err != nil {
		_ = root.Close()
		_ = removeTreeAt(int(s.viewDir.Fd()), name)
		return nil, fmt.Errorf("prepare ordinary-copy command view: %w", err)
	}
	if snapshot.TreeDigest != input.snapshot.TreeDigest || !sameManifest(snapshot.Manifest, input.snapshot.Manifest) {
		_ = root.Close()
		_ = removeTreeAt(int(s.viewDir.Fd()), name)
		return nil, ErrGenerationModified
	}
	if err := syncTreeDirectories(root); err != nil {
		_ = root.Close()
		_ = removeTreeAt(int(s.viewDir.Fd()), name)
		return nil, fmt.Errorf("sync command view directories: %w", err)
	}
	view := &CommandView{store: s, id: name, input: input, root: root, path: filepath.Join(s.rootPath, "views", name)}
	s.views[name] = view
	return view, nil
}

func (s *Store) rememberViewIDLocked(id string) error {
	if !validViewID(id) {
		return ErrInvalidOptions
	}
	if _, exists := s.usedViewIDs[id]; exists {
		return errors.New("command-view ID is reused in durable history")
	}
	s.usedViewIDs[id] = struct{}{}
	return nil
}

func (view *CommandView) ID() string {
	if view == nil {
		return ""
	}
	return view.id
}

// MountSource returns a duplicate O_PATH descriptor for a trusted launcher to
// bind into a command cell. The runtime must expose only the destination view
// as writable; this source descriptor must not be passed to the command.
func (view *CommandView) MountSource() (*os.File, error) {
	if view == nil || view.store == nil {
		return nil, ErrViewConsumed
	}
	if view.consumed || view.root == nil {
		return nil, ErrViewConsumed
	}
	fd, err := syscall.Openat(int(view.root.Fd()), ".", linuxOPath|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open command-view mount source: %w", err)
	}
	return os.NewFile(uintptr(fd), "command-view-mount-source"), nil
}

func (s *Store) discardViewLocked(view *CommandView) error {
	if view == nil || view.store != s || view.consumed || s.views[view.id] != view {
		return ErrViewConsumed
	}
	view.consumed = true
	delete(s.views, view.id)
	var closeErr error
	if view.root != nil {
		closeErr = view.root.Close()
		view.root = nil
	}
	removeErr := removeTreeAt(int(s.viewDir.Fd()), view.id)
	if err := syncDirectory(s.viewDir); err != nil {
		return errors.Join(closeErr, removeErr, err)
	}
	return errors.Join(closeErr, removeErr)
}

func (s *Store) discardUnstartedViewLocked(view *CommandView, cause error) error {
	if err := s.discardViewLocked(view); err != nil {
		s.quarantined = true
		return errors.Join(cause, fmt.Errorf("discard unauthorized command view: %w", err))
	}
	return cause
}

func (s *Store) recordOperationLocked(tool string, effectID string, operation delta.OperationIdentity, decision delta.PolicyDecision,
	argumentDigest, viewID, executionContextDigest, runnerProfile string, input, output *Generation, result []byte, command *CommandResult) error {
	trace, err := s.options.Journal.Trace()
	if err != nil {
		return err
	}
	var outcomeSequence uint64
	for _, effect := range trace.Effects {
		if effect.ID == effectID && !effect.Unresolved && effect.Outcome == "success" {
			outcomeSequence = effect.OutcomeSequence
			break
		}
	}
	if outcomeSequence == 0 {
		return errors.New("operation has no durable terminal audit outcome")
	}
	evidence := OperationEvidence{
		AuditSequence: outcomeSequence, Tool: tool, EffectID: effectID, Operation: operation, DecisionID: decision.ID,
		ArgumentDigest: argumentDigest, ViewID: viewID, ExecutionContextDigest: executionContextDigest,
		InputGeneration: input.id, InputTreeDigest: input.snapshot.TreeDigest,
		ResultDigest: digestBytes(result), Outcome: "success",
	}
	if output != nil {
		evidence.OutputGeneration = output.id
		evidence.OutputTreeDigest = output.snapshot.TreeDigest
	}
	if command != nil {
		code := command.ExitCode
		evidence.ExitCode = &code
		evidence.CommandContainmentStatus = "not-established"
		evidence.CommandRunnerProfile = runnerProfile
	}
	s.operations = append(s.operations, evidence)
	return nil
}

func (s *Store) testPath() string { return s.rootPath }

func (g *Generation) testPath() string {
	return filepath.Join(g.store.rootPath, "generations", g.id)
}

func (view *CommandView) testPath() string { return view.path }

func newEffectID() (string, error) {
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("mint supervisor effect ID: %w", err)
	}
	return "sessionrepo-effect-" + hex.EncodeToString(token[:]), nil
}

func validSupervisorEffectID(id string) bool {
	if len(id) != len("sessionrepo-effect-")+48 || !strings.HasPrefix(id, "sessionrepo-effect-") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "sessionrepo-effect-"))
	return err == nil
}

func argumentsDigest(arguments any) (string, error) {
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return "", err
	}
	return digestBytes(encoded), nil
}

func commandArgumentsDigest(spec CommandSpec) (string, error) {
	if err := validateCommandSpec(spec); err != nil {
		return "", err
	}
	return argumentsDigest(spec)
}

func commandExecutionContextDigest(viewID, viewTreeDigest, leaseID string) (string, error) {
	if !validViewID(viewID) || !validTreeDigest(viewTreeDigest) || !validIdentity(leaseID) {
		return "", ErrInvalidOptions
	}
	return argumentsDigest(struct {
		ViewID         string `json:"view_id"`
		ViewTreeDigest string `json:"view_tree_digest"`
		LeaseID        string `json:"lease_id"`
	}{viewID, viewTreeDigest, leaseID})
}

func newRandomViewID() (string, error) {
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("mint command-view ID: %w", err)
	}
	return "view-" + hex.EncodeToString(token[:]), nil
}

func validViewID(id string) bool {
	if len(id) != len("view-")+48 || !strings.HasPrefix(id, "view-") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "view-"))
	return err == nil && strings.ToLower(id) == id
}

func validateCommandSpec(spec CommandSpec) error {
	if !validIdentity(spec.Executable) || !filepath.IsAbs(spec.Executable) || filepath.Clean(spec.Executable) != spec.Executable || len(spec.Args) > 128 {
		return ErrInvalidOptions
	}
	if spec.WorkingDirectory != "" && validateRelativePath(spec.WorkingDirectory) != nil {
		return ErrInvalidPath
	}
	total := len(spec.Executable) + len(spec.WorkingDirectory)
	for _, argument := range spec.Args {
		if !utf8.ValidString(argument) || strings.ContainsRune(argument, '\x00') || len(argument) > 4096 {
			return ErrInvalidOptions
		}
		total += len(argument)
	}
	if total > 16<<10 {
		return ErrInvalidOptions
	}
	return nil
}

func validateOperation(operation delta.OperationIdentity) error {
	switch operation.Kind {
	case delta.OperationProposalCall:
		if !validIdentity(operation.ProposalID) || !validIdentity(operation.CallIssuer) || !validIdentity(operation.CallID) || operation.LeaseID != "" {
			return ErrInvalidOptions
		}
	case delta.OperationLease:
		if !validIdentity(operation.LeaseID) || operation.ProposalID != "" || operation.CallIssuer != "" || operation.CallID != "" {
			return ErrInvalidOptions
		}
	default:
		return ErrInvalidOptions
	}
	return nil
}

func validIdentity(value string) bool {
	if value == "" || len(value) > delta.MaxIdentityBytes || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+sha256.Size*2 {
		return false
	}
	encoded := value[len(prefix):]
	if strings.ToLower(encoded) != encoded {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}

func validTreeDigest(value string) bool {
	if !strings.HasPrefix(value, delta.TreeDigestProfile) {
		return false
	}
	return validSHA256("sha256:" + strings.TrimPrefix(value, delta.TreeDigestProfile))
}

func validGenerationID(id string) bool {
	if len(id) < 2 || id[0] != 'g' {
		return false
	}
	for _, r := range id[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func validInternalName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return false
	}
	return utf8.ValidString(name)
}

func validateRelativePath(value string) error {
	if value == "" || len(value) > delta.MaxPathBytes || !utf8.ValidString(value) || strings.HasPrefix(value, "/") || strings.ContainsRune(value, '\\') {
		return ErrInvalidPath
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return ErrInvalidPath
		}
		for _, r := range segment {
			if unicode.IsControl(r) {
				return ErrInvalidPath
			}
		}
	}
	return nil
}

func openDirectoryAt(parent *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func duplicateDirectory(source *os.File, name string) (*os.File, error) {
	fd, err := syscall.Dup(int(source.Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate %s descriptor: %w", name, err)
	}
	syscall.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), name), nil
}

func sameNode(left, right syscall.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode&syscall.S_IFMT == right.Mode&syscall.S_IFMT
}

func cloneSnapshot(snapshot workspace.Snapshot) workspace.Snapshot {
	snapshot.Manifest = cloneManifest(snapshot.Manifest)
	return snapshot
}

func cloneManifest(manifest delta.TreeManifest) delta.TreeManifest {
	manifest.Objects = append([]delta.ManifestObject(nil), manifest.Objects...)
	return manifest
}

func sameManifest(left, right delta.TreeManifest) bool {
	if left.Generation != right.Generation || left.MetadataPolicyDigest != right.MetadataPolicyDigest || len(left.Objects) != len(right.Objects) {
		return false
	}
	for index := range left.Objects {
		if left.Objects[index] != right.Objects[index] {
			return false
		}
	}
	return true
}

func cloneTransitions(transitions []delta.Transition) []delta.Transition {
	cloned := make([]delta.Transition, len(transitions))
	for index, transition := range transitions {
		transition.Changes = append([]delta.ObjectChange(nil), transition.Changes...)
		cloned[index] = transition
	}
	return cloned
}

func cloneOperations(operations []OperationEvidence) []OperationEvidence {
	cloned := append([]OperationEvidence(nil), operations...)
	for index := range cloned {
		if operations[index].ExitCode != nil {
			code := *operations[index].ExitCode
			cloned[index].ExitCode = &code
		}
	}
	return cloned
}

func cloneCommandResult(command *CommandResult) *CommandResult {
	if command == nil {
		return nil
	}
	cloned := *command
	cloned.Stdout = append([]byte(nil), command.Stdout...)
	cloned.Stderr = append([]byte(nil), command.Stderr...)
	return &cloned
}

func diffManifests(before, after delta.TreeManifest) []delta.ObjectChange {
	left := make(map[string]delta.ObjectState, len(before.Objects))
	right := make(map[string]delta.ObjectState, len(after.Objects))
	paths := make(map[string]struct{}, len(before.Objects)+len(after.Objects))
	for _, object := range before.Objects {
		left[object.Path] = object.State
		paths[object.Path] = struct{}{}
	}
	for _, object := range after.Objects {
		right[object.Path] = object.State
		paths[object.Path] = struct{}{}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	changes := make([]delta.ObjectChange, 0)
	for _, path := range ordered {
		oldState, hadOld := left[path]
		newState, hasNew := right[path]
		if !hadOld {
			oldState = delta.ObjectState{}
		}
		if !hasNew {
			newState = delta.ObjectState{}
		}
		if oldState != newState {
			changes = append(changes, delta.ObjectChange{Path: path, Before: oldState, After: newState})
		}
	}
	return changes
}

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func applyReplacements(original []byte, edits []Replacement) ([]byte, error) {
	type span struct {
		start int
		end   int
		text  []byte
	}
	spans := make([]span, 0, len(edits))
	for _, edit := range edits {
		needle := []byte(edit.OldText)
		first := bytes.Index(original, needle)
		if first < 0 || bytes.Index(original[first+len(needle):], needle) >= 0 {
			return nil, ErrInvalidEdit
		}
		spans = append(spans, span{start: first, end: first + len(needle), text: []byte(edit.NewText)})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for index := 1; index < len(spans); index++ {
		if spans[index].start < spans[index-1].end {
			return nil, ErrInvalidEdit
		}
	}
	var output bytes.Buffer
	position := 0
	for _, replacement := range spans {
		output.Write(original[position:replacement.start])
		output.Write(replacement.text)
		position = replacement.end
	}
	output.Write(original[position:])
	return output.Bytes(), nil
}

func readRegularAt(root *os.File, path string, maxBytes int64) ([]byte, uint32, error) {
	parent, leaf, err := openParent(root, path)
	if err != nil {
		return nil, 0, err
	}
	defer parent.Close()
	fd, err := syscall.Openat(int(parent.Fd()), leaf, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, 0, ErrUnsupportedTarget
		}
		return nil, 0, fmt.Errorf("open file without following links: %w", err)
	}
	file := os.NewFile(uintptr(fd), leaf)
	defer file.Close()
	var before syscall.Stat_t
	if err := syscall.Fstat(fd, &before); err != nil {
		return nil, 0, err
	}
	if before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Nlink != 1 || before.Uid != uint32(syscall.Geteuid()) || before.Gid != uint32(syscall.Getegid()) {
		return nil, 0, ErrUnsupportedTarget
	}
	if before.Size < 0 || before.Size > maxBytes {
		return nil, 0, workspace.ErrLimit
	}
	if err := verifyNameIdentity(parent, leaf, before); err != nil {
		return nil, 0, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, 0, err
	}
	if int64(len(data)) > maxBytes || int64(len(data)) != before.Size {
		return nil, 0, workspace.ErrSourceChanged
	}
	var after syscall.Stat_t
	if err := syscall.Fstat(fd, &after); err != nil {
		return nil, 0, err
	}
	if !sameVersion(before, after) {
		return nil, 0, workspace.ErrSourceChanged
	}
	return data, uint32(before.Mode & 0o111), nil
}

func regularModeAt(root *os.File, path string) (uint32, bool, error) {
	parent, leaf, err := openParent(root, path)
	if err != nil {
		return 0, false, err
	}
	defer parent.Close()
	fd, err := syscall.Openat(int(parent.Fd()), leaf, linuxOPath|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, syscall.ENOENT) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	defer syscall.Close(fd)
	var info syscall.Stat_t
	if err := syscall.Fstat(fd, &info); err != nil {
		return 0, false, err
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFREG || info.Nlink != 1 || info.Uid != uint32(syscall.Geteuid()) || info.Gid != uint32(syscall.Getegid()) {
		return 0, false, ErrUnsupportedTarget
	}
	return uint32(info.Mode & 0o111), true, nil
}

func replaceRegularAt(root *os.File, path string, content []byte, executableBits uint32) error {
	parent, leaf, err := openParent(root, path)
	if err != nil {
		return err
	}
	defer parent.Close()
	prior, existed, err := targetIdentity(parent, leaf)
	if err != nil {
		return err
	}
	token := make([]byte, 12)
	if _, err := rand.Read(token); err != nil {
		return fmt.Errorf("generate temporary file identity: %w", err)
	}
	tempName := ".tbound-write-" + hex.EncodeToString(token)
	fd, err := syscall.Openat(int(parent.Fd()), tempName, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary output file: %w", err)
	}
	file := os.NewFile(uintptr(fd), tempName)
	tempExists := true
	defer func() {
		_ = file.Close()
		if tempExists {
			_ = unlinkAt(int(parent.Fd()), tempName, 0)
		}
	}()
	if err := writeAll(file, content); err != nil {
		return err
	}
	if err := syscall.Fchown(fd, syscall.Geteuid(), syscall.Getegid()); err != nil {
		return fmt.Errorf("normalize output ownership: %w", err)
	}
	mode := uint32(0o644)
	if executableBits != 0 {
		mode = 0o755
	}
	if err := syscall.Fchmod(fd, mode); err != nil {
		return fmt.Errorf("normalize output mode: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync output contents: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close output contents: %w", err)
	}
	current, exists, err := targetIdentity(parent, leaf)
	if err != nil {
		return err
	}
	if existed != exists || (existed && !sameNode(prior, current)) {
		return workspace.ErrSourceChanged
	}
	if err := syscall.Renameat(int(parent.Fd()), tempName, int(parent.Fd()), leaf); err != nil {
		return fmt.Errorf("atomically replace file: %w", err)
	}
	tempExists = false
	if err := syncDirectory(parent); err != nil {
		return fmt.Errorf("sync output parent directory: %w", err)
	}
	return nil
}

// removeRegularAt unlinks one regular file beneath a validated descriptor
// chain. A missing, non-regular, linked, or owner-mismatched target is rejected
// so a delete can only remove an object already present in the approved tree;
// the affected parent directory is synced after the unlink so the deletion is
// durable before the generation is promoted.
func removeRegularAt(root *os.File, path string) error {
	parent, leaf, err := openParent(root, path)
	if err != nil {
		return err
	}
	defer parent.Close()
	if _, exists, err := targetIdentity(parent, leaf); err != nil {
		return err
	} else if !exists {
		return ErrUnsupportedTarget
	}
	if err := unlinkAt(int(parent.Fd()), leaf, 0); err != nil {
		return fmt.Errorf("unlink workspace file: %w", err)
	}
	if err := syncDirectory(parent); err != nil {
		return fmt.Errorf("sync unlink parent directory: %w", err)
	}
	return nil
}

func targetIdentity(parent *os.File, name string) (syscall.Stat_t, bool, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, linuxOPath|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, syscall.ENOENT) {
		return syscall.Stat_t{}, false, nil
	}
	if err != nil {
		return syscall.Stat_t{}, false, err
	}
	defer syscall.Close(fd)
	var info syscall.Stat_t
	if err := syscall.Fstat(fd, &info); err != nil {
		return syscall.Stat_t{}, false, err
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFREG || info.Nlink != 1 || info.Uid != uint32(syscall.Geteuid()) || info.Gid != uint32(syscall.Getegid()) {
		return syscall.Stat_t{}, false, ErrUnsupportedTarget
	}
	return info, true, nil
}

func openParent(root *os.File, path string) (*os.File, string, error) {
	if err := validateRelativePath(path); err != nil {
		return nil, "", err
	}
	segments := strings.Split(path, "/")
	current, err := duplicateDirectory(root, "workspace-parent")
	if err != nil {
		return nil, "", err
	}
	for _, segment := range segments[:len(segments)-1] {
		next, err := openDirectoryAt(current, segment)
		_ = current.Close()
		if err != nil {
			return nil, "", fmt.Errorf("open workspace parent %q without following links: %w", segment, err)
		}
		current = next
	}
	return current, segments[len(segments)-1], nil
}

func verifyNameIdentity(parent *os.File, name string, expected syscall.Stat_t) error {
	fd, err := syscall.Openat(int(parent.Fd()), name, linuxOPath|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return workspace.ErrSourceChanged
	}
	defer syscall.Close(fd)
	var actual syscall.Stat_t
	if err := syscall.Fstat(fd, &actual); err != nil || !sameNode(expected, actual) {
		return workspace.ErrSourceChanged
	}
	return nil
}

func sameVersion(left, right syscall.Stat_t) bool {
	return sameNode(left, right) && left.Mode == right.Mode && left.Uid == right.Uid && left.Gid == right.Gid &&
		left.Nlink == right.Nlink && left.Size == right.Size && left.Mtim == right.Mtim && left.Ctim == right.Ctim
}

func writeAll(file *os.File, data []byte) error {
	for len(data) != 0 {
		n, err := file.Write(data)
		if n < 0 || n > len(data) {
			return fmt.Errorf("invalid write count %d", n)
		}
		data = data[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func syncTreeDirectories(root *os.File) error {
	fd, err := syscall.Openat(int(root.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	copy := os.NewFile(uintptr(fd), "sync-tree-directory")
	defer copy.Close()
	entries, err := copy.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		fd, err := syscall.Openat(int(copy.Fd()), entry.Name(), linuxOPath|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return fmt.Errorf("inspect tree entry %q before directory sync: %w", entry.Name(), err)
		}
		var info syscall.Stat_t
		statErr := syscall.Fstat(fd, &info)
		_ = syscall.Close(fd)
		if statErr != nil {
			return statErr
		}
		if info.Mode&syscall.S_IFMT == syscall.S_IFDIR {
			child, err := openDirectoryAt(copy, entry.Name())
			if err != nil {
				return err
			}
			err = syncTreeDirectories(child)
			_ = child.Close()
			if err != nil {
				return err
			}
		} else if info.Mode&syscall.S_IFMT != syscall.S_IFREG || info.Nlink != 1 {
			return workspace.ErrSpecialFile
		}
	}
	return syncDirectory(copy)
}

func syncDirectory(directory *os.File) error {
	if directory == nil {
		return ErrUntrustedRoot
	}
	return directory.Sync()
}

func renameNoReplace(directory *os.File, oldName, newName string) error {
	oldPath, err := syscall.BytePtrFromString(oldName)
	if err != nil {
		return err
	}
	newPath, err := syscall.BytePtrFromString(newName)
	if err != nil {
		return err
	}
	const renameNoReplaceFlag = 1
	_, _, errno := syscall.Syscall6(linuxSysRenameat2,
		uintptr(directory.Fd()), uintptr(unsafe.Pointer(oldPath)),
		uintptr(directory.Fd()), uintptr(unsafe.Pointer(newPath)),
		renameNoReplaceFlag, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func removeTreeAt(parentFD int, name string) error {
	if !validInternalName(name) {
		return ErrInvalidPath
	}
	fd, err := syscall.Openat(parentFD, name, linuxOPath|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, syscall.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	var info syscall.Stat_t
	if err := syscall.Fstat(fd, &info); err != nil {
		_ = syscall.Close(fd)
		return err
	}
	_ = syscall.Close(fd)
	if info.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return unlinkAt(parentFD, name, 0)
	}
	directoryFD, err := syscall.Openat(parentFD, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(directoryFD), name)
	entries, readErr := directory.ReadDir(-1)
	if readErr != nil {
		_ = directory.Close()
		return readErr
	}
	var removeErr error
	for _, entry := range entries {
		if childErr := removeTreeAt(directoryFD, entry.Name()); childErr != nil {
			removeErr = errors.Join(removeErr, childErr)
		}
	}
	closeErr := directory.Close()
	if removeErr != nil || closeErr != nil {
		return errors.Join(removeErr, closeErr)
	}
	return unlinkAt(parentFD, name, 0x200) // AT_REMOVEDIR
}

func unlinkAt(parentFD int, name string, flags uintptr) error {
	path, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_UNLINKAT, uintptr(parentFD), uintptr(unsafe.Pointer(path)), flags)
	if errno != 0 {
		return errno
	}
	return nil
}
