//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"syscall"
	"unicode"
	"unicode/utf8"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
)

const (
	piServiceAbandonedSchema     = "tbound-pi-service-request-abandoned/v1"
	piServiceAbandonedSettlement = "abandoned-unknown"
	piServiceAbandonedMaxReason  = 512
)

// piServiceRequestAbandonedEvent is the operator's explicit settlement of one request
// whose terminal is unknown. It is appended after, and never replaces, the accepted and
// terminal records. It names the exact terminal it settles by digest.
//
// It settles the request for the purpose of the one-prior-at-a-time rule only: later
// requests are no longer blocked by it. It is not evidence that the workflow had no
// effect, not a completion, and not an outage claim. A rerun of the case is a separate
// admission (attempt 2) that the runner permits only with its own outage evidence, and
// the original request id still replays its stored unknown terminal.
type piServiceRequestAbandonedEvent struct {
	SchemaVersion  string `json:"schema_version"`
	ClientUID      int    `json:"client_uid"`
	RequestID      string `json:"request_id"`
	PayloadDigest  string `json:"payload_digest"`
	TerminalDigest string `json:"terminal_digest"`
	Settlement     string `json:"settlement"`
	Reason         string `json:"reason"`
}

func piServiceTerminalDigest(response piServiceResponse) (string, error) {
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return digestBytes(encoded), nil
}

func validPiServiceAbandonReason(reason string) bool {
	if reason == "" || len(reason) > piServiceAbandonedMaxReason || !utf8.ValidString(reason) {
		return false
	}
	visible := false
	for _, r := range reason {
		if unicode.IsControl(r) {
			return false
		}
		if !unicode.IsSpace(r) {
			visible = true
		}
	}
	return visible
}

// newPiServiceRequestAbandonedEvent builds the settlement of state, which must be an
// unknown terminal that has not been settled.
func newPiServiceRequestAbandonedEvent(state piServiceRequestState, reason string) (piServiceRequestAbandonedEvent, error) {
	if state.terminal == nil || state.terminal.Status != "unknown" || state.abandoned != nil ||
		!validPiServiceAbandonReason(reason) {
		return piServiceRequestAbandonedEvent{}, ErrPiServiceRequestConflict
	}
	terminalDigest, err := piServiceTerminalDigest(*state.terminal)
	if err != nil {
		return piServiceRequestAbandonedEvent{}, err
	}
	return piServiceRequestAbandonedEvent{SchemaVersion: piServiceAbandonedSchema, ClientUID: state.accepted.ClientUID,
		RequestID: state.accepted.RequestID, PayloadDigest: state.accepted.PayloadDigest, TerminalDigest: terminalDigest,
		Settlement: piServiceAbandonedSettlement, Reason: reason}, nil
}

func marshalPiServiceRequestAbandonedEvent(event piServiceRequestAbandonedEvent) ([]byte, error) {
	return json.Marshal(event)
}

// matches reports whether the persisted settlement binds exactly this unknown terminal.
func (event piServiceRequestAbandonedEvent) matches(state piServiceRequestState) bool {
	if event.SchemaVersion != piServiceAbandonedSchema || event.Settlement != piServiceAbandonedSettlement ||
		!validPiServiceAbandonReason(event.Reason) || state.terminal == nil || state.terminal.Status != "unknown" ||
		state.abandoned != nil || event.ClientUID != state.accepted.ClientUID || event.RequestID != state.accepted.RequestID ||
		event.PayloadDigest != state.accepted.PayloadDigest {
		return false
	}
	terminalDigest, err := piServiceTerminalDigest(*state.terminal)
	return err == nil && terminalDigest == event.TerminalDigest
}

