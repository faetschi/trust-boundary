package evalscore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// Load decodes one evalscore-trials/v1 JSON object from r and validates it. It
// reads at most MaxInputBytes. Every scoring field in the schema is required, must be
// spelled exactly as written, and must appear once and not as null. Unknown,
// duplicate, and mistyped fields are errors, as are trailing data and invalid
// UTF-8. encoding/json matches field names case-insensitively, so the exact-key
// check in decodeObject is what rejects variants such as "Case_Id".
//
// Schema (evalscore-trials/v1):
//
//	{
//	  "schema": "evalscore-trials/v1",
//	  "evq_passed": <bool>,
//	  "adversarial": [{
//	    "case_id": <string>, "attempt": <int >= 1>, "channel": <string>,
//	    "identity_verified": <bool>, "observer_coverage_complete": <bool>,
//	    "evidence_loss": "none"|"adversary_induced"|"resource_exhaustion"|"unclassified"|"external_outage",
//	    "c_effect": "none"|"observed"|"unknown",
//	    "c_tool_attempt": <bool>, "c_residual_attempt": <bool>,
//	    "p_static_manifested": <bool>, "p_adaptive_manifested": <bool>,
//	    "c_matched_delivery": <bool>, "pc_evidence_complete": <bool>,
//	    "outage_evidence_digest": <string>
//	  }],
//	  "compliant": [{
//	    "task_id": <string>, "attempt": <int >= 1>,
//	    "identity_verified": <bool>, "observer_coverage_complete": <bool>,
//	    "evidence_loss": <as above>,
//	    "b_success": <bool>, "c_success": <bool>, "v_success": <bool>,
//	    "attribution": "not_applicable"|"supported"|"unsupported"|"unresolved",
//	    "outage_evidence_digest": <string>
//	  }]
//	}
//
// A compliant record may also carry the optional "v_missing": <bool> (default
// false), set when the V result is absent. V is descriptive, so it never
// affects the friction decision.
//
// The optional post_denial_trials array carries descriptive C execution records
// (task_id, attempt, success, proposals). Each proposal has tool, verdict,
// reason_code and optional status. These records never affect Summarize.
//
// Load checks JSON shape and the structural rules of validate. It does not check
// enum values: a record with an unregistered enum value loads, and Summarize
// reports it as an invalid record, counted as UNSUPPORTED.
//
// The ambiguous-case set (tbound-thesis.md section 7.3) is outside H1 and has no
// record kind here.
func Load(r io.Reader) (Input, error) {
	if r == nil {
		return Input{}, errors.New("evalscore: nil reader")
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxInputBytes+1))
	if err != nil {
		return Input{}, fmt.Errorf("evalscore: read trials: %w", err)
	}
	if len(data) > MaxInputBytes {
		return Input{}, fmt.Errorf("evalscore: trials exceed %d bytes", MaxInputBytes)
	}
	if !utf8.Valid(data) {
		return Input{}, errors.New("evalscore: trials are not valid UTF-8")
	}

	var top struct {
		CDH              json.RawMessage   `json:"cdh"`
		Schema           string            `json:"schema"`
		EVQPassed        bool              `json:"evq_passed"`
		Adversarial      []json.RawMessage `json:"adversarial"`
		Compliant        []json.RawMessage `json:"compliant"`
		PostDenialTrials []json.RawMessage `json:"post_denial_trials"`
	}
	if err := decodeObjectOptional(data, &top, []string{"post_denial_trials", "cdh"}, "schema", "evq_passed", "adversarial", "compliant"); err != nil {
		return Input{}, fmt.Errorf("evalscore: top level: %w", err)
	}

	in := Input{
		Schema:      top.Schema,
		EVQPassed:   top.EVQPassed,
		Adversarial: make([]AdversarialRecord, 0, len(top.Adversarial)),
		Compliant:   make([]CompliantRecord, 0, len(top.Compliant)),
	}
	for i, raw := range top.Adversarial {
		var rec AdversarialRecord
		if err := decodeObject(raw, &rec, advFields...); err != nil {
			return Input{}, fmt.Errorf("evalscore: adversarial[%d]: %w", i, err)
		}
		in.Adversarial = append(in.Adversarial, rec)
	}
	for i, raw := range top.Compliant {
		var rec CompliantRecord
		if err := decodeObjectOptional(raw, &rec, []string{"v_missing"}, compFields...); err != nil {
			return Input{}, fmt.Errorf("evalscore: compliant[%d]: %w", i, err)
		}
		in.Compliant = append(in.Compliant, rec)
	}
	seenTrials := map[string]bool{}
	for i, raw := range top.PostDenialTrials {
		var trial struct {
			TaskID    string            `json:"task_id"`
			Attempt   int               `json:"attempt"`
			Success   bool              `json:"success"`
			Proposals []json.RawMessage `json:"proposals"`
		}
		if err := decodeObject(raw, &trial, "task_id", "attempt", "success", "proposals"); err != nil {
			return Input{}, fmt.Errorf("evalscore: post_denial_trials[%d]: %w", i, err)
		}
		key := fmt.Sprintf("%s/%d", trial.TaskID, trial.Attempt)
		if trial.TaskID == "" || trial.Attempt < 1 || seenTrials[key] {
			return Input{}, fmt.Errorf("evalscore: invalid or duplicate post-denial trial %q", key)
		}
		seenTrials[key] = true
		rec := PostDenialTrial{TaskID: trial.TaskID, Attempt: trial.Attempt, Success: trial.Success, Proposals: []ProposalFact{}}
		for _, raw := range trial.Proposals {
			var p ProposalFact
			if err := decodeObjectOptional(raw, &p, []string{"status"}, "tool", "verdict", "reason_code"); err != nil {
				return Input{}, fmt.Errorf("evalscore: post-denial proposal: %w", err)
			}
			rec.Proposals = append(rec.Proposals, p)
		}
		in.PostDenialTrials = append(in.PostDenialTrials, rec)
	}
	if err := in.validate(); err != nil {
		return Input{}, err
	}
	if len(top.CDH) != 0 {
		in.CDH, err = loadCDH(top.CDH)
		if err != nil {
			return Input{}, err
		}
	}
	return in, nil
}

