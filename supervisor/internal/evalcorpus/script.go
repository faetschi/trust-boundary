package evalcorpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The registered targeted configuration deltas of the P half of a pair. The set is closed and
// code-defined. "none" (or an empty p_delta) means P runs the full control package. net-sink/v1
// and no-reap/v1 are registered names that the governed service refuses until it implements them.
const (
	PDeltaNone     = "none"
	PDeltaAdmitAll = "admit-all/v1"
	PDeltaNetSink  = "net-sink/v1"
	PDeltaNoReap   = "no-reap/v1"
)

// ValidPDelta reports whether d is a registered delta name. The empty string is not a name; it is
// the absent member and means PDeltaNone (Trial.EffectivePDelta).
func ValidPDelta(d string) bool {
	switch d {
	case PDeltaNone, PDeltaAdmitAll, PDeltaNetSink, PDeltaNoReap:
		return true
	}
	return false
}

// EffectivePDelta is the registered delta of the trial: PDeltaNone when p_delta is absent.
func (t Trial) EffectivePDelta() string {
	if t.PDelta == "" {
		return PDeltaNone
	}
	return t.PDelta
}

// Bounds of the frozen stimulus. They keep a manifest of 299 scripted cases far below
// MaxManifestBytes and a script small enough to journal in a provider transcript.
const (
	MaxScriptTurns              = 16
	MaxScriptArgumentBytes      = 8 << 10
	MaxScriptTextBytes          = 4 << 10
	MaxScriptBytes              = 32 << 10
	MaxProtectedPaths           = 32
	MaxProtectedPathBytes       = 256
	MaxPDeltaJustificationBytes = 2 << 10
)

// CalibrationPayloadSchema names the canonical form that CalibrationPayloadDigest commits to.
// Adding the pivots member of increment E is a schema bump: the payload then names
// "tbound-calibration-payload/v2", so a v1 digest can never equal a v2 digest of the same script.
const CalibrationPayloadSchema = "tbound-calibration-payload/v1"

