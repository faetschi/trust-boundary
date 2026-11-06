//go:build linux

// worker_session_linux.go: Pi worker session: prompt dispatch, event pump, close and launch-failure settlement.

package piruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// SendAdmittedPrompt commits host/session admission in Go before writing the
// prompt over FD 0. The Pi SDK never sends prompt/history/results to FD 7.
type GoPromptConversation interface {
	AdmitPrompt(taskID, prompt string) error
	Close()
}

func (w *PiCLIWorker) SendAdmittedPrompt(ctx context.Context, conversation GoPromptConversation, taskID, requestID, prompt string) error {
	if w == nil || w.Session == nil || w.Channels == nil || conversation == nil || ctx == nil {
		return errors.New("governed Pi prompt requires an active session, Go conversation, and context")
	}
	if !workerValidIdentity(taskID, PiWorkerMaxTaskIDBytes) || !workerValidIdentity(requestID, PiWorkerMaxIdentityBytes) ||
		!utf8.ValidString(prompt) || len([]byte(prompt)) == 0 || len([]byte(prompt)) > PiWorkerMaxPromptBytes {
		return errors.New("governed Pi prompt is invalid or exceeds the broker byte bound")
	}
	return w.sendPromptControl(ctx, conversation, taskID, requestID, prompt)
}

// SendDevelopmentPrompt is a development-only process-test seam. It has no Go
// provider Conversation and therefore makes no provider-history or G1 claim.
func (w *PiCLIWorker) SendDevelopmentPrompt(ctx context.Context, taskID, requestID, prompt string) error {
	if w == nil || !w.development || ctx == nil {
		return errors.New("direct prompt control is available only in the explicit developer worker")
	}
	if !workerValidIdentity(taskID, 128) || !workerValidIdentity(requestID, 256) ||
		!utf8.ValidString(prompt) || len([]byte(prompt)) == 0 || len([]byte(prompt)) > PiWorkerMaxPromptBytes {
		return errors.New("development prompt is invalid or exceeds its byte bound")
	}
	return w.sendPromptControl(ctx, nil, taskID, requestID, prompt)
}

func (w *PiCLIWorker) sendPromptControl(ctx context.Context, conversation GoPromptConversation, taskID, requestID, prompt string) error {
	if w.rpc == nil {
		return errors.New("official Pi CLI RPC is required; embedded SDK worker is not an admitted fallback")
	}
	lease, err := w.Session.Acquire(ctx)
	if err != nil {
		return err
	}
	w.mu.Lock()
	if w.closed || len(w.leases) != 0 {
		w.mu.Unlock()
		lease.Release()
		return errors.New("governed Pi worker is closed or already has an active prompt")
	}
	w.leases[requestID] = &workerPromptLease{lease: lease}
	w.mu.Unlock()
	if conversation != nil {
		if err := conversation.AdmitPrompt(taskID, prompt); err != nil {
			w.releasePromptLease(requestID)
			return fmt.Errorf("Go provider conversation denied prompt admission: %w", err)
		}
	} else if !w.development {
		w.releasePromptLease(requestID)
		return errors.New("production Pi prompt has no Go-owned conversation admission")
	}
	stopCancel := context.AfterFunc(lease.Context(), func() {
		_ = w.rpc.Abort(requestID)
	})
	w.mu.Lock()
	if active := w.leases[requestID]; active != nil {
		active.stop = stopCancel
	}
	w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		w.releasePromptLease(requestID)
		return err
	}
	if err := w.rpc.SendPrompt(requestID, prompt); err != nil {
		if conversation != nil {
			conversation.Close()
		}
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()
		w.releasePromptLease(requestID)
		return fmt.Errorf("write admitted Pi prompt control: %w", err)
	}
	return nil
}

func (w *PiCLIWorker) releasePromptLease(requestID string) {
	w.mu.Lock()
	active := w.leases[requestID]
	delete(w.leases, requestID)
	w.mu.Unlock()
	if active == nil {
		return
	}
	if active.stop != nil {
		active.stop()
	}
	active.lease.Release()
}

