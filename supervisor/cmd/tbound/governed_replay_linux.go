//go:build linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
)

// GovernedReplayPins are all host-owned inputs needed by the inert join. The
// Baseline must come from the caller's separately verified source/D06 binding;
// this verifier only compares it to the retained Store evidence. Profile is
// the immutable broker profile supplied by the caller, not a profile ID read
// from either journal.
type GovernedReplayPins struct {
	Provider             providerbridge.RecoveryPins
	Profile              broker.Profile
	Baseline             sessionrepo.GenerationEvidence
	StorePolicyDigest    string
	MetadataPolicyDigest string
	BashOutputPaths      []string
}

// GovernedRecoveryTranscript binds one retained provider conversation to the
// workflow checkpoint's ordered transcript pin. JournalIdentityDigest is
// supplied by the trusted host opener and is never inferred from the journal's
// contents or pathname.
type GovernedRecoveryTranscript struct {
	Journal        *audit.Journal
	JournalPath    string
	IdentityDigest string
	Pin            workflowrecovery.TranscriptPin
	Replay         GovernedReplayPins
}

// GovernedRecoveryProjection is a sanitized, ordered full-workflow replay.
// It contains no provider prompt, response, tool argument, output, or raw
// Store bytes.
type GovernedRecoveryProjection struct {
	Transcripts    []GovernedTranscriptProjection `json:"transcripts"`
	StoreTip       string                         `json:"store_tip_generation"`
	StoreTree      string                         `json:"store_tip_tree_digest"`
	StoreHead      string                         `json:"store_journal_head"`
	StoreCount     uint64                         `json:"store_journal_record_count"`
	AdmissionHead  string                         `json:"admission_journal_head"`
	AdmissionCount uint64                         `json:"admission_journal_record_count"`
	Digest         string                         `json:"projection_digest"`
}

type governedRecoveryCommandVerifier interface {
	VerifyStoreBundle(sessionrepo.EvidenceBundle) error
	VerifyGovernedVerifier(*governedStoreReplayVerifier) error
	VerifySettlement(sessionrepo.CommandSettlement, string, string) error
	VerifyUnleasedProviderCallRetirement(providerbridge.ProviderCallRetirementBinding) error
	VerifyIdleProviderLineageRetirement(string, string, uint64, uint64, uint64) error
	VerifyUnchanged() error
}

// GovernedTranscriptProjection keeps provider and Store tips separate. It
// contains commitments and counts only; it never returns provider history,
// prompts, raw arguments, effect bytes, or tool output.
type GovernedTranscriptProjection struct {
	SchemaVersion               string                              `json:"schema_version"`
	VerificationScope           string                              `json:"verification_scope"`
	Provider                    providerbridge.TranscriptProjection `json:"provider"`
	BaselineGeneration          string                              `json:"baseline_generation"`
	BaselineTreeDigest          string                              `json:"baseline_tree_digest"`
	StoreHeadGeneration         string                              `json:"store_head_generation"`
	StoreHeadTreeDigest         string                              `json:"store_head_tree_digest"`
	StorePolicyDigest           string                              `json:"store_policy_digest"`
	MetadataPolicyDigest        string                              `json:"metadata_policy_digest"`
	StoreOperationCount         uint64                              `json:"store_operation_count"`
	StoreEffectCount            uint64                              `json:"store_effect_count"`
	StoreAuditRecordCount       uint64                              `json:"store_audit_record_count"`
	StoreAuditHead              string                              `json:"store_audit_head"`
	AdmissionRecordCount        uint64                              `json:"admission_record_count"`
	AdmissionJournalHead        string                              `json:"admission_journal_head"`
	StoreDispositionQuarantined bool                                `json:"store_disposition_quarantined"`
	ProjectionDigest            string                              `json:"projection_digest"`
}

const governedTranscriptProjectionSchema = "tbound-governed-transcript-projection/v1"

type governedReplayOperationIntent struct {
	SchemaVersion string                       `json:"schema_version"`
	Request       sessionrepo.OperationRequest `json:"request"`
	Path          string                       `json:"path"`
	MaxBytes      int64                        `json:"max_bytes"`
	Command       sessionrepo.CommandSpec      `json:"command"`
	RunnerProfile string                       `json:"runner_profile"`
}

type governedReplayMutationEffect struct {
	Transition delta.Transition               `json:"transition"`
	Admission  sessionrepo.AdmissionReference `json:"admission"`
}

type governedReplayCommandEffect struct {
	Transition    delta.Transition               `json:"transition"`
	Admission     sessionrepo.AdmissionReference `json:"admission"`
	Command       sessionrepo.CommandResult      `json:"command"`
	Settlement    sessionrepo.CommandSettlement  `json:"settlement"`
	Spec          sessionrepo.CommandSpec        `json:"spec"`
	RunnerProfile string                         `json:"runner_profile"`
}

type governedReplayAdmissionRecord struct {
	SchemaVersion         string                       `json:"schema_version"`
	Request               sessionrepo.OperationRequest `json:"request"`
	IntentDigest          string                       `json:"intent_digest"`
	PolicyDigest          string                       `json:"policy_digest"`
	MetadataPolicyDigest  string                       `json:"metadata_policy_digest"`
	AdmissionPolicyDigest string                       `json:"admission_policy_digest"`
	Input                 delta.TreeManifest           `json:"input"`
	Output                delta.TreeManifest           `json:"output"`
	Candidate             delta.Transition             `json:"candidate"`
	Outcome               string                       `json:"outcome"`
}

type governedReplayDecisionKey struct {
	Sequence uint64
	Hash     string
}

type governedReplayDecisionEvidence struct {
	binding    ProviderDecisionBindingAlias
	request    sessionrepo.OperationRequest
	auth       audit.Record
	effect     audit.EffectTrace
	entry      sessionrepo.OperationEvidence
	output     []byte
	toGen      string
	toTree     string
	resultSeen bool
	// denied marks an ALLOWed operation whose settled change delta admission refused: the Store holds a
	// denied_after_effect operation and no transition, denyReason is the reason code recomputed from the
	// retained admission record and scope, and the only releasable result is the matching DENY.
	denied     bool
	denyReason string
}

// Alias keeps helper fields explicit while avoiding accidental retention in
// the public projection. The real providerbridge value is copied at callback.
type ProviderDecisionBindingAlias struct {
	WorkflowID, ConversationID, ProfileID, Model               string
	PolicyDigest, SystemPromptDigest, DeveloperPromptDigest    string
	ProviderSequence                                           uint64
	ResponseIDIssuer, ResponseID, ToolCallIDIssuer, ToolCallID string
	Proposal                                                   protocol.Proposal
	GateDecision                                               gate.Decision
	InputGeneration, InputTreeDigest                           string
	ExchangeRecordSequence                                     uint64
	ExchangeRecordHash                                         string
	DecisionRecordSequence                                     uint64
	DecisionRecordHash                                         string
}

type governedStoreReplayVerifier struct {
	pins                                      GovernedReplayPins
	providerTrace, storeTrace, admissionTrace audit.Trace
	bundle                                    sessionrepo.EvidenceBundle
	disposition                               sessionrepo.Disposition
	providerRecords                           map[uint64]audit.Record
	authByDecision                            map[governedReplayDecisionKey]audit.Record
	authByEffect                              map[string]audit.Record
	effectByID                                map[string]audit.EffectTrace
	operationByEffect                         map[string]sessionrepo.OperationEvidence
	transitionByID                            map[string]delta.Transition
	usedAuth                                  map[governedReplayDecisionKey]bool
	decisions                                 map[governedReplayDecisionKey]*governedReplayDecisionEvidence
	providerTipGeneration                     string
	providerTipTree                           string
	initialAuthorizationSequence              uint64
	lastAuthSequence                          uint64
	called                                    bool
}

// ReplayGovernedTranscript performs a read-only replay using held journal
// handles and a retained sessionrepo bundle. A nil, corrupt, unresolved,
// duplicate, or mismatched Store reference fails closed. This verifies only
// local cross-links and deterministic output reconstruction: it does not
// authenticate journal bytes against privileged rewrite/truncation, source
// truth, provider authenticity, D06/privategit provenance, checkpoints,
// budgets, process containment, or permission to reopen or resume anything.
func ReplayGovernedTranscript(providerJournal, storeJournal *audit.Journal, store *sessionrepo.Store,
	pins GovernedReplayPins) (GovernedTranscriptProjection, error) {
	projection, _, err := replayGovernedTranscript(providerJournal, storeJournal, store, pins, 0, true)
	return projection, err
}

// ReplayGovernedPredecessorContext runs the ReplayGovernedTranscript pipeline over a completed
// predecessor turn and returns its history as a PredecessorContext ending in the boundary message.
// The transcript must end at a final provider answer, and its Store evidence must be fully resolved.
func ReplayGovernedPredecessorContext(providerJournal, storeJournal *audit.Journal, store *sessionrepo.Store,
	pins GovernedReplayPins, boundary providerbridge.TurnBoundary) (providerbridge.PredecessorContext, GovernedTranscriptProjection, error) {
	var predecessor providerbridge.PredecessorContext
	projection, _, err := runGovernedReplay(providerJournal, storeJournal, store, pins, 0, true,
		func(journal *audit.Journal, provider providerbridge.RecoveryPins, verifier providerbridge.StoreReplayVerifier) (providerbridge.TranscriptProjection, error) {
			replayed, projection, err := providerbridge.ReplayPredecessorContext(journal, provider, verifier, boundary)
			predecessor = replayed
			return projection, err
		})
	if err != nil {
		return providerbridge.PredecessorContext{}, GovernedTranscriptProjection{}, err
	}
	return predecessor, projection, nil
}

func replayGovernedTranscript(providerJournal, storeJournal *audit.Journal, store *sessionrepo.Store,
	pins GovernedReplayPins, priorAuthorizationSequence uint64, requireStoreTip bool) (GovernedTranscriptProjection, *governedStoreReplayVerifier, error) {
	return runGovernedReplay(providerJournal, storeJournal, store, pins, priorAuthorizationSequence, requireStoreTip,
		providerbridge.ReplayTranscript)
}

