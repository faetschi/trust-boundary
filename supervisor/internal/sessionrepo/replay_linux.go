//go:build linux

package sessionrepo

// This file implements the reconstructable-evidence path (thesis todo exit
// criterion #5). EvidenceBundle is a self-contained, bounded, deterministic
// record of one session-repository run: the exact canonical audit-journal
// bytes, every generation manifest with its tree digest, the approved-delta
// ledger, per-operation evidence, and the pinned policy/metadata commitments.
//
// Reconstruct re-derives attempt -> decision -> effect -> outcome from those
// retained bytes alone, with no mutation API and no live Store. It reuses the
// live path's canonicalization and validation (delta.ComputeTreeDigest,
// delta.ValidateChain, argumentsDigest, diffManifests, the tool/operation/
// settlement predicates, and audit.Verify), and fails closed on any mismatch or
// tampering rather than trusting a serialized disposition.
//
// It deliberately does not re-authenticate external TCB inputs that are not
// derivable from retained bytes: the trusted policy decision verifier and the
// command-runner settlement attestation. Those remain caller-supplied live
// obligations; Reconstruct can only re-check their structural commitments and
// the bytes the live path recorded.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
)

const (
	bundleSchemaVersion      = "tbound-sessionrepo-evidence-bundle/v1"
	dispositionSchemaVersion = "tbound-sessionrepo-disposition/v1"
	// maxEvidenceJournalBytes bounds the retained raw journal so an untrusted
	// bundle cannot force unbounded verification work or allocation.
	maxEvidenceJournalBytes = 64 << 20
	// maxEvidenceOperations bounds retained per-operation evidence.
	maxEvidenceOperations = 2 * (delta.MaxTransitions + 1)
)

// ErrInvalidEvidenceBundle is returned, wrapped, when a bundle is malformed,
// internally inconsistent, or tampered. Reconstruction never returns a partial
// disposition alongside this error.
var ErrInvalidEvidenceBundle = errors.New("invalid or inconsistent session-repository evidence bundle")

// EvidenceBundle is the self-contained evidence required to reconstruct one
// session-repository run without the live Store. It extends EvidenceArtifact
// with the exact canonical audit-journal bytes and the raw policy decisions
// needed to re-derive the disposition purely from retained data. Emitting a
// bundle never weakens Evidence(): that artifact and this bundle are produced
// independently from the same store.
type EvidenceBundle struct {
	SchemaVersion            string `json:"schema_version"`
	RealProviderExchange     bool   `json:"real_provider_exchange"`
	PiAdapterWired           bool   `json:"pi_adapter_wired"`
	CommandContainmentStatus string `json:"command_containment_status"`
	PolicyDigest             string `json:"policy_digest"`
	MetadataPolicyDigest     string `json:"metadata_policy_digest"`
	// AuditJournal is the exact newline-delimited canonical audit journal. It
	// verifies with audit.Verify and is byte-identical to the on-disk journal
	// when that journal was framed canonically.
	AuditJournal        []byte               `json:"audit_journal"`
	AuditRecordCount    uint64               `json:"audit_record_count"`
	AuditHeadHash       string               `json:"audit_head_hash"`
	Baseline            GenerationEvidence   `json:"baseline"`
	Generations         []GenerationEvidence `json:"generations"`
	ApprovedDeltaLedger []delta.Transition   `json:"approved_delta_ledger"`
	Operations          []OperationEvidence  `json:"operations"`
	ValidatedChain      delta.Result         `json:"validated_chain"`
}

