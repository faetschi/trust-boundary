//go:build linux

package sessionrepo

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/workspace"
)

// Open reconstructs one existing session repository from its private cache and
// the verified effects in the same locked audit journal. Unknown outcomes,
// unreferenced generations, or leftover writable views quarantine the store.
func Open(root *os.File, options Options) (*Store, error) {
	if err := validateOptions(options); err != nil {
		return nil, err
	}
	if err := validateRepositoryRoot(root); err != nil {
		return nil, err
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
	store.genDir, err = openDirectoryAt(rootCopy, "generations")
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("open repository generations: %w", err)
	}
	store.viewDir, err = openDirectoryAt(rootCopy, "views")
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("open repository views: %w", err)
	}
	if err := validatePrivateDirectory(store.genDir); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := validatePrivateDirectory(store.viewDir); err != nil {
		_ = store.Close()
		return nil, err
	}
	rootNames, err := directoryNames(rootCopy)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	rootEntries := make(map[string]bool, len(rootNames))
	for _, name := range rootNames {
		rootEntries[name] = true
	}
	if len(rootNames) < 2 || len(rootNames) > 3 || !rootEntries["generations"] || !rootEntries["views"] ||
		(len(rootNames) == 3 && !rootEntries[repositoryStateName]) {
		_ = store.Close()
		return nil, errors.New("repository root contains an unknown entry")
	}
	if err := store.recoverLocked(); err != nil {
		store.quarantined = true
		_ = store.Close()
		return nil, fmt.Errorf("%w: recover session repository: %w", ErrQuarantined, err)
	}
	return store, nil
}

func (s *Store) recoverLocked() error {
	trace, err := s.options.Journal.Trace()
	if err != nil {
		return err
	}
	for _, effect := range trace.Effects {
		if effect.Unresolved || effect.Outcome != "success" {
			return fmt.Errorf("audit effect %q has unresolved or unknown outcome", effect.ID)
		}
	}

	for _, effect := range trace.Effects {
		if !strings.HasPrefix(effect.ID, "sessionrepo-effect-") {
			continue
		}
		if !validSupervisorEffectID(effect.ID) {
			return fmt.Errorf("invalid supervisor effect ID %q", effect.ID)
		}
		if err := s.recoverEffectLocked(effect); err != nil {
			return fmt.Errorf("reconcile effect %s: %w", effect.ID, err)
		}
	}
	for _, record := range trace.Records {
		if record.Event.Kind != "operation_denied" {
			continue
		}
		var denied struct {
			Request OperationRequest `json:"request"`
			Reason  string           `json:"reason"`
		}
		if err := json.Unmarshal(record.Event.Data, &denied); err != nil || !validSupervisorEffectID(denied.Request.EffectID) {
			return fmt.Errorf("malformed denied-operation record %d", record.Sequence)
		}
		if denied.Request.Tool == "bash" {
			if !validSHA256(denied.Request.ExecutionContextDigest) {
				return fmt.Errorf("denied Bash operation %q has no valid execution context", denied.Request.EffectID)
			}
			if err := s.rememberViewIDLocked(denied.Request.ViewID); err != nil {
				return fmt.Errorf("denied Bash operation %q: %w", denied.Request.EffectID, err)
			}
		} else if denied.Request.ViewID != "" || denied.Request.ExecutionContextDigest != "" {
			return fmt.Errorf("denied non-Bash operation %q contains command-view context", denied.Request.EffectID)
		}
		s.operations = append(s.operations, OperationEvidence{
			AuditSequence: record.Sequence, Tool: denied.Request.Tool, EffectID: denied.Request.EffectID,
			Operation: denied.Request.Operation, DecisionID: denied.Request.Decision.ID,
			InputGeneration: denied.Request.InputGeneration, InputTreeDigest: denied.Request.InputTreeDigest,
			ArgumentDigest: denied.Request.ArgumentDigest, ViewID: denied.Request.ViewID,
			ExecutionContextDigest: denied.Request.ExecutionContextDigest,
			ResultDigest:           digestBytes(record.Event.Data), Outcome: "denied",
		})
	}
	sort.Slice(s.operations, func(i, j int) bool { return s.operations[i].AuditSequence < s.operations[j].AuditSequence })
	if err := s.reconcileDirectoryContentsLocked(); err != nil {
		return err
	}
	if len(s.generations) != 0 {
		if _, err := s.validateChainLocked(s.generations[len(s.generations)-1].snapshot); err != nil {
			return err
		}
	}
	state, exists, err := readRepositoryState(s.root)
	if err != nil {
		return err
	}
	if exists {
		if err := stateIsVerifiedPrefix(state, s); err != nil {
			return err
		}
	}
	if len(s.generations) > 0 && (!exists || !stateMatchesStore(state, s)) {
		if err := s.persistCandidateStateLocked(s.generations, s.transitions, s.generationEffects); err != nil {
			return fmt.Errorf("repair durable repository state: %w", err)
		}
	}
	if len(s.generations) == 0 && exists {
		return errors.New("repository state exists without a committed baseline")
	}
	return nil
}

