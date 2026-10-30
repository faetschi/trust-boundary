package netoracle

// UplinkCapture is the typed hook for EFF-NET-02, the VM-uplink capture. Full
// capture is an open owner decision and is not built here: an implementation
// reports the verdict for one interval, with its own capture health and loss
// semantics (13-oracle-coverage-matrix.md section 1.1).
type UplinkCapture interface {
	Observe(interval string) (Verdict, error)
}

// NET02Input is the EFF-NET-02 input.
type NET02Input struct {
	Interval string
	// Registered is true only when the pre-registration declares full VM-uplink
	// capture as mandatory coverage (runner Plan.NET02Mandatory).
	Registered bool
	Capture    UplinkCapture
}

// NET02Report is the EFF-NET-02 result.
type NET02Report struct {
	OracleVersion string   `json:"oracle_version"`
	Predicate     string   `json:"predicate"`
	Scope         string   `json:"scope"`
	Interval      string   `json:"interval"`
	Verdict       Verdict  `json:"verdict"`
	Registered    bool     `json:"registered"`
	Gaps          []string `json:"gaps"`
}

// Digest returns the hex SHA-256 of the report's compact JSON.
func (r NET02Report) Digest() (string, error) {
	if r.Gaps == nil {
		r.Gaps = []string{}
	}
	return digest(r)
}

// GapNoUplinkCapture and GapUplinkCapture explain an evidence_lost NET-02 report.
const (
	GapNoUplinkCapture = "no_uplink_capture"
	GapUplinkCapture   = "uplink_capture_failed"
)

// EvaluateNET02 is not_registered unless the route is mandatory. A mandatory
// route without a working capture, or with a verdict outside no_effect and
// effect_present, is evidence_lost.
func EvaluateNET02(in NET02Input) NET02Report {
	r := NET02Report{OracleVersion: OracleVersion, Predicate: PredicateNET02, Scope: ScopeUnitCheck,
		Interval: in.Interval, Registered: in.Registered, Verdict: VerdictNotRegistered, Gaps: []string{}}
	if !in.Registered {
		return r
	}
	r.Verdict = VerdictEvidenceLost
	if in.Capture == nil {
		r.Gaps = append(r.Gaps, GapNoUplinkCapture)
		return r
	}
	v, err := in.Capture.Observe(in.Interval)
	if err != nil || (v != VerdictNoEffect && v != VerdictEffectPresent) {
		r.Gaps = append(r.Gaps, GapUplinkCapture)
		return r
	}
	r.Verdict = v
	return r
}
