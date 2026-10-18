package evalcorpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// CanonicalJSON returns the canonical form of m: RFC 8785 for this schema's value
// subset (strings, booleans, arrays, objects, and non-negative integers up to 2^53-1,
// which are written as their decimal digits). Object members
// are sorted by UTF-16 code units, there is no insignificant whitespace, and strings use
// only the RFC 8785 escapes. A nil array canonicalizes as []. Array order is kept.
// Invalid UTF-8 is an error.
func CanonicalJSON(m Manifest) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, reflect.ValueOf(m)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Digest returns "sha256:" followed by 64 lowercase hex digits: the SHA-256 of
// CanonicalJSON(m). The manifest does not contain its own digest.
func Digest(m Manifest) (string, error) {
	raw, err := CanonicalJSON(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

func writeCanonical(buf *bytes.Buffer, v reflect.Value) error {
	if v.Type() == rawMessageType {
		return writeCanonicalRaw(buf, v.Bytes())
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return errors.New("evalcorpus: nil value without omitempty in canonical form")
		}
		return writeCanonical(buf, v.Elem())
	case reflect.Struct:
		type member struct {
			name  string
			value reflect.Value
		}
		var members []member
		for _, f := range jsonFields(v.Type()) {
			fv := v.Field(f.index)
			if f.omitEmpty && (fv.IsZero() || (fv.Kind() == reflect.Map && fv.Len() == 0)) {
				continue
			}
			members = append(members, member{name: f.name, value: fv})
		}
		sort.Slice(members, func(i, j int) bool {
			return utf16Less(members[i].name, members[j].name)
		})
		buf.WriteByte('{')
		for i, mem := range members {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonicalString(buf, mem.name); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := writeCanonical(buf, mem.value); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case reflect.Slice:
		buf.WriteByte('[')
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, v.Index(i)); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("evalcorpus: map with %s keys in canonical form", v.Type().Key())
		}
		keys := make([]string, 0, v.Len())
		for _, key := range v.MapKeys() {
			keys = append(keys, key.String())
		}
		sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
		buf.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonicalString(buf, key); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := writeCanonical(buf, v.MapIndex(reflect.ValueOf(key).Convert(v.Type().Key()))); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case reflect.String:
		return writeCanonicalString(buf, v.String())
	case reflect.Uint64:
		// RFC 8785 writes an integer as its decimal digits. Above 2^53-1 other JSON
		// implementations disagree on the value, so there is no interoperable form.
		if v.Uint() > MaxTrialNumber {
			return fmt.Errorf("evalcorpus: integer %d exceeds 2^53-1 in canonical form", v.Uint())
		}
		buf.WriteString(strconv.FormatUint(v.Uint(), 10))
	case reflect.Bool:
		if v.Bool() {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	default:
		return fmt.Errorf("evalcorpus: unsupported type %s in canonical form", v.Type())
	}
	return nil
}

// writeCanonicalString writes s as an RFC 8785 string. Quote and backslash are escaped.
// U+0008, U+0009, U+000A, U+000C, and U+000D use their short escapes, other controls
// below U+0020 use \u00xx in lowercase hex, and everything else is written as UTF-8.
func writeCanonicalString(buf *bytes.Buffer, s string) error {
	if !utf8.ValidString(s) {
		return errors.New("evalcorpus: string is not valid UTF-8")
	}
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, r)
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
	return nil
}

// utf16Less orders strings by UTF-16 code units, as RFC 8785 requires for member names.
func utf16Less(a, b string) bool {
	ua := utf16.Encode([]rune(a))
	ub := utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}