func (w *PiCLIWorker) pumpEvents() {
	defer close(w.readerDone)
	defer close(w.events)
	combined := make(chan PiWorkerEvent, PiWorkerEventQueue)
	readerDone := make(chan struct{}, 2)
	go func() {
		defer func() { readerDone <- struct{}{} }()
		for {
			event, err := w.Channels.NextEvent(context.Background())
			if err != nil {
				if !errors.Is(err, io.EOF) {
					w.mu.Lock()
					w.eventsErr = errors.Join(w.eventsErr, err)
					w.mu.Unlock()
				}
				return
			}
			combined <- event
		}
	}()
	go func() {
		defer func() { readerDone <- struct{}{} }()
		if w.rpc == nil {
			return
		}
		for {
			record, err := w.rpc.ReadRecord()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					w.mu.Lock()
					w.eventsErr = errors.Join(w.eventsErr, err)
					w.mu.Unlock()
				}
				return
			}
			switch record.Type {
			case "agent_settled":
				w.mu.Lock()
				var requestID string
				for id := range w.leases {
					requestID = id
					break
				}
				w.mu.Unlock()
				if requestID != "" {
					combined <- PiWorkerEvent{Type: "turn_end", RequestID: requestID}
				}
			case "response":
				if record.Command == "prompt" && record.Success != nil && !*record.Success {
					w.mu.Lock()
					active := w.leases[record.ID] != nil
					w.mu.Unlock()
					if active {
						combined <- PiWorkerEvent{Type: "worker_error", RequestID: record.ID, Code: "rpc_command_failed"}
					}
				}
			}
		}
	}()
	forwardEvent := func(event PiWorkerEvent) {
		if event.Type == "turn_end" || event.Type == "worker_error" {
			if event.RequestID != "" {
				w.releasePromptLease(event.RequestID)
			}
		}
		select {
		case w.events <- event:
		default:
			w.mu.Lock()
			w.eventsDrop++
			encoded, _ := json.Marshal(event)
			w.eventsBytes = saturatingAdd(w.eventsBytes, uint64(len(encoded)))
			w.mu.Unlock()
		}
	}
	for readers := 0; readers < 2; {
		select {
		case <-readerDone:
			readers++
		case event := <-combined:
			forwardEvent(event)
		}
	}
	// Each reader signals completion after its final send. The merged queue is
	// stable here, so drain it before closing events and exposing EOF.
	close(combined)
	for event := range combined {
		forwardEvent(event)
	}
}

