package protocol

import (
	"strings"
	"testing"
)

func TestFrameJSONKeepsCaseSensitiveKeysAndSizeBound(t *testing.T) {
	if err := ValidateStrictJSON([]byte(`{"route":"one","Route":"two","schema":1,"\u017fchema":2}`)); err != nil {
		t.Fatalf("distinct protocol keys rejected: %v", err)
	}
	if err := ValidateStrictJSON([]byte(`{"route":1,"route":2}`)); err == nil {
		t.Fatal("exact duplicate accepted by protocol")
	}
	if err := ValidateStrictJSON([]byte(`"` + strings.Repeat("x", MaxFrameBytes) + `"`)); err == nil {
		t.Fatal("oversized protocol frame accepted")
	}
}
