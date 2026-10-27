package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode"
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

func TestEvidenceJSONRejectsAmbiguousMembersWithoutFrameLimit(t *testing.T) {
	for _, raw := range []string{
		`{"route":1,"Route":2}`, `{"route":1,"\u0052oute":2}`,
		`{"schema":1,"\u017fchema":2}`, `{"K":1,"\u212a":2}`,
		`{"\u03a3":1,"\u03c2":2}`, `{"nested":[{"a":1,"A":2}]}`,
		`{"a":1,"a":2}`, `{} {}`, `{"n":-0}`, `{"n":1e999}`, `{"s":"\ud800"}`,
		strings.Repeat("[", maxJSONDepth+1) + strings.Repeat("]", maxJSONDepth+1),
	} {
		if err := ValidateEvidenceJSON([]byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, raw := range []string{
		`{"a":1,"nested":{"A":2}}`, `[{"a":1},{"A":2}]`, `{"a":1,"b":2}`,
		`"` + strings.Repeat("x", MaxFrameBytes) + `"`,
	} {
		if err := ValidateEvidenceJSON([]byte(raw)); err != nil {
			t.Errorf("valid evidence rejected: %v", err)
		}
	}
}

func TestFoldJSONKeyMatchesJSONFieldFolding(t *testing.T) {
	// Exercise every Unicode simple-fold edge, including the non-ASCII folds
	// that lowercasing alone misses, then check actual decoding for ASCII fields.
	for r := rune(0); r <= unicode.MaxRune; r++ {
		s := unicode.SimpleFold(r)
		if r != s && foldJSONKey(string(r)) != foldJSONKey(string(s)) {
			t.Fatalf("fold class split at %U and %U", r, s)
		}
	}
	for _, key := range []string{"Schema", "ſchema", "KEY", "Key"} {
		var doc struct {
			Schema int `json:"schema"`
			Key    int `json:"key"`
		}
		raw, err := json.Marshal(map[string]int{key: 7})
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &doc); err != nil || (doc.Schema != 7 && doc.Key != 7) {
			t.Fatalf("encoding/json did not fold %q as expected: %+v, %v", key, doc, err)
		}
		if foldJSONKey(key) != foldJSONKey("schema") && foldJSONKey(key) != foldJSONKey("key") {
			t.Fatalf("helper disagrees with encoding/json for %q", key)
		}
	}
}