func (s *Store) recoverEffectLocked(effect audit.EffectTrace) error {
	var action struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(effect.Intent, &action); err != nil {
		return fmt.Errorf("decode intent: %w", err)
	}
	if action.Action == "seed" {
		return s.recoverSeedLocked(effect)
	}
	request, readScope, err := decodeOperationIntent(effect.Intent)
	if err != nil {
		return err
	}
	if request.EffectID != effect.ID || !validSupervisorEffectID(request.EffectID) || !validSHA256(request.ArgumentDigest) {
		return errors.New("effect intent is not bound to its supervisor ID and argument digest")
	}
	if err := validateOperation(request.Operation); err != nil {
		return err
	}
	if request.Decision.Outcome != delta.PolicyAllow || request.Decision.PolicyDigest != s.options.PolicyDigest || request.Decision.MetadataPolicyDigest != s.options.MetadataPolicyDigest {
		return ErrInvalidDecision
	}
	if (request.Tool == "bash" && request.Operation.Kind != delta.OperationLease) ||
		((request.Tool == "write" || request.Tool == "edit" || request.Tool == "read") && request.Operation.Kind != delta.OperationProposalCall) {
		return ErrInvalidOptions
	}
	if request.Tool == "bash" {
		if !validViewID(request.ViewID) || !validSHA256(request.ExecutionContextDigest) {
			return errors.New("Bash intent omits its valid view and execution context")
		}
		if err := s.rememberViewIDLocked(request.ViewID); err != nil {
			return fmt.Errorf("Bash intent: %w", err)
		}
	} else if request.ViewID != "" || request.ExecutionContextDigest != "" {
		return errors.New("non-Bash intent contains command-view context")
	}
	if _, exists := s.usedDecisions[request.Decision.ID]; exists {
		return ErrOperationReplay
	}
	if _, exists := s.usedOperations[request.Operation]; exists {
		return ErrOperationReplay
	}
	if err := s.options.AuthorizeOperation(request); err != nil {
		return fmt.Errorf("revalidate trusted operation receipt: %w", err)
	}
	s.usedDecisions[request.Decision.ID] = struct{}{}
	s.usedOperations[request.Operation] = struct{}{}
	input := s.generationByID(request.InputGeneration)
	if input == nil || input.snapshot.TreeDigest != request.InputTreeDigest {
		return ErrStaleGeneration
	}
	if request.Tool == "read" {
		return s.recoverReadLocked(effect, request, readScope, input)
	}
	return s.recoverMutationLocked(effect, request, input, readScope.Command, readScope.RunnerProfile)
}

