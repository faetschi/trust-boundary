//go:build linux

package sessionrepo

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
	"tbound/supervisor/internal/workspace"
)

const (
	AdmissionSchemaVersion = "tbound-sessionrepo-admission/v1"
	admissionEventKind     = "sessionrepo_admission"
	admissionJournalName   = "delta-admission.jsonl"
)

// Create the fixed journal entry relative to the already-validated private
// root. Open uses requireAdmissionJournal first so audit.Open cannot create it.
func createAdmissionJournalAt(root *os.File) error {
	fd, err := syscall.Openat(int(root.Fd()), admissionJournalName,
		syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("create admission journal entry: %w", err)
	}
	if err := os.NewFile(uintptr(fd), admissionJournalName).Close(); err != nil {
		return fmt.Errorf("close new admission journal entry: %w", err)
	}
	return syncDirectory(root)
}

func requireAdmissionJournal(root *os.File) error {
	fd, err := syscall.Openat(int(root.Fd()), admissionJournalName,
		syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open existing admission journal entry: %w", err)
	}
	file := os.NewFile(uintptr(fd), admissionJournalName)
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil {
		return errors.Join(statErr, closeErr)
	}
	if !info.Mode().IsRegular() {
		return errors.Join(errors.New("admission journal entry is not a regular file"), closeErr)
	}
	return closeErr
}

var ErrInvalidAdmission = errors.New("invalid session repository admission")

// ErrAdmissionRecordLimit refuses a mutation before its effect: even its smallest admission record
// would not fit one audit record. It is a workspace limit, so errors.Is(err, workspace.ErrLimit) holds.
var ErrAdmissionRecordLimit = fmt.Errorf("%w: workspace manifest is too large for one durable admission record", workspace.ErrLimit)

// admissionRecordOverheadBytes bounds everything in an admission record and its audit frame except
// the two manifests: request, digests, candidate fields, the frame header and hashes.
const admissionRecordOverheadBytes = 64 << 10

// checkAdmissionRecordFits refuses a mutation of input whose smallest admission record (output equal
// to input, no changes) cannot be appended. Appending fails only after the effect, which leaves the
// effect unresolved and quarantines the journal, so the check must run before any effect.
func checkAdmissionRecordFits(input delta.TreeManifest) error {
	manifest, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("%w: encode input manifest: %v", ErrInvalidAdmission, err)
	}
	// The audit frame carries the record as base64 (audit.Event.Data is []byte).
	if base64.StdEncoding.EncodedLen(2*len(manifest))+admissionRecordOverheadBytes > audit.MaxRecordBytes {
		return ErrAdmissionRecordLimit
	}
	return nil
}

type admissionRecord struct {
	SchemaVersion         string             `json:"schema_version"`
	Request               OperationRequest   `json:"request"`
	IntentDigest          string             `json:"intent_digest"`
	PolicyDigest          string             `json:"policy_digest"`
	MetadataPolicyDigest  string             `json:"metadata_policy_digest"`
	AdmissionPolicyDigest string             `json:"admission_policy_digest"`
	Input                 delta.TreeManifest `json:"input"`
	Output                delta.TreeManifest `json:"output"`
	Candidate             delta.Transition   `json:"candidate"`
	Outcome               string             `json:"outcome"`
}

// AdmissionReference identifies the durable admission record and binds the
// reference to the exact primary intent and scope-policy version.
type AdmissionReference struct {
	Sequence              uint64 `json:"sequence"`
	RecordHash            string `json:"record_hash"`
	Outcome               string `json:"outcome"`
	IntentDigest          string `json:"intent_digest"`
	AdmissionPolicyDigest string `json:"admission_policy_digest"`
}

