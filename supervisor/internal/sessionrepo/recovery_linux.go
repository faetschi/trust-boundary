//go:build linux

package sessionrepo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/workflowrecovery"
	"tbound/supervisor/internal/workspace"
)

type historicalOperationAuthorization struct {
	SchemaVersion              string                  `json:"schema_version"`
	WorkflowID                 string                  `json:"workflow_id"`
	ConversationID             string                  `json:"conversation_id"`
	ProviderProfileID          string                  `json:"provider_profile_id"`
	EffectID                   string                  `json:"effect_id"`
	DecisionID                 string                  `json:"decision_id"`
	Operation                  delta.OperationIdentity `json:"operation"`
	Tool                       string                  `json:"tool"`
	OutputScopePaths           []string                `json:"output_scope_paths"`
	TypedArgumentDigest        string                  `json:"typed_argument_digest"`
	InputGeneration            string                  `json:"input_generation"`
	InputTreeDigest            string                  `json:"input_tree_digest"`
	GatePolicyDigest           string                  `json:"gate_policy_digest"`
	ProviderDecisionSequence   uint64                  `json:"provider_decision_sequence"`
	ProviderDecisionRecordHash string                  `json:"provider_decision_record_hash"`
	GateDecision               gate.Decision           `json:"gate_decision"`
}

type historicalAuthorizationEvidence struct {
	record audit.Record
	data   historicalOperationAuthorization
}

// Open reconstructs an existing repository for ordinary callers. Recovery
// integrations should use OpenRecoveryPreview, which separates historical
// verification callbacks from live authorization callbacks and performs no
// repository-state repair before the recovery checkpoint is reserved.
func Open(root *os.File, options Options) (*Store, error) {
	return open(root, options, false)
}

// OpenRecoveryPreview reconstructs retained state from exact Store, effect,
// and admission journal facts. It performs no repository-state repair and does
// not invoke any caller-supplied operation, decision, lease, or settlement
// callback. Provider and old command-owner joins are verified separately by
// the concrete workflow recovery orchestrator before receipt reservation.
func OpenRecoveryPreview(root *os.File, options Options) (*Store, error) {
	options.AuthorizeOperation = nil
	options.VerifyDecision = nil
	options.VerifySettlement = nil
	return open(root, options, true)
}

