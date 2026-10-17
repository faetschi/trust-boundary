// Package researchprofile defines the research-provider profile for the thesis's
// final headline runs: OpenRouter, model deepseek/deepseek-v4.1-flash, under the
// US$5 total cap (docs/decisions/2026-10-14-qualification-and-research-procedure.md).
// It provides the schema, the strict loader, the canonical digest, the freeze
// checks and the worst-case arithmetic. It holds no frozen profile. The
// committed draft is OPEN, so Frozen reports false for it, and the only fully
// decided profile in this package is a synthetic test fixture.
//
// It is separate from the engineering route. The OpenCode Go engineering model
// id is deepseek-v4.1-flash (docs/evidence/opencode-go-engineering-profile-20261015.json).
// The research id is deepseek/deepseek-v4.1-flash, and the two are never mapped
// to each other.
//
// A JSON null marks a value OPEN (not yet decided). Freezing requires every
// required field to be decided, a provider pin or a price ceiling pair, and
// temperature 0. The decision doc requires freezing provider routing, current
// price ceilings and request/token limits before paid execution.
//
// Scope: the tests verify code only, with synthetic numbers and no provider
// traffic. This package does not establish budget feasibility, real prices or
// provider routing, and it is not G1 or H1 evidence. It does not import usdledger;
// WorstCaseMicroUSD is the amount a caller passes to the ledger reservation.
package researchprofile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"reflect"
	"strings"
	"time"
)

const schemaVersion = 1
const fixedEndpoint = "https://openrouter.ai/api/v1/chat/completions"
const fixedHost = "openrouter.ai"
const fixedModel = "deepseek/deepseek-v4.1-flash"

// maxProfileBytes bounds the document size read by Load.
const maxProfileBytes = 64 << 10

// maxTemperatureMilli is the largest accepted temperature (2.0) in milli-units.
const maxTemperatureMilli = 2000

const microUSDPerMillionTokens = 1_000_000

var errOverflow = errors.New("worst-case cost overflows uint64")

// ErrNotFrozen wraps the specific reason a profile cannot yet be used for a
// reservation.
var ErrNotFrozen = errors.New("research profile is not frozen")

// ErrOpenField reports a required field that is still null (OPEN). The message
// names the field.
var ErrOpenField = errors.New("research profile field is OPEN")

// ErrUnpinnedRouting reports an empty provider order with no price ceiling pair.
var ErrUnpinnedRouting = errors.New("research profile needs a provider pin (non-empty routing.order) or both price ceilings")

// ErrTemperatureNotZero reports a temperature_milli other than 0. The thesis pins
// temperature at zero.
var ErrTemperatureNotZero = errors.New("research profile temperature_milli must be 0 (temperature-zero is pinned)")

// Routing mirrors OpenRouter's provider-routing request object. A nil field is
// OPEN. An empty order means no provider pin, which Frozen accepts only with a
// price ceiling pair. An empty quantizations list is an explicit "no constraint".
type Routing struct {
	Order          []string `json:"order"`
	AllowFallbacks *bool    `json:"allow_fallbacks"`
	DataCollection *string  `json:"data_collection"`
	Quantizations  []string `json:"quantizations"`
	// RequireParameters and ZDR are required, non-null routing decisions. Their
	// names are the ones used in this draft and must be re-verified against the
	// live OpenRouter provider-routing documentation at freeze time; this package
	// does not confirm their semantics.
	RequireParameters *bool `json:"require_parameters"`
	ZDR               *bool `json:"zdr"`
}

