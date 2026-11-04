package researchroute

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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

// Digest is the lowercase hex SHA-256 of the canonical encoding of the validated
// configuration (fixed member order, no insignificant whitespace), so file
// formatting and key order do not change it and any value change does.
func (c LedgerConfig) Digest() (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("%w: encode: %w", ErrLedgerConfig, err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

const maxCheckpointBytes = 1 << 10

// ErrCheckpoint reports a missing or malformed research ledger checkpoint.
var ErrCheckpoint = errors.New("research ledger checkpoint is invalid")

// Checkpoint is the operator-held anchor of the research ledger: the head hash
// after the first Records records (genesis counts as record 1, like
// usdledger.Snapshot.Records). The ledger may grow past it.
type Checkpoint struct {
	Records    uint64 `json:"records"`
	HeadSHA256 string `json:"head_sha256"`
}

// parseCheckpoint strictly decodes a checkpoint: unknown, duplicate and missing
// members, trailing data, zero records and a head that is not 64 lowercase hex
// digits are refused.
func parseCheckpoint(data []byte) (Checkpoint, error) {
	if err := protocol.ValidateStrictJSON(data); err != nil {
		return Checkpoint{}, fmt.Errorf("%w: %w", ErrCheckpoint, err)
	}
	var raw struct {
		Records *uint64 `json:"records"`
		Head    *string `json:"head_sha256"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return Checkpoint{}, fmt.Errorf("%w: %w", ErrCheckpoint, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Checkpoint{}, fmt.Errorf("%w: trailing data", ErrCheckpoint)
	}
	if raw.Records == nil || raw.Head == nil || *raw.Records == 0 {
		return Checkpoint{}, fmt.Errorf("%w: records (at least 1) and head_sha256 are required", ErrCheckpoint)
	}
	if _, err := hex.DecodeString(*raw.Head); err != nil || len(*raw.Head) != sha256.Size*2 || strings.ToLower(*raw.Head) != *raw.Head {
		return Checkpoint{}, fmt.Errorf("%w: head_sha256 must be 64 lowercase hex digits", ErrCheckpoint)
	}
	return Checkpoint{Records: *raw.Records, HeadSHA256: *raw.Head}, nil
}