// CalibrationPayloadDigest is the calibration_payload_digest of a scripted case: "sha256:" and the
// SHA-256 of the RFC 8785 canonical form (this package's value subset) of
//
//	{"p_delta": <effective delta>, "schema": "tbound-calibration-payload/v1", "script": <turns>}
//
// An empty p_delta digests as "none". A script that is not admissible (CheckStimulus) has no digest.
func CalibrationPayloadDigest(script []ScriptTurn, pDelta string) (string, error) {
	if pDelta == "" {
		pDelta = PDeltaNone
	}
	if !ValidPDelta(pDelta) {
		return "", fmt.Errorf("evalcorpus: p_delta %q is not a registered delta", pDelta)
	}
	if err := checkScript(script); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	buf.WriteString(`{"p_delta":`)
	if err := writeCanonicalString(&buf, pDelta); err != nil {
		return "", err
	}
	buf.WriteString(`,"schema":`)
	if err := writeCanonicalString(&buf, CalibrationPayloadSchema); err != nil {
		return "", err
	}
	buf.WriteString(`,"script":`)
	if err := writeCanonical(&buf, reflect.ValueOf(script)); err != nil {
		return "", err
	}
	buf.WriteByte('}')
	sum := sha256.Sum256(buf.Bytes())
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// CheckStimulus reports the first reason the stimulus members of t are not admissible: the script,
// the delta with its justification, and the protected paths. It does not look at the case kind or the
// calibration digest (CheckCalibrationDigest).
func (t Trial) CheckStimulus() error {
	if err := checkScript(t.Script); err != nil {
		return err
	}
	// Scripted turns count as host calls: one per turn, and one more for the final text turn that
	// the replay appends when the script does not end with its own.
	if n := len(t.Script); n > 0 {
		needed := uint64(n)
		if t.Script[n-1].Tool != "" {
			needed++
		}
		if t.MaxHostCalls < needed {
			return fmt.Errorf("max_host_calls %d is below the %d host calls of the script", t.MaxHostCalls, needed)
		}
	}
	delta := t.EffectivePDelta()
	if !ValidPDelta(delta) {
		return fmt.Errorf("p_delta %q is not a registered delta", t.PDelta)
	}
	just := strings.TrimSpace(t.PDeltaJustification)
	switch {
	case delta == PDeltaNone && t.PDeltaJustification != "":
		return errors.New("p_delta_justification is set but there is no p_delta")
	case delta != PDeltaNone && just == "":
		return fmt.Errorf("p_delta %s needs a p_delta_justification", delta)
	case len(t.PDeltaJustification) > MaxPDeltaJustificationBytes || !utf8.ValidString(t.PDeltaJustification) ||
		strings.ContainsRune(t.PDeltaJustification, 0):
		return fmt.Errorf("p_delta_justification exceeds %d bytes or is not valid text", MaxPDeltaJustificationBytes)
	case delta != PDeltaNone && len(t.Script) == 0:
		return fmt.Errorf("p_delta %s is registered for a trial without a script", delta)
	}
	return checkProtectedPaths(t.ProtectedPaths)
}

// HasStimulus reports whether any member that only an adversarial case may carry is set.
func (t Trial) HasStimulus() bool {
	return len(t.Script) != 0 || t.PDelta != "" || t.PDeltaJustification != "" || len(t.ProtectedPaths) != 0
}

// CheckCalibrationDigest requires an adversarial case with a script to carry the calibration payload
// digest that its script and delta produce. A case without a script keeps a free-form digest.
func CheckCalibrationDigest(c Case) error {
	if c.Trial == nil || len(c.Trial.Script) == 0 {
		return nil
	}
	if c.Adversarial == nil {
		return errors.New("a scripted case has no adversarial payload")
	}
	want, err := CalibrationPayloadDigest(c.Trial.Script, c.Trial.PDelta)
	if err != nil {
		return err
	}
	if c.Adversarial.CalibrationPayloadDigest != want {
		return fmt.Errorf("calibration_payload_digest is %q; the script and p_delta digest to %q",
			c.Adversarial.CalibrationPayloadDigest, want)
	}
	return nil
}

func checkScript(script []ScriptTurn) error {
	if len(script) > MaxScriptTurns {
		return fmt.Errorf("script has %d turns; at most %d", len(script), MaxScriptTurns)
	}
	total := 0
	for i, turn := range script {
		label := fmt.Sprintf("script turn %d", i)
		total += len(turn.Tool) + len(turn.Arguments) + len(turn.Text)
		switch {
		case turn.Tool != "":
			if !knownTool(Tool(turn.Tool)) {
				return fmt.Errorf("%s: tool %q is not read, write, edit, or bash", label, turn.Tool)
			}
			if turn.Text != "" {
				return fmt.Errorf("%s: a tool turn has no text", label)
			}
			if len(turn.Arguments) == 0 || len(turn.Arguments) > MaxScriptArgumentBytes {
				return fmt.Errorf("%s: arguments are absent or exceed %d bytes", label, MaxScriptArgumentBytes)
			}
			if trimmed := bytes.TrimSpace(turn.Arguments); len(trimmed) == 0 || trimmed[0] != '{' {
				return fmt.Errorf("%s: arguments are not a JSON object", label)
			}
			var canonical bytes.Buffer
			if err := writeCanonicalRaw(&canonical, turn.Arguments); err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
		default:
			if len(turn.Arguments) != 0 {
				return fmt.Errorf("%s: a text turn has no arguments", label)
			}
			if strings.TrimSpace(turn.Text) == "" || len(turn.Text) > MaxScriptTextBytes ||
				!utf8.ValidString(turn.Text) || strings.ContainsRune(turn.Text, 0) {
				return fmt.Errorf("%s: text is empty, exceeds %d bytes or is not valid text", label, MaxScriptTextBytes)
			}
			if i != len(script)-1 {
				return fmt.Errorf("%s: the text turn must be the last turn", label)
			}
		}
	}
	if total > MaxScriptBytes {
		return fmt.Errorf("script is %d bytes; at most %d", total, MaxScriptBytes)
	}
	return nil
}

func checkProtectedPaths(paths []string) error {
	if len(paths) > MaxProtectedPaths {
		return fmt.Errorf("protected_paths has %d entries; at most %d", len(paths), MaxProtectedPaths)
	}
	for i, p := range paths {
		if !validProtectedPath(p) {
			return fmt.Errorf("protected_paths[%d] %q is not a relative canonical path", i, p)
		}
		if i > 0 && paths[i-1] >= p {
			return fmt.Errorf("protected_paths[%d] %q is not strictly after %q (sorted, no duplicates)", i, p, paths[i-1])
		}
	}
	return nil
}

// validProtectedPath is the relative canonical path rule of the session repository: non-empty,
// valid UTF-8, no leading slash or backslash, no empty, "." or ".." segment, no control character.
func validProtectedPath(p string) bool {
	if p == "" || len(p) > MaxProtectedPathBytes || !utf8.ValidString(p) || strings.HasPrefix(p, "/") ||
		strings.ContainsRune(p, '\\') {
		return false
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, r := range segment {
			if unicode.IsControl(r) {
				return false
			}
		}
	}
	return true
}

// writeCanonicalRaw writes one JSON value in this package's canonical subset: objects with
// members sorted by UTF-16 code units and no repeated key, arrays, strings, booleans, and
// non-negative integers up to 2^53-1 in plain decimal form. null, fractions, exponents, negative
// numbers and anything deeper than eight levels are refused.
func writeCanonicalRaw(buf *bytes.Buffer, raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("arguments are not valid UTF-8")
	}
	if err := scanDuplicates(json.NewDecoder(bytes.NewReader(raw))); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return fmt.Errorf("arguments are not valid JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("arguments have trailing data")
	}
	return writeCanonicalAny(buf, value, 0)
}

func writeCanonicalAny(buf *bytes.Buffer, value any, depth int) error {
	if depth > 8 {
		return errors.New("arguments are nested deeper than 8 levels")
	}
	switch v := value.(type) {
	case nil:
		return errors.New("null is not permitted in arguments")
	case bool:
		buf.WriteString(strconv.FormatBool(v))
	case string:
		return writeCanonicalString(buf, v)
	case json.Number:
		s := v.String()
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil || strconv.FormatUint(n, 10) != s || n > MaxTrialNumber {
			return fmt.Errorf("number %q is not a non-negative integer up to 2^53-1", s)
		}
		buf.WriteString(s)
	case []any:
		buf.WriteByte('[')
		for i, e := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonicalAny(buf, e, depth+1); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonicalString(buf, k); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := writeCanonicalAny(buf, v[k], depth+1); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("unsupported JSON value %T", value)
	}
	return nil
}

// ScriptDigest is "sha256:" and the SHA-256 of the canonical form of the turn list alone. A governed
// service reports it in its receipt so that the host can see which frozen stimulus a run replayed.
func ScriptDigest(script []ScriptTurn) (string, error) {
	if err := checkScript(script); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, reflect.ValueOf(script)); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
