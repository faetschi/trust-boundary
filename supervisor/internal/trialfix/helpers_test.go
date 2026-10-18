package trialfix

import (
	"crypto/sha256"
	"encoding/hex"
)

func wsoracleSum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
