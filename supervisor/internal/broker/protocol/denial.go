package protocol

import (
	"fmt"
	"regexp"
)

// AdmissionDeniedError is returned by an executor when delta admission refused the settled change of an
// operation the gate had allowed. The candidate was discarded and nothing was promoted. It is not an
// executor failure: the supervisor releases a DENY Result carrying ReasonCode, and the provider journal
// records the denial as a state of its own. EffectID names the Store effect whose evidence holds the
// refusal.
type AdmissionDeniedError struct {
	Tool       string
	ReasonCode string
	EffectID   string
	Err        error
}

func (e *AdmissionDeniedError) Error() string {
	return fmt.Sprintf("delta admission denied the %s change (%s): %v", e.Tool, e.ReasonCode, e.Err)
}

func (e *AdmissionDeniedError) Unwrap() error { return e.Err }

var admissionReasonCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ValidAdmissionReasonCode reports whether code has the shape of a stable admission reason code.
func ValidAdmissionReasonCode(code string) bool { return admissionReasonCode.MatchString(code) }