func (s *Store) recoverSeedLocked(effect audit.EffectTrace) error {
	var intent struct {
		Action                  string `json:"action"`
		OutputGeneration        string `json:"output_generation"`
		SourceAttestedQuiescent bool   `json:"source_attested_quiescent"`
	}
	if err := json.Unmarshal(effect.Intent, &intent); err != nil || intent.Action != "seed" || intent.OutputGeneration != "g0" || !intent.SourceAttestedQuiescent {
		return errors.New("invalid baseline seed intent")
	}
	if s.baseline != nil || len(s.generations) != 0 {
		return ErrBaselineExists
	}
	var evidence GenerationEvidence
	if err := json.Unmarshal(effect.Result, &evidence); err != nil || evidence.ID != "g0" || evidence.Manifest.Generation != "g0" {
		return errors.New("invalid committed baseline evidence")
	}
	actual, err := s.scanGenerationLocked("g0")
	if err != nil {
		return err
	}
	if actual.TreeDigest != evidence.TreeDigest || !sameManifest(actual.Manifest, evidence.Manifest) {
		return ErrGenerationModified
	}
	generation, err := s.openGenerationLocked("g0", actual)
	if err != nil {
		return err
	}
	s.baseline = generation
	s.generations = append(s.generations, generation)
	s.generationEffects = append(s.generationEffects, effect.ID)
	return nil
}

func (s *Store) recoverReadLocked(effect audit.EffectTrace, request OperationRequest, scope readIntent, input *Generation) error {
	if scope.Request.EffectID != request.EffectID || scope.MaxBytes <= 0 || scope.MaxBytes > maxReadBytes || validateRelativePath(scope.Path) != nil {
		return errors.New("invalid read intent scope")
	}
	wantDigest, err := argumentsDigest(struct {
		Path     string `json:"path"`
		MaxBytes int64  `json:"max_bytes"`
	}{scope.Path, scope.MaxBytes})
	if err != nil || wantDigest != request.ArgumentDigest {
		return errors.New("read argument digest does not match its path and bound")
	}
	if err := s.verifyGenerationLocked(input); err != nil {
		return err
	}
	actual, _, err := readRegularAt(input.root, scope.Path, scope.MaxBytes)
	if err != nil || !bytes.Equal(actual, effect.Result) {
		return errors.New("durable read result does not match sealed generation")
	}
	s.operations = append(s.operations, OperationEvidence{
		AuditSequence: effect.OutcomeSequence, Tool: "read", EffectID: effect.ID, Operation: request.Operation, DecisionID: request.Decision.ID,
		InputGeneration: input.id, InputTreeDigest: input.snapshot.TreeDigest, ArgumentDigest: request.ArgumentDigest,
		OutputGeneration: input.id, OutputTreeDigest: input.snapshot.TreeDigest,
		ResultDigest: digestBytes(effect.Result), Outcome: "success",
	})
	return nil
}

