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
// micro-USD. It is the larger of two figures, each used when present:
//
//   - the provider-reported "cost" (US dollars, as OpenRouter reports it with
//     usage accounting enabled), rounded up to whole micro-USD, exactly;
//   - "prompt_tokens" and "completion_tokens" priced at the profile's worst price
//     per token, rounded up.
//
// Taking the larger figure never understates spend when one of them is wrong. If
// neither can be read, the cost is unknown and ErrUsageMissing is returned.
func (b *Bundle) actualCost(usage json.RawMessage) (int64, error) {
	if len(usage) == 0 {
		return 0, ErrUsageMissing
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(usage, &members); err != nil || members == nil {
		return 0, fmt.Errorf("%w: usage is not an object", ErrUsageMissing)
	}
	var best int64
	found := false
	if raw, ok := members["cost"]; ok && string(raw) != "null" {
		micro, err := usdToMicro(raw)
		if err != nil {
			return 0, fmt.Errorf("%w: cost: %w", ErrUsageMissing, err)
		}
		best, found = micro, true
	}
	prompt, promptOK := tokenCount(members["prompt_tokens"])
	completion, completionOK := tokenCount(members["completion_tokens"])
	if promptOK && completionOK {
		priced, err := b.profile.ActualMicroUSD(prompt, completion)
		if err != nil || priced > math.MaxInt64 {
			return 0, errors.Join(ErrUsageMissing, err)
		}
		if !found || int64(priced) > best {
			best = int64(priced)
		}
		found = true
	}
	if !found {
		return 0, ErrUsageMissing
	}
	return best, nil
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