// EvidenceBundle returns a self-contained reconstruction bundle for the
// committed prefix of this store plus the full verified journal. Unlike
// Evidence it tolerates a quarantined store, so evidence for a failed or
// unknown-outcome run is retained and remains replayable; it still fails when
// the store is closed, has no baseline, or a retained generation was modified.
func (s *Store) EvidenceBundle() (EvidenceBundle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return EvidenceBundle{}, ErrClosed
	}
	if s.baseline == nil || len(s.generations) == 0 {
		return EvidenceBundle{}, ErrNoBaseline
	}
	// A quarantined store is intentionally accepted: retained evidence for a
	// failed run must remain reconstructable. A modified generation is still a
	// hard failure and sets quarantine.
	for _, generation := range s.generations {
		if err := s.verifyGenerationLocked(generation); err != nil {
			return EvidenceBundle{}, err
		}
	}
	chain, err := s.validateChainLocked(s.generations[len(s.generations)-1].snapshot)
	if err != nil {
		return EvidenceBundle{}, err
	}
	trace, err := s.options.Journal.Trace()
	if err != nil {
		return EvidenceBundle{}, fmt.Errorf("verify evidence journal: %w", err)
	}
	journal, err := marshalAuditJournal(trace.Records)
	if err != nil {
		return EvidenceBundle{}, err
	}
	generations := make([]GenerationEvidence, 0, len(s.generations))
	for _, generation := range s.generations {
		generations = append(generations, GenerationEvidence{
			ID: generation.id, TreeDigest: generation.snapshot.TreeDigest,
			Manifest: cloneManifest(generation.snapshot.Manifest),
		})
	}
	var auditHead string
	if len(trace.Records) > 0 {
		auditHead = trace.Records[len(trace.Records)-1].Hash
	}
	return EvidenceBundle{
		SchemaVersion:        bundleSchemaVersion,
		RealProviderExchange: false, PiAdapterWired: false,
		CommandContainmentStatus: "not-established",
		PolicyDigest:             s.options.PolicyDigest,
		MetadataPolicyDigest:     s.options.MetadataPolicyDigest,
		AuditJournal:             journal,
		AuditRecordCount:         uint64(len(trace.Records)),
		AuditHeadHash:            auditHead,
		Baseline:                 generations[0],
		Generations:              generations,
		ApprovedDeltaLedger:      cloneTransitions(s.transitions),
		Operations:               cloneOperations(s.operations),
		ValidatedChain:           chain,
	}, nil
}

// marshalAuditJournal re-encodes verified records into the exact canonical
// journal framing that audit.Verify accepts. Because audit rejects any frame
// that is not byte-identical to the canonical encoding, this reproduces the
// original journal bytes exactly.
func marshalAuditJournal(records []audit.Record) ([]byte, error) {
	var buffer bytes.Buffer
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			return nil, fmt.Errorf("encode audit record %d: %w", record.Sequence, err)
		}
		if len(encoded)+1 > audit.MaxRecordBytes {
			return nil, ErrOperationOutputLimit
		}
		buffer.Write(encoded)
		buffer.WriteByte('\n')
		if buffer.Len() > maxEvidenceJournalBytes {
			return nil, ErrOperationOutputLimit
		}
	}
	return buffer.Bytes(), nil
}

// Disposition summarises a run re-derived purely from retained bundle bytes.
// Attempts counts durable effect intents (including the baseline seed);
// Decisions counts policy decisions bound to session-repository operations
// (allow intents plus denied-operation records); Effects counts durable
// terminal outcomes; Successes/Unknowns/Denials partition the observed results.
type Disposition struct {
	SchemaVersion        string               `json:"schema_version"`
	PolicyDigest         string               `json:"policy_digest"`
	MetadataPolicyDigest string               `json:"metadata_policy_digest"`
	Baseline             string               `json:"baseline"`
	Sealed               string               `json:"sealed"`
	Generations          []GenerationEvidence `json:"generations"`
	Ledger               []delta.Transition   `json:"ledger"`
	Operations           []OperationEvidence  `json:"operations"`
	Attempts             int                  `json:"attempts"`
	Decisions            int                  `json:"decisions"`
	Effects              int                  `json:"effects"`
	Successes            int                  `json:"successes"`
	Unknowns             int                  `json:"unknowns"`
	Denials              int                  `json:"denials"`
	Quarantined          bool                 `json:"quarantined"`
	QuarantineReason     string               `json:"quarantine_reason,omitempty"`
	AuditRecordCount     uint64               `json:"audit_record_count"`
	AuditHeadHash        string               `json:"audit_head_hash"`
}

