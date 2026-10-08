// Package install implements the user-facing "install TBound on Linux"
// workflow: host preflight, a managed, pinned runtime bundle, host-profile
// verification, isolation planning, broker-credential templating, and safe
// uninstall.
//
// Everything here is deliberately fail-closed. A check that cannot be evaluated
// (UNKNOWN) never counts as a pass, and an unsupported platform (non-Linux) is
// reported as unsupported rather than silently succeeding. Nothing in this
// package performs privileged operations; the operator-gated steps (signing,
// provisioning /etc/tbound, building the signed cell image) are reported as
// remediation, not performed.
package install

import (
	"fmt"
	"strings"
)

// Status is the result of a single preflight or verification check.
type Status string

const (
	StatusPass        Status = "pass"
	StatusFail        Status = "fail"
	StatusUnsupported Status = "unsupported"
	StatusUnknown     Status = "unknown"
)

// Result is one named check outcome.
type Result struct {
	Name        string `json:"name"`
	Status      Status `json:"status"`
	Detail      string `json:"detail,omitempty"`
	Remediation string `json:"remediation,omitempty"`
}

// Report aggregates preflight results. Ready is true only when every check
// passed; unsupported or unknown checks never yield Ready.
type Report struct {
	Results []Result `json:"results"`
	Ready   bool     `json:"ready"`
}

// Add appends a check result.
func (r *Report) Add(name string, status Status, detail, remediation string) {
	r.Results = append(r.Results, Result{Name: name, Status: status, Detail: detail, Remediation: remediation})
}

// Pass records a passing check.
func (r *Report) Pass(name, detail string) { r.Add(name, StatusPass, detail, "") }

// Fail records a failing check with remediation guidance.
func (r *Report) Fail(name, detail, remediation string) {
	r.Add(name, StatusFail, detail, remediation)
}

// Unsupported records a platform-unsupported check.
func (r *Report) Unsupported(name, detail, remediation string) {
	r.Add(name, StatusUnsupported, detail, remediation)
}

// Unknown records a check that could not be evaluated.
func (r *Report) Unknown(name, detail, remediation string) {
	r.Add(name, StatusUnknown, detail, remediation)
}

// Finalize recomputes Ready from the collected results.
func (r *Report) Finalize() {
	r.Ready = len(r.Results) > 0
	for _, res := range r.Results {
		if res.Status != StatusPass {
			r.Ready = false
			return
		}
	}
}

// Failures returns the names of non-passing checks for concise error messages.
func (r *Report) Failures() []string {
	var names []string
	for _, res := range r.Results {
		if res.Status != StatusPass {
			names = append(names, string(res.Status)+":"+res.Name)
		}
	}
	return names
}

// Summary renders a compact human-readable report.
func (r *Report) Summary() string {
	var b strings.Builder
	for _, res := range r.Results {
		fmt.Fprintf(&b, "[%s] %s", res.Status, res.Name)
		if res.Detail != "" {
			fmt.Fprintf(&b, ": %s", res.Detail)
		}
		if res.Status != StatusPass && res.Remediation != "" {
			fmt.Fprintf(&b, " -> %s", res.Remediation)
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "ready: %t", r.Ready)
	return b.String()
}

// ErrNotReady indicates one or more preflight checks did not pass.
type ErrNotReady struct{ Failures []string }

func (e ErrNotReady) Error() string {
	return "preflight not ready: " + strings.Join(e.Failures, ", ")
}

// RequireReady returns ErrNotReady when the report is not fully passing.
func (r *Report) RequireReady() error {
	if r.Ready {
		return nil
	}
	return ErrNotReady{Failures: r.Failures()}
}