func (s *Store) recoverMutationLocked(effect audit.EffectTrace, request OperationRequest, input *Generation, requestedSpec CommandSpec, runnerProfile string) error {
	if len(s.generations) == 0 || input != s.generations[len(s.generations)-1] {
		return ErrStaleGeneration
	}
	outputID := fmt.Sprintf("g%d", len(s.generations))
	var transition delta.Transition
	var command *CommandResult
	if request.Tool == "bash" {
		var result commandEffectResult
		if err := json.Unmarshal(effect.Result, &result); err != nil {
			return fmt.Errorf("decode command effect result: %w", err)
		}
		transition, command = result.Transition, &result.Command
		if int64(len(command.Stdout)) > maxCommandOutputBytes || int64(len(command.Stderr)) > maxCommandOutputBytes || !command.ExitObserved {
			return ErrOperationOutputLimit
		}
		wantArguments, err := commandArgumentsDigest(requestedSpec)
		wantContext, contextErr := commandExecutionContextDigest(request.ViewID, input.snapshot.TreeDigest, request.Operation.LeaseID)
		if err != nil || contextErr != nil || wantArguments != request.ArgumentDigest || wantContext != request.ExecutionContextDigest ||
			!sameCommandSpec(result.Spec, requestedSpec) || result.RunnerProfile != runnerProfile || result.Settlement.LeaseID != request.Operation.LeaseID ||
			result.Settlement.ViewID != request.ViewID ||
			!result.Settlement.ProcessScopeEmpty || !result.Settlement.WritersStopped || !result.Settlement.MountDetached ||
			!result.Settlement.ExitObserved || result.Settlement.ExitCode != command.ExitCode || strings.TrimSpace(result.Settlement.EvidenceClass) == "" {
			return ErrInvalidSettlement
		}
		if err := s.options.VerifySettlement(result.Settlement, request.ViewID, request.Operation.LeaseID); err != nil {
			return fmt.Errorf("revalidate command settlement: %w", err)
		}
	} else if request.Tool == "write" || request.Tool == "edit" {
		if err := json.Unmarshal(effect.Result, &transition); err != nil {
			return fmt.Errorf("decode mutation transition: %w", err)
		}
	} else {
		return fmt.Errorf("unsupported committed tool %q", request.Tool)
	}
	if transition.InputGeneration != input.id || transition.OutputGeneration != outputID ||
		transition.InputTreeDigest != input.snapshot.TreeDigest || transition.Operation != request.Operation ||
		transition.Tool != request.Tool || transition.ArgumentDigest != request.ArgumentDigest ||
		transition.ViewID != request.ViewID || transition.ExecutionContextDigest != request.ExecutionContextDigest ||
		!samePolicyDecision(transition.Decision, request.Decision) {
		return errors.New("transition does not match its durable operation request")
	}
	actual, err := s.scanGenerationLocked(outputID)
	if err != nil {
		return err
	}
	if transition.OutputTreeDigest != actual.TreeDigest || !sameChanges(transition.Changes, diffManifests(input.snapshot.Manifest, actual.Manifest)) {
		return errors.New("transition does not match the complete output manifest")
	}
	candidate := append(cloneTransitions(s.transitions), transition)
	if _, err := s.validateChainForLocked(actual, candidate); err != nil {
		return err
	}
	generation, err := s.openGenerationLocked(outputID, actual)
	if err != nil {
		return err
	}
	s.transitions = candidate
	s.generations = append(s.generations, generation)
	s.generationEffects = append(s.generationEffects, effect.ID)
	var recordedCommand *CommandResult
	if command != nil {
		recordedCommand = cloneCommandResult(command)
	}
	return s.recordOperationLocked(request.Tool, effect.ID, request.Operation, request.Decision, request.ArgumentDigest,
		request.ViewID, request.ExecutionContextDigest, runnerProfile, input, generation, effect.Result, recordedCommand)
}

type readIntent struct {
	Request       OperationRequest `json:"request"`
	Path          string           `json:"path"`
	MaxBytes      int64            `json:"max_bytes"`
	Command       CommandSpec      `json:"command"`
	RunnerProfile string           `json:"runner_profile"`
}

func decodeOperationIntent(data []byte) (OperationRequest, readIntent, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return OperationRequest{}, readIntent{}, err
	}
	if _, exists := envelope["request"]; exists {
		var read readIntent
		if err := json.Unmarshal(data, &read); err != nil {
			return OperationRequest{}, readIntent{}, err
		}
		return read.Request, read, nil
	}
	var request OperationRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return OperationRequest{}, readIntent{}, err
	}
	return request, readIntent{}, nil
}

func (s *Store) reconcileDirectoryContentsLocked() error {
	generationNames, err := directoryNames(s.genDir)
	if err != nil {
		return err
	}
	if len(generationNames) != len(s.generations) {
		return fmt.Errorf("generation directory contains %d entries for %d committed generations", len(generationNames), len(s.generations))
	}
	nameSet := make(map[string]bool, len(generationNames))
	for _, name := range generationNames {
		nameSet[name] = true
	}
	for index, generation := range s.generations {
		if !nameSet[generation.id] || generation.id != fmt.Sprintf("g%d", index) {
			return errors.New("generation directory contains an uncommitted or out-of-order entry")
		}
		if err := s.verifyGenerationLocked(generation); err != nil {
			return err
		}
	}
	viewNames, err := directoryNames(s.viewDir)
	if err != nil {
		return err
	}
	if len(viewNames) != 0 {
		return errors.New("repository contains an unfinished writable command view")
	}
	return nil
}

