package workflowrecovery

import (
	"strings"
	"testing"
)

func retryReadyCheckpoint() Checkpoint {
	digest := strings.Repeat("a", 64)
	return Checkpoint{Pins: Pins{
		ProviderDisposition: DispositionAwaitingProviderExchange, PendingCallDisposition: PendingCallNone,
		ProviderTranscripts: []TranscriptPin{
			{Ordinal: 1, ConversationID: "old", Disposition: DispositionTerminal},
			{Ordinal: 2, ConversationID: "active", Disposition: DispositionAwaitingProviderExchange},
		},
		HostCallReservation: &HostCallReservationPin{Binding: HostCallBinding{CallID: "call-1"},
			Disposition: HostCallTerminal, OutcomeDigest: digest, RetryableFailure: true},
	}}
}

// After a failed, retryable provider attempt was journaled and settled, the next attempt must be
// able to reserve its own host call from the awaiting-exchange checkpoint.
func TestRetryableFailureCheckpointAdmitsTheNextReservation(t *testing.T) {
	if !isRetryableFailureProviderExchangeReady(retryReadyCheckpoint()) {
		t.Fatal("the settled failed attempt did not admit another reservation")
	}
	withTerminalEffect := retryReadyCheckpoint()
	withTerminalEffect.Pins.PendingCallDisposition = PendingCallTerminal
	withTerminalEffect.Pins.EffectReservation = &EffectReservationPin{Disposition: EffectReservationTerminal}
	if !isRetryableFailureProviderExchangeReady(withTerminalEffect) {
		t.Fatal("a failed attempt after an earlier settled effect did not admit another reservation")
	}
	for name, mutate := range map[string]func(*Pins){
		"failure not marked retryable": func(p *Pins) { p.HostCallReservation.RetryableFailure = false },
		"host call still reserved":     func(p *Pins) { p.HostCallReservation.Disposition = HostCallReserved },
		"host call unknown":            func(p *Pins) { p.HostCallReservation.Disposition = HostCallUnknown },
		"outcome digest missing":       func(p *Pins) { p.HostCallReservation.OutcomeDigest = "" },
		"call captured":                func(p *Pins) { p.HostCallReservation.CapturedCall = &CapturedCallPin{} },
		"host call retired by digest":  func(p *Pins) { p.HostCallReservation.RetirementDigest = strings.Repeat("b", 64) },
		"no host call":                 func(p *Pins) { p.HostCallReservation = nil },
		"effect reserved":              func(p *Pins) { p.EffectReservation = &EffectReservationPin{Disposition: EffectReservationReserved} },
		"effect unknown":               func(p *Pins) { p.EffectReservation = &EffectReservationPin{Disposition: EffectReservationUnknown} },
		"awaiting gate decision":       func(p *Pins) { p.ProviderDisposition = DispositionAwaitingGateDecision },
		"pending result":               func(p *Pins) { p.ProviderDisposition = DispositionPendingProviderResult },
		"awaiting host prompt":         func(p *Pins) { p.ProviderDisposition = DispositionAwaitingHostPrompt },
		"pending call captured":        func(p *Pins) { p.PendingCallDisposition = PendingCallCaptured },
		"pending call in flight":       func(p *Pins) { p.PendingCallDisposition = PendingCallInFlight },
		"active transcript not exch.":  func(p *Pins) { p.ProviderTranscripts[1].Disposition = DispositionAwaitingGateDecision },
		"older transcript unsettled":   func(p *Pins) { p.ProviderTranscripts[0].Disposition = DispositionAwaitingProviderExchange },
		"no transcript":                func(p *Pins) { p.ProviderTranscripts = nil },
	} {
		checkpoint := retryReadyCheckpoint()
		checkpoint.Pins.ProviderTranscripts = append([]TranscriptPin(nil), checkpoint.Pins.ProviderTranscripts...)
		hostCall := *checkpoint.Pins.HostCallReservation
		checkpoint.Pins.HostCallReservation = &hostCall
		mutate(&checkpoint.Pins)
		if isRetryableFailureProviderExchangeReady(checkpoint) {
			t.Errorf("%s: another reservation was admitted", name)
		}
	}
}
