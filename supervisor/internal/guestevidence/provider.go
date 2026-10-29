package guestevidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// G1ToolSequence is the tool sequence of the E05 G1 fixture: read(g0), edit(g1), Bash(g2), read(g2).
var G1ToolSequence = []string{"read", "edit", "bash", "read"}

const (
	responseSchema = "tbound-pi-service-response/v1"
	receiptSchema  = "tbound-production-pi-receipt/v1"
)

// ProviderExchange is the data of a guest-provider-exchange artifact: the one response line the
// governed client printed for a real run, byte for byte, with its digest.
type ProviderExchange struct {
	Response       string `json:"response"`
	ResponseSHA256 string `json:"response_sha256"`
	// Binding ties the run's own signed profile and template to what the run shares with the pilot.
	Binding ProfileBinding `json:"binding"`
}

// NewProviderExchange wraps a response line.
func NewProviderExchange(response string) ProviderExchange {
	sum := sha256.Sum256([]byte(response))
	return ProviderExchange{Response: response, ResponseSHA256: "sha256:" + hex.EncodeToString(sum[:])}
}

type serviceResponse struct {
	SchemaVersion string          `json:"schema_version"`
	RequestID     string          `json:"request_id"`
	Status        string          `json:"status"`
	ErrorCode     string          `json:"error_code"`
	Receipt       json.RawMessage `json:"receipt"`
}

// receiptFacts are the receipt members this validator reads. The receipt is the service's own
// document: members this validator does not read are not constrained here.
type receiptFacts struct {
	SchemaVersion      string   `json:"schema_version"`
	RunID              string   `json:"run_id"`
	ProfileHash        string   `json:"profile_hash"`
	TemplateDigest     string   `json:"containment_profile_digest"`
	FrozenSourceDigest string   `json:"frozen_source_digest"`
	ProviderExchange   bool     `json:"provider_exchange"`
	ClaimBearing       bool     `json:"claim_bearing"`
	TaskAdmitted       bool     `json:"task_admitted"`
	TurnCompleted      bool     `json:"turn_completed"`
	ToolSequence       []string `json:"tool_sequence"`
	ProviderTurns      int      `json:"provider_turns"`
	SourceGeneration   string   `json:"source_generation"`
	FinalGeneration    string   `json:"final_generation"`
	GenerationCount    int      `json:"generation_count"`
	Settlement         string   `json:"settlement"`
	ExitObserved       bool     `json:"exit_observed"`
	ExitCode           int      `json:"exit_code"`
	DescendantsSettled bool     `json:"descendants_settled"`
	Success            bool     `json:"success"`
	Disposition        string   `json:"disposition"`
	PublicationStatus  string   `json:"publication_status"`
}