// governedProviderReplay is the strict provider replay the governed pipeline runs with its Store verifier.
type governedProviderReplay func(*audit.Journal, providerbridge.RecoveryPins, providerbridge.StoreReplayVerifier) (providerbridge.TranscriptProjection, error)

func runGovernedReplay(providerJournal, storeJournal *audit.Journal, store *sessionrepo.Store,
	pins GovernedReplayPins, priorAuthorizationSequence uint64, requireStoreTip bool,
	replayProvider governedProviderReplay) (GovernedTranscriptProjection, *governedStoreReplayVerifier, error) {
	if providerJournal == nil || storeJournal == nil || store == nil {
		return GovernedTranscriptProjection{}, nil, errors.New("governed replay requires held provider/Store journals and the concrete Store")
	}
	if err := validateGovernedReplayPins(pins); err != nil {
		return GovernedTranscriptProjection{}, nil, err
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		return GovernedTranscriptProjection{}, nil, fmt.Errorf("snapshot concrete Store evidence: %w", err)
	}
	providerTrace, err := providerJournal.Trace()
	if err != nil {
		return GovernedTranscriptProjection{}, nil, err
	}
	storeTrace, err := storeJournal.Trace()
	if err != nil {
		return GovernedTranscriptProjection{}, nil, err
	}
	if !traceMatchesBundle(storeTrace, bundle.AuditJournal, bundle.AuditRecordCount, bundle.AuditHeadHash) {
		return GovernedTranscriptProjection{}, nil, errors.New("held Store journal differs from the retained canonical bundle")
	}
	admissionTrace, err := audit.Verify(bytes.NewReader(bundle.AdmissionJournal))
	if err != nil || uint64(len(admissionTrace.Records)) != bundle.AdmissionRecordCount || auditHeadHash(admissionTrace.Records) != bundle.AdmissionHeadHash {
		return GovernedTranscriptProjection{}, nil, errors.Join(errors.New("Store admission snapshot differs from its protected count/head"), err)
	}
	disposition, err := sessionrepo.Reconstruct(bundle)
	if err != nil {
		return GovernedTranscriptProjection{}, nil, fmt.Errorf("reconstruct retained Store evidence: %w", err)
	}
	if disposition.Quarantined || disposition.Unknowns != 0 || disposition.Successes != disposition.Attempts {
		return GovernedTranscriptProjection{}, nil, errors.New("Store evidence is unresolved or quarantined")
	}
	if bundle.Baseline.ID != pins.Baseline.ID || bundle.Baseline.TreeDigest != pins.Baseline.TreeDigest ||
		!sameManifest(bundle.Baseline.Manifest, pins.Baseline.Manifest) ||
		bundle.PolicyDigest != pins.StorePolicyDigest || bundle.MetadataPolicyDigest != pins.MetadataPolicyDigest {
		return GovernedTranscriptProjection{}, nil, errors.New("Store policy or D06 baseline pins differ from verified Store evidence")
	}
	if _, ok := findGovernedGeneration(bundle, pins.Provider.Config.InitialGenerationID, pins.Provider.Config.InitialTreeDigest); !ok {
		return GovernedTranscriptProjection{}, nil, errors.New("provider conversation starts from a generation outside the verified Store history")
	}
	verifier, err := newGovernedStoreReplayVerifier(pins, providerTrace, storeTrace, admissionTrace, bundle, disposition)
	if err != nil {
		return GovernedTranscriptProjection{}, nil, err
	}
	verifier.lastAuthSequence = priorAuthorizationSequence
	verifier.initialAuthorizationSequence = priorAuthorizationSequence
	projection, err := replayProvider(providerJournal, pins.Provider, verifier)
	if err != nil {
		return GovernedTranscriptProjection{}, nil, err
	}
	if verifier.called && projection.VerificationScope != providerbridge.ProjectionScopeStoreCrosslink {
		return GovernedTranscriptProjection{}, nil, errors.New("provider replay did not record successful Store cross-link verification")
	}
	if !verifier.called && projection.VerificationScope != providerbridge.ProjectionScopeProviderOnly {
		return GovernedTranscriptProjection{}, nil, errors.New("provider replay claimed a Store callback that did not run")
	}
	if err := verifier.finish(projection, requireStoreTip); err != nil {
		return GovernedTranscriptProjection{}, nil, err
	}
	providerAfter, err := providerJournal.Trace()
	if err != nil {
		return GovernedTranscriptProjection{}, nil, err
	}
	storeAfter, err := storeJournal.Trace()
	if err != nil {
		return GovernedTranscriptProjection{}, nil, err
	}
	bundleAfter, bundleErr := store.EvidenceBundle()
	if bundleErr != nil {
		return GovernedTranscriptProjection{}, nil, bundleErr
	}
	if !reflect.DeepEqual(providerTrace, providerAfter) || !reflect.DeepEqual(storeTrace, storeAfter) ||
		!reflect.DeepEqual(bundle, bundleAfter) {
		return GovernedTranscriptProjection{}, nil, errors.New("a held audit journal changed during governed replay")
	}
	storeOperationCount, storeEffectCount := verifier.scopedStoreCounts()
	result := GovernedTranscriptProjection{
		SchemaVersion:     governedTranscriptProjectionSchema,
		VerificationScope: "store_crosslinked",
		Provider:          projection, BaselineGeneration: bundle.Baseline.ID, BaselineTreeDigest: bundle.Baseline.TreeDigest,
		StoreHeadGeneration: disposition.Sealed, StoreHeadTreeDigest: bundle.Generations[len(bundle.Generations)-1].TreeDigest,
		StorePolicyDigest: bundle.PolicyDigest, MetadataPolicyDigest: bundle.MetadataPolicyDigest,
		StoreOperationCount: storeOperationCount, StoreEffectCount: storeEffectCount,
		StoreAuditRecordCount: bundle.AuditRecordCount, StoreAuditHead: bundle.AuditHeadHash,
		AdmissionRecordCount: bundle.AdmissionRecordCount, AdmissionJournalHead: bundle.AdmissionHeadHash,
		StoreDispositionQuarantined: disposition.Quarantined,
	}
	if !verifier.called {
		result.VerificationScope = "baseline_crosslinked"
	}
	result.ProjectionDigest = governedTranscriptProjectionDigest(result)
	return result, verifier, nil
}

func (v *governedStoreReplayVerifier) resetForReadOnlyReplay() {
	if v == nil {
		return
	}
	v.usedAuth = make(map[governedReplayDecisionKey]bool)
	v.decisions = make(map[governedReplayDecisionKey]*governedReplayDecisionEvidence)
	v.lastAuthSequence = v.initialAuthorizationSequence
	v.called = false
}

// VerifyGovernedTranscriptProjectionCommitment checks the domain-separated
// commitment over the sanitized outer result, including the provider
// projection commitment and separate baseline/Store heads.
func VerifyGovernedTranscriptProjectionCommitment(projection GovernedTranscriptProjection) bool {
	return projection.SchemaVersion == governedTranscriptProjectionSchema &&
		providerbridge.VerifyTranscriptProjectionCommitment(projection.Provider) && projection.ProjectionDigest != "" &&
		projection.ProjectionDigest == governedTranscriptProjectionDigest(projection)
}

