//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/sessionrepo"
	"tbound/supervisor/internal/workflowrecovery"
)

// openInterruptedProductBash opens cleanup evidence only, never a usable Store. Abandonment
// discards the private candidate; a drained owner does not certify a command result.
func openInterruptedProductBash(ctx context.Context, paths productTurnPaths, registration string, options sessionrepo.Options) (_ *retainedProductTurn, ownerHead string, err error) {
	r := &retainedProductTurn{}
	defer func() {
		if err != nil {
			err = errors.Join(err, r.Close())
		}
	}()
	for _, input := range []struct {
		path    string
		journal **audit.Journal
	}{
		{paths.StoreJournalPath, &r.StoreJournal}, {paths.ProviderJournalPath, &r.ProviderJournal}, {paths.CommandJournalPath, &r.CommandJournal},
	} {
		*input.journal, err = openExistingRecoveryJournal(input.path)
		if err != nil {
			return nil, "", err
		}
		r.closers = append(r.closers, (*input.journal).Close)
	}
	owner, err := openExistingRecoveryJournal(filepath.Join(filepath.Dir(paths.StoreJournalPath), productCommandOwnerJournal))
	if err != nil {
		return nil, "", err
	}
	r.closers = append(r.closers, owner.Close)
	trace, err := owner.Trace()
	if err != nil || len(trace.Records) < 2 || trace.Records[len(trace.Records)-1].Event.Kind != "product_command_scope_drained" {
		return nil, "", errors.Join(errors.New("interrupted Bash scope has no durable drain evidence"), err)
	}
	// Reuse the owner protocol validator; the terminal drain makes this read-only.
	if err := reconcileProductCommandScope(ctx, nil, owner, map[string]struct{}{}, nil); err != nil {
		return nil, "", err
	}
	var scope productD09ScopeOwner
	if json.Unmarshal(trace.Records[0].Event.Data, &scope) != nil || scope.WorkflowID != paths.Link.WorkflowID || scope.RunID != paths.Link.RunID || scope.RegistrationDigest != registration {
		return nil, "", errors.New("interrupted Bash owner differs from the catalog")
	}
	commands := map[string]piruntime.D09ProductCommandOwnership{}
	for _, record := range trace.Records {
		if record.Event.Kind == "product_command_intent" {
			var command piruntime.D09ProductCommandOwnership
			if json.Unmarshal(record.Event.Data, &command) != nil {
				return nil, "", errors.New("invalid command owner")
			}
			if _, duplicate := commands[command.LeaseID]; duplicate {
				return nil, "", errors.New("command owner repeats a lease")
			}
			commands[command.LeaseID] = command
		}
	}
	storeTrace, err := r.StoreJournal.Trace()
	if err != nil {
		return nil, "", err
	}
	// Reuse authorization validation, but grant no command settlement authority: this candidate is discarded.
	historical, err := newGovernedHistoricalStoreVerifier(r.StoreJournal, options.PolicyDigest, options.MetadataPolicyDigest,
		func(sessionrepo.CommandSettlement, string, string) error { return errHistoricalBashContext })
	if err != nil {
		return nil, "", err
	}
	interrupted := 0
	for _, effect := range storeTrace.Effects {
		if !effect.Unresolved && effect.Outcome != "success" {
			return nil, "", errors.New("Store evidence has a failed effect")
		}
		if !strings.HasPrefix(effect.ID, "sessionrepo-effect-") {
			return nil, "", errors.New("Store evidence has a foreign effect")
		}
		var seed struct {
			Action string `json:"action"`
		}
		if json.Unmarshal(effect.Intent, &seed) == nil && seed.Action == "seed" && !effect.Unresolved {
			continue
		}
		var intent governedReplayOperationIntent
		if decodeCanonicalGoverned(effect.Intent, &intent) != nil || intent.SchemaVersion != sessionrepo.OperationIntentSchemaVersion || intent.Request.EffectID != effect.ID {
			return nil, "", errors.New("invalid retained Store operation intent")
		}
		if err := historical.verifyOperationAuthorization(intent.Request); err != nil {
			return nil, "", err
		}
		record := historical.byEffect[effect.ID]
		var auth governedOperationAdmissionRecord
		if decodeCanonicalGoverned(record.Event.Data, &auth) != nil || record.Sequence >= effect.IntentSequence ||
			auth.WorkflowID != paths.Link.WorkflowID || auth.ConversationID != paths.Link.ConversationID {
			return nil, "", errors.New("interrupted Bash authorization differs")
		}
		if !effect.Unresolved {
			continue
		}
		if intent.Request.Tool != "bash" {
			return nil, "", errors.New("unresolved effect is not an interrupted Bash command")
		}
		// Store.RunEffect syncs first; the launcher persists ownership before command.Start.
		// With no intent for this lease no command started; the validated drain covers every recorded command.
		if command, exists := commands[intent.Request.Operation.LeaseID]; exists && command.ViewID != intent.Request.ViewID {
			return nil, "", errors.New("interrupted Bash owner differs from its effect view")
		}
		interrupted++
	}
	if interrupted == 0 {
		return nil, "", errors.New("no interrupted Bash effect to abandon")
	}
	return r, "sha256:" + auditHeadHash(trace.Records), nil
}

// After openInterruptedProductBash proves the drain, only its exact interrupted reservation is inert.
// This discards the candidate with a full charge; it never settles or retires the workflow ledger.
func productDrainedBashReservationInert(trace audit.Trace, pins workflowrecovery.Pins) error {
	reservation := pins.EffectReservation
	if reservation == nil || reservation.Disposition == workflowrecovery.EffectReservationTerminal || reservation.Disposition == workflowrecovery.EffectReservationRetired {
		return nil
	}
	if reservation.Disposition != workflowrecovery.EffectReservationReserved && reservation.Disposition != workflowrecovery.EffectReservationUnknown {
		return errors.New("invalid interrupted effect reservation")
	}
	host := pins.HostCallReservation
	if host == nil || host.CapturedCall == nil || host.CapturedCall.Disposition != workflowrecovery.CapturedCallConsumed || host.CapturedCall.WorkflowID != pins.WorkflowID {
		return errors.New("interrupted effect reservation has no consumed provider call")
	}
	captured := host.CapturedCall
	for _, effect := range trace.Effects {
		var intent governedReplayOperationIntent
		if !effect.Unresolved || decodeCanonicalGoverned(effect.Intent, &intent) != nil || intent.SchemaVersion != sessionrepo.OperationIntentSchemaVersion || intent.Request.Tool != "bash" || intent.Request.EffectID != effect.ID {
			continue
		}
		expected := workflowrecovery.EffectBinding{OperationID: effect.ID, SpecDigest: bareSHA256Digest(intent.Request.ArgumentDigest),
			WorkflowID: pins.WorkflowID, ConversationID: captured.ConversationID, HostCallID: captured.HostCallID, ToolCallID: captured.ToolCallID,
			HostCallOutcomeDigest: captured.OutcomeDigest, RecoveryGeneration: captured.RecoveryGeneration, OwnerEpoch: captured.OwnerEpoch}
		if reservation.Binding == expected {
			return nil
		}
	}
	return errors.New("effect reservation is not the drained interrupted Bash effect")
}