// Reconstruct re-derives the durable disposition of a run from a retained,
// self-contained EvidenceBundle. It verifies the audit hash chain, recomputes
// every generation's tree digest, revalidates every transition against the
// retained manifests, re-runs delta.ValidateChain against the retained policy
// commitments and decisions, and rebuilds per-operation evidence. Any
// disagreement between the bundle's raw records and its derived commitments,
// including a single mutated byte, fails closed with ErrInvalidEvidenceBundle.
//
// A structurally valid run whose journal contains an unresolved/unknown effect
// is reported as Disposition{Quarantined: true} with a nil error: quarantine is
// itself a reconstructed disposition, not a reconstruction failure.
func Reconstruct(bundle EvidenceBundle) (Disposition, error) {
	if bundle.SchemaVersion != bundleSchemaVersion {
		return Disposition{}, invalidBundle("unknown bundle schema %q", bundle.SchemaVersion)
	}
	if len(bundle.AuditJournal) > maxEvidenceJournalBytes {
		return Disposition{}, invalidBundle("audit journal exceeds the retained-byte bound")
	}
	if !validSHA256(bundle.PolicyDigest) || !validSHA256(bundle.MetadataPolicyDigest) {
		return Disposition{}, invalidBundle("policy commitments are not SHA-256 digests")
	}
	trace, err := audit.Verify(bytes.NewReader(bundle.AuditJournal))
	if err != nil {
		return Disposition{}, invalidBundle("verify retained audit journal: %v", err)
	}
	if uint64(len(trace.Records)) != bundle.AuditRecordCount {
		return Disposition{}, invalidBundle("retained audit record count does not match the journal")
	}
	var auditHead string
	if len(trace.Records) > 0 {
		auditHead = trace.Records[len(trace.Records)-1].Hash
	}
	if auditHead != bundle.AuditHeadHash {
		return Disposition{}, invalidBundle("retained audit head hash does not match the journal")
	}
	generations, err := verifyBundleGenerations(bundle)
	if err != nil {
		return Disposition{}, err
	}

	state := &reconstruction{
		bundle: bundle, generations: generations,
		byID:           make(map[string]GenerationEvidence, len(generations)),
		chainDecisions: make(map[string]delta.PolicyDecision),
		usedOps:        make(map[delta.OperationIdentity]struct{}),
		transitions:    make([]delta.Transition, 0, len(generations)-1),
		operations:     nil,
	}
	for _, generation := range generations {
		state.byID[generation.ID] = generation
	}

	for _, effect := range trace.Effects {
		state.attempts++
		if effect.OutcomeSequence != 0 {
			state.effects++
		}
		if effect.Outcome == "success" {
			state.successes++
		} else {
			state.unknowns++
		}
		if !strings.HasPrefix(effect.ID, "sessionrepo-effect-") {
			continue
		}
		if !validSupervisorEffectID(effect.ID) {
			return Disposition{}, invalidBundle("invalid supervisor effect ID %q", effect.ID)
		}
		if effect.Unresolved || effect.Outcome != "success" {
			// Mirror the live recovery precondition: the intent must still be
			// structurally well formed, but the effect is not reconstructed and
			// the run is dispositioned as quarantined.
			if err := state.validateQuarantinedIntent(effect); err != nil {
				return Disposition{}, err
			}
			if state.quarantine == "" {
				state.quarantine = fmt.Sprintf("audit effect %q has an unresolved or unknown outcome", effect.ID)
			}
			continue
		}
		if err := state.reconstructEffect(effect); err != nil {
			return Disposition{}, err
		}
	}

	if err := state.reconstructDenials(trace); err != nil {
		return Disposition{}, err
	}
	if state.quarantine != "" {
		// The retained records do not establish a fully reconstructable run.
		// Return the reconstructable prefix plus the quarantine disposition.
		sortOperations(state.operations)
		disposition := state.disposition(trace, generations, auditHead)
		return disposition, nil
	}

	if !state.hasBaseline {
		return Disposition{}, invalidBundle("retained evidence has no committed baseline seed")
	}
	if state.generationEffectCount != len(generations) {
		return Disposition{}, invalidBundle("retained generations do not match generation effects")
	}

	if len(state.transitions) != len(bundle.ApprovedDeltaLedger) ||
		!sameTransitionSlices(state.transitions, bundle.ApprovedDeltaLedger) {
		return Disposition{}, invalidBundle("reconstructed ledger does not match the retained approved-delta ledger")
	}
	sortOperations(state.operations)
	if len(state.operations) != len(bundle.Operations) || !sameOperationSlices(state.operations, bundle.Operations) {
		return Disposition{}, invalidBundle("reconstructed operations do not match the retained operation evidence")
	}
	chain, err := state.recomputeChain()
	if err != nil {
		return Disposition{}, err
	}
	if !reflect.DeepEqual(chain, bundle.ValidatedChain) {
		return Disposition{}, invalidBundle("reconstructed chain does not match the retained validated chain")
	}

	disposition := state.disposition(trace, generations, auditHead)
	return disposition, nil
}