var advFields = []string{
	"case_id", "attempt", "channel", "identity_verified", "observer_coverage_complete",
	"evidence_loss", "c_effect", "c_tool_attempt", "c_residual_attempt",
	"p_static_manifested", "p_adaptive_manifested", "c_matched_delivery", "pc_evidence_complete",
	"outage_evidence_digest",
}

var compFields = []string{
	"task_id", "attempt", "identity_verified", "observer_coverage_complete",
	"evidence_loss", "b_success", "c_success", "v_success", "attribution",
	"outage_evidence_digest",
}

// decodeObject checks the exact key set of one JSON object and then decodes it
// into dst. It rejects duplicate, unknown, missing, and null fields, and any
// data after the object.
func decodeObject(raw []byte, dst any, fields ...string) error {
	return decodeObjectOptional(raw, dst, nil, fields...)
}

// Optional fields are additive descriptive evidence; existing v1 inputs remain valid.
func decodeObjectOptional(raw []byte, dst any, optional []string, fields ...string) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return errors.New("expected a JSON object")
	}
	known := make(map[string]bool, len(fields))
	for _, f := range fields {
		known[f] = true
	}
	for _, f := range optional {
		known[f] = true
	}
	seen := make(map[string]json.RawMessage, len(fields))
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok {
			return errors.New("expected a field name")
		}
		if !known[key] {
			return fmt.Errorf("unknown field %q", key)
		}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("duplicate field %q", key)
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return fmt.Errorf("field %q: %w", key, err)
		}
		seen[key] = v
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return fmt.Errorf("field %q is null", key)
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return errors.New("unterminated JSON object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing data after JSON object")
	}
	for _, f := range fields {
		_, ok := seen[f]
		if !ok {
			return fmt.Errorf("missing field %q", f)
		}
	}
	return json.Unmarshal(raw, dst)
}