// Profile is one research provider profile. Every JSON field must be present;
// null is OPEN. Money is integer micro-USD and temperature is integer
// milli-units, so the document contains no floats.
type Profile struct {
	SchemaVersion       uint64  `json:"schema_version"`
	Endpoint            string  `json:"endpoint"`
	Model               string  `json:"model"`
	Routing             Routing `json:"routing"`
	InputMicroUSDPer1M  *uint64 `json:"input_micro_usd_per_1m_tokens"`
	OutputMicroUSDPer1M *uint64 `json:"output_micro_usd_per_1m_tokens"`
	// MaxPriceMicroPerMTokIn and MaxPriceMicroPerMTokOut are optional price
	// ceilings. They are set as a pair or not at all, and the worst case uses the
	// larger of each declared price and its ceiling.
	MaxPriceMicroPerMTokIn  *uint64 `json:"max_price_micro_usd_per_1m_tokens_in"`
	MaxPriceMicroPerMTokOut *uint64 `json:"max_price_micro_usd_per_1m_tokens_out"`
	MaxInputTokens          *uint64 `json:"max_input_tokens"`
	MaxOutputTokens         *uint64 `json:"max_output_tokens"`
	MaxRequestsPerUnit      *uint64 `json:"max_requests_per_unit"`
	TemperatureMilli        *uint64 `json:"temperature_milli"`
	Seed                    *uint64 `json:"seed"`
	TimeoutMS               *uint64 `json:"timeout_ms"`
	FrozenAt                *string `json:"frozen_at"`
}