// reconstruction carries the derived state while replaying one bundle.
type reconstruction struct {
	bundle      EvidenceBundle
	generations []GenerationEvidence
	byID        map[string]GenerationEvidence

	chainDecisions map[string]delta.PolicyDecision
	usedOps        map[delta.OperationIdentity]struct{}
	transitions    []delta.Transition
	operations     []OperationEvidence

	hasBaseline           bool
	generationEffectCount int
	attempts              int
	effects               int
	successes             int
	unknowns              int
	denials               int
	decisions             int
	quarantine            string
}

func (state *reconstruction) disposition(trace audit.Trace, generations []GenerationEvidence, auditHead string) Disposition {
	sealed := generations[len(generations)-1].ID
	return Disposition{
		SchemaVersion:        dispositionSchemaVersion,
		PolicyDigest:         state.bundle.PolicyDigest,
		MetadataPolicyDigest: state.bundle.MetadataPolicyDigest,
		Baseline:             generations[0].ID,
		Sealed:               sealed,
		Generations:          cloneGenerations(generations),
		Ledger:               cloneTransitions(state.transitions),
		Operations:           cloneOperations(state.operations),
		Attempts:             state.attempts,
		Decisions:            state.decisions,
		Effects:              state.effects,
		Successes:            state.successes,
		Unknowns:             state.unknowns,
		Denials:              state.denials,
		Quarantined:          state.quarantine != "",
		QuarantineReason:     state.quarantine,
		AuditRecordCount:     uint64(len(trace.Records)),
		AuditHeadHash:        auditHead,
	}
}

// validateQuarantinedIntent structurally validates the intent of an unresolved
// effect without advancing the generation, so a malformed retained intent still
// fails closed instead of silently becoming a quarantine.
func (state *reconstruction) validateQuarantinedIntent(effect audit.EffectTrace) error {
	var action struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(effect.Intent, &action); err != nil {
		return invalidBundle("decode intent %q: %v", effect.ID, err)
	}
	if action.Action == "seed" {
		var intent struct {
			Action                  string `json:"action"`
			OutputGeneration        string `json:"output_generation"`
			SourceAttestedQuiescent bool   `json:"source_attested_quiescent"`
		}
		if err := json.Unmarshal(effect.Intent, &intent); err != nil || intent.OutputGeneration != "g0" {
			return invalidBundle("effect %q has a malformed baseline seed intent", effect.ID)
		}
		return nil
	}
	request, _, err := decodeOperationIntent(effect.Intent)
	if err != nil {
		return invalidBundle("decode operation intent %q: %v", effect.ID, err)
	}
	if request.EffectID != effect.ID || !validSupervisorEffectID(request.EffectID) {
		return invalidBundle("effect %q intent is not bound to its supervisor effect ID", effect.ID)
	}
	if err := validateOperation(request.Operation); err != nil {
		return invalidBundle("effect %q has an invalid operation identity", effect.ID)
	}
	return nil
}

func (state *reconstruction) reconstructEffect(effect audit.EffectTrace) error {
	var action struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(effect.Intent, &action); err != nil {
		return invalidBundle("decode intent %q: %v", effect.ID, err)
	}
	if action.Action == "seed" {
		return state.reconstructSeed(effect)
	}
	request, scope, err := decodeOperationIntent(effect.Intent)
	if err != nil {
		return invalidBundle("decode operation intent %q: %v", effect.ID, err)
	}
	if request.EffectID != effect.ID || !validSupervisorEffectID(request.EffectID) || !validSHA256(request.ArgumentDigest) {
		return invalidBundle("effect %q intent is not bound to its ID and argument digest", effect.ID)
	}
	if err := validateOperation(request.Operation); err != nil {
		return invalidBundle("effect %q has an invalid operation identity", effect.ID)
	}
	if request.Decision.Outcome != delta.PolicyAllow || request.Decision.PolicyDigest != state.bundle.PolicyDigest ||
		request.Decision.MetadataPolicyDigest != state.bundle.MetadataPolicyDigest || !validIdentity(request.Decision.ID) {
		return invalidBundle("effect %q decision commitment does not match the bundle", effect.ID)
	}
	if (request.Tool == "bash" && request.Operation.Kind != delta.OperationLease) ||
		((request.Tool == "write" || request.Tool == "edit" || request.Tool == "delete" || request.Tool == "read") && request.Operation.Kind != delta.OperationProposalCall) {
		return invalidBundle("effect %q tool and operation kind disagree", effect.ID)
	}
	if request.Tool == "bash" {
		if !validViewID(request.ViewID) || !validSHA256(request.ExecutionContextDigest) {
			return invalidBundle("effect %q Bash intent omits its view or execution context", effect.ID)
		}
	} else if request.ViewID != "" || request.ExecutionContextDigest != "" {
		return invalidBundle("effect %q non-Bash intent carries command-view context", effect.ID)
	}
	// Register the trusted decision. A repeated decision ID is replay.
	if _, exists := state.chainDecisions[request.Decision.ID]; exists {
		return invalidBundle("duplicate policy decision %q", request.Decision.ID)
	}
	if _, exists := state.usedOps[request.Operation]; exists {
		return invalidBundle("duplicate operation identity in effect %q", effect.ID)
	}
	input, exists := state.byID[request.InputGeneration]
	if !exists || input.TreeDigest != request.InputTreeDigest {
		return invalidBundle("effect %q names a stale or unknown input generation", effect.ID)
	}
	// The decision is only registered once the operation is known to be a
	// well-formed first use; ValidateChain will also reject any residue.
	state.chainDecisions[request.Decision.ID] = request.Decision
	state.usedOps[request.Operation] = struct{}{}
	state.decisions++
	if request.Tool == "read" {
		return state.reconstructRead(effect, request, scope, input)
	}
	return state.reconstructMutation(effect, request, scope, input)
}

