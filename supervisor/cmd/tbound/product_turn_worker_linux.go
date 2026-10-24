//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
	"tbound/supervisor/internal/providerbridge"
)

// errProductWorkerStepBound marks a turn whose provider did not give a final answer within the
// worker's step bound.
var errProductWorkerStepBound = errors.New("product turn worker reached its step bound before a final answer")

// productTurnWorker is the in-process deterministic headless worker of one turn. It drives the
// governed conversation exactly as the Pi adapter would: each provider tool call becomes a proposal
// with the same tool call ID, name and arguments, which is correlated, gated, durably decided and,
// when allowed, executed through the durable executor.
type productTurnWorker struct {
	instance string
	maxSteps int
}

// productTurnWork is what the worker observed: the tools it proposed in order, the denials released
// to the provider, and whether the transcript ended at a final provider answer.
type productTurnWork struct {
	toolSequence []string
	denials      []productTurnDenial
	finalAnswer  bool
}

func newProductTurnWorker(maxSteps int) (*productTurnWorker, error) {
	if maxSteps < 1 {
		return nil, errors.New("product turn worker needs a positive step bound")
	}
	instance, err := randomProductIdentity("pworker-")
	if err != nil {
		return nil, err
	}
	return &productTurnWorker{instance: instance, maxSteps: maxSteps}, nil
}

func (w *productTurnWorker) run(ctx context.Context, conversation *providerbridge.Conversation, durable *DurableExecutor,
	policy gate.Policy) (productTurnWork, error) {
	work := productTurnWork{toolSequence: []string{}, denials: []productTurnDenial{}}
	executor, err := providerbridge.NewExecutor(conversation, durable)
	if err != nil {
		return work, err
	}
	for step := 0; step < w.maxSteps; step++ {
		turn, err := conversation.NextTurn(ctx)
		if err != nil {
			return work, err
		}
		if turn.ToolCall == nil {
			work.finalAnswer = true
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
		if err := conversation.RecordDecision(ctx, proposal, decision); err != nil {
			return work, err
		}
		work.toolSequence = append(work.toolSequence, proposal.Tool)
		if decision.Verdict != gate.Allow {
			work.denials = append(work.denials, productTurnDenial{ToolCallID: proposal.ToolCallID, Tool: proposal.Tool,
				ReasonCode: decision.ReasonCode})
			continue
		}
		if _, err := executor.Execute(ctx, proposal, decision); err != nil {
			return work, err
		}
	}
	return work, errProductWorkerStepBound
}
