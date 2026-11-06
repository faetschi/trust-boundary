//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/productdriver"
	"tbound/supervisor/internal/productsession"
	"tbound/supervisor/internal/providerbridge"
)

// errProductWorkerStepBound marks a turn whose provider did not give a final answer within the
// worker's step bound.
var errProductWorkerStepBound = errors.New("product turn worker reached its step bound before a final answer")

// productTurnWorker is the in-process supervisor-loop worker of one turn. It is the supervisor's own agent
// loop, and only its provider conversation is scripted in tests. It drives the governed conversation
// exactly as the Pi adapter would: each provider tool call becomes a proposal with the same tool call ID,
// name and arguments, which is correlated, gated, durably decided and, when allowed, executed through the
// durable executor.
type productTurnWorker struct {
	instance string
	maxSteps int
	// cancelled reports a durable cancel request; the worker reads it before every provider exchange
	// and never interrupts an exchange in flight.
	cancelled func() bool
	// boundary is the driver's crash seam after an allowed effect settled.
	boundary func(point string)
}

func newProductTurnWorker(maxSteps int, cancelled func() bool, boundary func(string)) (*productTurnWorker, error) {
	if maxSteps < 1 {
		return nil, errors.New("product turn worker needs a positive step bound")
	}
	instance, err := randomProductIdentity("pworker-")
	if err != nil {
		return nil, err
	}
	return &productTurnWorker{instance: instance, maxSteps: maxSteps, cancelled: cancelled, boundary: boundary}, nil
}

func (w *productTurnWorker) run(ctx context.Context, conversation *providerbridge.Conversation, durable *DurableExecutor,
	policy gate.Policy) (productdriver.Work, error) {
	work := productdriver.Work{ToolSequence: []string{}, Denials: []productsession.ReceiptDenial{}}
	executor, err := providerbridge.NewExecutor(conversation, durable)
	if err != nil {
		return work, err
	}
	for step := 0; step < w.maxSteps; step++ {
		if w.cancelled() {
			return work, productdriver.ErrTurnCancelled
		}
		turn, err := conversation.NextTurn(ctx)
		if err != nil {
			return work, err
		}
		if turn.ToolCall == nil {
			work.FinalAnswer = true
			return work, nil
		}
		proposal := protocol.Proposal{SchemaVersion: protocol.ProposalSchemaVersion, ToolCallID: turn.ToolCall.ID,
			Tool: turn.ToolCall.Name, Arguments: append(json.RawMessage(nil), turn.ToolCall.Arguments...)}
		matched, err := conversation.Correlate(ctx, proposal)
		if err != nil {
			return work, err
		}
		if !matched.Accepted {
			return work, fmt.Errorf("provider call %s did not correlate: %s", proposal.ToolCallID, matched.ReasonCode)
		}
		decision := gate.Evaluate(proposal, matched, policy)
		if decision.Verdict == gate.Allow && proposal.Tool != "bash" {
			refusal, err := durable.productPreflight(proposal)
			if err != nil {
				return work, err
			}
			if refusal != nil {
				if err := conversation.RecordSoftRefusal(ctx, proposal, refusal.ArgumentDigest, refusal.Code, refusal.Hint, w.boundary); err != nil {
					return work, err
				}
				work.ToolSequence = append(work.ToolSequence, proposal.Tool)
				continue
			}
		}
		if err := conversation.RecordDecision(ctx, proposal, decision); err != nil {
			return work, err
		}
		work.ToolSequence = append(work.ToolSequence, proposal.Tool)
		if decision.Verdict != gate.Allow {
			work.Denials = append(work.Denials, productsession.ReceiptDenial{ToolCallID: proposal.ToolCallID, Tool: proposal.Tool,
				ReasonCode: decision.ReasonCode})
			continue
		}
		if _, err := executor.Execute(ctx, proposal, decision); err != nil {
			// Delta admission refused the settled change: the conversation recorded the DENY and its
			// hint for the provider, nothing was promoted, and the turn goes on.
			var denied *protocol.AdmissionDeniedError
			if !errors.As(err, &denied) {
				return work, err
			}
			work.Denials = append(work.Denials, productsession.ReceiptDenial{ToolCallID: proposal.ToolCallID, Tool: proposal.Tool,
				ReasonCode: denied.ReasonCode})
			continue
		}
		w.boundary("after-effect")
	}
	return work, errProductWorkerStepBound
}