// Load reads one research profile. It is strict: unknown, duplicate and missing
// fields are rejected, as are trailing data and documents over 64 KiB. Numbers
// must be non-negative integers. Validate checks the remaining rules.
func Load(r io.Reader) (Profile, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxProfileBytes+1))
	if err != nil {
		return Profile{}, fmt.Errorf("read research profile: %w", err)
	}
	if len(data) > maxProfileBytes {
		return Profile{}, errors.New("research profile exceeds the size limit")
	}
	members, err := strictObject(data, jsonKeys(reflect.TypeFor[Profile]()))
	if err != nil {
		return Profile{}, err
	}
	if _, err := strictObject(members["routing"], jsonKeys(reflect.TypeFor[Routing]())); err != nil {
		return Profile{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var p Profile
	if err := dec.Decode(&p); err != nil {
		return Profile{}, fmt.Errorf("decode research profile: %w", err)
	}
	if err := p.Validate(); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// Validate checks the fixed identity and every decided field. OPEN (nil) fields
// pass here; Frozen is the stricter test that none remains OPEN.
func (p Profile) Validate() error {
	if p.SchemaVersion != schemaVersion {
		return errors.New("research profile schema_version must be 1")
	}
	if err := validateEndpoint(p.Endpoint); err != nil {
		return err
	}
	if p.Model != fixedModel {
		return fmt.Errorf("research profile model must be %q", fixedModel)
	}
	if err := p.Routing.validate(); err != nil {
		return err
	}
	positive := []struct {
		name  string
		value *uint64
	}{
		{"input_micro_usd_per_1m_tokens", p.InputMicroUSDPer1M},
		{"output_micro_usd_per_1m_tokens", p.OutputMicroUSDPer1M},
		{"max_price_micro_usd_per_1m_tokens_in", p.MaxPriceMicroPerMTokIn},
		{"max_price_micro_usd_per_1m_tokens_out", p.MaxPriceMicroPerMTokOut},
		{"max_input_tokens", p.MaxInputTokens},
		{"max_output_tokens", p.MaxOutputTokens},
		{"max_requests_per_unit", p.MaxRequestsPerUnit},
		{"seed", p.Seed},
		{"timeout_ms", p.TimeoutMS},
	}
	for _, f := range positive {
		if f.value != nil && *f.value == 0 {
			return fmt.Errorf("research profile %s must be positive", f.name)
		}
	}
	if (p.MaxPriceMicroPerMTokIn == nil) != (p.MaxPriceMicroPerMTokOut == nil) {
		return errors.New("research profile max_price_micro_usd_per_1m_tokens_in and _out must be set together")
	}
	if p.TemperatureMilli != nil && *p.TemperatureMilli > maxTemperatureMilli {
		return errors.New("research profile temperature_milli must be from 0 to 2000")
	}
	if p.FrozenAt != nil {
		if _, err := time.Parse(time.RFC3339, *p.FrozenAt); err != nil {
			return errors.New("research profile frozen_at must be an RFC 3339 timestamp")
		}
	}
	return nil
}

// Frozen reports whether FrozenReason is nil: the profile is valid and fully
// decided. See FrozenReason for the rules.
func (p Profile) Frozen() bool {
	return p.FrozenReason() == nil
}

// FrozenReason returns nil when the profile is valid and fully decided. Otherwise
// it returns the first reason it is not frozen. The checks are:
//
//   - the profile validates;
//   - every required field is non-null: routing order, allow_fallbacks,
//     data_collection, quantizations, require_parameters and zdr; both prices;
//     max_input_tokens, max_output_tokens and max_requests_per_unit;
//     temperature_milli, seed, timeout_ms and frozen_at. A null field is OPEN
//     and fails with ErrOpenField, which names the field;
//   - routing has a provider pin (a non-empty order) or both price ceilings,
//     else ErrUnpinnedRouting;
//   - temperature_milli is 0, else ErrTemperatureNotZero.
//
// The ceilings are optional. Frozen never reads them as OPEN.
func (p Profile) FrozenReason() error {
	if err := p.Validate(); err != nil {
		return err
	}
	required := []struct {
		name string
		set  bool
	}{
		{"routing.order", p.Routing.Order != nil},
		{"routing.allow_fallbacks", p.Routing.AllowFallbacks != nil},
		{"routing.data_collection", p.Routing.DataCollection != nil},
		{"routing.quantizations", p.Routing.Quantizations != nil},
		{"routing.require_parameters", p.Routing.RequireParameters != nil},
		{"routing.zdr", p.Routing.ZDR != nil},
		{"input_micro_usd_per_1m_tokens", p.InputMicroUSDPer1M != nil},
		{"output_micro_usd_per_1m_tokens", p.OutputMicroUSDPer1M != nil},
		{"max_input_tokens", p.MaxInputTokens != nil},
		{"max_output_tokens", p.MaxOutputTokens != nil},
		{"max_requests_per_unit", p.MaxRequestsPerUnit != nil},
		{"temperature_milli", p.TemperatureMilli != nil},
		{"seed", p.Seed != nil},
		{"timeout_ms", p.TimeoutMS != nil},
		{"frozen_at", p.FrozenAt != nil},
	}
	for _, f := range required {
		if !f.set {
			return fmt.Errorf("%w: %s", ErrOpenField, f.name)
		}
	}
	// Validate has already required the two ceilings to be set together.
	if len(p.Routing.Order) == 0 && p.MaxPriceMicroPerMTokIn == nil {
		return ErrUnpinnedRouting
	}
	if *p.TemperatureMilli != 0 {
		return ErrTemperatureNotZero
	}
	return nil
}

// WorstCaseMicroUSD returns the micro-USD to reserve for one request whose input
// and output token counts are at most inputTokensMax and outputTokensMax. It is
// (inputTokensMax*inputPrice + outputTokensMax*outputPrice) rounded up to whole
// micro-USD, computed in integers. Each price is the larger of its declared value
// and its ceiling, when a ceiling is set. It refuses a profile that is not Frozen
// (wrapping ErrNotFrozen with the reason), bounds above max_input_tokens or
// max_output_tokens, and any overflow. Provider-side rounding, minimum charges and
// retries are not modelled. Use WorstCaseUnitMicroUSD for the unit reservation.
func (p Profile) WorstCaseMicroUSD(inputTokensMax, outputTokensMax uint64) (uint64, error) {
	if err := p.FrozenReason(); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrNotFrozen, err)
	}
	if inputTokensMax > *p.MaxInputTokens {
		return 0, errors.New("input token bound exceeds the frozen max_input_tokens")
	}
	if outputTokensMax > *p.MaxOutputTokens {
		return 0, errors.New("output token bound exceeds the frozen max_output_tokens")
	}
	return p.priceMicroUSD(inputTokensMax, outputTokensMax)
}

