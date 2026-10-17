package researchroute

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"tbound/supervisor/internal/broker/protocol"
)

const (
	ledgerConfigSchema   = 1
	maxLedgerConfigBytes = 4 << 10

	// ReserveBasisRequest reserves the worst case of one request.
	ReserveBasisRequest = "request"
	// ReserveBasisUnit reserves the worst case of a whole unit (the per-request
	// worst case times max_requests_per_unit) for every request.
	ReserveBasisUnit = "unit"
)

// ErrLedgerConfig reports a research ledger configuration that is malformed or
// outside its bounds.
var ErrLedgerConfig = errors.New("research ledger configuration is invalid")

// LedgerConfig is the owner-decided accounting input of the research ledger. It
// is a separate root-owned file so that neither the profile schema nor the code
// bakes in a cap, a stop rule or an accounting basis. Every member is required.
type LedgerConfig struct {
	SchemaVersion uint64 `json:"schema_version"`
	// LedgerID names the spend domain. It is written to the ledger genesis record.
	LedgerID string `json:"ledger_id"`
	// CapMicroUSD is the hard total cap in integer micro-USD (US$5 is 5000000).
	// It is written to the genesis record, and a ledger created with another cap
	// is refused.
	CapMicroUSD int64 `json:"cap_micro_usd"`
	// MinPerUnitWorstCaseMicroUSD is the stop-rule threshold: no request starts
	// while the remaining cap is below it. It is not stored in the ledger file.
	MinPerUnitWorstCaseMicroUSD int64 `json:"min_per_unit_worst_case_micro_usd"`
	// ReserveBasis is "request" or "unit": whether each provider request reserves
	// the worst case of one request or of a whole unit. Either way, each request
	// is its own ledger unit and is settled to its actual cost.
	ReserveBasis string `json:"reserve_basis"`
}

// LoadLedgerConfig strictly decodes a ledger configuration: unknown, duplicate
// and missing members, trailing data, fractional numbers and documents over 4 KiB
// are refused.
func LoadLedgerConfig(r io.Reader) (LedgerConfig, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxLedgerConfigBytes+1))
	if err != nil {
		return LedgerConfig{}, fmt.Errorf("%w: read: %w", ErrLedgerConfig, err)
	}
	if len(data) == 0 || len(data) > maxLedgerConfigBytes {
		return LedgerConfig{}, fmt.Errorf("%w: size", ErrLedgerConfig)
	}
	if err := protocol.ValidateStrictJSON(data); err != nil {
		return LedgerConfig{}, fmt.Errorf("%w: %w", ErrLedgerConfig, err)
	}
	var raw struct {
		SchemaVersion *uint64 `json:"schema_version"`
		LedgerID      *string `json:"ledger_id"`
		Cap           *int64  `json:"cap_micro_usd"`
		MinPerUnit    *int64  `json:"min_per_unit_worst_case_micro_usd"`
		Basis         *string `json:"reserve_basis"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return LedgerConfig{}, fmt.Errorf("%w: %w", ErrLedgerConfig, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return LedgerConfig{}, fmt.Errorf("%w: trailing data", ErrLedgerConfig)
	}
	if raw.SchemaVersion == nil || raw.LedgerID == nil || raw.Cap == nil || raw.MinPerUnit == nil || raw.Basis == nil {
		return LedgerConfig{}, fmt.Errorf("%w: every member is required", ErrLedgerConfig)
	}
	config := LedgerConfig{SchemaVersion: *raw.SchemaVersion, LedgerID: *raw.LedgerID, CapMicroUSD: *raw.Cap,
		MinPerUnitWorstCaseMicroUSD: *raw.MinPerUnit, ReserveBasis: *raw.Basis}
	if err := config.validate(); err != nil {
		return LedgerConfig{}, err
	}
	return config, nil
}

func (c LedgerConfig) validate() error {
	switch {
	case c.SchemaVersion != ledgerConfigSchema:
		return fmt.Errorf("%w: schema_version must be %d", ErrLedgerConfig, ledgerConfigSchema)
	case !validLedgerID(c.LedgerID):
		return fmt.Errorf("%w: ledger_id must be 1 to 128 of letters, digits and ._-:/+", ErrLedgerConfig)
	case c.CapMicroUSD <= 0:
		return fmt.Errorf("%w: cap_micro_usd must be positive", ErrLedgerConfig)
	case c.MinPerUnitWorstCaseMicroUSD <= 0 || c.MinPerUnitWorstCaseMicroUSD > c.CapMicroUSD:
		return fmt.Errorf("%w: min_per_unit_worst_case_micro_usd must be in (0, cap]", ErrLedgerConfig)
	case c.ReserveBasis != ReserveBasisRequest && c.ReserveBasis != ReserveBasisUnit:
		return fmt.Errorf("%w: reserve_basis must be %q or %q", ErrLedgerConfig, ReserveBasisRequest, ReserveBasisUnit)
	}
	return nil
}

func validLedgerID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("._-:/+", c) >= 0) {
			return false
		}
	}
	return true
}