// AppendAdmissionScoped is AppendAdmission under an admission scope. The scope decides the outcome
// and is part of the admission policy digest of the record.
func AppendAdmissionScoped(
	journal *audit.Journal,
	request OperationRequest,
	primaryIntent []byte,
	policyDigest, metadataPolicyDigest string,
	input, output delta.TreeManifest,
	candidate delta.Transition,
	scope AdmissionScope,
) (AdmissionReference, error) {
	if journal == nil || len(primaryIntent) == 0 || len(primaryIntent) > audit.MaxRecordBytes {
		return AdmissionReference{}, fmt.Errorf("%w: journal and bounded primary intent are required", ErrInvalidAdmission)
	}

	var err error
	record := admissionRecord{
		SchemaVersion:        AdmissionSchemaVersion,
		Request:              cloneOperationRequest(request),
		IntentDigest:         digestBytes(primaryIntent),
		PolicyDigest:         policyDigest,
		MetadataPolicyDigest: metadataPolicyDigest,
		Input:                cloneManifest(input),
		Output:               cloneManifest(output),
		Candidate:            candidate,
	}
	record.Candidate.Changes = append([]delta.ObjectChange(nil), candidate.Changes...)

	record.AdmissionPolicyDigest, err = computeAdmissionPolicyDigest(policyDigest, metadataPolicyDigest, scope)
	if err != nil {
		return AdmissionReference{}, err
	}
	if err := verifyAdmissionIntent(record, primaryIntent); err != nil {
		return AdmissionReference{}, err
	}
	outcome, err := validateAdmissionRecord(record, policyDigest, metadataPolicyDigest, scope)
	if err != nil {
		return AdmissionReference{}, err
	}
	record.Outcome = outcome

	data, err := json.Marshal(record)
	if err != nil {
		return AdmissionReference{}, fmt.Errorf("%w: encode record: %v", ErrInvalidAdmission, err)
	}
	appended, err := journal.Append(audit.Event{
		Kind: admissionEventKind,
		ID:   "admission-" + record.IntentDigest,
		Data: data,
	})
	if err != nil {
		return AdmissionReference{}, fmt.Errorf("append durable admission: %w", err)
	}
	return referenceFor(appended, record), nil
}

// VerifyAdmissionScoped is VerifyAdmission under the admission scope the record was written under.
func VerifyAdmissionScoped(
	journal *audit.Journal,
	reference AdmissionReference,
	primaryIntent []byte,
	policyDigest, metadataPolicyDigest string,
	scope AdmissionScope,
) error {
	if journal == nil || len(primaryIntent) == 0 || len(primaryIntent) > audit.MaxRecordBytes {
		return fmt.Errorf("%w: journal and bounded primary intent are required", ErrInvalidAdmission)
	}
	trace, err := journal.Trace()
	if err != nil {
		return fmt.Errorf("verify admission journal: %w", err)
	}
	if reference.Sequence == 0 || reference.Sequence > uint64(len(trace.Records)) {
		return fmt.Errorf("%w: referenced sequence is absent", ErrInvalidAdmission)
	}

	appended := trace.Records[reference.Sequence-1]
	if appended.Sequence != reference.Sequence || appended.Event.Kind != admissionEventKind || appended.Event.Outcome != "" {
		return fmt.Errorf("%w: referenced record has invalid admission framing", ErrInvalidAdmission)
	}
	record, err := decodeAdmissionRecord(appended.Event.Data)
	if err != nil {
		return err
	}
	if err := verifyAdmissionIntent(record, primaryIntent); err != nil {
		return err
	}
	if record.IntentDigest != digestBytes(primaryIntent) {
		return fmt.Errorf("%w: primary intent digest mismatch", ErrInvalidAdmission)
	}
	outcome, err := validateAdmissionRecord(record, policyDigest, metadataPolicyDigest, scope)
	if err != nil {
		return err
	}
	if record.Outcome != outcome {
		return fmt.Errorf("%w: persisted outcome does not match recomputed scope decision", ErrInvalidAdmission)
	}
	if reference != referenceFor(appended, record) {
		return fmt.Errorf("%w: reference does not match persisted record", ErrInvalidAdmission)
	}
	if appended.Event.ID != "admission-"+record.IntentDigest {
		return fmt.Errorf("%w: event ID does not match primary intent", ErrInvalidAdmission)
	}
	return nil
}