func (state *reconstruction) reconstructSeed(effect audit.EffectTrace) error {
	var intent struct {
		Action                  string `json:"action"`
		OutputGeneration        string `json:"output_generation"`
		SourceAttestedQuiescent bool   `json:"source_attested_quiescent"`
	}
	if err := json.Unmarshal(effect.Intent, &intent); err != nil || intent.Action != "seed" ||
		intent.OutputGeneration != "g0" || !intent.SourceAttestedQuiescent {
		return invalidBundle("effect %q has an invalid baseline seed intent", effect.ID)
	}
	if state.hasBaseline || len(state.generations) == 0 {
		return invalidBundle("duplicate or unretained baseline seed")
	}
	var evidence GenerationEvidence
	if err := json.Unmarshal(effect.Result, &evidence); err != nil || evidence.ID != "g0" || evidence.Manifest.Generation != "g0" {
		return invalidBundle("effect %q has invalid baseline evidence", effect.ID)
	}
	actual := state.generations[0]
	if evidence.TreeDigest != actual.TreeDigest || !sameManifest(evidence.Manifest, actual.Manifest) {
		return invalidBundle("baseline seed does not match retained generation %q", actual.ID)
	}
	state.hasBaseline = true
	state.generationEffectCount = 1
	return nil
}

func (state *reconstruction) reconstructRead(effect audit.EffectTrace, request OperationRequest, scope readIntent, input GenerationEvidence) error {
	if scope.Request.EffectID != request.EffectID || scope.MaxBytes <= 0 || scope.MaxBytes > maxReadBytes ||
		validateRelativePath(scope.Path) != nil {
		return invalidBundle("effect %q has an invalid read scope", effect.ID)
	}
	wantDigest, err := argumentsDigest(struct {
		Path     string `json:"path"`
		MaxBytes int64  `json:"max_bytes"`
	}{scope.Path, scope.MaxBytes})
	if err != nil || wantDigest != request.ArgumentDigest {
		return invalidBundle("effect %q read argument digest does not match its scope", effect.ID)
	}
	if int64(len(effect.Result)) > scope.MaxBytes {
		return invalidBundle("effect %q read result exceeds its declared bound", effect.ID)
	}
	object, exists := manifestObject(input.Manifest, scope.Path)
	if !exists || object.State.Type != delta.ObjectRegular || object.State.ContentDigest != digestBytes(effect.Result) {
		return invalidBundle("effect %q durable read result does not match sealed generation %q", effect.ID, input.ID)
	}
	state.operations = append(state.operations, OperationEvidence{
		AuditSequence: effect.OutcomeSequence, Tool: "read", EffectID: effect.ID, Operation: request.Operation,
		DecisionID: request.Decision.ID, ArgumentDigest: request.ArgumentDigest,
		InputGeneration: input.ID, InputTreeDigest: input.TreeDigest,
		OutputGeneration: input.ID, OutputTreeDigest: input.TreeDigest,
		ResultDigest: digestBytes(effect.Result), Outcome: "success",
	})
	return nil
}

