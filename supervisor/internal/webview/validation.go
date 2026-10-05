package webview

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const maxJSONNestingDepth = 128

// validateJSONDocument rejects invalid UTF-8, duplicate object keys at any
// depth, malformed values, and trailing top-level data before typed decoding.
func validateJSONDocument(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("JSON is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkJSONValueAt(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON has trailing data")
		}
		return err
	}
	return nil
}

func validateExactObjectFields(raw []byte, allowed ...string) error {
	if err := validateJSONDocument(raw); err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return err
	}
	if object == nil {
		return errors.New("JSON value must be an object")
	}
	allow := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allow[key] = struct{}{}
	}
	for key := range object {
		if _, ok := allow[key]; !ok {
			return errors.New("JSON object contains an unknown or incorrectly cased field")
		}
	}
	return nil
}

func walkJSONValueAt(decoder *json.Decoder, depth int) error {
	if depth > maxJSONNestingDepth {
		return errors.New("JSON nesting exceeds the configured depth limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			foldedKey := strings.ToLower(key)
			if _, exists := seen[foldedKey]; exists {
				return errors.New("JSON object contains a duplicate field")
			}
			seen[foldedKey] = struct{}{}
			if err := walkJSONValueAt(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return errors.New("JSON object is not terminated")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValueAt(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("JSON array is not terminated")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}
