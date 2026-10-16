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
//   - a field without omitempty that is absent, and null anywhere;
//   - data after the top-level value.
//
// Load checks shape only. Use Validate for the registered counts, quotas, and rules.
// Known limit: encoding/json replaces an unpaired \u surrogate escape with U+FFFD, and
// Load does not reject that input, so two manifests that differ only in such escapes
// decode to the same digest.
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
// exactly the JSON names of the struct's fields. A field without omitempty is required.
// Null is rejected at every position. Leaf types are checked by the typed decode.
func checkShape(v any, t reflect.Type, path string) error {
	if v == nil {
		return fmt.Errorf("%s: null is not permitted", path)
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
			if _, ok := obj[f.name]; !ok && !f.omitEmpty {
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