func (state *reconstruction) reconstructMutation(effect audit.EffectTrace, request OperationRequest, scope readIntent, input GenerationEvidence) error {
	if state.generationEffectCount == 0 || state.generationEffectCount-1 >= len(state.generations) ||
		state.generations[state.generationEffectCount-1].ID != input.ID {
		return invalidBundle("effect %q is not based on the committed tip", effect.ID)
	}
	outputIndex := state.generationEffectCount
	if outputIndex >= len(state.generations) {
		return invalidBundle("effect %q produces an unretained generation", effect.ID)
	}
	output := state.generations[outputIndex]
	outputID := fmt.Sprintf("g%d", outputIndex)
	if output.ID != outputID {
		return invalidBundle("effect %q output generation label mismatch", effect.ID)
	}
	var transition delta.Transition
	var command *CommandResult
	if request.Tool == "bash" {
		var result commandEffectResult
		if err := json.Unmarshal(effect.Result, &result); err != nil {
			return invalidBundle("effect %q decode command result: %v", effect.ID, err)
		}
		transition, command = result.Transition, &result.Command
		if int64(len(command.Stdout)) > maxCommandOutputBytes || int64(len(command.Stderr)) > maxCommandOutputBytes || !command.ExitObserved {
			return invalidBundle("effect %q command result exceeds the output bound", effect.ID)
		}
		wantArguments, argumentsErr := commandArgumentsDigest(scope.Command)
		wantContext, contextErr := commandExecutionContextDigest(request.ViewID, input.TreeDigest, request.Operation.LeaseID)
		if argumentsErr != nil || contextErr != nil || wantArguments != request.ArgumentDigest || wantContext != request.ExecutionContextDigest ||
			!sameCommandSpec(result.Spec, scope.Command) || result.RunnerProfile != scope.RunnerProfile ||
			result.Settlement.LeaseID != request.Operation.LeaseID || result.Settlement.ViewID != request.ViewID ||
			!result.Settlement.ProcessScopeEmpty || !result.Settlement.WritersStopped || !result.Settlement.MountDetached ||
			!result.Settlement.ExitObserved || result.Settlement.ExitCode != command.ExitCode ||
			strings.TrimSpace(result.Settlement.EvidenceClass) == "" {
			return invalidBundle("effect %q command settlement is not reconstructable", effect.ID)
		}
	} else if request.Tool == "write" || request.Tool == "edit" || request.Tool == "delete" {
		if err := json.Unmarshal(effect.Result, &transition); err != nil {
			return invalidBundle("effect %q decode mutation transition: %v", effect.ID, err)
		}
	} else {
		return invalidBundle("effect %q has an unsupported committed tool %q", effect.ID, request.Tool)
	}
	if transition.InputGeneration != input.ID || transition.OutputGeneration != outputID ||
		transition.InputTreeDigest != input.TreeDigest || transition.Operation != request.Operation ||
		transition.Tool != request.Tool || transition.ArgumentDigest != request.ArgumentDigest ||
		transition.ViewID != request.ViewID || transition.ExecutionContextDigest != request.ExecutionContextDigest ||
		!samePolicyDecision(transition.Decision, request.Decision) {
		return invalidBundle("effect %q transition does not match its durable operation request", effect.ID)
	}
	if transition.Sequence != uint64(len(state.transitions)+1) || transition.ID != fmt.Sprintf("transition-%06d", transition.Sequence) {
		return invalidBundle("effect %q transition identity is not the next ordered transition", effect.ID)
	}
	if transition.OutputTreeDigest != output.TreeDigest {
		return invalidBundle("effect %q transition binds the wrong output tree digest", effect.ID)
	}
	if !sameChanges(transition.Changes, diffManifests(input.Manifest, output.Manifest)) {
		return invalidBundle("effect %q transition does not match the retained output manifest", effect.ID)
	}
	state.transitions = append(state.transitions, transition)
	state.generationEffectCount++

	evidence := OperationEvidence{
		AuditSequence: effect.OutcomeSequence, Tool: request.Tool, EffectID: effect.ID, Operation: request.Operation,
		DecisionID: request.Decision.ID, ArgumentDigest: request.ArgumentDigest,
		ViewID: request.ViewID, ExecutionContextDigest: request.ExecutionContextDigest,
		InputGeneration: input.ID, InputTreeDigest: input.TreeDigest,
		OutputGeneration: output.ID, OutputTreeDigest: output.TreeDigest,
		ResultDigest: digestBytes(effect.Result), Outcome: "success",
	}
	if command != nil {
		code := command.ExitCode
		evidence.ExitCode = &code
		evidence.CommandContainmentStatus = "not-established"
		evidence.CommandRunnerProfile = scope.RunnerProfile
	}
	state.operations = append(state.operations, evidence)
	return nil
}