func governedTranscriptProjectionDigest(projection GovernedTranscriptProjection) string {
	projection.ProjectionDigest = ""
	encoded, err := json.Marshal(projection)
	if err != nil {
		return ""
	}
	h := sha256.New()
	_, _ = h.Write([]byte(governedTranscriptProjectionSchema))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(encoded)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// ReplayGovernedHistory verifies every retained provider conversation, in
// order, against the same concrete Store history. Earlier conversations must
// end at the next conversation's pinned starting generation; the last must
// end at the current Store tip. No raw history or operation output is returned.
func ReplayGovernedHistory(storeJournal *audit.Journal, store *sessionrepo.Store,
	transcripts []GovernedRecoveryTranscript, commandVerifiers ...governedRecoveryCommandVerifier) (GovernedRecoveryProjection, error) {
	projection, _, err := replayGovernedHistory(storeJournal, store, transcripts, false, commandVerifiers...)
	return projection, err
}

// ReplayGovernedHistoryForRecovery permits only a final captured call that is
// still waiting for its first gate decision. The returned verifier is fresh
// and read-only, and is used solely to append an externally verified lineage
// retirement before the strict full replay and checkpoint observation.
func ReplayGovernedHistoryForRecovery(storeJournal *audit.Journal, store *sessionrepo.Store,
	transcripts []GovernedRecoveryTranscript, commandVerifiers ...governedRecoveryCommandVerifier) (GovernedRecoveryProjection, *governedStoreReplayVerifier, error) {
	return replayGovernedHistory(storeJournal, store, transcripts, true, commandVerifiers...)
}

func replayGovernedHistory(storeJournal *audit.Journal, store *sessionrepo.Store,
	transcripts []GovernedRecoveryTranscript, allowPendingLatest bool,
	commandVerifiers ...governedRecoveryCommandVerifier) (GovernedRecoveryProjection, *governedStoreReplayVerifier, error) {
	if storeJournal == nil || store == nil {
		return GovernedRecoveryProjection{}, nil, errors.New("full recovery replay requires the held Store journal and read-only Store preview")
	}
	if len(commandVerifiers) > 1 {
		return GovernedRecoveryProjection{}, nil, errors.New("full recovery replay accepts at most one old command-journal verifier")
	}
	bundle, err := store.EvidenceBundle()
	if err != nil {
		return GovernedRecoveryProjection{}, nil, err
	}
	storeTrace, err := storeJournal.Trace()
	if err != nil {
		return GovernedRecoveryProjection{}, nil, err
	}
	if !traceMatchesBundle(storeTrace, bundle.AuditJournal, bundle.AuditRecordCount, bundle.AuditHeadHash) {
		return GovernedRecoveryProjection{}, nil, errors.New("full recovery replay Store journal differs from its read-only evidence bundle")
	}
	disposition, err := sessionrepo.Reconstruct(bundle)
	if err != nil || disposition.Quarantined || disposition.Unknowns != 0 || disposition.Successes != disposition.Attempts {
		return GovernedRecoveryProjection{}, nil, errors.Join(errors.New("full recovery replay refuses unresolved Store effects"), err)
	}
	if len(bundle.Generations) == 0 {
		return GovernedRecoveryProjection{}, nil, sessionrepo.ErrNoBaseline
	}
	hasSuccessfulBash := false
	for _, operation := range disposition.Operations {
		if operation.Tool == "bash" && operation.Outcome == "success" {
			hasSuccessfulBash = true
		}
	}
	if hasSuccessfulBash && len(commandVerifiers) != 1 {
		return GovernedRecoveryProjection{}, nil, errors.New("historical Bash effects require their independently held old command context")
	}
	if len(transcripts) == 0 {
		if len(disposition.Operations) != 0 || disposition.Sealed != bundle.Baseline.ID {
			return GovernedRecoveryProjection{}, nil, errors.New("Store activity has no retained provider transcript to authorize it")
		}
	}
	projections := make([]GovernedTranscriptProjection, 0, len(transcripts))
	seen := make(map[string]struct{}, len(transcripts))
	expectedGeneration, expectedTree := bundle.Baseline.ID, bundle.Baseline.TreeDigest
	var priorAuthorizationSequence uint64
	var replayedOperations, replayedEffects uint64
	workflowID := ""
	var lastVerifier *governedStoreReplayVerifier
	for index, transcript := range transcripts {
		replayPins := transcript.Replay
		if replayPins.Provider.Config.InitialGenerationID == "" {
			replayPins.Provider.Config.InitialGenerationID = expectedGeneration
		}
		if replayPins.Provider.Config.InitialTreeDigest == "" {
			replayPins.Provider.Config.InitialTreeDigest = expectedTree
		}
		if replayPins.Profile.Generation == "" {
			replayPins.Profile.Generation = expectedGeneration
		}
		transcript.Replay = replayPins
		if transcript.Journal == nil || transcript.JournalPath == "" || transcript.IdentityDigest == "" ||
			transcript.IdentityDigest != transcript.Pin.JournalIdentityDigest ||
			transcript.Pin.Ordinal != uint64(index+1) || transcript.Pin.ConversationID == "" ||
			transcript.Replay.Provider.Config.WorkflowID == "" ||
			transcript.Replay.Provider.Config.ConversationID != transcript.Pin.ConversationID ||
			transcript.Replay.Provider.Config.InitialGenerationID != expectedGeneration ||
			transcript.Replay.Provider.Config.InitialTreeDigest != expectedTree {
			return GovernedRecoveryProjection{}, nil, fmt.Errorf("retained provider transcript %d is missing a host pin or breaks the ordered Store lineage", index)
		}
		journalIdentity, err := recoveryFileIdentityDigest(transcript.JournalPath, false)
		if err != nil || journalIdentity != transcript.IdentityDigest {
			return GovernedRecoveryProjection{}, nil, errors.Join(fmt.Errorf("provider transcript %d journal identity differs from its trusted pin", index), err)
		}
		if workflowID == "" {
			workflowID = transcript.Replay.Provider.Config.WorkflowID
		} else if workflowID != transcript.Replay.Provider.Config.WorkflowID {
			return GovernedRecoveryProjection{}, nil, errors.New("ordered recovery transcripts cross workflow identities")
		}
		if _, duplicate := seen[transcript.Pin.ConversationID]; duplicate {
			return GovernedRecoveryProjection{}, nil, errors.New("recovery transcript repeats a conversation identity")
		}
		seen[transcript.Pin.ConversationID] = struct{}{}
		journalTrace, err := transcript.Journal.Trace()
		if err != nil {
			return GovernedRecoveryProjection{}, nil, fmt.Errorf("verify retained provider transcript %d: %w", index, err)
		}
		if transcript.Pin.JournalCount > uint64(len(journalTrace.Records)) ||
			(transcript.Pin.JournalCount == 0 && transcript.Pin.JournalHead != "") ||
			(transcript.Pin.JournalCount > 0 && journalTrace.Records[transcript.Pin.JournalCount-1].Hash != transcript.Pin.JournalHead) {
			return GovernedRecoveryProjection{}, nil, fmt.Errorf("provider transcript %d does not retain its checkpointed journal prefix", index)
		}
		if transcript.Pin.ProjectionCount > uint64(len(journalTrace.Records)) ||
			(transcript.Pin.ProjectionCount == 0 && (transcript.Pin.ProjectionDigest != "" || transcript.Pin.ProjectionHead != "")) ||
			(transcript.Pin.ProjectionCount > 0 && journalTrace.Records[transcript.Pin.ProjectionCount-1].Hash != transcript.Pin.ProjectionHead) {
			return GovernedRecoveryProjection{}, nil, fmt.Errorf("provider transcript %d projection head is not a retained journal prefix", index)
		}
		transcript.Replay.Baseline = bundle.Baseline
		projection, verifier, err := replayGovernedTranscript(transcript.Journal, storeJournal, store,
			transcript.Replay, priorAuthorizationSequence, index == len(transcripts)-1)
		if err != nil {
			return GovernedRecoveryProjection{}, nil, fmt.Errorf("cross-link retained provider transcript %d: %w", index, err)
		}
		lastVerifier = verifier
		if len(commandVerifiers) == 1 {
			if err := commandVerifiers[0].VerifyGovernedVerifier(verifier); err != nil {
				return GovernedRecoveryProjection{}, nil, fmt.Errorf("cross-link retained Bash authority for transcript %d: %w", index, err)
			}
		}
		if projection.Provider.ProviderJournalRecordCount != uint64(len(journalTrace.Records)) ||
			projection.Provider.ProviderJournalHead != transcript.Pin.JournalHead &&
				transcript.Pin.JournalCount == uint64(len(journalTrace.Records)) ||
			projection.Provider.EventCount < transcript.Pin.ProjectionCount ||
			(projection.Provider.EventCount == transcript.Pin.ProjectionCount &&
				transcript.Pin.ProjectionCount > 0 &&
				(bareWorkflowDigest(projection.Provider.ProjectionDigest) != transcript.Pin.ProjectionDigest ||
					projection.Provider.ProviderJournalHead != transcript.Pin.ProjectionHead)) {
			return GovernedRecoveryProjection{}, nil, fmt.Errorf("provider transcript %d differs from its exact checkpointed projection", index)
		}
		pendingLatest := allowPendingLatest && index == len(transcripts)-1 &&
			projection.Provider.Disposition == providerbridge.DispositionAwaitingGateDecision && projection.Provider.PendingCall != nil
		if projection.Provider.Disposition == providerbridge.DispositionPendingProviderResult ||
			(projection.Provider.Disposition == providerbridge.DispositionAwaitingGateDecision && !pendingLatest) {
			return GovernedRecoveryProjection{}, nil, workflowrecovery.ErrPendingUnknown
		}
		retiredUnexecuted := projection.Provider.Disposition == providerbridge.DispositionRetiredUnexecuted &&
			projection.Provider.RetiredCall != nil && projection.Provider.PendingCall == nil
		if projection.Provider.DecisionCount != projection.Provider.ResultCount ||
			(!pendingLatest && !retiredUnexecuted && projection.Provider.CapturedCallCount != projection.Provider.DecisionCount) ||
			((pendingLatest || retiredUnexecuted) && projection.Provider.CapturedCallCount != projection.Provider.DecisionCount+1) {
			return GovernedRecoveryProjection{}, nil, fmt.Errorf("provider transcript %d contains an unaccounted captured call", index)
		}
		if !VerifyGovernedTranscriptProjectionCommitment(projection) {
			return GovernedRecoveryProjection{}, nil, errors.New("provider transcript projection commitment is invalid")
		}
		projections = append(projections, projection)
		replayedOperations += projection.StoreOperationCount
		replayedEffects += projection.StoreEffectCount
		if verifier.lastAuthSequence > priorAuthorizationSequence {
			priorAuthorizationSequence = verifier.lastAuthSequence
		}
		expectedGeneration, expectedTree = projection.Provider.ProviderGenerationID, projection.Provider.ProviderTreeDigest
	}
	if expectedGeneration != disposition.Sealed || expectedTree != bundle.Generations[len(bundle.Generations)-1].TreeDigest ||
		replayedOperations != uint64(len(disposition.Operations)) || replayedEffects != uint64(len(disposition.Operations)) {
		return GovernedRecoveryProjection{}, nil, errors.New("full provider transcript set does not account for the exact current Store tip and operations")
	}
	if len(commandVerifiers) == 1 {
		if err := commandVerifiers[0].VerifyStoreBundle(bundle); err != nil {
			return GovernedRecoveryProjection{}, nil, fmt.Errorf("verify all retained D09 command settlements against Store evidence: %w", err)
		}
	}
	admissionTrace, err := audit.Verify(bytes.NewReader(bundle.AdmissionJournal))
	if err != nil {
		return GovernedRecoveryProjection{}, nil, err
	}
	storeHead := auditHeadHash(storeTrace.Records)
	admissionHead := auditHeadHash(admissionTrace.Records)
	result := GovernedRecoveryProjection{
		Transcripts: projections, StoreTip: disposition.Sealed,
		StoreTree: bundle.Generations[len(bundle.Generations)-1].TreeDigest,
		StoreHead: storeHead, StoreCount: uint64(len(storeTrace.Records)),
		AdmissionHead: admissionHead, AdmissionCount: uint64(len(admissionTrace.Records)),
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return GovernedRecoveryProjection{}, nil, err
	}
	h := sha256.Sum256(append([]byte("tbound-governed-recovery-projection/v1\x00"), encoded...))
	result.Digest = "sha256:" + hex.EncodeToString(h[:])
	return result, lastVerifier, nil
}

func findGovernedGeneration(bundle sessionrepo.EvidenceBundle, id, treeDigest string) (sessionrepo.GenerationEvidence, bool) {
	for _, generation := range bundle.Generations {
		if generation.ID == id && generation.TreeDigest == treeDigest {
			return generation, true
		}
	}
	return sessionrepo.GenerationEvidence{}, false
}

func validateGovernedReplayPins(pins GovernedReplayPins) error {
	cfg := pins.Provider.Config
	profile := pins.Profile
	route := profile.Route.Resolved()
	wantMessages := providerbridge.InitialPromptMessages(route, cfg.SystemPrompt, cfg.DeveloperPrompt)
	if cfg.WorkflowID == "" || cfg.ConversationID == "" || cfg.ProfileID == "" || cfg.InitialGenerationID == "" ||
		cfg.InitialTreeDigest == "" || cfg.SystemPrompt == "" || cfg.DeveloperPrompt == "" ||
		pins.Provider.Model != route.Model || cfg.Route.Resolved() != route || profile.ID != cfg.ProfileID ||
		profile.Model != route.Model || profile.ToolCallIssuer != governedCallIssuer ||
		profile.ResponseIDIssuer != governedResponseIssuer || profile.Generation != cfg.InitialGenerationID ||
		!reflect.DeepEqual(profile.ToolManifest, broker.DeclaredToolManifest()) || !reflect.DeepEqual(profile.Messages, wantMessages) ||
		!validBareSHA256(pins.StorePolicyDigest) || !validBareSHA256(pins.MetadataPolicyDigest) {
		return errors.New("governed replay host profile, policy, or baseline pin is incomplete")
	}
	if pins.BashOutputPaths != nil {
		if err := sessionrepo.ValidateOutputScopePaths(pins.BashOutputPaths); err != nil {
			return err
		}
	}
	return nil
}

func stringPointer(value string) *string { copy := value; return &copy }

func traceMatchesBundle(trace audit.Trace, encoded []byte, count uint64, head string) bool {
	canonical, err := marshalGovernedAuditRecords(trace.Records)
	return err == nil && bytes.Equal(canonical, encoded) && uint64(len(trace.Records)) == count && auditHeadHash(trace.Records) == head
}

func newGovernedStoreReplayVerifier(pins GovernedReplayPins, providerTrace, storeTrace, admissionTrace audit.Trace,
	bundle sessionrepo.EvidenceBundle, disposition sessionrepo.Disposition) (*governedStoreReplayVerifier, error) {
	v := &governedStoreReplayVerifier{
		pins: pins, providerTrace: providerTrace, storeTrace: storeTrace, admissionTrace: admissionTrace,
		bundle: bundle, disposition: disposition,
		providerRecords: make(map[uint64]audit.Record), authByDecision: make(map[governedReplayDecisionKey]audit.Record),
		authByEffect: make(map[string]audit.Record),
		effectByID:   make(map[string]audit.EffectTrace), operationByEffect: make(map[string]sessionrepo.OperationEvidence),
		transitionByID: make(map[string]delta.Transition), usedAuth: make(map[governedReplayDecisionKey]bool),
		decisions:             make(map[governedReplayDecisionKey]*governedReplayDecisionEvidence),
		providerTipGeneration: pins.Provider.Config.InitialGenerationID,
		providerTipTree:       pins.Provider.Config.InitialTreeDigest,
	}
	for _, record := range providerTrace.Records {
		v.providerRecords[record.Sequence] = record
	}
	for _, effect := range storeTrace.Effects {
		if _, duplicate := v.effectByID[effect.ID]; duplicate {
			return nil, errors.New("Store journal repeats an effect reference")
		}
		v.effectByID[effect.ID] = effect
	}
	for _, op := range disposition.Operations {
		if _, duplicate := v.operationByEffect[op.EffectID]; duplicate {
			return nil, errors.New("Store evidence repeats an operation effect reference")
		}
		v.operationByEffect[op.EffectID] = op
	}
	for _, transition := range bundle.ApprovedDeltaLedger {
		if _, duplicate := v.transitionByID[transition.ID]; duplicate {
			return nil, errors.New("Store ledger repeats a transition reference")
		}
		v.transitionByID[transition.ID] = transition
	}
	for _, record := range storeTrace.Records {
		if record.Event.Kind != governedOperationAuthorizationKind {
			continue
		}
		var auth governedOperationAdmissionRecord
		if decodeCanonicalGoverned(record.Event.Data, &auth) != nil || auth.SchemaVersion != "tbound-governed-operation-admission/v2" ||
			record.Event.ID != "governed-operation:"+auth.EffectID || auth.EffectID == "" || auth.GateDecision.Verdict != gate.Allow {
			return nil, errors.New("Store contains malformed or non-ALLOW governed authorization evidence")
		}
		key := governedReplayDecisionKey{auth.ProviderDecisionSequence, auth.ProviderDecisionRecordHash}
		if key.Sequence == 0 || key.Hash == "" {
			return nil, errors.New("Store authorization omits its provider decision reference")
		}
		if _, duplicate := v.authByDecision[key]; duplicate {
			return nil, errors.New("provider gate decision has duplicate Store authorizations")
		}
		if _, duplicate := v.authByEffect[auth.EffectID]; duplicate {
			return nil, errors.New("Store effect has duplicate governed authorizations")
		}
		v.authByDecision[key] = record
		v.authByEffect[auth.EffectID] = record
	}
	for _, op := range disposition.Operations {
		if op.Outcome != "success" && !deniedAfterEffect(op) {
			return nil, errors.New("Store contains an operation without a successful, releasable outcome")
		}
	}
	return v, nil
}

func (v *governedStoreReplayVerifier) VerifyDecision(binding providerbridge.ProviderDecisionBinding) error {
	if v == nil {
		return errors.New("Store replay verifier is unavailable")
	}
	if binding.WorkflowID != v.pins.Provider.Config.WorkflowID || binding.ConversationID != v.pins.Provider.Config.ConversationID ||
		binding.ProfileID != v.pins.Profile.ID || binding.Model != v.pins.Profile.Model || binding.PolicyDigest != v.pins.Provider.Policy.Digest() ||
		binding.SystemPromptDigest != digestBytes([]byte(v.pins.Provider.Config.SystemPrompt)) ||
		binding.DeveloperPromptDigest != digestBytes([]byte(v.pins.Provider.Config.DeveloperPrompt)) ||
		binding.InputGeneration != v.providerTipGeneration || binding.InputTreeDigest != v.providerTipTree ||
		binding.ResponseIDIssuer != governedResponseIssuer || binding.ToolCallIDIssuer != governedCallIssuer ||
		binding.Proposal.ToolCallID != binding.ToolCallID || binding.Proposal.Tool == "" {
		return errors.New("provider decision differs from the immutable host profile or released Store-linked tip")
	}
	if err := v.verifyGateDecision(binding); err != nil {
		return err
	}
	key := governedReplayDecisionKey{binding.DecisionRecordSequence, binding.DecisionRecordHash}
	if _, duplicate := v.decisions[key]; duplicate {
		return errors.New("provider decision callback repeated a protected record")
	}
	decisionRecord, ok := v.providerRecords[key.Sequence]
	if !ok || decisionRecord.Hash != key.Hash || decisionRecord.Event.Kind != "provider_gate_decision" {
		return errors.New("provider decision sequence/hash does not resolve to exactly one protected gate record")
	}
	var stored gate.Decision
	if decodeCanonicalGoverned(decisionRecord.Event.Data, &stored) != nil || !reflect.DeepEqual(stored, binding.GateDecision) {
		return errors.New("provider gate record differs from callback decision")
	}
	if binding.ExchangeRecordSequence >= key.Sequence || binding.ExchangeRecordHash == "" {
		return errors.New("provider decision has an invalid exchange record reference")
	}
	exchange, ok := v.providerRecords[binding.ExchangeRecordSequence]
	if !ok || exchange.Hash != binding.ExchangeRecordHash || exchange.Event.Kind != "provider_exchange" {
		return errors.New("provider exchange sequence/hash does not resolve to exactly one protected record")
	}
	var exchangeEvidence providerExchangeEvidence
	if decodeCanonicalGoverned(exchange.Event.Data, &exchangeEvidence) != nil || exchangeEvidence.WorkflowID != binding.WorkflowID ||
		exchangeEvidence.Turn != binding.ProviderSequence || exchangeEvidence.ResponseIDIssuer != binding.ResponseIDIssuer ||
		exchangeEvidence.ResponseID != binding.ResponseID || exchangeEvidence.ToolCallIDIssuer != binding.ToolCallIDIssuer ||
		exchangeEvidence.ToolCall == nil || exchangeEvidence.ToolCall.ID != binding.ToolCallID ||
		exchangeEvidence.ToolCall.Name != binding.Proposal.Tool || !sameGovernedToolCallProjection(exchangeEvidence.ToolCall, binding.Proposal) ||
		exchangeEvidence.Request.ProfileID != v.pins.Profile.ID || !v.pins.Profile.Route.AcceptsResponseModel(exchangeEvidence.Model) {
		return errors.New("provider exchange does not bind the exact call proposal and pinned profile")
	}
	if binding.GateDecision.Verdict == gate.Deny {
		if _, exists := v.authByDecision[key]; exists {
			return errors.New("DENY provider decision has a Store authorization")
		}
		v.decisions[key] = &governedReplayDecisionEvidence{binding: aliasDecision(binding)}
		v.called = true
		return nil
	}
	if binding.GateDecision.Verdict != gate.Allow {
		return errors.New("provider decision has unsupported verdict")
	}
	authRecord, exists := v.authByDecision[key]
	if !exists {
		return errors.New("ALLOW provider decision has no unique Store authorization")
	}
	if v.usedAuth[key] || authRecord.Sequence <= v.lastAuthSequence {
		return errors.New("Store authorizations are duplicated or out of provider decision order")
	}
	var auth governedOperationAdmissionRecord
	if decodeCanonicalGoverned(authRecord.Event.Data, &auth) != nil {
		return errors.New("Store authorization is not canonical")
	}
	request, effect, operation, err := v.bindStoreOperation(binding, auth)
	if err != nil {
		return err
	}
	if authRecord.Sequence >= effect.IntentSequence {
		return errors.New("Store authorization was not durably recorded before its effect intent")
	}
	verified := &governedReplayDecisionEvidence{binding: aliasDecision(binding), request: request,
		auth: authRecord, effect: effect, entry: operation}
	if deniedAfterEffect(operation) {
		reason, err := v.deniedAdmissionReason(request, effect, operation)
		if err != nil {
			return err
		}
		verified.denied, verified.denyReason = true, reason
		v.usedAuth[key] = true
		v.lastAuthSequence = authRecord.Sequence
		v.decisions[key] = verified
		v.called = true
		return nil
	}
	expected, toGeneration, toTree, _, _, _, err := v.expectedDurableOutput(verified, binding)
	if err != nil {
		return err
	}
	if len(expected) == 0 {
		return errors.New("Store output reconstruction returned an empty provider summary")
	}
	verified.output = append([]byte(nil), expected...)
	verified.toGen, verified.toTree = toGeneration, toTree
	v.usedAuth[key] = true
	v.lastAuthSequence = authRecord.Sequence
	v.decisions[key] = verified
	v.called = true
	return nil
}

// sameGovernedToolCallProjection compares the redundant journal projection
// using the same JSON encoding path as the writer. RawMessage arguments are
// HTML-escaped when nested by encoding/json, so their persisted in-memory bytes
// can differ from the original CaptureSSE proposal even though the canonical
// projection matches. The protected exchange response and request remain
// checked separately as exact bytes.
func sameGovernedToolCallProjection(recorded *struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}, proposal protocol.Proposal) bool {
	if recorded == nil {
		return false
	}
	type toolCallProjection struct {
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	expected := toolCallProjection{
		ID: proposal.ToolCallID, Name: proposal.Tool,
		Arguments: append(json.RawMessage(nil), proposal.Arguments...),
	}
	actual := toolCallProjection{
		ID: recorded.ID, Name: recorded.Name,
		Arguments: append(json.RawMessage(nil), recorded.Arguments...),
	}
	want, err := json.Marshal(expected)
	if err != nil {
		return false
	}
	got, err := json.Marshal(actual)
	return err == nil && bytes.Equal(got, want)
}

func (v *governedStoreReplayVerifier) verifyGateDecision(binding providerbridge.ProviderDecisionBinding) error {
	decision := binding.GateDecision
	if decision.PolicyDigest != v.pins.Provider.Policy.Digest() || decision.Tool != binding.Proposal.Tool ||
		decision.ToolCallID != binding.ToolCallID || decision.Sequence != binding.ProviderSequence || decision.ResponseID == nil ||
		decision.ResponseID.Issuer != governedResponseIssuer || decision.ResponseID.Opaque != binding.ResponseID {
		return errors.New("gate decision correlation, sequence, or policy digest differs from pinned replay inputs")
	}
	digest, err := protocol.CanonicalArgumentsDigest(binding.Proposal.Tool, binding.Proposal.Arguments)
	if err != nil || digest != decision.CanonicalArgumentsDigest {
		return errors.New("gate decision raw proposal digest differs")
	}
	stream, err := protocol.NewStream(governedCallIssuer, governedResponseIssuer)
	if err != nil {
		return err
	}
	captured := stream.Capture(protocol.TrustedCapture{
		ResponseID: correlation.Identifier{Issuer: governedResponseIssuer, Opaque: binding.ResponseID},
		ToolCallID: correlation.Identifier{Issuer: governedCallIssuer, Opaque: binding.ToolCallID},
		ToolName:   binding.Proposal.Tool, RawArguments: append(json.RawMessage(nil), binding.Proposal.Arguments...),
		CanonicalizationProfile: protocol.CanonicalizationProfile, CanonicalArgumentsDigest: digest,
		Sequence: binding.ProviderSequence, Generation: binding.InputGeneration,
	})
	proposalBytes, err := protocol.MarshalProposal(binding.Proposal)
	if err != nil || !captured.Accepted || captured.StreamClosed {
		return errors.New("provider call cannot be independently registered for gate replay")
	}
	matched := stream.Propose(proposalBytes)
	expected := gate.Evaluate(binding.Proposal, matched, v.pins.Provider.Policy)
	if !matched.Accepted || matched.StreamClosed || !reflect.DeepEqual(expected, decision) {
		return errors.New("provider gate record differs from deterministic evaluation under the pinned policy")
	}
	return nil
}

func (v *governedStoreReplayVerifier) bindStoreOperation(binding providerbridge.ProviderDecisionBinding,
	auth governedOperationAdmissionRecord) (sessionrepo.OperationRequest, audit.EffectTrace, sessionrepo.OperationEvidence, error) {
	operation, err := durableOperationIdentity(binding.Proposal, binding.GateDecision, governedCallIssuer)
	if err != nil {
		return sessionrepo.OperationRequest{}, audit.EffectTrace{}, sessionrepo.OperationEvidence{}, err
	}
	decision, err := durablePolicyDecision(binding.Proposal, binding.GateDecision, v.pins.StorePolicyDigest, v.pins.MetadataPolicyDigest)
	if err != nil {
		return sessionrepo.OperationRequest{}, audit.EffectTrace{}, sessionrepo.OperationEvidence{}, err
	}
	argumentDigest, err := sessionrepoArgumentDigest(binding.Proposal, v.pins.BashOutputPaths)
	if err != nil {
		return sessionrepo.OperationRequest{}, audit.EffectTrace{}, sessionrepo.OperationEvidence{}, err
	}
	paths, err := governedOperationOutputScope(binding.Proposal, v.pins.BashOutputPaths)
	if err != nil {
		return sessionrepo.OperationRequest{}, audit.EffectTrace{}, sessionrepo.OperationEvidence{}, err
	}
	if auth.WorkflowID != binding.WorkflowID || auth.ConversationID != binding.ConversationID || auth.ProviderProfileID != binding.ProfileID ||
		auth.EffectID == "" || auth.DecisionID != decision.ID || auth.Operation != operation || auth.Tool != binding.Proposal.Tool ||
		!sameDurableOutputScopePaths(auth.OutputScopePaths, paths) || auth.TypedArgumentDigest != argumentDigest ||
		auth.InputGeneration != binding.InputGeneration || auth.InputTreeDigest != binding.InputTreeDigest ||
		auth.GatePolicyDigest != binding.PolicyDigest || auth.ProviderDecisionSequence != binding.DecisionRecordSequence ||
		auth.ProviderDecisionRecordHash != binding.DecisionRecordHash || !reflect.DeepEqual(auth.GateDecision, binding.GateDecision) {
		return sessionrepo.OperationRequest{}, audit.EffectTrace{}, sessionrepo.OperationEvidence{}, errors.New("Store authorization does not bind the exact provider decision and typed operation")
	}
	request := sessionrepo.OperationRequest{Tool: binding.Proposal.Tool, Operation: operation, Decision: decision,
		InputGeneration: binding.InputGeneration, InputTreeDigest: binding.InputTreeDigest, ArgumentDigest: argumentDigest,
		EffectID: auth.EffectID, OutputScopePaths: cloneDurableOutputScopePaths(paths)}
	if binding.Proposal.Tool == "bash" {
		// The authorization record does not carry the view ID/context; they are
		// recovered from the exact Store effect intent below.
	}
	effect, ok := v.effectByID[auth.EffectID]
	if !ok || effect.Unresolved || effect.Outcome != "success" || effect.OutcomeSequence == 0 ||
		effect.IntentSequence == 0 || effect.IntentSequence >= effect.OutcomeSequence || effect.Intent == nil {
		return sessionrepo.OperationRequest{}, audit.EffectTrace{}, sessionrepo.OperationEvidence{}, errors.New("authorized Store effect is absent, unresolved, or unsuccessful")
	}
	var intent governedReplayOperationIntent
	if decodeCanonicalGoverned(effect.Intent, &intent) != nil || intent.SchemaVersion != sessionrepo.OperationIntentSchemaVersion ||
		intent.Request.EffectID != effect.ID || intent.Request.Tool != request.Tool || intent.Request.Operation != request.Operation ||
		!reflect.DeepEqual(intent.Request.Decision, request.Decision) || intent.Request.InputGeneration != request.InputGeneration ||
		intent.Request.InputTreeDigest != request.InputTreeDigest || intent.Request.ArgumentDigest != request.ArgumentDigest ||
		!sameDurableOutputScopePaths(intent.Request.OutputScopePaths, request.OutputScopePaths) {
		return sessionrepo.OperationRequest{}, audit.EffectTrace{}, sessionrepo.OperationEvidence{}, errors.New("Store effect intent differs from reconstructed provider operation mapping")
	}
	request = intent.Request
	if binding.Proposal.Tool != "bash" && (request.ViewID != "" || request.ExecutionContextDigest != "") {
		return sessionrepo.OperationRequest{}, audit.EffectTrace{}, sessionrepo.OperationEvidence{}, errors.New("non-Bash Store intent carries command-view authority")
	}
	if binding.Proposal.Tool == "bash" {
		intent.Command.DeclaredOutputPaths = cloneDurableOutputScopePaths(request.OutputScopePaths)
		command, commandErr := durableBashArguments(binding.Proposal.Arguments)
		wantSpec := sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", command},
			DeclaredOutputPaths: cloneDurableOutputScopePaths(v.pins.BashOutputPaths)}
		if commandErr != nil || !sameReplayCommandSpec(intent.Command, wantSpec) || intent.RunnerProfile == "" ||
			request.ViewID == "" || request.ExecutionContextDigest != governedExecutionContextDigest(request.ViewID, request.InputTreeDigest, request.Operation.LeaseID) {
			return sessionrepo.OperationRequest{}, audit.EffectTrace{}, sessionrepo.OperationEvidence{}, errors.New("Bash intent differs from exact pinned command, scope, or execution context")
		}
	} else if !sameReplayCommandSpec(intent.Command, sessionrepo.CommandSpec{}) || intent.RunnerProfile != "" {
		return sessionrepo.OperationRequest{}, audit.EffectTrace{}, sessionrepo.OperationEvidence{}, errors.New("non-Bash Store intent carries command authority")
	}
	entry, ok := v.operationByEffect[auth.EffectID]
	if !ok || entry.AuditSequence != effect.OutcomeSequence || entry.Tool != request.Tool || entry.Operation != request.Operation ||
		entry.DecisionID != decision.ID || entry.ArgumentDigest != argumentDigest || entry.InputGeneration != request.InputGeneration ||
		entry.InputTreeDigest != request.InputTreeDigest || (entry.Outcome != "success" && !deniedAfterEffect(entry)) ||
		entry.ResultDigest != digestBytes(effect.Result) {
		return sessionrepo.OperationRequest{}, audit.EffectTrace{}, sessionrepo.OperationEvidence{}, errors.New("Store OperationEvidence does not bind the authorized successful effect")
	}
	if request.Tool == "read" {
		path, pathErr := durableReadArguments(binding.Proposal.Arguments)
		if pathErr != nil || intent.Path != path || intent.MaxBytes != durableReadMaxBytes || entry.OutputGeneration != request.InputGeneration ||
			entry.OutputTreeDigest != request.InputTreeDigest || entry.Admission != nil || entry.ExitCode != nil {
			return sessionrepo.OperationRequest{}, audit.EffectTrace{}, sessionrepo.OperationEvidence{}, errors.New("read intent or Store evidence differs from the exact bounded read")
		}
	} else {
		if binding.Proposal.Tool == "bash" {
			if intent.Path != "" || intent.MaxBytes != 0 {
				return sessionrepo.OperationRequest{}, audit.EffectTrace{}, sessionrepo.OperationEvidence{}, errors.New("Bash Store intent unexpectedly carries a file path or read limit")
			}
		} else {
			path, pathErr := durableMutationPath(binding.Proposal)
			if pathErr != nil || intent.Path != path || intent.MaxBytes != 0 {
				return sessionrepo.OperationRequest{}, audit.EffectTrace{}, sessionrepo.OperationEvidence{}, errors.New("Store mutation intent path differs from the exact provider proposal")
			}
		}
		if entry.Admission == nil {
			return sessionrepo.OperationRequest{}, audit.EffectTrace{}, sessionrepo.OperationEvidence{}, errors.New("Store mutation has no verified admission reference")
		}
		if deniedAfterEffect(entry) {
			// No transition exists for a refused candidate; VerifyDecision checks its admission record.
			return request, effect, entry, nil
		}
		transition, found := v.transitionForOperation(entry)
		if !found || transition.InputGeneration != request.InputGeneration || transition.InputTreeDigest != request.InputTreeDigest ||
			transition.OutputGeneration != entry.OutputGeneration || transition.OutputTreeDigest != entry.OutputTreeDigest ||
			transition.Operation != request.Operation || transition.Tool != request.Tool || transition.ArgumentDigest != request.ArgumentDigest {
			return sessionrepo.OperationRequest{}, audit.EffectTrace{}, sessionrepo.OperationEvidence{}, errors.New("Store mutation has no unique matching approved transition")
		}
		var admission governedReplayAdmissionRecord
		if err := v.verifyAdmission(*entry.Admission, request, transition, effect, &admission); err != nil {
			return sessionrepo.OperationRequest{}, audit.EffectTrace{}, sessionrepo.OperationEvidence{}, err
		}
	}
	return request, effect, entry, nil
}