func open(root *os.File, options Options, preview bool) (*Store, error) {
	var optionsErr error
	if preview {
		optionsErr = validateRecoveryPreviewOptions(options)
	} else {
		optionsErr = validateOptions(options)
	}
	if optionsErr != nil {
		return nil, optionsErr
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
		recoveryPreview: preview,
		viewIDGenerator: newRandomViewID,
		views:           make(map[string]*CommandView),
		exposedViews:    make(map[string]*ExposedView),
		sealedSources:   make(map[*VerifiedSealedSource]struct{}),
		usedDecisions:   make(map[string]struct{}), usedOperations: make(map[delta.OperationIdentity]struct{}), usedViewIDs: make(map[string]struct{}),
		historicalAuthorizations:     make(map[string]historicalAuthorizationEvidence),
		usedHistoricalAuthorizations: make(map[string]struct{}),
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
	store.exposedDir, err = openDirectoryAt(rootCopy, "exposed")
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("open repository exposed views: %w", err)
	}
	if err := validatePrivateDirectory(store.genDir); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := validatePrivateDirectory(store.viewDir); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := validatePrivateDirectory(store.exposedDir); err != nil {
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
	if (len(rootNames) != 4 && len(rootNames) != 5) ||
		!rootEntries["generations"] || !rootEntries["views"] || !rootEntries["exposed"] ||
		!rootEntries[admissionJournalName] ||
		(len(rootNames) == 5 && !rootEntries[repositoryStateName]) {
		_ = store.Close()
		return nil, errors.New("repository root contains an unknown entry")
	}
	if err := requireAdmissionJournal(rootCopy); err != nil {
		_ = store.Close()
		return nil, err
	}
	store.admissionJournal, err = audit.Open(filepath.Join(store.rootPath, admissionJournalName))
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("open session admission journal: %w", err)
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
	admissionTrace, err := s.admissionJournal.Trace()
	if err != nil {
		return fmt.Errorf("verify admission journal: %w", err)
	}
	if s.recoveryPreview {
		s.previewJournalHead, s.previewJournalCount = traceHead(trace)
		s.previewAdmissionHead, s.previewAdmissionCount = traceHead(admissionTrace)
		if err := s.indexHistoricalAuthorizationsLocked(trace); err != nil {
			return err
		}
	}
	admissions := &admissionRecovery{records: admissionTrace.Records, consumed: make(map[uint64]struct{})}
	for _, record := range admissions.records {
		if record.Event.Kind != admissionEventKind || record.Event.Outcome != "" {
			return fmt.Errorf("unexpected admission journal record %d", record.Sequence)
		}
		if _, err := decodeAdmissionRecord(record.Event.Data); err != nil {
			return err
		}
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
		if err := s.recoverEffectLocked(effect, admissions); err != nil {
			return fmt.Errorf("reconcile effect %s: %w", effect.ID, err)
		}
	}
	if s.recoveryPreview && len(s.usedHistoricalAuthorizations) != len(s.historicalAuthorizations) {
		return errors.New("Store journal contains a governed authorization without one retained operation effect")
	}
	if len(admissions.consumed) != len(admissions.records) {
		return fmt.Errorf("%w: admission journal has unreferenced records", ErrInvalidAdmission)
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
	if s.recoveryPreview {
		s.previewState, s.previewStateExists = state, exists
	}
	if len(s.generations) > 0 && (!exists || !stateMatchesStore(state, s)) {
		if !s.recoveryPreview {
			if err := s.persistCandidateStateLocked(s.generations, s.transitions, s.generationEffects); err != nil {
				return fmt.Errorf("repair durable repository state: %w", err)
			}
		}
	}
	if len(s.generations) == 0 && exists {
		return errors.New("repository state exists without a committed baseline")
	}
	return nil
}

// ActivateRecovery applies deferred Store-state repair only after verifying the
// exact latest owner-authenticated clean-restart receipt. The journal prefixes
// and decoded repository state must still match the read-only preflight exactly.
func (s *Store) ActivateRecovery(ctx context.Context, ledger *workflowrecovery.Ledger,
	receipt workflowrecovery.Receipt, pins workflowrecovery.Pins, options Options) error {
	if s == nil || ctx == nil || ledger == nil || !receipt.IsCleanRestart() {
		return ErrInvalidOptions
	}
	if err := ledger.VerifyCleanRestartReceipt(ctx, receipt, pins); err != nil {
		return fmt.Errorf("verify durable clean-restart receipt before Store activation: %w", err)
	}
	if err := validateOptions(options); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkUsableLocked(); err != nil {
		return err
	}
	if !s.recoveryPreview {
		return errors.New("session repository is not awaiting recovery activation")
	}
	if options.Journal != s.options.Journal || options.PolicyDigest != s.options.PolicyDigest ||
		options.MetadataPolicyDigest != s.options.MetadataPolicyDigest || options.Limits != s.options.Limits ||
		options.XattrVisibility != s.options.XattrVisibility || !reflect.DeepEqual(options.ExposureBinder, s.options.ExposureBinder) {
		return errors.New("live recovery callbacks changed retained Store identity or profile")
	}
	if err := verifyRecoveryStorePinsLocked(s, pins, options); err != nil {
		return err
	}
	trace, err := options.Journal.Trace()
	if err != nil {
		return err
	}
	storeHead, storeCount := traceHead(trace)
	admissionTrace, err := s.admissionJournal.Trace()
	if err != nil {
		return err
	}
	admissionHead, admissionCount := traceHead(admissionTrace)
	if storeHead != s.previewJournalHead || storeCount != s.previewJournalCount ||
		admissionHead != s.previewAdmissionHead || admissionCount != s.previewAdmissionCount {
		return errors.New("retained Store or admission journal changed after read-only recovery preview")
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
	if exists != s.previewStateExists || !reflect.DeepEqual(state, s.previewState) {
		return errors.New("durable repository state changed after read-only recovery preview")
	}
	if len(s.generations) != 0 && (!exists || !stateMatchesStore(state, s)) {
		if err := s.persistCandidateStateLocked(s.generations, s.transitions, s.generationEffects); err != nil {
			return fmt.Errorf("repair durable repository state after restart reservation: %w", err)
		}
	}
	if err := ledger.VerifyCleanRestartReceipt(ctx, receipt, pins); err != nil {
		return fmt.Errorf("reverify durable clean-restart receipt after Store repair: %w", err)
	}
	s.options = options
	s.recoveryPreview = false
	return nil
}

func verifyRecoveryStorePinsLocked(s *Store, pins workflowrecovery.Pins, options Options) error {
	if s.baseline == nil || len(s.generations) == 0 ||
		pins.StoreBaselineGenerationID != s.baseline.ID() ||
		strings.TrimPrefix(s.baseline.TreeDigest(), delta.TreeDigestProfile) != pins.StoreBaselineTreeDigest {
		return ErrStaleGeneration
	}
	tip := s.generations[len(s.generations)-1]
	if pins.StoreTipGenerationID != tip.ID() ||
		strings.TrimPrefix(tip.TreeDigest(), delta.TreeDigestProfile) != pins.StoreTipTreeDigest {
		return ErrStaleGeneration
	}
	storeTrace, err := options.Journal.Trace()
	if err != nil {
		return err
	}
	storeHead, storeCount := traceHead(storeTrace)
	if storeCount != pins.StoreJournalCount || storeHead != pins.StoreJournalHead {
		return errors.New("clean-restart receipt Store journal pins differ from the held Store journal")
	}
	admissionTrace, err := s.admissionJournal.Trace()
	if err != nil {
		return err
	}
	admissionHead, admissionCount := traceHead(admissionTrace)
	if admissionCount != pins.AdmissionJournalCount || admissionHead != pins.AdmissionJournalHead {
		return errors.New("clean-restart receipt admission pins differ from the held Store journal")
	}
	return nil
}

func traceHead(trace audit.Trace) (string, uint64) {
	if len(trace.Records) == 0 {
		return "", 0
	}
	last := trace.Records[len(trace.Records)-1]
	return last.Hash, last.Sequence
}

func (s *Store) indexHistoricalAuthorizationsLocked(trace audit.Trace) error {
	for _, record := range trace.Records {
		if record.Event.Kind != "governed_operation_authorization" {
			continue
		}
		var authorization historicalOperationAuthorization
		if err := decodeCanonicalHistoricalAuthorization(record.Event.Data, &authorization); err != nil ||
			authorization.SchemaVersion != "tbound-governed-operation-admission/v2" || authorization.EffectID == "" ||
			record.Event.ID != "governed-operation:"+authorization.EffectID || authorization.DecisionID == "" ||
			authorization.Tool == "" ||
			(authorization.Operation.Kind != delta.OperationProposalCall && authorization.Operation.Kind != delta.OperationLease) ||
			!validSHA256(authorization.TypedArgumentDigest) || !validTreeDigest(authorization.InputTreeDigest) ||
			!validSHA256(authorization.GatePolicyDigest) || authorization.ProviderDecisionSequence == 0 ||
			!validBareAuditHash(authorization.ProviderDecisionRecordHash) || authorization.GateDecision.Verdict != gate.Allow ||
			authorization.GateDecision.PolicyDigest != authorization.GatePolicyDigest ||
			authorization.GateDecision.Tool != authorization.Tool ||
			authorization.GateDecision.ToolCallID == "" || len(authorization.GateDecision.CanonicalArgumentsDigest) == 0 {
			return fmt.Errorf("malformed retained governed operation authorization at sequence %d", record.Sequence)
		}
		if _, duplicate := s.historicalAuthorizations[authorization.EffectID]; duplicate {
			return errors.New("Store journal contains duplicate governed operation authorizations")
		}
		s.historicalAuthorizations[authorization.EffectID] = historicalAuthorizationEvidence{record: record, data: authorization}
	}
	return nil
}

func decodeCanonicalHistoricalAuthorization(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("historical authorization has trailing JSON")
		}
		return err
	}
	canonical, err := json.Marshal(target)
	if err != nil || !bytes.Equal(canonical, data) {
		return errors.Join(errors.New("historical authorization is not canonical JSON"), err)
	}
	return nil
}

func validBareAuditHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (s *Store) verifyHistoricalAuthorizationLocked(request OperationRequest, effect audit.EffectTrace) error {
	if !s.recoveryPreview {
		return ErrInvalidOptions
	}
	authorization, exists := s.historicalAuthorizations[request.EffectID]
	if !exists {
		return errors.New("retained operation effect has no governed Store authorization")
	}
	if _, duplicate := s.usedHistoricalAuthorizations[request.EffectID]; duplicate {
		return ErrOperationReplay
	}
	data := authorization.data
	if authorization.record.Sequence == 0 || authorization.record.Sequence >= effect.IntentSequence ||
		data.EffectID != request.EffectID || data.DecisionID != request.Decision.ID ||
		data.Operation != request.Operation || data.Tool != request.Tool ||
		!sameOutputScopePaths(data.OutputScopePaths, request.OutputScopePaths) ||
		data.TypedArgumentDigest != request.ArgumentDigest || data.InputGeneration != request.InputGeneration ||
		data.InputTreeDigest != request.InputTreeDigest || data.GateDecision.Verdict != gate.Allow ||
		request.Decision.Outcome != delta.PolicyAllow || request.Decision.PolicyDigest != s.options.PolicyDigest ||
		request.Decision.MetadataPolicyDigest != s.options.MetadataPolicyDigest {
		return errors.New("retained operation effect differs from its exact prior Store authorization")
	}
	s.usedHistoricalAuthorizations[request.EffectID] = struct{}{}
	return nil
}

func (s *Store) verifyRecoveryPreviewDecision(decision delta.PolicyDecision, binding delta.TransitionBinding) error {
	if s == nil || !s.recoveryPreview || decision.Outcome != delta.PolicyAllow ||
		decision.PolicyDigest != s.options.PolicyDigest || decision.MetadataPolicyDigest != s.options.MetadataPolicyDigest {
		return ErrInvalidDecision
	}
	matches := 0
	for effectID, authorization := range s.historicalAuthorizations {
		data := authorization.data
		if data.Operation != binding.Operation {
			continue
		}
		if data.DecisionID != decision.ID || data.Tool != binding.Tool ||
			data.TypedArgumentDigest != binding.ArgumentDigest || data.InputGeneration != binding.InputGeneration ||
			data.InputTreeDigest != binding.InputTreeDigest || data.EffectID != effectID || data.GateDecision.Verdict != gate.Allow {
			return ErrInvalidDecision
		}
		matches++
	}
	if matches != 1 {
		return ErrInvalidDecision
	}
	return nil
}

func (s *Store) recoverEffectLocked(effect audit.EffectTrace, admissions *admissionRecovery) error {
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
		((request.Tool == "write" || request.Tool == "edit" || request.Tool == "delete" || request.Tool == "read") && request.Operation.Kind != delta.OperationProposalCall) {
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
	if s.recoveryPreview {
		if err := s.verifyHistoricalAuthorizationLocked(request, effect); err != nil {
			return fmt.Errorf("verify exact retained Store authorization: %w", err)
		}
	} else {
		if s.options.AuthorizeOperation == nil {
			return ErrInvalidOptions
		}
		if err := s.options.AuthorizeOperation(cloneOperationRequest(request)); err != nil {
			return fmt.Errorf("revalidate trusted operation receipt: %w", err)
		}
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
	return s.recoverMutationLocked(effect, request, input, readScope.Command, readScope.RunnerProfile, admissions)
}

type admissionRecovery struct {
	records  []audit.Record
	consumed map[uint64]struct{}
}

func (s *Store) recoverAdmissionLocked(admissions *admissionRecovery, effect audit.EffectTrace, request OperationRequest,
	input *Generation, transition delta.Transition, reference AdmissionReference) (admissionRecord, error) {
	if reference.Sequence == 0 || reference.Sequence > uint64(len(admissions.records)) {
		return admissionRecord{}, ErrInvalidAdmission
	}
	if _, exists := admissions.consumed[reference.Sequence]; exists {
		return admissionRecord{}, fmt.Errorf("%w: admission reference is reused", ErrInvalidAdmission)
	}
	if err := VerifyAdmission(s.admissionJournal, reference, effect.Intent,
		s.options.PolicyDigest, s.options.MetadataPolicyDigest); err != nil {
		return admissionRecord{}, err
	}
	appended := admissions.records[reference.Sequence-1]
	record, err := decodeAdmissionRecord(appended.Event.Data)
	if err != nil {
		return admissionRecord{}, err
	}
	requestJSON, _ := json.Marshal(request)
	recordRequestJSON, _ := json.Marshal(record.Request)
	transitionJSON, _ := json.Marshal(transition)
	candidateJSON, _ := json.Marshal(record.Candidate)
	if appended.Sequence != reference.Sequence ||
		!bytes.Equal(requestJSON, recordRequestJSON) ||
		!bytes.Equal(transitionJSON, candidateJSON) ||
		!sameManifest(record.Input, input.snapshot.Manifest) {
		return admissionRecord{}, fmt.Errorf("%w: admission does not match its primary effect", ErrInvalidAdmission)
	}
	admissions.consumed[reference.Sequence] = struct{}{}
	return record, nil
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

func (s *Store) recoverMutationLocked(effect audit.EffectTrace, request OperationRequest, input *Generation,
	requestedSpec CommandSpec, runnerProfile string, admissions *admissionRecovery) error {
	if len(s.generations) == 0 || input != s.generations[len(s.generations)-1] {
		return ErrStaleGeneration
	}
	outputID := fmt.Sprintf("g%d", len(s.generations))
	var reference AdmissionReference
	var transition delta.Transition
	var command *CommandResult
	if request.Tool == "bash" {
		var result commandEffectResult
		if err := json.Unmarshal(effect.Result, &result); err != nil {
			return fmt.Errorf("decode command effect result: %w", err)
		}
		result.Spec.DeclaredOutputPaths = cloneOutputScopePaths(request.OutputScopePaths)
		reference = result.Admission
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
		if !s.recoveryPreview {
			if s.options.VerifySettlement == nil {
				return ErrInvalidOptions
			}
			if err := s.options.VerifySettlement(result.Settlement, request.ViewID, request.Operation.LeaseID); err != nil {
				return fmt.Errorf("revalidate command settlement: %w", err)
			}
		}
	} else if request.Tool == "write" || request.Tool == "edit" || request.Tool == "delete" {
		var result mutationEffectResult
		if err := json.Unmarshal(effect.Result, &result); err != nil {
			return fmt.Errorf("decode mutation transition: %w", err)
		}
		transition, reference = result.Transition, result.Admission
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
	admission, err := s.recoverAdmissionLocked(admissions, effect, request, input, transition, reference)
	if err != nil {
		return err
	}
	outputDigest, err := delta.ComputeTreeDigest(admission.Output)
	if err != nil || admission.Output.Generation != outputID ||
		transition.OutputTreeDigest != outputDigest ||
		!sameChanges(transition.Changes, diffManifests(input.snapshot.Manifest, admission.Output)) {
		return fmt.Errorf("%w: candidate does not match its complete output manifest", ErrInvalidAdmission)
	}
	candidate := append(cloneTransitions(s.transitions), transition)
	output := workspace.Snapshot{Manifest: cloneManifest(admission.Output), TreeDigest: outputDigest}
	if reference.Outcome == "allow" {
		actual, err := s.scanGenerationLocked(outputID)
		if err != nil {
			return err
		}
		if actual.TreeDigest != outputDigest || !sameManifest(actual.Manifest, admission.Output) {
			return errors.New("promoted generation does not match its admission manifest")
		}
		output = actual
	} else if reference.Outcome != "deny" {
		return ErrInvalidAdmission
	}
	if _, err := s.validateChainForLocked(output, candidate); err != nil {
		return err
	}
	if reference.Outcome == "deny" {
		var recordedCommand *CommandResult
		if command != nil {
			recordedCommand = cloneCommandResult(command)
		}
		return s.recordOperationLocked(request.Tool, effect.ID, request.Operation, request.Decision,
			request.ArgumentDigest, request.ViewID, request.ExecutionContextDigest, runnerProfile,
			input, nil, effect.Result, recordedCommand, reference)
	}
	generation, err := s.openGenerationLocked(outputID, output)
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
		request.ViewID, request.ExecutionContextDigest, runnerProfile, input, generation, effect.Result, recordedCommand, reference)
}

type readIntent struct {
	SchemaVersion string           `json:"schema_version"`
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
	required := []string{"schema_version", "request", "path", "max_bytes", "command", "runner_profile"}
	if envelope == nil || len(envelope) != len(required) {
		return OperationRequest{}, readIntent{}, errors.New("operation intent is not the versioned v2 envelope")
	}
	for _, key := range required {
		if _, exists := envelope[key]; !exists {
			return OperationRequest{}, readIntent{}, fmt.Errorf("operation intent omits %q", key)
		}
	}
	var requestFields map[string]json.RawMessage
	if err := json.Unmarshal(envelope["request"], &requestFields); err != nil || requestFields == nil {
		return OperationRequest{}, readIntent{}, errors.New("operation intent request is invalid")
	}
	if _, exists := requestFields["output_scope_paths"]; !exists {
		return OperationRequest{}, readIntent{}, errors.New("operation intent request omits output scope")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var intent readIntent
	if err := decoder.Decode(&intent); err != nil {
		return OperationRequest{}, readIntent{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return OperationRequest{}, readIntent{}, errors.New("operation intent has trailing JSON")
		}
		return OperationRequest{}, readIntent{}, err
	}
	if intent.SchemaVersion != OperationIntentSchemaVersion {
		return OperationRequest{}, readIntent{}, errors.New("unsupported operation intent schema version")
	}
	switch intent.Request.Tool {
	case "read":
		if intent.Request.OutputScopePaths != nil || validateRelativePath(intent.Path) != nil ||
			intent.MaxBytes <= 0 || intent.MaxBytes > maxReadBytes || intent.RunnerProfile != "" ||
			!sameCommandSpec(intent.Command, CommandSpec{}) {
			return OperationRequest{}, readIntent{}, errors.New("invalid read operation intent")
		}
	case "write", "edit", "delete":
		if ValidateOutputScopePaths(intent.Request.OutputScopePaths) != nil || len(intent.Request.OutputScopePaths) != 1 ||
			intent.Path != intent.Request.OutputScopePaths[0] || intent.MaxBytes != 0 || intent.RunnerProfile != "" ||
			!sameCommandSpec(intent.Command, CommandSpec{}) {
			return OperationRequest{}, readIntent{}, errors.New("invalid mutation operation intent")
		}
	case "bash":
		if ValidateOutputScopePaths(intent.Request.OutputScopePaths) != nil || intent.Path != "" || intent.MaxBytes != 0 ||
			!validIdentity(intent.RunnerProfile) {
			return OperationRequest{}, readIntent{}, errors.New("invalid Bash operation intent")
		}
		intent.Command.DeclaredOutputPaths = cloneOutputScopePaths(intent.Request.OutputScopePaths)
		if err := validateCommandSpec(intent.Command); err != nil {
			return OperationRequest{}, readIntent{}, fmt.Errorf("invalid Bash command scope: %w", err)
		}
	default:
		return OperationRequest{}, readIntent{}, errors.New("unsupported operation intent tool")
	}
	canonical, err := json.Marshal(intent)
	if err != nil || !bytes.Equal(canonical, data) {
		return OperationRequest{}, readIntent{}, errors.New("operation intent is not canonical v2 JSON")
	}
	return intent.Request, intent, nil
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
	exposedNames, err := directoryNames(s.exposedDir)
	if err != nil {
		return err
	}
	if len(exposedNames) != 0 {
		return errors.New("repository contains an unfinished exposed generation view")
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
		len(left.Args) != len(right.Args) || (left.Args == nil) != (right.Args == nil) ||
		!sameOutputScopePaths(left.DeclaredOutputPaths, right.DeclaredOutputPaths) {
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
	spec.DeclaredOutputPaths = cloneOutputScopePaths(spec.DeclaredOutputPaths)
	return spec
}