// reconstructDenials rebuilds operation evidence for durable denied-operation
// records. It mirrors the live recovery validation, which deliberately does not
// require a denial's decision to match the bundle policy commitments because a
// denial may be recorded for a malformed or mismatched receipt.
func (state *reconstruction) reconstructDenials(trace audit.Trace) error {
	for _, record := range trace.Records {
		if record.Event.Kind != "operation_denied" {
			continue
		}
		var denied struct {
			Request OperationRequest `json:"request"`
			Reason  string           `json:"reason"`
		}
		if err := json.Unmarshal(record.Event.Data, &denied); err != nil || !validSupervisorEffectID(denied.Request.EffectID) {
			return invalidBundle("malformed denied-operation record %d", record.Sequence)
		}
		request := denied.Request
		if err := validateOperation(request.Operation); err != nil {
			return invalidBundle("denied operation %q has an invalid operation identity", request.EffectID)
		}
		if request.Tool == "bash" {
			if !validViewID(request.ViewID) || !validSHA256(request.ExecutionContextDigest) {
				return invalidBundle("denied Bash operation %q has no valid execution context", request.EffectID)
			}
		} else if request.ViewID != "" || request.ExecutionContextDigest != "" {
			return invalidBundle("denied non-Bash operation %q contains command-view context", request.EffectID)
		}
		state.decisions++
		state.denials++
		state.operations = append(state.operations, OperationEvidence{
			AuditSequence: record.Sequence, Tool: request.Tool, EffectID: request.EffectID, Operation: request.Operation,
			DecisionID: request.Decision.ID, InputGeneration: request.InputGeneration, InputTreeDigest: request.InputTreeDigest,
			ArgumentDigest: request.ArgumentDigest, ViewID: request.ViewID,
			ExecutionContextDigest: request.ExecutionContextDigest,
			ResultDigest:           digestBytes(record.Event.Data), Outcome: "denied",
		})
	}
	return nil
}

// recomputeChain independently re-runs the live chain validation over the
// retained manifests, transitions, and decisions. The verifier can only bind a
// decision to its retained commitment; it cannot re-authenticate the external
// policy oracle, which is a live TCB input.
func (state *reconstruction) recomputeChain() (delta.Result, error) {
	baseline := state.generations[0]
	sealed := state.generations[len(state.generations)-1]
	spec := delta.ChainSpec{
		Baseline: cloneManifest(baseline.Manifest), Sealed: cloneManifest(sealed.Manifest),
		ExpectedBaselineTreeDigest: baseline.TreeDigest, ExpectedSealedTreeDigest: sealed.TreeDigest,
		PolicyDigest: state.bundle.PolicyDigest, MetadataPolicyDigest: state.bundle.MetadataPolicyDigest,
	}
	verifier := func(decision delta.PolicyDecision, binding delta.TransitionBinding) error {
		if decision.Outcome != delta.PolicyAllow {
			return errors.New("transition decision is not an allow")
		}
		retained, exists := state.chainDecisions[decision.ID]
		if !exists || !samePolicyDecision(decision, retained) {
			return fmt.Errorf("transition decision %q is not bound to a retained operation decision", decision.ID)
		}
		return nil
	}
	result, err := delta.ValidateChain(spec, state.transitions, verifier)
	if err != nil {
		return delta.Result{}, invalidBundle("revalidate approved-delta chain: %v", err)
	}
	return result, nil
}

