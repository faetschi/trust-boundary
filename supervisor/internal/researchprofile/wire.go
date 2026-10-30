package researchprofile

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Endpoint and Model are the one fixed research identity. A Profile whose
// Endpoint or Model differs is refused by Validate, so a caller that builds a
// request from a loaded Profile cannot reach any other URL or model.
const (
	Endpoint = fixedEndpoint
	Model    = fixedModel
)

// wireProvider is the OpenRouter "provider" request object rendered from the
// frozen routing decisions. An empty order or quantizations list is omitted:
// the profile's empty list means "no pin" and "no constraint", and OpenRouter
// treats a missing member the same way. The field names are the ones the
// profile uses and must be re-verified against the live provider-routing
// documentation at freeze time (see Routing).
type wireProvider struct {
	Order             []string      `json:"order,omitempty"`
	AllowFallbacks    bool          `json:"allow_fallbacks"`
	DataCollection    string        `json:"data_collection"`
	Quantizations     []string      `json:"quantizations,omitempty"`
	RequireParameters bool          `json:"require_parameters"`
	ZDR               bool          `json:"zdr"`
	MaxPrice          *wireMaxPrice `json:"max_price,omitempty"`
}

// wireMaxPrice carries the price ceilings in US dollars per million tokens, as
// OpenRouter expects, rendered exactly from the integer micro-USD profile values.
type wireMaxPrice struct {
	Prompt     json.Number `json:"prompt"`
	Completion json.Number `json:"completion"`
}

type wireMembers struct {
	Provider    wireProvider `json:"provider"`
	Temperature json.Number  `json:"temperature"`
	Seed        uint64       `json:"seed"`
	Reasoning   Reasoning    `json:"reasoning"`
}

// RequestMembers returns the canonical JSON object of top-level chat-completions
// members that this profile fixes: the provider routing object, temperature,
// seed and the reasoning control. It does not send usage.include: OpenRouter
// deprecated it, and usage (with usage.cost, which the ledger settles) is returned
// without it. The caller appends them to the fixed
// request members (model, messages, tools, stream, tool choice and the host's
// completion cap). The result has no insignificant whitespace and a fixed member
// order, so equal profiles give byte-equal requests. A profile that is not
// Frozen is refused with ErrNotFrozen.
func (p Profile) RequestMembers() (string, error) {
	if err := p.FrozenReason(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrNotFrozen, err)
	}
	provider := wireProvider{
		Order:             append([]string(nil), p.Routing.Order...),
		AllowFallbacks:    *p.Routing.AllowFallbacks,
		DataCollection:    *p.Routing.DataCollection,
		Quantizations:     append([]string(nil), p.Routing.Quantizations...),
		RequireParameters: *p.Routing.RequireParameters,
		ZDR:               *p.Routing.ZDR,
	}
	if p.MaxPriceMicroPerMTokIn != nil {
		provider.MaxPrice = &wireMaxPrice{
			Prompt:     decimalFromMicro(*p.MaxPriceMicroPerMTokIn, 6),
			Completion: decimalFromMicro(*p.MaxPriceMicroPerMTokOut, 6),
		}
	}
	encoded, err := json.Marshal(wireMembers{
		Provider:    provider,
		Temperature: decimalFromMicro(*p.TemperatureMilli, 3),
		Seed:        *p.Seed,
		Reasoning:   *p.Reasoning,
	})
	if err != nil {
		return "", fmt.Errorf("encode research request members: %w", err)
	}
	return string(encoded), nil
}

// ActualMicroUSD prices observed token counts at the profile's worst price per
// token (the larger of each declared price and its ceiling), rounded up to whole
// micro-USD. Unlike WorstCaseMicroUSD it does not bound the counts by the frozen
// maxima: a provider can report more tokens than the profile allowed, and the
// ledger needs the true cost to record that overage. It refuses a profile that
// is not Frozen and any overflow.
func (p Profile) ActualMicroUSD(promptTokens, completionTokens uint64) (uint64, error) {
	if err := p.FrozenReason(); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrNotFrozen, err)
	}
	return p.priceMicroUSD(promptTokens, completionTokens)
}

// CeilingMicroUSD prices observed token counts at the transmitted price ceilings
// alone (provider.max_price prompt and completion), rounded up to whole
// micro-USD. The ceilings are what the provider was told never to exceed, so a
// charge above this amount is a ceiling violation even when a declared (base)
// price is higher than its ceiling and ActualMicroUSD would accept it. It is for
// anomaly detection only, never for a reservation. Like ActualMicroUSD it does
// not bound the counts. It refuses a profile that is not Frozen and any overflow.
func (p Profile) CeilingMicroUSD(promptTokens, completionTokens uint64) (uint64, error) {
	if err := p.FrozenReason(); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrNotFrozen, err)
	}
	return priceAt(promptTokens, completionTokens, *p.MaxPriceMicroPerMTokIn, *p.MaxPriceMicroPerMTokOut)
}

// priceMicroUSD is the integer arithmetic shared by WorstCaseMicroUSD and
// ActualMicroUSD. The profile must already be Frozen.
func (p Profile) priceMicroUSD(inputTokens, outputTokens uint64) (uint64, error) {
	return priceAt(inputTokens, outputTokens,
		maxPrice(*p.InputMicroUSDPer1M, p.MaxPriceMicroPerMTokIn),
		maxPrice(*p.OutputMicroUSDPer1M, p.MaxPriceMicroPerMTokOut))
}

// priceAt is the integer arithmetic: the token counts at the given prices per
// million tokens, rounded up to whole micro-USD, with overflow checked.
func priceAt(inputTokens, outputTokens, inputPrice, outputPrice uint64) (uint64, error) {
	inputCost, ok := mulUint64(inputTokens, inputPrice)
	if !ok {
		return 0, errOverflow
	}
	outputCost, ok := mulUint64(outputTokens, outputPrice)
	if !ok {
		return 0, errOverflow
	}
	total := inputCost + outputCost
	if total < inputCost {
		return 0, errOverflow
	}
	microUSD := total / microUSDPerMillionTokens
	if total%microUSDPerMillionTokens != 0 {
		microUSD++
	}
	return microUSD, nil
}

// decimalFromMicro renders value/10^digits as a JSON number without an exponent
// and without trailing zeros: 270000 with 6 digits is 0.27, 1000000 is 1, and 0
// is 0.
func decimalFromMicro(value uint64, digits int) json.Number {
	text := strconv.FormatUint(value, 10)
	if len(text) <= digits {
		text = strings.Repeat("0", digits-len(text)+1) + text
	}
	whole, fraction := text[:len(text)-digits], strings.TrimRight(text[len(text)-digits:], "0")
	if fraction == "" {
		return json.Number(whole)
	}
	return json.Number(whole + "." + fraction)
}
