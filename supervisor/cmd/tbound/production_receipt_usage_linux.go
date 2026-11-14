//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
)

// applyProviderLineage fills the receipt's provider turns, tool sequence, retries and token usage
// from the run's provider journal (the provider audit).
//
// InTokens and OutTokens are the sum of prompt_tokens and completion_tokens over the run's
// provider_exchange records: the usage object of each completed exchange, captured again from the
// journaled response bytes with the session's route. It is the same object the research route's USD
// ledger settles (providerbridge SettleSpend), never a model self-report. completion_tokens already
// includes any reasoning tokens, so those are not added again. Not counted:
//   - a refused or resampled attempt (provider_exchange_retryable_failure, decision 13 / D54) or a
//     pre-effect retry: it is in ProviderRetries, and the ledger keeps its reservation uncertain
//     instead of settling it, so its usage is not established;
//   - a provider_exchange_unknown: such a request ends UNKNOWN, and an UNKNOWN answer carries no
//     receipt at all.
//
// A scripted exchange has no usage object and adds nothing.
func applyProviderLineage(receipt *productionPiReceipt, trace audit.Trace, route openrouter.Route) error {
	var errs []error
	for _, record := range trace.Records {
		if record.Event.Kind == "provider_exchange_retryable_failure" {
			receipt.ProviderRetries++
			continue
		}
		if record.Event.Kind != "provider_exchange" {
			continue
		}
		var exchange providerExchangeEvidence
		if err := unmarshalGovernedJSON(record.Event.Data, &exchange); err != nil {
			errs = append(errs, fmt.Errorf("decode production provider lineage: %w", err))
			continue
		}
		receipt.ProviderTurns++
		if exchange.ToolCall != nil && len(receipt.ToolSequence) < 32 {
			receipt.ToolSequence = append(receipt.ToolSequence, exchange.ToolCall.Name)
		}
		in, out, err := exchangeTokens(exchange.Response, route)
		if err != nil {
			errs = append(errs, fmt.Errorf("read the provider usage of record %d: %w", record.Sequence, err))
			continue
		}
		receipt.InTokens += in
		receipt.OutTokens += out
	}
	return errors.Join(errs...)
}

// exchangeTokens reads the token counts of one completed exchange's response.
func exchangeTokens(response []byte, route openrouter.Route) (in, out int, err error) {
	captured, err := openrouter.CaptureSSEForRoute(bytes.NewReader(response), route)
	if err != nil {
		return 0, 0, err
	}
	usage := captured.Usage()
	if len(usage) == 0 {
		return 0, 0, nil
	}
	var counts struct {
		Prompt     *uint32 `json:"prompt_tokens"`
		Completion *uint32 `json:"completion_tokens"`
	}
	if err := json.Unmarshal(usage, &counts); err != nil {
		return 0, 0, fmt.Errorf("usage token counts: %w", err)
	}
	if counts.Prompt == nil || counts.Completion == nil || *counts.Prompt > math.MaxInt32 || *counts.Completion > math.MaxInt32 {
		return 0, 0, errors.New("usage has no bounded prompt_tokens and completion_tokens")
	}
	return int(*counts.Prompt), int(*counts.Completion), nil
}
