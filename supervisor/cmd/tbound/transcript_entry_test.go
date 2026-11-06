package main

import (
	"tbound/supervisor/internal/broker/correlation"
	"tbound/supervisor/internal/broker/protocol"
	"tbound/supervisor/internal/gate"
)

// transcriptEntry decodes one transcript line in the smoke/integration tests.
// The shared runtime owns the writer type.
type transcriptEntry struct {
	Proposal    protocol.Proposal    `json:"proposal"`
	Correlation correlation.Decision `json:"correlation"`
	Decision    gate.Decision        `json:"decision"`
	Result      protocol.Result      `json:"result"`
}