func (v *governedStoreReplayVerifier) VerifyResult(binding providerbridge.ProviderResultBinding) error {
	if v == nil {
		return errors.New("Store replay verifier is unavailable")
	}
	decisionKey := governedReplayDecisionKey{binding.Decision.DecisionRecordSequence, binding.Decision.DecisionRecordHash}
	verified, ok := v.decisions[decisionKey]
	if !ok || verified.binding.DecisionRecordHash != binding.Decision.DecisionRecordHash ||
		verified.binding.Proposal.Tool != binding.Decision.Proposal.Tool || verified.resultSeen || verified.denied ||
		binding.GenerationFrom != binding.Decision.InputGeneration || binding.TreeDigestFrom != binding.Decision.InputTreeDigest ||
		!validBareSHA256(binding.ResultDigest) {
		return errors.New("provider result has no unique successful Store-linked decision")
	}
	providerRecord, ok := v.providerRecords[binding.ResultRecordSequence]
	if !ok || providerRecord.Hash != binding.ResultRecordHash || providerRecord.Event.Kind != "provider_tool_result" {
		return errors.New("provider result sequence/hash does not resolve to exactly one protected record")
	}
	var evidence providerResultEvidence
	if decodeCanonicalGoverned(providerRecord.Event.Data, &evidence) != nil || evidence.ResultID != binding.ResultID ||
		evidence.ResultDigest != binding.ResultDigest || evidence.ResultDigest != digestBytes(evidence.Result) ||
		!bytes.Equal(evidence.Result, mustMarshalProtocolResult(binding.Result)) || evidence.WorkflowID != binding.Decision.WorkflowID ||
		evidence.ConversationID != binding.Decision.ConversationID || evidence.ResponseID != binding.Decision.ResponseID ||
		evidence.ResponseIDIssuer != binding.Decision.ResponseIDIssuer || evidence.ToolCallID != binding.Decision.ToolCallID ||
		evidence.ToolCallIDIssuer != binding.Decision.ToolCallIDIssuer || evidence.Tool != binding.Decision.Proposal.Tool ||
		evidence.ArgumentDigest != binding.Decision.GateDecision.CanonicalArgumentsDigest || evidence.Sequence != binding.Decision.ProviderSequence ||
		evidence.GenerationFrom != binding.GenerationFrom || evidence.GenerationTo != binding.GenerationTo ||
		evidence.TreeDigestFrom != binding.TreeDigestFrom || evidence.TreeDigestTo != binding.TreeDigestTo ||
		evidence.TransitionID != binding.TransitionID || evidence.TransitionSeq != binding.TransitionSequence || evidence.EffectID != binding.EffectID {
		return errors.New("provider result journal record differs from exact callback result binding")
	}
	if providerRecord.Event.ID != fmt.Sprintf("%s:%s:%d", binding.Decision.ConversationID, binding.ResultID, binding.ResultRecordSequence) {
		return errors.New("provider result event ID does not match its protected sequence")
	}
	if binding.Result.SchemaVersion != protocol.ResultSchemaVersion || binding.Result.Verdict != string(gate.Allow) ||
		binding.Result.Tool != binding.Decision.Proposal.Tool || binding.Result.ToolCallID != binding.Decision.ToolCallID ||
		binding.Result.Sequence != binding.Decision.ProviderSequence || binding.Result.PolicyDigest != binding.Decision.PolicyDigest ||
		binding.Result.ReasonCode != binding.Decision.GateDecision.ReasonCode || binding.Result.ResponseID == nil ||
		binding.Result.ResponseID.Issuer != governedResponseIssuer || binding.Result.ResponseID.Opaque != binding.Decision.ResponseID ||
		binding.Result.CanonicalArgumentsDigest != binding.Decision.GateDecision.CanonicalArgumentsDigest {
		return errors.New("provider protocol result correlation differs from its gate decision")
	}
	expected, toGeneration, toTree, transitionID, transitionSequence, effectID, err := v.expectedDurableOutput(verified, binding.Decision)
	if err != nil {
		return err
	}
	if !bytes.Equal(binding.Result.Output, expected) || binding.GenerationTo != toGeneration || binding.TreeDigestTo != toTree ||
		binding.TransitionID != transitionID || binding.TransitionSequence != transitionSequence || binding.EffectID != effectID {
		return errors.New("provider-facing DurableExecutor output bytes or Store transition references differ")
	}
	encoded, err := protocol.MarshalResult(binding.Result)
	if err != nil || !bytes.Equal(encoded, evidence.Result) || digestBytes(encoded) != binding.ResultDigest {
		return errors.New("provider result is not the exact canonical protocol result for reconstructed Store bytes")
	}
	verified.output = append([]byte(nil), expected...)
	verified.toGen, verified.toTree, verified.resultSeen = toGeneration, toTree, true
	v.providerTipGeneration, v.providerTipTree = toGeneration, toTree
	return nil
}