// WorstCaseUnitMicroUSD returns the micro-USD to reserve for one unit: the
// per-request worst case from WorstCaseMicroUSD times max_requests_per_unit, with
// overflow checked. The durable ledger reserves per unit, so a caller passes this
// value to usdledger.Ledger.Reserve. This package does not import usdledger.
func (p Profile) WorstCaseUnitMicroUSD(inputTokensMax, outputTokensMax uint64) (uint64, error) {
	perRequest, err := p.WorstCaseMicroUSD(inputTokensMax, outputTokensMax)
	if err != nil {
		return 0, err
	}
	unit, ok := mulUint64(perRequest, *p.MaxRequestsPerUnit)
	if !ok {
		return 0, errOverflow
	}
	return unit, nil
}

// Digest returns the lowercase hex SHA-256 of the canonical encoding of a valid
// profile: encoding/json output of the struct, in field order, with no
// insignificant whitespace and explicit nulls for OPEN fields. Input formatting
// and key order do not change it; any value change does. Drafts can be digested.
func (p Profile) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("encode research profile: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// maxPrice returns the declared price, or its ceiling when the ceiling is higher.
// The worst case never assumes a price below what the profile allows.
func maxPrice(declared uint64, ceiling *uint64) uint64 {
	if ceiling != nil && *ceiling > declared {
		return *ceiling
	}
	return declared
}

func mulUint64(a, b uint64) (uint64, bool) {
	if a != 0 && b > math.MaxUint64/a {
		return 0, false
	}
	return a * b, true
}

func validateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" {
		return errors.New("research profile endpoint must use https")
	}
	if u.Host != fixedHost {
		return fmt.Errorf("research profile endpoint host must be %s", fixedHost)
	}
	if endpoint != fixedEndpoint {
		return errors.New("research profile endpoint must be the documented chat completions URL")
	}
	return nil
}

func (r Routing) validate() error {
	for _, provider := range r.Order {
		if provider == "" {
			return errors.New("research profile routing order entries must be non-empty")
		}
	}
	if r.DataCollection != nil && *r.DataCollection != "allow" && *r.DataCollection != "deny" {
		return errors.New(`research profile routing data_collection must be "allow" or "deny"`)
	}
	for _, quantization := range r.Quantizations {
		if quantization == "" {
			return errors.New("research profile routing quantizations entries must be non-empty")
		}
	}
	return nil
}

// strictObject returns the members of one JSON object. It rejects non-objects,
// keys outside want, duplicate keys, missing keys and trailing data. A member
// whose value is null is still present.
func strictObject(raw []byte, want []string) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("research profile object expected")
	}
	allowed := make(map[string]bool, len(want))
	for _, key := range want {
		allowed[key] = true
	}
	members := make(map[string]json.RawMessage, len(want))
	for dec.More() {
		tok, err := dec.Token()
		key, isString := tok.(string)
		if err != nil || !isString {
			return nil, errors.New("malformed research profile field name")
		}
		if !allowed[key] {
			return nil, fmt.Errorf("unknown research profile field %q", key)
		}
		if _, dup := members[key]; dup {
			return nil, fmt.Errorf("duplicate research profile field %q", key)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, fmt.Errorf("research profile field %q: %w", key, err)
		}
		members[key] = value
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, errors.New("malformed research profile object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data after research profile")
	}
	for _, key := range want {
		if _, ok := members[key]; !ok {
			return nil, fmt.Errorf("missing research profile field %q", key)
		}
	}
	return members, nil
}

// jsonKeys lists the JSON field names of a struct type, so that the presence
// check cannot drift from the struct tags.
func jsonKeys(t reflect.Type) []string {
	keys := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		keys = append(keys, strings.Split(t.Field(i).Tag.Get("json"), ",")[0])
	}
	return keys
}
