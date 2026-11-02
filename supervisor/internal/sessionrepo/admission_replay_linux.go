//go:build linux

package sessionrepo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/delta"
)

type retainedAdmission struct {
	reference AdmissionReference
	record    admissionRecord
}

// validateAdmissionEvidence cross-checks both complete audit chains. Every
// admission record must bind to exactly one primary mutation intent. A
// completed mutation must carry the exact referenced record in its effect
// result; an admission for an unresolved effect is retained as quarantine
// evidence and is never treated as a committed transition.
func validateAdmissionEvidence(primary, admission audit.Trace, policyDigest, metadataPolicyDigest string, scope AdmissionScope, generations []GenerationEvidence) (map[string]retainedAdmission, error) {
	effectsByIntent := make(map[string]audit.EffectTrace)
	for _, effect := range primary.Effects {
		if !strings.HasPrefix(effect.ID, "sessionrepo-effect-") {
			continue
		}
		if !validSupervisorEffectID(effect.ID) || len(effect.Intent) == 0 || len(effect.Intent) > audit.MaxRecordBytes {
			return nil, invalidBundle("primary effect %q has an invalid bounded intent", effect.ID)
		}
		digest := digestBytes(effect.Intent)
		if _, exists := effectsByIntent[digest]; exists {
			return nil, invalidBundle("duplicate primary effect intent digest")
		}
		effectsByIntent[digest] = effect
	}

	byEffect := make(map[string]retainedAdmission)
	for index, appended := range admission.Records {
		if appended.Sequence != uint64(index+1) || appended.Event.Kind != admissionEventKind || appended.Event.Outcome != "" {
			return nil, invalidBundle("admission record %d has an unexpected sequence, kind, or outcome", appended.Sequence)
		}
		record, err := decodeAdmissionRecord(appended.Event.Data)
		if err != nil {
			return nil, invalidBundle("decode admission record %d: %v", appended.Sequence, err)
		}
		if appended.Event.ID != "admission-"+record.IntentDigest {
			return nil, invalidBundle("admission record %d has an invalid event ID", appended.Sequence)
		}
		effect, exists := effectsByIntent[record.IntentDigest]
		if !exists {
			return nil, invalidBundle("admission record %d has no matching primary effect intent", appended.Sequence)
		}
		request, _, err := decodeOperationIntent(effect.Intent)
		if err != nil || !isAdmissionMutationTool(request.Tool) || request.EffectID != effect.ID {
			return nil, invalidBundle("admission record %d does not match a mutation effect", appended.Sequence)
		}
		if err := verifyAdmissionIntent(record, effect.Intent); err != nil {
			return nil, invalidBundle("admission record %d primary intent: %v", appended.Sequence, err)
		}
		outcome, err := validateAdmissionRecord(record, policyDigest, metadataPolicyDigest, scope)
		if err != nil || outcome != record.Outcome {
			return nil, invalidBundle("admission record %d fails its scope predicate: %v", appended.Sequence, err)
		}
		if _, exists := byEffect[effect.ID]; exists {
			return nil, invalidBundle("primary effect %q has duplicate admission records", effect.ID)
		}
		byEffect[effect.ID] = retainedAdmission{reference: referenceFor(appended, record), record: record}
	}

	for _, effect := range primary.Effects {
		if !strings.HasPrefix(effect.ID, "sessionrepo-effect-") || len(effect.Intent) == 0 {
			continue
		}
		request, _, err := decodeOperationIntent(effect.Intent)
		if err != nil {
			// The baseline seed intent is not an operation intent.
			var seed struct {
				Action string `json:"action"`
			}
			if json.Unmarshal(effect.Intent, &seed) == nil && seed.Action == "seed" {
				continue
			}
			return nil, invalidBundle("decode primary intent %q while checking admissions: %v", effect.ID, err)
		}
		if !isAdmissionMutationTool(request.Tool) {
			if _, exists := byEffect[effect.ID]; exists {
				return nil, invalidBundle("non-mutation effect %q has an admission record", effect.ID)
			}
			continue
		}
		admitted, exists := byEffect[effect.ID]
		if effect.Unresolved || effect.Outcome != "success" {
			// Preserve a valid matching record for an unknown effect, but do not
			// accept its candidate or infer that its generation was promoted.
			if exists {
				input, ok := generationByID(generations, request.InputGeneration)
				if !ok || input.TreeDigest != request.InputTreeDigest || !sameManifest(input.Manifest, admitted.record.Input) {
					return nil, invalidBundle("orphan admission for %q does not match its committed input generation", effect.ID)
				}
			}
			continue
		}
		if !exists {
			return nil, invalidBundle("completed mutation effect %q has no admission record", effect.ID)
		}
		transition, reference, err := mutationEffectAdmission(effect, request)
		if err != nil || reference != admitted.reference || !sameTransition(transition, admitted.record.Candidate) {
			return nil, invalidBundle("mutation effect %q does not cross-reference its exact admission record", effect.ID)
		}
		input, ok := generationByID(generations, request.InputGeneration)
		if !ok || input.TreeDigest != request.InputTreeDigest || !sameManifest(input.Manifest, admitted.record.Input) {
			return nil, invalidBundle("admission record for %q does not match its committed input generation", effect.ID)
		}
		if admitted.record.Outcome == "allow" {
			output, ok := generationByID(generations, admitted.record.Output.Generation)
			if !ok || output.TreeDigest != transition.OutputTreeDigest || !sameManifest(output.Manifest, admitted.record.Output) {
				return nil, invalidBundle("allowed admission for %q does not match its exact promoted manifest", effect.ID)
			}
		}
	}
	return byEffect, nil
}