// deniedAfterEffect reports whether the Store evidence is that of an operation whose effect completed and
// whose settled change delta admission refused: nothing was promoted, so there is no output generation.
func deniedAfterEffect(operation sessionrepo.OperationEvidence) bool {
	return operation.Outcome == "denied_after_effect" && operation.Admission != nil && operation.Admission.Outcome == "deny" &&
		operation.OutputGeneration == "" && operation.OutputTreeDigest == ""
}

// deniedAdmissionReason verifies the retained evidence of a refused candidate and returns its reason code,
// recomputed from the admission record and the Store's admission scope: the record is a denial of exactly
// this operation, the effect result carries the same record and candidate, and the scope denies it.
func (v *governedStoreReplayVerifier) deniedAdmissionReason(request sessionrepo.OperationRequest, effect audit.EffectTrace,
	operation sessionrepo.OperationEvidence) (string, error) {
	var admission governedReplayAdmissionRecord
	if err := v.decodeAdmission(*operation.Admission, request, effect, &admission); err != nil {
		return "", err
	}
	candidate := admission.Candidate
	if operation.Admission.Outcome != "deny" || admission.Outcome != "deny" || candidate.Tool != request.Tool ||
		candidate.Operation != request.Operation || candidate.Decision != request.Decision ||
		candidate.ArgumentDigest != request.ArgumentDigest || candidate.InputGeneration != request.InputGeneration ||
		candidate.InputTreeDigest != request.InputTreeDigest || candidate.ViewID != request.ViewID ||
		candidate.ExecutionContextDigest != request.ExecutionContextDigest {
		return "", errors.New("denied admission record does not bind the exact refused operation")
	}
	outcome, reason := v.bundle.AdmissionScope.Decide(request.OutputScopePaths, candidate.Changes)
	if outcome != "deny" || !protocol.ValidAdmissionReasonCode(reason) {
		return "", errors.New("the Store admission scope does not deny the retained candidate")
	}
	if request.Tool == "bash" {
		var result governedReplayCommandEffect
		if decodeCanonicalGoverned(effect.Result, &result) != nil || !sameDurableTransition(result.Transition, candidate) ||
			result.Admission != *operation.Admission || result.RunnerProfile != operation.CommandRunnerProfile ||
			operation.ExitCode == nil || *operation.ExitCode != result.Command.ExitCode || !result.Command.ExitObserved ||
			result.Settlement.ExitCode != result.Command.ExitCode || !result.Settlement.ExitObserved ||
			result.Settlement.LeaseID != request.Operation.LeaseID || result.Settlement.ViewID != request.ViewID ||
			!result.Settlement.ProcessScopeEmpty || !result.Settlement.WritersStopped || !result.Settlement.MountDetached {
			return "", errors.New("denied Bash effect result differs from its retained candidate and settlement")
		}
		return reason, nil
	}
	var result governedReplayMutationEffect
	if decodeCanonicalGoverned(effect.Result, &result) != nil || !sameDurableTransition(result.Transition, candidate) ||
		result.Admission != *operation.Admission {
		return "", errors.New("denied mutation effect result differs from its retained candidate")
	}
	return reason, nil
}

