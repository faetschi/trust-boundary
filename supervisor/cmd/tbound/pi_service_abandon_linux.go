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
	"tbound/supervisor/internal/researchroute"
)

const (
	piServiceAbandonedSchema     = "tbound-pi-service-request-abandoned/v3"
	piServiceAbandonedSettlement = "abandoned-unknown"
	piServiceAbandonedMaxReason  = 512
)

// piServiceRequestAbandonedEvent is the operator's explicit settlement of one request
// whose terminal is unknown. It is appended after, and never replaces, the accepted and
// terminal records. It names the exact terminal it settles by digest.
//
// It settles the request for the purpose of the one-prior-at-a-time rule only, and only
// after the host reconciled the request's workflow (owner drained, no pending effect or
// publication, no undisposed provider request): the record binds that evidence, and every
// later begin re-derives it before it skips the request. It is not a claim that the
// workflow had no effect, not a completion, and not an outage claim. Above all it never
// claims remote quiescence: whether a provider request still runs on the provider's side
// cannot be proven from host state. The evidence claims only that local effects are drained
// and settled, and that provider cost is booked at the worst case. Its ProviderSettlement
// says which: "terminal" when every exchange has a terminal record, or
// "bounded-retained-liability" when an exchange is UNCERTAIN in the USD ledger or unknown in
// the provider-bridge journal, whose cost is retained at its reserved worst case. A rerun of the case is a separate
// admission (attempt 2) that the runner permits only with its own outage evidence, and
// the original request id still replays its stored unknown terminal.
type piServiceRequestAbandonedEvent struct {
	SchemaVersion  string `json:"schema_version"`
	ClientUID      int    `json:"client_uid"`
	RequestID      string `json:"request_id"`
	PayloadDigest  string `json:"payload_digest"`
	TerminalDigest string `json:"terminal_digest"`
	Settlement     string `json:"settlement"`
	// Reason is the operator's statement. It is not evidence.
	Reason string `json:"reason"`
	// Reconciliation is the host evidence that the request's workflow was quiescent when the
	// operator abandoned it, and ReconciliationDigest commits to it. Both are bound apart from
	// the reason, and begin re-derives the evidence before it skips this request.
	Reconciliation       piServiceReconciliation `json:"reconciliation"`
	ReconciliationDigest string                  `json:"reconciliation_digest"`
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
func newPiServiceRequestAbandonedEvent(state piServiceRequestState, reason string,
	reconciliation piServiceReconciliation) (piServiceRequestAbandonedEvent, error) {
	if !abandonableState(state) || !validPiServiceAbandonReason(reason) || !reconciliation.valid() {
		return piServiceRequestAbandonedEvent{}, ErrPiServiceRequestConflict
	}
	terminalDigest, err := piServiceTerminalDigest(*state.terminal)
	if err != nil {
		return piServiceRequestAbandonedEvent{}, err
	}
	reconciliationDigest, err := reconciliation.digest()
	if err != nil {
		return piServiceRequestAbandonedEvent{}, err
	}
	return piServiceRequestAbandonedEvent{SchemaVersion: piServiceAbandonedSchema, ClientUID: state.accepted.ClientUID,
		RequestID: state.accepted.RequestID, PayloadDigest: state.accepted.PayloadDigest, TerminalDigest: terminalDigest,
		Settlement: piServiceAbandonedSettlement, Reason: reason, Reconciliation: reconciliation,
		ReconciliationDigest: reconciliationDigest}, nil
}

func marshalPiServiceRequestAbandonedEvent(event piServiceRequestAbandonedEvent) ([]byte, error) {
	return json.Marshal(event)
}

// abandonableState is an unknown terminal that no operator settlement has settled yet.
func abandonableState(state piServiceRequestState) bool {
	return state.terminal != nil && state.terminal.Status == "unknown" && state.abandoned == nil
}

// matches reports whether the persisted settlement binds exactly this unknown terminal and
// carries well formed reconciliation evidence that its digest commits to.
func (event piServiceRequestAbandonedEvent) matches(state piServiceRequestState) bool {
	return state.abandoned == nil && event.bindsTerminal(state)
}

// bindsTerminal is matches for a state that already carries this settlement.
func (event piServiceRequestAbandonedEvent) bindsTerminal(state piServiceRequestState) bool {
	if event.SchemaVersion != piServiceAbandonedSchema || event.Settlement != piServiceAbandonedSettlement ||
		!validPiServiceAbandonReason(event.Reason) || state.terminal == nil || state.terminal.Status != "unknown" ||
		event.ClientUID != state.accepted.ClientUID || event.RequestID != state.accepted.RequestID ||
		event.PayloadDigest != state.accepted.PayloadDigest || !event.Reconciliation.valid() {
		return false
	}
	reconciliationDigest, err := event.Reconciliation.digest()
	if err != nil || reconciliationDigest != event.ReconciliationDigest {
		return false
	}
	terminalDigest, err := piServiceTerminalDigest(*state.terminal)
	return err == nil && terminalDigest == event.TerminalDigest
}

// admissionReconciler is the reconciler one begin uses for all of its abandoned priors. A
// reconciler that can scope itself to an admission returns a view whose publication-journal
// snapshot is read lazily, once, and dropped when the begin returns. j.mu is held.
func (j *piServiceRequestJournal) admissionReconciler() piServiceAbandonReconciler {
	if scoped, ok := j.reconciler.(interface {
		forAdmission() piServiceAbandonReconciler
	}); ok {
		return scoped.forAdmission()
	}
	return j.reconciler
}

// recheckAbandoned is the condition under which begin may skip an abandoned request: the
// record still binds this terminal, a reconciler is installed, and the workflow's evidence,
// derived again now, still equals the evidence bound at abandonment. j.mu is held.
func (j *piServiceRequestJournal) recheckAbandoned(ctx context.Context, reconciler piServiceAbandonReconciler,
	state piServiceRequestState) error {
	if state.abandoned == nil || !state.abandoned.bindsTerminal(state) {
		return fmt.Errorf("request %q: the abandon record is missing or does not bind its terminal", state.accepted.RequestID)
	}
	if reconciler == nil {
		return fmt.Errorf("request %q: no workflow reconciler is installed to re-check its abandon record", state.accepted.RequestID)
	}
	current, err := reconciler.Reconcile(ctx, state)
	if err != nil {
		return fmt.Errorf("request %q is abandoned but its workflow is no longer reconciled: %w", state.accepted.RequestID, err)
	}
	if !state.abandoned.Reconciliation.stillHolds(current) {
		return fmt.Errorf("request %q: the evidence bound in its abandon record no longer matches the host", state.accepted.RequestID)
	}
	return nil
}

// abandonUnknown appends the operator settlement of the one request with this id whose
// terminal is unknown. The id must match exactly one journaled request, so a reused id
// under another client uid is refused as ambiguous rather than guessed.
//
// The settlement is appended only after the installed reconciler proved the request's
// workflow quiescent (owner scopes drained, no pending effect or publication, no provider
// request without a recorded disposition). If any of that is unproven the request stays
// quarantined, nothing is written, and the error lists what is unreconciled.
func (j *piServiceRequestJournal) abandonUnknown(ctx context.Context, requestID, reason string) (audit.Record, error) {
	if j == nil || j.journal == nil || ctx == nil {
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
	if !abandonableState(state) || !validPiServiceAbandonReason(reason) {
		return audit.Record{}, fmt.Errorf("%w: request %q is not an unsettled unknown terminal, or the reason is not 1..%d printable bytes",
			ErrPiServiceRequestConflict, requestID, piServiceAbandonedMaxReason)
	}
	if j.reconciler == nil {
		return audit.Record{}, fmt.Errorf("%w: no workflow reconciler is installed; request %q stays quarantined",
			ErrPiServiceUnreconciled, requestID)
	}
	reconciliation, err := j.reconciler.Reconcile(ctx, state)
	if err != nil {
		if !errors.Is(err, ErrPiServiceUnreconciled) {
			err = fmt.Errorf("%w: %w", ErrPiServiceUnreconciled, err)
		}
		return audit.Record{}, fmt.Errorf("request %q stays quarantined and its barrier on later requests stays: %w", requestID, err)
	}
	event, err := newPiServiceRequestAbandonedEvent(state, reason, reconciliation)
	if err != nil {
		return audit.Record{}, fmt.Errorf("%w: the reconciliation of request %q is not well formed evidence", ErrPiServiceUnreconciled, requestID)
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
	// The abandon record is written first, so a terminal never exists without it. A failure here
	// leaves the reset refusing (fail closed) and the request abandoned; the next service start
	// (settleAbandonedPriors) and every later admission retry it.
	if err := j.closeAbandonedWorkflow(state); err != nil {
		return record, err
	}
	return record, nil
}

// abandonUnknownUsage is the help text of the abandon-unknown command.
const abandonUnknownUsage = `usage: tbound abandon-unknown --request-id ID --reason TEXT

Settles one unknown Pi service request so later requests are admitted again. Root-only; stop
tbound-pi.service first.

The request is abandoned only after the host reconciled its workflow: owner scopes drained,
no pending effect or publication, no provider request without a recorded disposition. The
evidence is bound in the journal and re-derived on every later admission.

Abandonment never claims remote quiescence. Whether a provider request still runs on the
provider's side cannot be proven from host state. It claims only that local effects are drained
and settled, and that provider cost is booked at the worst case. The recorded provider
settlement says which: "terminal" (every exchange has a terminal record) or
"bounded-retained-liability" (an exchange is UNCERTAIN in the USD ledger or unknown in the
provider-bridge journal; its cost is retained at the reserved worst case).

`

// operatorEffectiveUID is a test seam for the root-only check of the operator verbs.
var operatorEffectiveUID = os.Geteuid

// dropToServiceIdentity gives up root for the service uid and gid of the signed registration. The
// journals and stores of the service belong to that identity, so an operator verb that opens them
// takes it first and keeps no supplementary groups.
func dropToServiceIdentity(registration piruntime.D09ManagedLiveRootRegistration) error {
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
	return nil
}

// runAbandonUnknown is `tbound abandon-unknown --request-id ID --reason TEXT`, the
// root-only operator action that settles one unknown terminal of the Pi service request
// journal so later requests are admitted again. Stop tbound-pi.service first: the
// journal is flock-held while the service runs, and the command is then refused.
//
// The settlement is appended only after the host reconciled the request's workflow: owner
// scopes drained, no pending effect or publication, no provider request without a recorded
// disposition (the USD ledger on the research route, the provider-bridge journals on the
// engineering route). If any of that is unproven the command refuses, lists what is
// unreconciled, and writes nothing; the request stays quarantined.
//
// The journal belongs to the service identity (audit.Open requires the owner to be the
// effective uid), so the command, after the root check, takes the service uid and gid
// from the signed registration and drops to them before it opens the journal. Nothing
// is rewritten: one record is appended and printed.
func runAbandonUnknown(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("tbound abandon-unknown", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprint(stderr, abandonUnknownUsage)
		flags.PrintDefaults()
	}
	requestID := flags.String("request-id", "", "request id whose unknown terminal is settled")
	reason := flags.String("reason", "", "why the unknown request is abandoned (1..512 printable bytes, kept in the journal)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !boundedProductionIdentity(*requestID, productionRequestMaxBytes) || !validPiServiceAbandonReason(*reason) {
		return errors.New("abandon-unknown needs --request-id and --reason and takes no other arguments")
	}
	if operatorEffectiveUID() != 0 {
		return errors.New("abandon-unknown is root-only: run it as root with tbound-pi.service stopped")
	}
	registration, err := piruntime.LoadVerifiedD09ManagedLiveRootPolicy(context.Background())
	if err != nil {
		return errors.Join(ErrPiServiceUnavailable, err)
	}
	if err := dropToServiceIdentity(registration); err != nil {
		return err
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
	ctx := context.Background()
	reconciler, closeReconciler, err := openAbandonReconciler(ctx, registration)
	if err != nil {
		_ = requests.close()
		return errors.Join(ErrPiServiceUnreconciled, err)
	}
	requests.setReconciler(reconciler)
	record, err := requests.abandonUnknown(ctx, *requestID, *reason)
	closeErr := errors.Join(closeReconciler(), requests.close())
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

// openAbandonReconciler is a test seam for the host stores the command reconciles against.
var openAbandonReconciler = openProductionAbandonReconciler

// openProductionAbandonReconciler opens, as the service identity and with the service stopped,
// the stores the running service reconciles against: the signed service config, the provider
// route (the exclusive USD ledger on the research route) and the workflow catalog. The command
// is not the managed service process, so it cannot take the service's root capability and needs
// none: the reconciler reads the owner store through the protected root and the audit journals
// directly.
func openProductionAbandonReconciler(ctx context.Context, registration piruntime.D09ManagedLiveRootRegistration) (
	piServiceAbandonReconciler, func() error, error) {
	_, contract, err := serviceSocketRegistrationFromSignedRegistration(registration)
	if err != nil {
		return nil, nil, err
	}
	config, err := readProductionPiServiceConfig(contract.ServiceConfigPath, contract.ServiceConfigDigest)
	if err != nil {
		return nil, nil, err
	}
	route, err := openProductionProviderRoute(config, researchroute.DefaultPaths())
	if err != nil {
		return nil, nil, err
	}
	catalog, err := openPiServiceWorkflowCatalog(config.WorkflowCatalogPath)
	if err != nil {
		_ = route.Close()
		return nil, nil, err
	}
	return newProductionUnknownReconciler(catalog, config, route), func() error {
		return errors.Join(catalog.Close(), route.Close())
	}, nil
}

// closeAbandonedWorkflow writes the catalog terminal of an abandoned request through the
// reconciler, when it can. The call is idempotent. The caller has the abandon record in hand and
// holds j.mu.
func (j *piServiceRequestJournal) closeAbandonedWorkflow(state piServiceRequestState) error {
	settler, ok := j.reconciler.(interface {
		markAbandoned(piServiceRequestState) error
	})
	if !ok {
		return nil
	}
	if err := settler.markAbandoned(state); err != nil {
		return fmt.Errorf("request %q is abandoned but its catalog workflow was not closed: %w", state.accepted.RequestID, err)
	}
	return nil
}

// settleAbandonedPriors is the service-start recovery of an abandon whose catalog terminal is
// missing (a crash or catalog failure after the abandon record, or an abandon from before the
// terminal existed). Each abandoned prior is re-reconciled first, so a terminal is written only
// while the evidence bound in its abandon record still holds. A prior that fails stays as it
// was, and its error is joined into the result.
func (j *piServiceRequestJournal) settleAbandonedPriors(ctx context.Context) error {
	if j == nil || ctx == nil {
		return ErrPiServiceUnavailable
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	reconciler := j.admissionReconciler()
	var failures []error
	for _, prior := range j.entries {
		if prior.abandoned == nil || prior.terminal == nil || prior.terminal.Status != "unknown" {
			continue
		}
		if err := j.recheckAbandoned(ctx, reconciler, prior); err != nil {
			failures = append(failures, err)
			continue
		}
		if err := j.closeAbandonedWorkflow(prior); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
