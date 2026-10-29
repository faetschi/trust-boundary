package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

// The published tool schemas do not forbid additional properties, so the adapter may send a key the schema does not
// list. Go permits such a key at every argument object, including each edit, and the canonical digest covers the
// complete provider arguments, so the extra key stays part of the correlated identity.
func TestCanonicalArgumentsPermitAndCoverExtraKeys(t *testing.T) {
	cases := map[string]string{
		"read":  `{"path":"a.txt","extra":1}`,
		"write": `{"path":"a.txt","content":"x","extra":true}`,
		"edit":  `{"path":"a.txt","edits":[{"oldText":"a","newText":"b","extra":null}],"extra":[1]}`,
		"bash":  `{"command":"true","extra":{"nested":1}}`,
	}
	for tool, arguments := range cases {
		canonical, err := CanonicalArguments(tool, json.RawMessage(arguments))
		if err != nil {
			t.Errorf("%s: a key the schema does not list was refused: %v", tool, err)
			continue
		}
		if !strings.Contains(string(canonical), `"extra"`) {
			t.Errorf("%s: canonical arguments %s drop the extra key the correlated digest must cover", tool, canonical)
		}
	}
}

// Extra keys do not relax the rest of the strict-input boundary: duplicate keys, malformed or trailing JSON,
// wrong types for known fields, numeric strings and missing required fields stay refused.
func TestCanonicalArgumentsStillRefuseMalformedAndWrongTypedInput(t *testing.T) {
	cases := map[string]struct {
		tool      string
		arguments string
	}{
		"duplicate key":          {"read", `{"path":"a","path":"b"}`},
		"duplicate nested key":   {"edit", `{"path":"a","edits":[{"oldText":"a","oldText":"b","newText":"c"}]}`},
		"trailing json":          {"read", `{"path":"a"}{}`},
		"malformed json":         {"read", `{"path":`},
		"wrong type path":        {"read", `{"path":5}`},
		"numeric string offset":  {"read", `{"path":"a","offset":"2"}`},
		"wrong type old text":    {"edit", `{"path":"a","edits":[{"oldText":1,"newText":"b"}]}`},
		"missing new text":       {"edit", `{"path":"a","edits":[{"oldText":"a","extra":1}]}`},
		"missing required write": {"write", `{"path":"a","extra":1}`},
	}
	for name, tc := range cases {
		if _, err := CanonicalArguments(tc.tool, json.RawMessage(tc.arguments)); err == nil {
			t.Errorf("%s: %s was accepted", name, tc.arguments)
		}
	}
}