// VerifyAdmissionDenial is the released-result check of a refused ALLOW: the provider_admission_denied
// record is exactly the DENY Result of the reason the Store evidence yields, for a decision whose Store
// operation was denied after its effect, and the provider generation stays where it was.
func (v *governedStoreReplayVerifier) VerifyAdmissionDenial(binding providerbridge.ProviderAdmissionDenialBinding) error {
	if v == nil {
		return errors.New("Store replay verifier is unavailable")
	}
	decisionKey := governedReplayDecisionKey{binding.Decision.DecisionRecordSequence, binding.Decision.DecisionRecordHash}
	verified, ok := v.decisions[decisionKey]
	if !ok || !verified.denied || verified.resultSeen || verified.binding.DecisionRecordHash != binding.Decision.DecisionRecordHash ||
		verified.binding.Proposal.Tool != binding.Decision.Proposal.Tool || !validBareSHA256(binding.ResultDigest) ||
		binding.EffectID != verified.request.EffectID || binding.ReasonCode != verified.denyReason {
		return errors.New("admission denial has no unique Store-denied decision with this effect and reason")
	}
	providerRecord, ok := v.providerRecords[binding.ResultRecordSequence]
	if !ok || providerRecord.Hash != binding.ResultRecordHash || providerRecord.Event.Kind != "provider_admission_denied" ||
		providerRecord.Event.ID != fmt.Sprintf("%s:%s:%d", binding.Decision.ConversationID, binding.ResultID, binding.ResultRecordSequence) {
		return errors.New("admission denial sequence/hash does not resolve to exactly one protected record")
	}
	var denial providerAdmissionDeniedEvidence
	if decodeCanonicalGoverned(providerRecord.Event.Data, &denial) != nil || denial.ResultID != binding.ResultID ||
		denial.ResultDigest != binding.ResultDigest || denial.ResultDigest != digestBytes(denial.Result) ||
		!bytes.Equal(denial.Result, mustMarshalProtocolResult(binding.Result)) || denial.ReasonCode != binding.ReasonCode ||
		denial.EffectID != binding.EffectID || denial.WorkflowID != binding.Decision.WorkflowID ||
		denial.ConversationID != binding.Decision.ConversationID || denial.ResponseID != binding.Decision.ResponseID ||
		denial.ResponseIDIssuer != binding.Decision.ResponseIDIssuer || denial.ToolCallID != binding.Decision.ToolCallID ||
		denial.ToolCallIDIssuer != binding.Decision.ToolCallIDIssuer || denial.Tool != binding.Decision.Proposal.Tool ||
		denial.GenerationFrom != binding.Decision.InputGeneration || denial.TreeDigestFrom != binding.Decision.InputTreeDigest {
		return errors.New("admission denial journal record differs from the exact callback binding")
	}
	if _, err := checkAdmissionDenialResult(denial, binding.Decision.GateDecision); err != nil {
		return err
	}
	verified.resultSeen = true
	return nil
}