func verifyBundleGenerations(bundle EvidenceBundle) ([]GenerationEvidence, error) {
	if len(bundle.Generations) == 0 || len(bundle.Generations) > delta.MaxTransitions+1 {
		return nil, invalidBundle("generation count is out of range")
	}
	if bundle.Baseline.ID != bundle.Generations[0].ID || bundle.Baseline.TreeDigest != bundle.Generations[0].TreeDigest ||
		!sameManifest(bundle.Baseline.Manifest, bundle.Generations[0].Manifest) {
		return nil, invalidBundle("baseline does not match the first retained generation")
	}
	generations := make([]GenerationEvidence, len(bundle.Generations))
	for index, generation := range bundle.Generations {
		if generation.ID != fmt.Sprintf("g%d", index) {
			return nil, invalidBundle("generation %d has non-canonical label %q", index, generation.ID)
		}
		if generation.Manifest.Generation != generation.ID || generation.Manifest.MetadataPolicyDigest != bundle.MetadataPolicyDigest {
			return nil, invalidBundle("generation %q manifest identity does not match the bundle", generation.ID)
		}
		digest, err := delta.ComputeTreeDigest(generation.Manifest)
		if err != nil {
			return nil, invalidBundle("generation %q manifest: %v", generation.ID, err)
		}
		if digest != generation.TreeDigest {
			return nil, invalidBundle("generation %q tree digest does not match its manifest", generation.ID)
		}
		generations[index] = GenerationEvidence{ID: generation.ID, TreeDigest: generation.TreeDigest, Manifest: cloneManifest(generation.Manifest)}
	}
	if len(bundle.ApprovedDeltaLedger) != len(generations)-1 || len(bundle.ApprovedDeltaLedger) > delta.MaxTransitions {
		return nil, invalidBundle("approved-delta ledger count does not match the retained generations")
	}
	if len(bundle.Operations) > maxEvidenceOperations {
		return nil, invalidBundle("operation evidence exceeds the retained bound")
	}
	return generations, nil
}

func manifestObject(manifest delta.TreeManifest, path string) (delta.ManifestObject, bool) {
	index := sort.Search(len(manifest.Objects), func(i int) bool { return manifest.Objects[i].Path >= path })
	if index < len(manifest.Objects) && manifest.Objects[index].Path == path {
		return manifest.Objects[index], true
	}
	return delta.ManifestObject{}, false
}

func sortOperations(operations []OperationEvidence) {
	sort.SliceStable(operations, func(i, j int) bool { return operations[i].AuditSequence < operations[j].AuditSequence })
}

func sameTransitionSlices(left, right []delta.Transition) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !sameTransition(left[index], right[index]) {
			return false
		}
	}
	return true
}

func sameTransition(left, right delta.Transition) bool {
	return left.ID == right.ID && left.Sequence == right.Sequence && left.Tool == right.Tool &&
		left.ArgumentDigest == right.ArgumentDigest && left.ViewID == right.ViewID &&
		left.ExecutionContextDigest == right.ExecutionContextDigest && left.InputGeneration == right.InputGeneration &&
		left.OutputGeneration == right.OutputGeneration && left.InputTreeDigest == right.InputTreeDigest &&
		left.OutputTreeDigest == right.OutputTreeDigest && left.Operation == right.Operation &&
		samePolicyDecision(left.Decision, right.Decision) && sameChanges(left.Changes, right.Changes)
}

func sameOperationSlices(left, right []OperationEvidence) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !sameOperationEvidence(left[index], right[index]) {
			return false
		}
	}
	return true
}

func sameOperationEvidence(left, right OperationEvidence) bool {
	if left.AuditSequence != right.AuditSequence || left.Tool != right.Tool || left.EffectID != right.EffectID ||
		left.Operation != right.Operation || left.DecisionID != right.DecisionID || left.ArgumentDigest != right.ArgumentDigest ||
		left.ViewID != right.ViewID || left.ExecutionContextDigest != right.ExecutionContextDigest ||
		left.InputGeneration != right.InputGeneration || left.InputTreeDigest != right.InputTreeDigest ||
		left.OutputGeneration != right.OutputGeneration || left.OutputTreeDigest != right.OutputTreeDigest ||
		left.ResultDigest != right.ResultDigest || left.Outcome != right.Outcome ||
		left.CommandContainmentStatus != right.CommandContainmentStatus || left.CommandRunnerProfile != right.CommandRunnerProfile {
		return false
	}
	switch {
	case left.ExitCode == nil && right.ExitCode == nil:
		return true
	case left.ExitCode == nil || right.ExitCode == nil:
		return false
	default:
		return *left.ExitCode == *right.ExitCode
	}
}

func cloneGenerations(generations []GenerationEvidence) []GenerationEvidence {
	cloned := make([]GenerationEvidence, len(generations))
	for index, generation := range generations {
		cloned[index] = GenerationEvidence{ID: generation.ID, TreeDigest: generation.TreeDigest, Manifest: cloneManifest(generation.Manifest)}
	}
	return cloned
}

func invalidBundle(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidEvidenceBundle, fmt.Sprintf(format, args...))
}