func (w *PiCLIWorker) NextEvent(ctx context.Context) (PiWorkerEvent, error) {
	if w == nil || ctx == nil {
		return PiWorkerEvent{}, errors.New("Pi worker event channel is unavailable")
	}
	// Cancellation stops only this wait; it does not cancel the worker's shared
	// event pump or consume an event that is already buffered.
	if err := ctx.Err(); err != nil {
		return PiWorkerEvent{}, err
	}
	select {
	case event, ok := <-w.events:
		if ok {
			return event, nil
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.eventsDrop > 0 && !w.gapReturned {
			w.gapReturned = true
			return PiWorkerEvent{Type: "observation_gap", DroppedEvents: w.eventsDrop, DroppedBytes: w.eventsBytes}, nil
		}
		if w.eventsErr != nil && !w.eventsErrReturned {
			w.eventsErrReturned = true
			return PiWorkerEvent{}, w.eventsErr
		}
		return PiWorkerEvent{}, io.EOF
	case <-ctx.Done():
		return PiWorkerEvent{}, ctx.Err()
	}
}

func (w *PiCLIWorker) waitForOutputDrain(ctx context.Context) error {
	if w.readerDone == nil {
		return errors.New("Pi worker output pump is not configured")
	}
	select {
	case <-w.readerDone:
		return nil
	default:
	}
	select {
	case <-w.readerDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *PiCLIWorker) closeOwnedStdout() error {
	w.stdoutCloseMu.Lock()
	defer w.stdoutCloseMu.Unlock()
	if w.stdoutCloser == nil || w.stdoutClosed {
		return w.stdoutCloseErr
	}
	err := w.stdoutCloser.Close()
	w.stdoutCloseErr = errors.Join(w.stdoutCloseErr, err)
	if err == nil {
		w.stdoutClosed = true
	}
	return w.stdoutCloseErr
}

// Close cancels/drains active prompt leases, kills the direct worker if needed,
// and returns only the host scope's measured settlement. Missing or unavailable
// descendant proof remains UNKNOWN.
func (w *PiCLIWorker) Close(ctx context.Context) (Settlement, error) {
	if w == nil || w.Session == nil || ctx == nil {
		return Settlement{State: SettlementUnknown, Detail: "Pi worker session is not configured"}, ErrUnknownSettlement
	}
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	var closeErr error
	if w.rpc != nil {
		closeErr = w.rpc.CloseInput()
	}
	settlement, err := w.Session.Close(ctx)
	if w.Channels != nil && settlement.State == SettlementStopped && settlement.ExitObserved && settlement.DescendantsSettled && err == nil {
		if drainErr := w.waitForOutputDrain(ctx); drainErr != nil {
			return settlement, errors.Join(closeErr, err,
				fmt.Errorf("wait for Pi worker RPC/event output drain: %w", drainErr))
		}
		closeErr = errors.Join(closeErr, w.closeOwnedStdout())
		closeErr = errors.Join(closeErr, w.Channels.Close())
	}
	return settlement, errors.Join(closeErr, err)
}

type PiWorkerLaunchError struct {
	Cause      error
	Settlement Settlement
	CleanupErr error
	// RouteProbe is set only on a launch from a route-probe template (ErrD09RouteProbeLaunched).
	RouteProbe *D09RouteProbeLaunch

	// processOwner keeps an unsettled launch's stdout reader reachable. It is
	// released only with this error/owner; UNKNOWN cleanup never closes it.
	processOwner *Process
}

func (e *PiWorkerLaunchError) Error() string {
	return fmt.Sprintf("launch governed Pi worker: %v (settlement=%s)", e.Cause, e.Settlement.State)
}
func (e *PiWorkerLaunchError) Unwrap() error { return e.Cause }

func piLaunchSettlementComplete(err *PiWorkerLaunchError) bool {
	return err != nil && err.Settlement.State == SettlementStopped &&
		err.Settlement.ExitObserved && err.Settlement.DescendantsSettled &&
		err.CleanupErr == nil
}

func containsIncompletePiLaunchError(err error) bool {
	if err == nil {
		return false
	}
	switch current := err.(type) {
	case *PiWorkerLaunchError:
		if current == nil {
			return false
		}
		if !piLaunchSettlementComplete(current) {
			return true
		}
		return containsIncompletePiLaunchError(current.Cause)
	case interface{ Unwrap() []error }:
		for _, nested := range current.Unwrap() {
			if containsIncompletePiLaunchError(nested) {
				return true
			}
		}
	case interface{ Unwrap() error }:
		return containsIncompletePiLaunchError(current.Unwrap())
	}
	return false
}

// An UNKNOWN inside a complete typed launch error is resolved by that error's
// exact settlement. Separate joined branches remain independently unresolved.
func hasUnresolvedLaunchUnknown(err error, suppressCauseUnknown bool) bool {
	if err == nil {
		return false
	}
	switch current := err.(type) {
	case *PiWorkerLaunchError:
		if current == nil {
			return false
		}
		if piLaunchSettlementComplete(current) {
			return hasUnresolvedLaunchUnknown(current.Cause, true)
		}
		return false // Incomplete typed settlements are handled above.
	case interface{ Unwrap() []error }:
		for _, nested := range current.Unwrap() {
			if hasUnresolvedLaunchUnknown(nested, false) {
				return true
			}
		}
	case interface{ Unwrap() error }:
		return hasUnresolvedLaunchUnknown(current.Unwrap(), suppressCauseUnknown)
	default:
		return !suppressCauseUnknown && errors.Is(err, ErrUnknownSettlement)
	}
	return false
}

func retainD09LaunchOwners(err error) bool {
	return containsIncompletePiLaunchError(err) || hasUnresolvedLaunchUnknown(err, false)
}

func boundedStartupContext(parent context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) < piWorkerStartupTimeout {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, piWorkerStartupTimeout)
}

func validateReadyEvent(event PiWorkerEvent, profile HostProfile, bootstrap PiWorkerBootstrap) error {
	if event.SchemaVersion != PiWorkerSchemaVersion || event.ProfileID != profile.ID || event.ProfileDigest != profile.Digest ||
		event.SourceBindingDigest != bootstrap.SourceBindingDigest || event.SourceProvenanceDigest != bootstrap.SourceProvenanceDigest ||
		event.SourceGenerationID != bootstrap.SourceGenerationID || event.SourceTreeDigest != bootstrap.SourceTreeDigest ||
		event.SourceManifestDigest != bootstrap.SourceManifestDigest || event.WorkerViewIdentity != bootstrap.WorkerViewIdentity ||
		event.WorkerMountID != bootstrap.WorkerMountID ||
		event.RuntimeBundleDigest != profile.PiRuntimeBundleDigest ||
		event.DescriptorProfile != profile.DescriptorProfile || event.DescriptorDigest != profile.DescriptorProfileDigest ||
		event.NodeVersion != profile.NodeVersion || event.PiVersion != "0.87.1" ||
		event.ProviderID != PiWorkerProviderID || event.ModelID != PiWorkerModelID ||
		strings.Join(event.Tools, "\x00") != "read\x00write\x00edit\x00bash" || !equalToolSchemas(event.ToolSchemaSHA256, piWorkerToolSchemaDigests()) {
		return fmt.Errorf("Pi SDK worker ready receipt differs from the admitted fixed profile (schema=%t profile=%t source_binding=%t source_provenance=%t bundle=%t descriptor=%t node=%t pi=%t provider=%t model=%t tools=%t schemas=%t)",
			event.SchemaVersion == PiWorkerSchemaVersion,
			event.ProfileID == profile.ID && event.ProfileDigest == profile.Digest,
			event.SourceBindingDigest == bootstrap.SourceBindingDigest,
			event.SourceProvenanceDigest == bootstrap.SourceProvenanceDigest,
			event.RuntimeBundleDigest == profile.PiRuntimeBundleDigest,
			event.DescriptorProfile == profile.DescriptorProfile && event.DescriptorDigest == profile.DescriptorProfileDigest,
			event.NodeVersion == profile.NodeVersion,
			event.PiVersion == "0.87.1",
			event.ProviderID == PiWorkerProviderID,
			event.ModelID == PiWorkerModelID,
			strings.Join(event.Tools, "\x00") == "read\x00write\x00edit\x00bash",
			equalToolSchemas(event.ToolSchemaSHA256, piWorkerToolSchemaDigests()))
	}
	return nil
}

func stopFailedPiLaunch(process *Process, channels *PiWorkerChannels, cause error) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), piWorkerStopTimeout)
	defer cancel()
	settlement, cleanupErr := process.Stop(cleanupCtx)
	var closeErr error
	if settlement.State == SettlementStopped && settlement.ExitObserved && settlement.DescendantsSettled && cleanupErr == nil {
		if closer, ok := process.stdout.(io.Closer); ok {
			closeErr = errors.Join(closeErr, closer.Close())
		}
		closeErr = errors.Join(closeErr, channels.Close())
	}
	result := &PiWorkerLaunchError{
		Cause: cause, Settlement: settlement, CleanupErr: errors.Join(cleanupErr, closeErr),
	}
	if settlement.State != SettlementStopped || !settlement.ExitObserved ||
		!settlement.DescendantsSettled || result.CleanupErr != nil {
		result.processOwner = process
	}
	return result
}

type developmentUnknownSettler struct{}

func (developmentUnknownSettler) Settle(context.Context) (bool, error) {
	return false, errors.New("development launch has no admitted descendant settlement profile")
}

type discardObservationSink struct{}

func (discardObservationSink) Observe(context.Context, Observation) error { return nil }
