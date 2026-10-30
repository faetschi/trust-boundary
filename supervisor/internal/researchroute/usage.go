package researchroute

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
)

// actualCost establishes the cost of one response from its usage object, in
// micro-USD, and reports whether the cost is an anomaly.
//
// Two prices apply to the token counts. The anomaly threshold is the counts
// priced at the transmitted ceilings alone (researchprofile.Profile.
// CeilingMicroUSD): the ceilings are sent with every request as
// provider.max_price, so the provider must not charge above them, and a profile
// whose declared price is higher than its ceiling does not widen the threshold.
// Settlement without a reported cost uses the larger of the declared price and the
// ceiling (researchprofile.Profile.ActualMicroUSD), which is conservative. Tokens
// are never priced at a lower declared (base) rate: a weekday peak rate is twice
// the base rate.
//
//   - usage.cost present (US dollars, as OpenRouter reports it), rounded up to
//     whole micro-USD, exactly: the cost is the settled amount, so that genuine
//     costs are not over-counted. If the token counts are also present and the
//     cost is greater than their price at the transmitted ceilings alone, the
//     provider charged beyond what the ceilings allow. That is an anomaly
//     (anomaly is true): the caller records it with SettleOverage, which stops
//     the ledger.
//   - usage.cost absent: the token counts priced at the larger of declared price
//     and ceiling are the cost.
//
// If neither can be read, the cost is unknown and ErrUsageMissing is returned.
func (b *Bundle) actualCost(usage json.RawMessage) (micro int64, anomaly bool, err error) {
	if len(usage) == 0 {
		return 0, false, ErrUsageMissing
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(usage, &members); err != nil || members == nil {
		return 0, false, fmt.Errorf("%w: usage is not an object", ErrUsageMissing)
	}
	var reported int64
	costOK := false
	if raw, ok := members["cost"]; ok && string(raw) != "null" {
		reported, err = usdToMicro(raw)
		if err != nil {
			return 0, false, fmt.Errorf("%w: cost: %w", ErrUsageMissing, err)
		}
		costOK = true
	}
	var threshold, conservative int64 // at the ceilings alone; at the larger of declared and ceiling
	tokensOK := false
	prompt, promptOK := tokenCount(members["prompt_tokens"])
	completion, completionOK := tokenCount(members["completion_tokens"])
	if promptOK && completionOK {
		atCeilings, err := b.profile.CeilingMicroUSD(prompt, completion)
		if err != nil || atCeilings > math.MaxInt64 {
			return 0, false, errors.Join(ErrUsageMissing, err)
		}
		atMax, err := b.profile.ActualMicroUSD(prompt, completion)
		if err != nil || atMax > math.MaxInt64 {
			return 0, false, errors.Join(ErrUsageMissing, err)
		}
		threshold, conservative, tokensOK = int64(atCeilings), int64(atMax), true
	}
	switch {
	case costOK:
		return reported, tokensOK && reported > threshold, nil
	case tokensOK:
		return conservative, false, nil
	}
	return 0, false, ErrUsageMissing
}

// tokenCount reads a non-negative integer token count.
func tokenCount(raw json.RawMessage) (uint64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	n, err := strconv.ParseUint(string(raw), 10, 64)
	return n, err == nil
}

// usdToMicro converts a JSON number of US dollars to whole micro-USD, rounding
// up. It is exact: the number is parsed as a rational, so exponents and long
// fractions cannot lose precision. Negative and non-finite values are refused.
func usdToMicro(raw json.RawMessage) (int64, error) {
	if len(raw) > 64 {
		return 0, errors.New("number is too long")
	}
	rat, ok := new(big.Rat).SetString(string(raw))
	if !ok || rat.Sign() < 0 {
		return 0, errors.New("not a non-negative number")
	}
	micro := new(big.Rat).Mul(rat, big.NewRat(1_000_000, 1))
	quotient, remainder := new(big.Int).QuoRem(micro.Num(), micro.Denom(), new(big.Int))
	if remainder.Sign() > 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return 0, errors.New("cost overflows int64 micro-USD")
	}
	return quotient.Int64(), nil
}
