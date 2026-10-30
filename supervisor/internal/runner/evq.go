package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"tbound/supervisor/internal/broker/protocol"
)

// MaxEVQResultsBytes bounds an EVQ results file.
const MaxEVQResultsBytes = 1 << 20

// EVQResult is one obligation result established independently of the executor of a
// trial: the verdict of the profile conformance suite for one EVQ obligation, with the
// reference to the artifact that shows it. A passed result needs an evidence reference.
type EVQResult struct {
	ID          string `json:"id"`
	Passed      bool   `json:"passed"`
	EvidenceRef string `json:"evidence_ref"`
}

// EVQResults are the pinned conformance results of a run.
type EVQResults []EVQResult

// Validate refuses an empty or repeated obligation ID and a pass without evidence.
func (r EVQResults) Validate() error {
	seen := make(map[string]bool, len(r))
	for _, o := range r {
		switch {
		case o.ID == "" || seen[o.ID]:
			return fmt.Errorf("EVQ result %q is empty or repeated", o.ID)
		case o.Passed && o.EvidenceRef == "":
			return fmt.Errorf("EVQ result %q passed without an evidence_ref", o.ID)
		}
		seen[o.ID] = true
	}
	return nil
}

// ParseEVQResults reads the minimal EVQ results document
//
//	{"obligations":[{"id":"...","passed":true,"evidence_ref":"..."}]}
//
// strictly: a duplicate key, an unknown member, trailing data, an empty list, an empty or
// repeated ID, or a pass without an evidence reference is refused.
func ParseEVQResults(data []byte) (EVQResults, error) {
	if len(data) > MaxEVQResultsBytes {
		return nil, errors.New("EVQ results exceed the size limit")
	}
	if err := protocol.ValidateStrictJSON(data); err != nil {
		return nil, fmt.Errorf("EVQ results: %w", err)
	}
	var doc struct {
		Obligations []EVQResult `json:"obligations"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("EVQ results: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("EVQ results: trailing data")
	}
	if len(doc.Obligations) == 0 {
		return nil, errors.New("EVQ results: no obligations")
	}
	if err := EVQResults(doc.Obligations).Validate(); err != nil {
		return nil, fmt.Errorf("EVQ results: %w", err)
	}
	return doc.Obligations, nil
}