// validateAdmissionReferencesLocked checks the two journals while the caller
// holds Store.mu. It never calls back into Store and the admission journal has
// its own lock, so this does not re-enter or deadlock the Store lock.
func (s *Store) validateAdmissionReferencesLocked() error {
	primary, err := s.options.Journal.Trace()
	if err != nil {
		return fmt.Errorf("verify primary journal admission references: %w", err)
	}
	admission, err := s.admissionJournal.Trace()
	if err != nil {
		return fmt.Errorf("verify admission journal: %w", err)
	}
	generations := s.generationEvidenceLocked()
	if _, err := validateAdmissionEvidence(primary, admission, s.options.PolicyDigest, s.options.MetadataPolicyDigest, s.options.AdmissionScope, generations); err != nil {
		return fmt.Errorf("validate durable admission references: %w", err)
	}
	return nil
}

func isAdmissionMutationTool(tool string) bool {
	return tool == "write" || tool == "edit" || tool == "delete" || tool == "bash"
}

func generationByID(generations []GenerationEvidence, id string) (GenerationEvidence, bool) {
	for _, generation := range generations {
		if generation.ID == id {
			return generation, true
		}
	}
	return GenerationEvidence{}, false
}

func mutationEffectAdmission(effect audit.EffectTrace, request OperationRequest) (delta.Transition, AdmissionReference, error) {
	if request.Tool == "bash" {
		var result commandEffectResult
		if err := decodeCanonicalAdmissionJSON(effect.Result, &result); err != nil {
			return delta.Transition{}, AdmissionReference{}, fmt.Errorf("decode Bash effect result: %w", err)
		}
		return result.Transition, result.Admission, nil
	}
	var result mutationEffectResult
	if err := decodeCanonicalAdmissionJSON(effect.Result, &result); err != nil {
		return delta.Transition{}, AdmissionReference{}, fmt.Errorf("decode mutation effect result: %w", err)
	}
	return result.Transition, result.Admission, nil
}

func decodeCanonicalAdmissionJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON")
		}
		return err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, data) {
		return fmt.Errorf("effect result is not canonical JSON")
	}
	return nil
}
