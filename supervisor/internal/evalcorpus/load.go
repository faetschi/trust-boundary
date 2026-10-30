package evalcorpus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

// Load reads one manifest from r and decodes it strictly. It reads at most
// MaxManifestBytes, requires valid UTF-8, and then rejects:
//   - a repeated key in any object, at any depth (encoding/json would keep the last);
//   - any key that is not the exact JSON name of a field of the schema at that position.
//     encoding/json matches names case-insensitively, so this check is what rejects
//     variants such as "Case_Id";
//   - a required field that is absent, and null anywhere;
//   - data after the top-level value.
//
// Load also rejects an unpaired surrogate \u escape (a lone high or low surrogate).
// encoding/json would decode it to U+FFFD, so two manifests that differ only in such
// an escape would share a digest. A valid high-low pair is accepted.
//
// Load checks shape only. Use Validate for the registered counts, quotas, and rules.
func Load(r io.Reader) (Manifest, error) {
	if r == nil {
		return Manifest{}, errors.New("evalcorpus: nil reader")
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxManifestBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("evalcorpus: read manifest: %w", err)
	}
	if len(data) > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("evalcorpus: manifest exceeds %d bytes", MaxManifestBytes)
	}
	if !utf8.Valid(data) {
		return Manifest{}, errors.New("evalcorpus: manifest is not valid UTF-8")
	}
	if err := rejectLoneSurrogates(data); err != nil {
		return Manifest{}, err
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return Manifest{}, err
	}
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		return Manifest{}, fmt.Errorf("evalcorpus: decode manifest: %w", err)
	}
	if err := checkShape(generic, reflect.TypeOf(Manifest{}), "$"); err != nil {
		return Manifest{}, fmt.Errorf("evalcorpus: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("evalcorpus: decode manifest: %w", err)
	}
	if err := checkScriptArguments(m); err != nil {
		return Manifest{}, fmt.Errorf("evalcorpus: %w", err)
	}
	return m, nil
}

// rejectDuplicateKeys scans every object in data and fails on a repeated key. It also
// fails on data after the first JSON value.
func rejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := scanDuplicates(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("evalcorpus: trailing data after manifest")
	}
	return nil
}

// rejectLoneSurrogates scans the raw bytes for \u escapes and fails on a surrogate
// (U+D800 to U+DFFF) that is not part of a high-low pair. An escaped backslash is
// skipped, so the text \\uD800 is not read as an escape.
func rejectLoneSurrogates(data []byte) error {
	const lone = "evalcorpus: manifest has an unpaired surrogate \\u escape"
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' || i+1 >= len(data) {
			continue
		}
		if data[i+1] != 'u' {
			i++ // skip the escaped character
			continue
		}
		u, ok := hex4(data, i+2)
		if !ok {
			i++ // malformed; encoding/json reports it
			continue
		}
		switch {
		case u >= 0xDC00 && u <= 0xDFFF:
			return errors.New(lone)
		case u >= 0xD800 && u <= 0xDBFF:
			j := i + 6
			if j+1 >= len(data) || data[j] != '\\' || data[j+1] != 'u' {
				return errors.New(lone)
			}
			if v, ok := hex4(data, j+2); !ok || v < 0xDC00 || v > 0xDFFF {
				return errors.New(lone)
			}
			i = j + 5
		default:
			i += 5
		}
	}
	return nil
}

// hex4 decodes the four hexadecimal digits at data[at:at+4].
func hex4(data []byte, at int) (rune, bool) {
	if at < 0 || at+4 > len(data) {
		return 0, false
	}
	var v rune
	for _, c := range data[at : at+4] {
		switch {
		case c >= '0' && c <= '9':
			v = v<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			v = v<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			v = v<<4 | rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return v, true
}

func scanDuplicates(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("evalcorpus: decode manifest: %w", err)
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil // a scalar value
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return fmt.Errorf("evalcorpus: decode manifest: %w", err)
			}
			key, _ := kt.(string)
			if seen[key] {
				return fmt.Errorf("evalcorpus: duplicate field %q", key)
			}
			seen[key] = true
			if err := scanDuplicates(dec); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := scanDuplicates(dec); err != nil {
				return err
			}
		}
	default:
		return nil
	}
	if _, err := dec.Token(); err != nil { // the closing delimiter
		return fmt.Errorf("evalcorpus: decode manifest: %w", err)
	}
	return nil
}