// decodeLenient decodes a document another component owns: a duplicate key and trailing data are
// refused, as are two member names equal under case folding; members this package does not read are accepted.
func decodeLenient(data []byte, v any) error {
	if err := checkEvidenceJSON(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}

func validateProviderExchange(raw []byte, e Expect) error {
	var p ProviderExchange
	if err := DecodeStrict(raw, &p); err != nil {
		return err
	}
	if p.ResponseSHA256 != NewProviderExchange(p.Response).ResponseSHA256 {
		return errors.New("response_sha256 does not match the response")
	}
	var resp serviceResponse
	if err := decodeLenient([]byte(p.Response), &resp); err != nil {
		return fmt.Errorf("the response is not a service response: %w", err)
	}
	if len(resp.Receipt) == 0 {
		return errors.New("the response carries no receipt")
	}
	var f receiptFacts
	if err := decodeLenient(resp.Receipt, &f); err != nil {
		return fmt.Errorf("the receipt is malformed: %w", err)
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if resp.SchemaVersion != responseSchema || resp.Status != "completed" || resp.ErrorCode != "" || resp.RequestID == "" {
		add("the response is not a completed %s (schema %q, status %q, error %q)", responseSchema, resp.SchemaVersion, resp.Status, resp.ErrorCode)
	}
	if f.SchemaVersion != receiptSchema {
		add("receipt schema %q, want %q", f.SchemaVersion, receiptSchema)
	}
	if !IsDigest(f.ProfileHash) || !IsDigest(f.TemplateDigest) || f.FrozenSourceDigest != e.FrozenSourceDigest {
		add("the receipt is for profile %q, template %q and source %q, the run pins source %q", f.ProfileHash, f.TemplateDigest, f.FrozenSourceDigest, e.FrozenSourceDigest)
	}
	bindingProblems(p.Binding, e, f.ProfileHash, f.TemplateDigest, p.Binding.ImageDigest, f.FrozenSourceDigest, add)
	if !f.ProviderExchange || !f.TaskAdmitted || !f.TurnCompleted || !f.Success {
		add("the receipt does not show a completed, successful run with a provider exchange (exchange %t, admitted %t, completed %t, success %t)",
			f.ProviderExchange, f.TaskAdmitted, f.TurnCompleted, f.Success)
	}
	// A G1 success publishes the edit and its Bash output, so a NO_DIFF run is not one. The status is publication.StatusSucceeded.
	if f.Disposition != "PUBLISHED" || f.PublicationStatus != "SUCCEEDED" {
		add("the receipt disposition %q and publication status %q, want PUBLISHED and SUCCEEDED", f.Disposition, f.PublicationStatus)
	}
	if f.ClaimBearing {
		add("a claim-bearing receipt is not an engineering fixture")
	}
	if !slices.Equal(f.ToolSequence, G1ToolSequence) {
		add("tool sequence %q, want %q", f.ToolSequence, G1ToolSequence)
	}
	if f.ProviderTurns < len(G1ToolSequence) {
		add("%d provider turns for %d tool calls", f.ProviderTurns, len(G1ToolSequence))
	}
	if f.SourceGeneration != "g0" || f.FinalGeneration != "g2" || f.GenerationCount != 3 {
		add("generations %q to %q (%d), want g0 to g2 (3)", f.SourceGeneration, f.FinalGeneration, f.GenerationCount)
	}
	// The production rule (productionWorkerSettlementAllowsPublication): the supervisor stops the
	// worker after the turn, so a clean run ends STOPPED with exit -1; the exit code is not part of it.
	if f.Settlement != "STOPPED" || !f.ExitObserved || !f.DescendantsSettled {
		add("the run did not settle cleanly (settlement %q, exit observed %t, exit %d, descendants settled %t)", f.Settlement, f.ExitObserved, f.ExitCode, f.DescendantsSettled)
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// RunIdentity is what a completed service response states about its run: the run id and the profile
// and frozen source the receipt is for.
type RunIdentity struct {
	RunID              string
	ProfileHash        string
	FrozenSourceDigest string
}

// RunIdentityFromResponse reads one response line of the governed client and returns the identity of
// the run if the response is a completed, successful, non-claim-bearing run of the production
// receipt schema.
func RunIdentityFromResponse(response string) (RunIdentity, error) {
	var resp serviceResponse
	if err := decodeLenient([]byte(response), &resp); err != nil {
		return RunIdentity{}, fmt.Errorf("the response is not a service response: %w", err)
	}
	var f receiptFacts
	if err := decodeLenient(resp.Receipt, &f); err != nil || len(resp.Receipt) == 0 {
		return RunIdentity{}, fmt.Errorf("the response carries no readable receipt: %v", err)
	}
	switch {
	case resp.SchemaVersion != responseSchema || resp.Status != "completed" || resp.ErrorCode != "":
		return RunIdentity{}, fmt.Errorf("the response is not a completed %s (status %q, error %q)", responseSchema, resp.Status, resp.ErrorCode)
	case f.SchemaVersion != receiptSchema || !f.TaskAdmitted || !f.TurnCompleted || !f.Success || f.ClaimBearing:
		return RunIdentity{}, errors.New("the receipt does not show a completed, successful, non-claim-bearing run")
	case f.RunID == "" || !IsDigest(f.ProfileHash) || !IsDigest(f.FrozenSourceDigest):
		return RunIdentity{}, errors.New("the receipt does not name the run, its profile and its source")
	}
	return RunIdentity{RunID: f.RunID, ProfileHash: f.ProfileHash, FrozenSourceDigest: f.FrozenSourceDigest}, nil
}