func (v *governedStoreReplayVerifier) expectedDurableOutput(verified *governedReplayDecisionEvidence,
	decisionBinding providerbridge.ProviderDecisionBinding) ([]byte, string, string, string, uint64, string, error) {
	request, effect, operation := verified.request, verified.effect, verified.entry
	if request.EffectID == "" || effect.ID != request.EffectID {
		return nil, "", "", "", 0, "", errors.New("Store result mapping is incomplete")
	}
	if request.Tool == "read" {
		path, err := durableReadArguments(decisionBinding.Proposal.Arguments)
		if err != nil {
			return nil, "", "", "", 0, "", err
		}
		payload := durableReadSummaryPayload{Tool: "read", Path: path,
			Generation: durableGenerationSummary{ID: request.InputGeneration, TreeDigest: request.InputTreeDigest},
			Size:       len(effect.Result), ContentDigest: durableContentDigest(effect.Result), Content: string(effect.Result), Outcome: "success"}
		encoded, err := json.Marshal(payload)
		if err == nil && len(encoded) > protocol.MaxFrameBytes {
			err = errors.New("reconstructed read summary exceeds the DurableExecutor frame bound")
		}
		return encoded, request.InputGeneration, request.InputTreeDigest, "", 0, "", err
	}
	transition, found := v.transitionForOperation(operation)
	if !found || transition.InputGeneration != request.InputGeneration || transition.InputTreeDigest != request.InputTreeDigest ||
		transition.Operation != request.Operation || transition.Tool != request.Tool || transition.ArgumentDigest != request.ArgumentDigest ||
		transition.OutputGeneration != operation.OutputGeneration || transition.OutputTreeDigest != operation.OutputTreeDigest {
		return nil, "", "", "", 0, "", errors.New("Store ledger transition does not match OperationEvidence")
	}
	var admissionRecord governedReplayAdmissionRecord
	if operation.Admission == nil || v.verifyAdmission(*operation.Admission, request, transition, effect, &admissionRecord) != nil {
		return nil, "", "", "", 0, "", errors.New("Store mutation admission record does not match its effect and transition")
	}
	var summary durableMutationSummaryPayload
	summary.Tool = request.Tool
	summary.Generation = durableGenerationSummary{ID: operation.OutputGeneration, TreeDigest: operation.OutputTreeDigest}
	summary.Transition = durableTransitionSummary{ID: transition.ID, Sequence: transition.Sequence}
	summary.EffectID, summary.Outcome = effect.ID, "success"
	if request.Tool == "write" || request.Tool == "edit" {
		var result governedReplayMutationEffect
		if decodeCanonicalGoverned(effect.Result, &result) != nil || !sameDurableTransition(result.Transition, transition) || result.Admission != *operation.Admission {
			return nil, "", "", "", 0, "", errors.New("Store mutation effect result differs from ledger/admission evidence")
		}
		path, pathErr := durableMutationPath(decisionBinding.Proposal)
		if pathErr != nil || len(request.OutputScopePaths) != 1 || request.OutputScopePaths[0] != path {
			return nil, "", "", "", 0, "", errors.New("Store mutation output scope differs from the provider proposal")
		}
		if request.Tool == "write" {
			_, _, err := durableWriteArguments(decisionBinding.Proposal.Arguments)
			if err != nil {
				return nil, "", "", "", 0, "", err
			}
		} else if _, _, err := durableEditArguments(decisionBinding.Proposal.Arguments); err != nil {
			return nil, "", "", "", 0, "", err
		}
	} else if request.Tool == "bash" {
		var result governedReplayCommandEffect
		if decodeCanonicalGoverned(effect.Result, &result) != nil || !sameDurableTransition(result.Transition, transition) ||
			result.Admission != *operation.Admission || result.RunnerProfile != admissionRecordRunnerProfile(verified) ||
			!result.Command.ExitObserved || int64(len(result.Command.Stdout)) > protocol.MaxFrameBytes ||
			int64(len(result.Command.Stderr)) > protocol.MaxFrameBytes || result.Settlement.ExitCode != result.Command.ExitCode ||
			!result.Settlement.ExitObserved || result.Settlement.LeaseID != request.Operation.LeaseID ||
			result.Settlement.ViewID != request.ViewID || !result.Settlement.ProcessScopeEmpty ||
			!result.Settlement.WritersStopped || !result.Settlement.MountDetached || strings.TrimSpace(result.Settlement.EvidenceClass) == "" {
			return nil, "", "", "", 0, "", errors.New("Bash effect output or settlement fields differ from Store evidence")
		}
		result.Spec.DeclaredOutputPaths = cloneDurableOutputScopePaths(request.OutputScopePaths)
		command, err := durableBashArguments(decisionBinding.Proposal.Arguments)
		wantSpec := sessionrepo.CommandSpec{Executable: "/bin/bash", Args: []string{"-lc", command},
			DeclaredOutputPaths: cloneDurableOutputScopePaths(v.pins.BashOutputPaths)}
		if err != nil || !sameReplayCommandSpec(result.Spec, wantSpec) || result.RunnerProfile != operation.CommandRunnerProfile ||
			operation.ExitCode == nil || *operation.ExitCode != result.Command.ExitCode || operation.CommandContainmentStatus != "not-established" {
			return nil, "", "", "", 0, "", errors.New("Bash operation evidence differs from exact command specification or observed exit")
		}
		summary.Command = &durableCommandSummary{ExitCode: result.Command.ExitCode, ExitObserved: true,
			CommandContainmentStatus: "not-established", CommandRunnerProfile: result.RunnerProfile}
	} else {
		return nil, "", "", "", 0, "", errors.New("unsupported Store tool in governed provider result")
	}
	encoded, err := json.Marshal(summary)
	if err == nil && len(encoded) > protocol.MaxFrameBytes {
		err = errors.New("reconstructed mutation summary exceeds the DurableExecutor frame bound")
	}
	return encoded, operation.OutputGeneration, operation.OutputTreeDigest, transition.ID, transition.Sequence, effect.ID, err
}

func (v *governedStoreReplayVerifier) transitionForOperation(operation sessionrepo.OperationEvidence) (delta.Transition, bool) {
	var found delta.Transition
	count := 0
	for _, transition := range v.bundle.ApprovedDeltaLedger {
		if transition.Operation == operation.Operation && transition.Tool == operation.Tool && transition.InputGeneration == operation.InputGeneration &&
			transition.OutputGeneration == operation.OutputGeneration {
			found, count = transition, count+1
		}
	}
	return found, count == 1
}

func (v *governedStoreReplayVerifier) verifyAdmission(reference sessionrepo.AdmissionReference, request sessionrepo.OperationRequest,
	transition delta.Transition, effect audit.EffectTrace, decoded *governedReplayAdmissionRecord) error {
	if err := v.decodeAdmission(reference, request, effect, decoded); err != nil {
		return err
	}
	if decoded.Candidate.ID != transition.ID || !sameDurableTransition(decoded.Candidate, transition) || reference.Outcome != "allow" {
		return errors.New("admission record does not bind exact effect intent, Store policy, and transition")
	}
	return nil
}

