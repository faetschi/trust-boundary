package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/gowebpki/jcs"
)

const maxBashTimeoutSeconds = 2_147_483_647.0 / 1000.0

// CanonicalArguments returns the RFC 8785 canonical UTF-8 JSON bytes for one
// argument object after the fixed Pi proxy schema and strict-input checks pass.
func CanonicalArguments(tool string, raw json.RawMessage) ([]byte, error) {
	if !registeredTool(tool) {
		return nil, errors.New("tool is outside the fixed proxy surface")
	}
	if len(raw) == 0 || len(raw) > MaxFrameBytes {
		return nil, errors.New("arguments have invalid size")
	}
	if err := checkStrictJSON(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("tool arguments must be an object")
	}
	if err := validateArgumentSchema(tool, object); err != nil {
		return nil, err
	}
	return canonicalizeValidatedJSON(raw)
}

// CanonicalArgumentsDigest binds the tool's validated arguments to their
// RFC 8785 canonical bytes. Numeric serialization follows JCS; argument
// validation remains governed by the actual Pi tool schemas and semantics.
func CanonicalArgumentsDigest(tool string, raw json.RawMessage) (string, error) {
	canonical, err := CanonicalArguments(tool, raw)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return CanonicalizationProfile + ":sha256:" + hex.EncodeToString(digest[:]), nil
}

// canonicalizeJSON exposes the lower-level JCS transform to package tests so
// normative RFC vectors can be checked independently of TBound's tool schema.
func canonicalizeJSON(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > MaxFrameBytes {
		return nil, errors.New("JSON has invalid size")
	}
	if err := checkStrictJSON(raw); err != nil {
		return nil, err
	}
	return canonicalizeValidatedJSON(raw)
}

func canonicalizeValidatedJSON(raw []byte) ([]byte, error) {
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("RFC 8785 canonicalization: %w", err)
	}
	return canonical, nil
}

func validateArgumentSchema(tool string, object map[string]any) error {
	switch tool {
	case "read":
		if err := fields(object, []string{"path"}, []string{"offset", "limit"}); err != nil {
			return err
		}
		if _, ok := object["path"].(string); !ok {
			return errors.New("read.path must be a string")
		}
		return optionalNumbers(object, "offset", "limit")
	case "write":
		if err := fields(object, []string{"path", "content"}, nil); err != nil {
			return err
		}
		if _, ok := object["path"].(string); !ok {
			return errors.New("write.path must be a string")
		}
		if _, ok := object["content"].(string); !ok {
			return errors.New("write.content must be a string")
		}
		return nil
	case "edit":
		if err := fields(object, []string{"path", "edits"}, nil); err != nil {
			return err
		}
		if _, ok := object["path"].(string); !ok {
			return errors.New("edit.path must be a string")
		}
		edits, ok := object["edits"].([]any)
		if !ok {
			return errors.New("edit.edits must be an array")
		}
		for index, item := range edits {
			edit, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("edit.edits[%d] must be an object", index)
			}
			if err := fields(edit, []string{"oldText", "newText"}, nil); err != nil {
				return fmt.Errorf("edit.edits[%d]: %w", index, err)
			}
			if _, ok := edit["oldText"].(string); !ok {
				return fmt.Errorf("edit.edits[%d].oldText must be a string", index)
			}
			if _, ok := edit["newText"].(string); !ok {
				return fmt.Errorf("edit.edits[%d].newText must be a string", index)
			}
		}
		return nil
	case "bash":
		if err := fields(object, []string{"command"}, []string{"timeout"}); err != nil {
			return err
		}
		if _, ok := object["command"].(string); !ok {
			return errors.New("bash.command must be a string")
		}
		if err := optionalNumbers(object, "timeout"); err != nil {
			return err
		}
		return validateBashTimeout(object)
	default:
		return errors.New("tool is outside the fixed proxy surface")
	}
}

func fields(object map[string]any, required, optional []string) error {
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = struct{}{}
		if _, ok := object[name]; !ok {
			return fmt.Errorf("missing required argument %q", name)
		}
	}
	for _, name := range optional {
		allowed[name] = struct{}{}
	}
	for name := range object {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("unknown argument %q", name)
		}
	}
	return nil
}

// Pi's pinned Type.Number schemas accept finite JSON numbers, not only integer
// tokens. JCS representability is checked before schema validation.
func optionalNumbers(object map[string]any, names ...string) error {
	for _, name := range names {
		if value, ok := object[name]; ok {
			if _, ok := value.(json.Number); !ok {
				return fmt.Errorf("argument %q must be a JSON number", name)
			}
		}
	}
	return nil
}

// validateBashTimeout mirrors the pinned Pi v0.87.1 execution validation. This
// is a tool-semantic check; it is intentionally independent of JCS.
func validateBashTimeout(object map[string]any) error {
	value, exists := object["timeout"]
	if !exists {
		return nil
	}
	parsed, err := strconv.ParseFloat(string(value.(json.Number)), 64)
	if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) || parsed <= 0 {
		return errors.New("bash.timeout must be a finite number of seconds greater than zero")
	}
	if parsed > maxBashTimeoutSeconds {
		return fmt.Errorf("bash.timeout exceeds the Pi maximum of %g seconds", maxBashTimeoutSeconds)
	}
	return nil
}
