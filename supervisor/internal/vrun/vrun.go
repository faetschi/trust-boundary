package vrun

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"tbound/supervisor/internal/taskaccept"
)

// Schema is the schema of the document `tbound trial v-run` prints.
const Schema = "tbound-v-run/v1"

// Doc is what `tbound trial v-run <case_id>` reports: the identities of the run (the pinned cell
// image digest and the fixture digest of the case), how the container ended, Pi's own usage
// numbers, and the acceptance result on the run's workspace. Accept is null when the tree could
// not be evaluated; AcceptError then says why.
//
// TokensIn counts input, cache-read and cache-write tokens of every assistant message Pi
// completed, TokensOut the output tokens, and CostMicroUSD the cost Pi computed from its own
// price table for the model, in millionths of a US dollar (it is not a provider invoice).
// UsageEvents is the number of completed assistant messages the numbers were summed over; zero
// means Pi reported no usage at all. StartedNS and FinishedNS are guest wall-clock Unix
// nanoseconds around the container run.
type Doc struct {
	Schema        string                `json:"schema"`
	CaseID        string                `json:"case_id"`
	ImageDigest   string                `json:"image_digest"`
	FixtureDigest string                `json:"fixture_digest"`
	Exit          int                   `json:"exit"`
	TimedOut      bool                  `json:"timed_out"`
	TokensIn      int64                 `json:"tokens_in"`
	TokensOut     int64                 `json:"tokens_out"`
	CostMicroUSD  int64                 `json:"cost_micro_usd"`
	ToolCalls     int                   `json:"tool_calls"`
	UsageEvents   int                   `json:"usage_events"`
	Accept        *taskaccept.AcceptDoc `json:"accept"`
	AcceptError   string                `json:"accept_error,omitempty"`
	StartedNS     int64                 `json:"started_ns"`
	FinishedNS    int64                 `json:"finished_ns"`
}

// Parse reads one Doc strictly: unknown members and data after the document are refused, and the
// schema and the identities must be present. It does not decide whether the run was good.
func Parse(raw []byte) (Doc, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var d Doc
	if err := decoder.Decode(&d); err != nil {
		return Doc{}, fmt.Errorf("v-run document: %w", err)
	}
	if decoder.More() {
		return Doc{}, errors.New("v-run document: data after the document")
	}
	switch {
	case d.Schema != Schema:
		return Doc{}, fmt.Errorf("v-run document: schema %q, want %q", d.Schema, Schema)
	case d.CaseID == "" || d.ImageDigest == "" || d.FixtureDigest == "":
		return Doc{}, errors.New("v-run document: case id, image digest and fixture digest are required")
	case d.TokensIn < 0 || d.TokensOut < 0 || d.CostMicroUSD < 0 || d.ToolCalls < 0 || d.UsageEvents < 0:
		return Doc{}, errors.New("v-run document: negative usage")
	case d.FinishedNS < d.StartedNS:
		return Doc{}, errors.New("v-run document: the run finished before it started")
	}
	return d, nil
}