func (s *Store) scanGenerationLocked(id string) (workspace.Snapshot, error) {
	root, err := openDirectoryAt(s.genDir, id)
	if err != nil {
		return workspace.Snapshot{}, err
	}
	defer root.Close()
	if err := validatePrivateDirectory(root); err != nil {
		return workspace.Snapshot{}, err
	}
	return workspace.Scan(root, s.options.workspaceOptions(id, true))
}

func (s *Store) generationByID(id string) *Generation {
	for _, generation := range s.generations {
		if generation.id == id {
			return generation
		}
	}
	return nil
}

func validatePrivateDirectory(directory *os.File) error {
	var info syscall.Stat_t
	if err := syscall.Fstat(int(directory.Fd()), &info); err != nil {
		return err
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFDIR || info.Uid != uint32(syscall.Geteuid()) ||
		info.Gid != uint32(syscall.Getegid()) || info.Mode&0o7777 != 0o700 {
		return ErrUntrustedRoot
	}
	return nil
}

func (s *Store) persistCandidateStateLocked(generations []*Generation, transitions []delta.Transition, effects []string) error {
	if len(generations) != len(effects) || len(transitions)+1 != len(generations) || len(generations) == 0 {
		return errors.New("invalid repository state candidate")
	}
	evidence := make([]GenerationEvidence, 0, len(generations))
	for _, generation := range generations {
		evidence = append(evidence, GenerationEvidence{
			ID: generation.id, TreeDigest: generation.snapshot.TreeDigest,
			Manifest: cloneManifest(generation.snapshot.Manifest),
		})
	}
	state := durableRepositoryState{
		SchemaVersion:  "tbound-sessionrepo-state/v1",
		HeadGeneration: generations[len(generations)-1].id,
		HeadTreeDigest: generations[len(generations)-1].snapshot.TreeDigest,
		Generations:    evidence, GenerationEffects: append([]string(nil), effects...),
		ApprovedDeltaLedger: cloneTransitions(transitions),
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(encoded) > maxRepositoryState {
		return errors.New("repository state exceeds storage bound")
	}
	return atomicWriteRepositoryState(s.root, encoded)
}

func atomicWriteRepositoryState(root *os.File, encoded []byte) error {
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		return fmt.Errorf("mint repository state staging name: %w", err)
	}
	tempName := ".repository-state-" + hex.EncodeToString(token[:])
	fd, err := syscall.Openat(int(root.Fd()), tempName, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("create repository state staging file: %w", err)
	}
	file := os.NewFile(uintptr(fd), tempName)
	tempExists := true
	defer func() {
		_ = file.Close()
		if tempExists {
			_ = unlinkAt(int(root.Fd()), tempName, 0)
		}
	}()
	if err := writeAll(file, encoded); err != nil {
		return err
	}
	if err := syscall.Fchown(fd, syscall.Geteuid(), syscall.Getegid()); err != nil {
		return err
	}
	if err := syscall.Fchmod(fd, 0o600); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	prior, exists, err := targetIdentity(root, repositoryStateName)
	if err != nil {
		return err
	}
	if exists && (prior.Mode&syscall.S_IFMT != syscall.S_IFREG || prior.Mode&0o7777 != 0o600) {
		return ErrUntrustedRoot
	}
	if !exists {
		// renameat is safe here because the repository root is private and this
		// initial state file is created only by the supervisor.
	}
	if err := syscall.Renameat(int(root.Fd()), tempName, int(root.Fd()), repositoryStateName); err != nil {
		return fmt.Errorf("promote repository state: %w", err)
	}
	tempExists = false
	return syncDirectory(root)
}

func readRepositoryState(root *os.File) (durableRepositoryState, bool, error) {
	fd, err := syscall.Openat(int(root.Fd()), repositoryStateName, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ENOENT) {
		return durableRepositoryState{}, false, nil
	}
	if err != nil {
		return durableRepositoryState{}, false, err
	}
	file := os.NewFile(uintptr(fd), repositoryStateName)
	defer file.Close()
	var info syscall.Stat_t
	if err := syscall.Fstat(fd, &info); err != nil {
		return durableRepositoryState{}, false, err
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFREG || info.Nlink != 1 || info.Uid != uint32(syscall.Geteuid()) ||
		info.Gid != uint32(syscall.Getegid()) || info.Mode&0o7777 != 0o600 || info.Size < 0 || info.Size > maxRepositoryState {
		return durableRepositoryState{}, false, ErrUntrustedRoot
	}
	data, err := io.ReadAll(io.LimitReader(file, maxRepositoryState+1))
	if err != nil || len(data) > maxRepositoryState || int64(len(data)) != info.Size {
		return durableRepositoryState{}, false, errors.Join(err, errors.New("repository state is oversized or changed"))
	}
	var state durableRepositoryState
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return durableRepositoryState{}, false, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return durableRepositoryState{}, false, errors.New("repository state has trailing data")
	}
	canonical, err := json.Marshal(state)
	if err != nil || !bytes.Equal(canonical, data) || state.SchemaVersion != "tbound-sessionrepo-state/v1" {
		return durableRepositoryState{}, false, errors.New("repository state is not canonical or has an unknown schema")
	}
	return state, true, nil
}

func stateIsVerifiedPrefix(state durableRepositoryState, store *Store) error {
	if len(state.Generations) > len(store.generations) || len(state.GenerationEffects) != len(state.Generations) ||
		len(state.ApprovedDeltaLedger)+1 != len(state.Generations) {
		return errors.New("repository state is not a prefix of verified audit history")
	}
	prefix := storeStateFor(store.generations[:len(state.Generations)], store.transitions[:len(state.ApprovedDeltaLedger)], store.generationEffects[:len(state.GenerationEffects)])
	want, err := json.Marshal(prefix)
	if err != nil {
		return err
	}
	got, err := json.Marshal(state)
	if err != nil || !bytes.Equal(got, want) {
		return errors.New("repository state conflicts with verified audit history")
	}
	return nil
}

func stateMatchesStore(state durableRepositoryState, store *Store) bool {
	want, err := json.Marshal(storeStateFor(store.generations, store.transitions, store.generationEffects))
	if err != nil {
		return false
	}
	got, err := json.Marshal(state)
	return err == nil && bytes.Equal(got, want)
}

func storeStateFor(generations []*Generation, transitions []delta.Transition, effects []string) durableRepositoryState {
	state := durableRepositoryState{SchemaVersion: "tbound-sessionrepo-state/v1", GenerationEffects: append([]string(nil), effects...), ApprovedDeltaLedger: cloneTransitions(transitions)}
	for _, generation := range generations {
		state.Generations = append(state.Generations, GenerationEvidence{
			ID: generation.id, TreeDigest: generation.snapshot.TreeDigest,
			Manifest: cloneManifest(generation.snapshot.Manifest),
		})
	}
	if len(generations) > 0 {
		state.HeadGeneration = generations[len(generations)-1].id
		state.HeadTreeDigest = generations[len(generations)-1].snapshot.TreeDigest
	}
	return state
}

func directoryNames(directory *os.File) ([]string, error) {
	fd, err := syscall.Openat(int(directory.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "directory-list")
	defer file.Close()
	names, err := file.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func sameChanges(left, right []delta.ObjectChange) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func samePolicyDecision(left, right delta.PolicyDecision) bool {
	return left.ID == right.ID && left.Outcome == right.Outcome && left.PolicyDigest == right.PolicyDigest &&
		left.MetadataPolicyDigest == right.MetadataPolicyDigest
}

func sameCommandSpec(left, right CommandSpec) bool {
	if left.Executable != right.Executable || left.WorkingDirectory != right.WorkingDirectory ||
		len(left.Args) != len(right.Args) || (left.Args == nil) != (right.Args == nil) {
		return false
	}
	for index := range left.Args {
		if left.Args[index] != right.Args[index] {
			return false
		}
	}
	return true
}

func cloneCommandSpec(spec CommandSpec) CommandSpec {
	if spec.Args != nil {
		arguments := make([]string, len(spec.Args))
		copy(arguments, spec.Args)
		spec.Args = arguments
	}
	return spec
}