func validateAdmissionRecord(
	record admissionRecord,
	policyDigest, metadataPolicyDigest string,
	scope AdmissionScope,
) (string, error) {
	fail := func(reason string) (string, error) {
		return "", fmt.Errorf("%w: %s", ErrInvalidAdmission, reason)
	}
	if record.SchemaVersion != AdmissionSchemaVersion ||
		!validSHA256(record.IntentDigest) ||
		!validSHA256(policyDigest) ||
		!validSHA256(metadataPolicyDigest) ||
		record.PolicyDigest != policyDigest ||
		record.MetadataPolicyDigest != metadataPolicyDigest {
		return fail("schema or pinned policy identity mismatch")
	}

	admissionPolicyDigest, err := computeAdmissionPolicyDigest(policyDigest, metadataPolicyDigest, scope)
	if err != nil {
		return fail("cannot compute admission policy digest")
	}
	if record.AdmissionPolicyDigest != admissionPolicyDigest {
		return fail("admission policy digest mismatch")
	}

	request := record.Request
	if validateRequestOutputScope(request) != nil {
		return fail("request scope is invalid")
	}
	if request.Tool == "bash" {
		if request.Operation.Kind != delta.OperationLease || !validViewID(request.ViewID) || !validSHA256(request.ExecutionContextDigest) {
			return fail("invalid command context")
		}
		contextDigest, err := commandExecutionContextDigest(request.ViewID, request.InputTreeDigest, request.Operation.LeaseID)
		if err != nil || contextDigest != request.ExecutionContextDigest {
			return fail("command context digest mismatch")
		}
	} else if request.Operation.Kind != delta.OperationProposalCall || request.ViewID != "" || request.ExecutionContextDigest != "" {
		return fail("invalid mutation context")
	}
	if !validSupervisorEffectID(request.EffectID) ||
		!validSHA256(request.ArgumentDigest) ||
		validateOperation(request.Operation) != nil ||
		!validIdentity(request.Decision.ID) ||
		(request.Tool != "write" && request.Tool != "edit" && request.Tool != "delete" && request.Tool != "bash") ||
		request.Decision.Outcome != delta.PolicyAllow ||
		request.Decision.PolicyDigest != policyDigest ||
		request.Decision.MetadataPolicyDigest != metadataPolicyDigest {
		return fail("request identity or pinned decision fields are invalid")
	}

	inputDigest, err := delta.ComputeTreeDigest(record.Input)
	if err != nil {
		return fail("input manifest is invalid")
	}
	outputDigest, err := delta.ComputeTreeDigest(record.Output)
	if err != nil {
		return fail("output manifest is invalid")
	}
	if record.Input.MetadataPolicyDigest != metadataPolicyDigest ||
		record.Output.MetadataPolicyDigest != metadataPolicyDigest ||
		record.Input.Generation != request.InputGeneration ||
		inputDigest != request.InputTreeDigest {
		return fail("input manifest does not match the request")
	}

	candidate := record.Candidate
	if !validIdentity(candidate.ID) || candidate.Sequence == 0 ||
		candidate.InputGeneration == candidate.OutputGeneration ||
		candidate.InputGeneration != record.Input.Generation ||
		candidate.OutputGeneration != record.Output.Generation ||
		candidate.InputTreeDigest != inputDigest ||
		candidate.OutputTreeDigest != outputDigest ||
		candidate.Tool != request.Tool ||
		candidate.ArgumentDigest != request.ArgumentDigest ||
		candidate.ViewID != request.ViewID ||
		candidate.ExecutionContextDigest != request.ExecutionContextDigest ||
		candidate.Operation != request.Operation ||
		candidate.Decision != request.Decision ||
		len(candidate.Changes) > delta.MaxChangesPerTransition ||
		!sameChanges(candidate.Changes, diffManifests(record.Input, record.Output)) {
		return fail("candidate transition does not match the manifests and request")
	}

	// Scope validity and coverage are intentionally evaluated here, after all
	// structural checks. Invalid or uncovered scopes produce DENY and do not
	// discard the structurally valid candidate evidence.
	outcome, _ := scope.Decide(request.OutputScopePaths, candidate.Changes)
	return outcome, nil
}

func decodeAdmissionRecord(data []byte) (admissionRecord, error) {
	var record admissionRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return admissionRecord{}, fmt.Errorf("%w: decode persisted record: %v", ErrInvalidAdmission, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return admissionRecord{}, fmt.Errorf("%w: trailing persisted JSON", ErrInvalidAdmission)
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(canonical, data) {
		return admissionRecord{}, fmt.Errorf("%w: persisted record is not canonical", ErrInvalidAdmission)
	}
	return record, nil
}

func referenceFor(appended audit.Record, record admissionRecord) AdmissionReference {
	return AdmissionReference{
		Sequence:              appended.Sequence,
		RecordHash:            appended.Hash,
		Outcome:               record.Outcome,
		IntentDigest:          record.IntentDigest,
		AdmissionPolicyDigest: record.AdmissionPolicyDigest,
	}
}

func verifyAdmissionIntent(record admissionRecord, intent []byte) error {
	request, decoded, err := decodeOperationIntent(intent)
	if err != nil {
		return fmt.Errorf("%w: primary intent: %v", ErrInvalidAdmission, err)
	}
	if request.Tool == "bash" {
		argumentDigest, err := CommandArgumentsDigest(decoded.Command)
		if err != nil || argumentDigest != request.ArgumentDigest {
			return fmt.Errorf("%w: command argument digest mismatch", ErrInvalidAdmission)
		}
	}
	expected, err := json.Marshal(request)
	if err != nil {
		return err
	}
	actual, err := json.Marshal(record.Request)
	if err != nil || !bytes.Equal(expected, actual) || digestBytes(intent) != record.IntentDigest {
		return fmt.Errorf("%w: primary request mismatch", ErrInvalidAdmission)
	}
	return nil
}