// decodeAdmission resolves a Store admission reference to its protected record and checks that it binds
// the operation request, the effect intent and the pinned Store policies, whatever its outcome.
func (v *governedStoreReplayVerifier) decodeAdmission(reference sessionrepo.AdmissionReference, request sessionrepo.OperationRequest,
	effect audit.EffectTrace, decoded *governedReplayAdmissionRecord) error {
	var match *audit.Record
	for index := range v.admissionTrace.Records {
		record := &v.admissionTrace.Records[index]
		if record.Sequence != reference.Sequence {
			continue
		}
		if match != nil {
			return errors.New("duplicate admission sequence")
		}
		match = record
	}
	if match == nil || match.Hash != reference.RecordHash || match.Event.Kind != "sessionrepo_admission" {
		return errors.New("admission reference does not resolve to one protected record")
	}
	if decodeCanonicalGoverned(match.Event.Data, decoded) != nil || decoded.SchemaVersion != sessionrepo.AdmissionSchemaVersion ||
		!reflect.DeepEqual(decoded.Request, request) || decoded.IntentDigest != digestBytes(effect.Intent) || decoded.PolicyDigest != v.pins.StorePolicyDigest ||
		decoded.MetadataPolicyDigest != v.pins.MetadataPolicyDigest || decoded.Outcome != reference.Outcome ||
		reference.IntentDigest != decoded.IntentDigest {
		return errors.New("admission record does not bind exact effect intent and Store policy")
	}
	return nil
}

func admissionRecordRunnerProfile(verified *governedReplayDecisionEvidence) string {
	var intent governedReplayOperationIntent
	if decodeCanonicalGoverned(verified.effect.Intent, &intent) != nil {
		return ""
	}
	return intent.RunnerProfile
}

func durableMutationPath(proposal protocol.Proposal) (string, error) {
	switch proposal.Tool {
	case "write":
		path, _, err := durableWriteArguments(proposal.Arguments)
		return path, err
	case "edit":
		path, _, err := durableEditArguments(proposal.Arguments)
		return path, err
	default:
		return "", errors.New("proposal is not a file mutation")
	}
}

func sameReplayCommandSpec(left, right sessionrepo.CommandSpec) bool {
	return left.Executable == right.Executable && left.WorkingDirectory == right.WorkingDirectory &&
		reflect.DeepEqual(left.Args, right.Args) && sameDurableOutputScopePaths(left.DeclaredOutputPaths, right.DeclaredOutputPaths)
}

func decodeCanonicalGoverned(raw []byte, target any) error {
	if err := unmarshalGovernedJSON(raw, target); err != nil {
		return err
	}
	canonical, err := json.Marshal(target)
	if err != nil || !bytes.Equal(canonical, raw) {
		return errors.New("evidence is not canonical JSON")
	}
	return nil
}

func aliasDecision(binding providerbridge.ProviderDecisionBinding) ProviderDecisionBindingAlias {
	return ProviderDecisionBindingAlias{
		WorkflowID: binding.WorkflowID, ConversationID: binding.ConversationID, ProfileID: binding.ProfileID, Model: binding.Model,
		PolicyDigest: binding.PolicyDigest, SystemPromptDigest: binding.SystemPromptDigest, DeveloperPromptDigest: binding.DeveloperPromptDigest,
		ProviderSequence: binding.ProviderSequence, ResponseIDIssuer: binding.ResponseIDIssuer, ResponseID: binding.ResponseID,
		ToolCallIDIssuer: binding.ToolCallIDIssuer, ToolCallID: binding.ToolCallID,
		Proposal: protocol.Proposal{SchemaVersion: binding.Proposal.SchemaVersion, ToolCallID: binding.Proposal.ToolCallID,
			Tool: binding.Proposal.Tool, Arguments: append(json.RawMessage(nil), binding.Proposal.Arguments...)},
		GateDecision: binding.GateDecision, InputGeneration: binding.InputGeneration, InputTreeDigest: binding.InputTreeDigest,
		ExchangeRecordSequence: binding.ExchangeRecordSequence, ExchangeRecordHash: binding.ExchangeRecordHash,
		DecisionRecordSequence: binding.DecisionRecordSequence, DecisionRecordHash: binding.DecisionRecordHash,
	}
}

func mustMarshalProtocolResult(result protocol.Result) []byte {
	encoded, _ := protocol.MarshalResult(result)
	return encoded
}

func (v *governedStoreReplayVerifier) finish(projection providerbridge.TranscriptProjection, requireStoreTip bool) error {
	if v == nil || !providerbridge.VerifyTranscriptProjectionCommitment(projection) {
		return errors.New("provider projection commitment is invalid")
	}
	conversationID := v.pins.Provider.Config.ConversationID
	scopedAuthorizationCount, scopedOperationCount := 0, 0
	for _, record := range v.authByEffect {
		var authorization governedOperationAdmissionRecord
		if decodeCanonicalGoverned(record.Event.Data, &authorization) != nil {
			return errors.New("Store authorization cannot be decoded during scoped history verification")
		}
		if authorization.ConversationID == conversationID {
			scopedAuthorizationCount++
		}
	}
	for effectID := range v.authByEffect {
		if entry, ok := v.operationByEffect[effectID]; ok {
			if authRecord, exists := v.authByEffect[effectID]; exists {
				var authorization governedOperationAdmissionRecord
				if decodeCanonicalGoverned(authRecord.Event.Data, &authorization) != nil {
					return errors.New("Store operation authorization cannot be decoded")
				}
				if authorization.ConversationID == conversationID && entry.EffectID == effectID {
					scopedOperationCount++
				}
			}
		}
	}
	if !v.called {
		if projection.VerificationScope != providerbridge.ProjectionScopeProviderOnly || projection.DecisionCount != 0 ||
			scopedAuthorizationCount != 0 || scopedOperationCount != 0 {
			return errors.New("callback-free transcript contains Store authorization or operation evidence")
		}
		if _, ok := findGovernedGeneration(v.bundle, projection.ProviderGenerationID, projection.ProviderTreeDigest); !ok {
			return errors.New("callback-free provider tip is outside the verified Store generation history")
		}
		return nil
	}
	if projection.VerificationScope != providerbridge.ProjectionScopeStoreCrosslink {
		return errors.New("Store callback ran but provider projection does not claim Store cross-link verification")
	}
	usedScopedAuth := 0
	for key := range v.usedAuth {
		if record, ok := v.authByDecision[key]; ok {
			var authorization governedOperationAdmissionRecord
			if decodeCanonicalGoverned(record.Event.Data, &authorization) != nil {
				return errors.New("Store authorization cannot be decoded")
			}
			if authorization.ConversationID == conversationID {
				usedScopedAuth++
			}
		}
	}
	if usedScopedAuth != scopedAuthorizationCount {
		return errors.New("Store authorization for this conversation has no unique provider ALLOW decision")
	}
	if scopedOperationCount != scopedAuthorizationCount {
		return errors.New("Store operation evidence is not one-to-one with this conversation's provider ALLOW authorization")
	}
	for _, decision := range v.decisions {
		if decision.binding.GateDecision.Verdict == gate.Allow && decision.output == nil && decision.resultSeen && !decision.denied {
			return errors.New("released provider result has an empty Store output reconstruction")
		}
	}
	storeHead := v.disposition.Sealed
	storeTree := v.bundle.Generations[len(v.bundle.Generations)-1].TreeDigest
	if projection.Disposition == providerbridge.DispositionPendingProviderResult {
		var lastDecision *governedReplayDecisionEvidence
		for _, decision := range v.decisions {
			if lastDecision == nil || decision.binding.DecisionRecordSequence > lastDecision.binding.DecisionRecordSequence {
				lastDecision = decision
			}
		}
		if lastDecision == nil || lastDecision.resultSeen || lastDecision.binding.InputGeneration != projection.ProviderGenerationID ||
			lastDecision.binding.InputTreeDigest != projection.ProviderTreeDigest {
			return errors.New("pending provider result does not preserve the last released provider generation")
		}
		if lastDecision.binding.GateDecision.Verdict == gate.Allow {
			if lastDecision.toGen == "" || lastDecision.toTree == "" ||
				(requireStoreTip && (storeHead != lastDecision.toGen || storeTree != lastDecision.toTree)) {
				return errors.New("pending ALLOW has no matching completed Store head; provider result remains withheld")
			}
		} else if requireStoreTip && (storeHead != projection.ProviderGenerationID || storeTree != projection.ProviderTreeDigest) {
			return errors.New("pending DENY unexpectedly has Store authorization or an advanced Store head")
		}
	} else {
		if _, ok := findGovernedGeneration(v.bundle, projection.ProviderGenerationID, projection.ProviderTreeDigest); !ok {
			return errors.New("released provider tip is outside the verified Store generation history")
		}
		if requireStoreTip && (storeHead != projection.ProviderGenerationID || storeTree != projection.ProviderTreeDigest) {
			return errors.New("released provider tip differs from the independently reconstructed Store head")
		}
	}
	// Every provider ALLOW must have one callback and every Store operation must
	// be accounted for. DENY callbacks intentionally have no Store authorization.
	for key, record := range v.authByDecision {
		var authorization governedOperationAdmissionRecord
		if decodeCanonicalGoverned(record.Event.Data, &authorization) != nil || authorization.ConversationID != conversationID {
			continue
		}
		decision := v.decisions[key]
		if decision == nil || decision.binding.GateDecision.Verdict != gate.Allow || record.Sequence == 0 || !v.usedAuth[key] {
			return errors.New("Store authorization is not joined to one provider ALLOW decision")
		}
	}
	return nil
}

func (v *governedStoreReplayVerifier) scopedStoreCounts() (uint64, uint64) {
	if v == nil {
		return 0, 0
	}
	conversationID := v.pins.Provider.Config.ConversationID
	var operations, effects uint64
	for effectID, record := range v.authByEffect {
		var authorization governedOperationAdmissionRecord
		if decodeCanonicalGoverned(record.Event.Data, &authorization) != nil || authorization.ConversationID != conversationID {
			continue
		}
		if _, ok := v.operationByEffect[effectID]; ok {
			operations++
		}
		if effect, ok := v.effectByID[effectID]; ok && !effect.Unresolved && effect.Outcome == "success" {
			effects++
		}
	}
	return operations, effects
}