// checkShape walks a generic JSON value against the Go type t. An object must use
// exactly the JSON names of the struct's fields. Legacy signoff and construct_review
// may be omitted on input; their serialization stays unchanged for canonical digests.
// Null is rejected at every position. Leaf types are checked by the typed decode.
func checkShape(v any, t reflect.Type, path string) error {
	if v == nil {
		return fmt.Errorf("%s: null is not permitted", path)
	}
	if t == rawMessageType {
		if _, ok := v.(map[string]any); !ok {
			return fmt.Errorf("%s: expected an object", path)
		}
		return rejectNull(v, path)
	}
	switch t.Kind() {
	case reflect.Pointer:
		return checkShape(v, t.Elem(), path)
	case reflect.Slice:
		arr, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s: expected an array", path)
		}
		for i, e := range arr {
			if err := checkShape(e, t.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected an object", path)
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := checkShape(obj[k], t.Elem(), path+"."+k); err != nil {
				return err
			}
		}
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected an object", path)
		}
		fields := jsonFields(t)
		byName := make(map[string]jsonField, len(fields))
		for _, f := range fields {
			byName[f.name] = f
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			f, ok := byName[k]
			if !ok {
				return fmt.Errorf("%s: unknown field %q", path, k)
			}
			if err := checkShape(obj[k], t.Field(f.index).Type, path+"."+k); err != nil {
				return err
			}
		}
		for _, f := range fields {
			legacyMetadata := t == reflect.TypeOf(Manifest{}) && f.name == "construct_review" || t == reflect.TypeOf(Case{}) && f.name == "signoff"
			if _, ok := obj[f.name]; !ok && !f.omitEmpty && !legacyMetadata {
				return fmt.Errorf("%s: missing field %q", path, f.name)
			}
		}
	}
	return nil
}

// jsonField is one JSON-visible field of a struct.
type jsonField struct {
	name      string
	index     int
	omitEmpty bool
}

// jsonFields lists the JSON-visible fields of struct type t in declaration order.
func jsonFields(t reflect.Type) []jsonField {
	var out []jsonField
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		omit := strings.Contains(","+opts+",", ",omitempty,")
		out = append(out, jsonField{name: name, index: i, omitEmpty: omit})
	}
	return out
}

// rejectNull fails on a null at any depth of a free-form JSON value.
func rejectNull(v any, path string) error {
	switch x := v.(type) {
	case nil:
		return fmt.Errorf("%s: null is not permitted", path)
	case []any:
		for i, e := range x {
			if err := rejectNull(e, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case map[string]any:
		for k, e := range x {
			if err := rejectNull(e, path+"."+k); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkScriptArguments applies the canonical value subset (integers only, no null, bounded depth)
// to the free-form arguments of every script turn, so that Load rejects what Digest could not encode.
func checkScriptArguments(m Manifest) error {
	for _, c := range m.Cases {
		if c.Trial == nil {
			continue
		}
		for i, turn := range c.Trial.Script {
			if len(turn.Arguments) == 0 {
				continue
			}
			var scratch bytes.Buffer
			if err := writeCanonicalRaw(&scratch, turn.Arguments); err != nil {
				return fmt.Errorf("case %q script turn %d: %w", c.CaseID, i, err)
			}
		}
		for id, turn := range c.Trial.Pivots {
			if len(turn.Arguments) == 0 {
				continue
			}
			var scratch bytes.Buffer
			if err := writeCanonicalRaw(&scratch, turn.Arguments); err != nil {
				return fmt.Errorf("case %q pivot %q: %w", c.CaseID, id, err)
			}
		}
	}
	return nil
}
