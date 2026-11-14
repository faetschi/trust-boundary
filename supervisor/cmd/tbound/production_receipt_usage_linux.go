//go:build linux

package main

import (
	"errors"
	"fmt"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
)

// applyProviderLineage fills the receipt's provider turns, tool sequence and retries from the run's
// provider journal.
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
	}
	return errors.Join(errs...)
}
