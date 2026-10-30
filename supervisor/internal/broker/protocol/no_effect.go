package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
)

// ResultStatusNoEffectRefused marks an ALLOWed call that the executor refused before any effect. Its output is
// always the fixed refusal below, never executor output.
const ResultStatusNoEffectRefused = "no_effect_refused"

// noEffectRefusalOutput is fixed in size and text, so no argument, path or executor error reaches the model.
const noEffectRefusalOutput = `{"code":"invalid_arguments","message":"The tool arguments are not valid. Check the tool schema and send a corrected call."}`

// NoEffectError marks a call whose arguments the durable executor refused before any effect. Only the executor
// creates it, after it reproduces the refusal from the retained arguments. Err keeps the local cause for callers
// and tests; the message never repeats it.
type NoEffectError struct {
	Tool string
	Err  error
}

func (e *NoEffectError) Error() string { return e.Tool + " arguments refused before any effect" }

func (e *NoEffectError) Unwrap() error { return e.Err }

// NoEffectResult returns base with the fixed refusal status and output. It validates the result with the ordinary
// Result checks and changes no verdict, reason, identity, or policy field.
func NoEffectResult(base Result) (Result, error) {
	if base.Status != "" || len(base.Output) != 0 {
		return Result{}, errors.New("a no-effect refusal must start from a Result without status or output")
	}
	result := base
	result.Status = ResultStatusNoEffectRefused
	result.Output = json.RawMessage(noEffectRefusalOutput)
	if _, err := MarshalResult(result); err != nil {
		return Result{}, err
	}
	return result, nil
}

// NoEffectResultMatches reports whether encoded is exactly the no-effect refusal that result's identity produces.
// It ignores result's status and output, so a decoded record is checked against the fixed bytes it claims.
func NoEffectResultMatches(result Result, encoded []byte) bool {
	base := result
	base.Status, base.Output = "", nil
	fixed, err := NoEffectResult(base)
	if err != nil {
		return false
	}
	fixedBytes, err := MarshalResult(fixed)
	return err == nil && bytes.Equal(fixedBytes, encoded)
}