// abandonUnknown appends the operator settlement of the one request with this id whose
// terminal is unknown. The id must match exactly one journaled request, so a reused id
// under another client uid is refused as ambiguous rather than guessed.
func (j *piServiceRequestJournal) abandonUnknown(requestID, reason string) (audit.Record, error) {
	if j == nil || j.journal == nil {
		return audit.Record{}, ErrPiServiceUnavailable
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	var matchedKey string
	for key, state := range j.entries {
		if state.accepted.RequestID != requestID {
			continue
		}
		if matchedKey != "" {
			return audit.Record{}, fmt.Errorf("%w: request id %q is used by more than one client uid", ErrPiServiceRequestConflict, requestID)
		}
		matchedKey = key
	}
	if matchedKey == "" {
		return audit.Record{}, fmt.Errorf("%w: no journaled request has id %q", ErrPiServiceRequestConflict, requestID)
	}
	state := j.entries[matchedKey]
	event, err := newPiServiceRequestAbandonedEvent(state, reason)
	if err != nil {
		return audit.Record{}, fmt.Errorf("%w: request %q is not an unsettled unknown terminal, or the reason is not 1..%d printable bytes",
			err, requestID, piServiceAbandonedMaxReason)
	}
	data, err := marshalPiServiceRequestAbandonedEvent(event)
	if err != nil {
		return audit.Record{}, err
	}
	record, err := j.journal.Append(audit.Event{Kind: "pi_service_request_abandoned", ID: matchedKey + "/abandoned", Data: data})
	if err != nil {
		return audit.Record{}, err
	}
	state.abandoned = &event
	j.entries[matchedKey] = state
	return record, nil
}

// abandonUnknownEffectiveUID is a test seam for the root-only check.
var abandonUnknownEffectiveUID = os.Geteuid

// runAbandonUnknown is `tbound abandon-unknown --request-id ID --reason TEXT`, the
// root-only operator action that settles one unknown terminal of the Pi service request
// journal so later requests are admitted again. Stop tbound-pi.service first: the
// journal is flock-held while the service runs, and the command is then refused.
//
// The journal belongs to the service identity (audit.Open requires the owner to be the
// effective uid), so the command, after the root check, takes the service uid and gid
// from the signed registration and drops to them before it opens the journal. Nothing
// is rewritten: one record is appended and printed.
func runAbandonUnknown(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound abandon-unknown", flag.ContinueOnError)
	flags.SetOutput(stderr)
	requestID := flags.String("request-id", "", "request id whose unknown terminal is settled")
	reason := flags.String("reason", "", "why the unknown request is abandoned (1..512 printable bytes, kept in the journal)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !boundedProductionIdentity(*requestID, productionRequestMaxBytes) || !validPiServiceAbandonReason(*reason) {
		return errors.New("abandon-unknown needs --request-id and --reason and takes no other arguments")
	}
	if abandonUnknownEffectiveUID() != 0 {
		return errors.New("abandon-unknown is root-only: run it as root with tbound-pi.service stopped")
	}
	registration, err := piruntime.LoadVerifiedD09ManagedLiveRootPolicy(context.Background())
	if err != nil {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	if registration.ServiceUID <= 0 || registration.ServiceGID <= 0 {
		return ErrPiServiceUnavailable
	}
	if err := syscall.Setgroups([]int{}); err != nil {
		return fmt.Errorf("drop supplementary groups: %w", err)
	}
	if err := syscall.Setgid(registration.ServiceGID); err != nil {
		return fmt.Errorf("take the service gid: %w", err)
	}
	if err := syscall.Setuid(registration.ServiceUID); err != nil {
		return fmt.Errorf("take the service uid: %w", err)
	}
	journal, err := audit.Open(piServiceRequestJournalPath)
	if err != nil {
		return fmt.Errorf("open the request journal (is tbound-pi.service stopped?): %w", err)
	}
	requests, err := newPiServiceRequestJournal(journal)
	if err != nil {
		_ = journal.Close()
		return err
	}
	record, err := requests.abandonUnknown(*requestID, *reason)
	closeErr := requests.close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	encoded, err := json.Marshal(struct {
		Settlement string `json:"settlement"`
		RequestID  string `json:"request_id"`
		Sequence   uint64 `json:"journal_sequence"`
		Hash       string `json:"journal_hash"`
	}{piServiceAbandonedSettlement, *requestID, record.Sequence, record.Hash})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s\n", encoded)
	return err
}
