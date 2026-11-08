// Package corpusadmit holds the per-case trial admission rules that the corpus-mode
// service (cmd/tbound) and the offline corpus tooling (cmd/tbound-profile) must apply
// identically: a manifest is admitted only if every case carries a trial whose identity,
// prompt and budgets are bounded. A digest printed by the tooling is only useful if the
// service admits the manifest, so the rules live here once.
package corpusadmit

import (
	"fmt"
	"strconv"
	"strings"

	"tbound/supervisor/internal/evalcorpus"
)

// Bounds of a production task identity, request identity and prompt.
const (
	TaskIDMaxBytes    = 256
	RequestIDMaxBytes = 256
	PromptMaxBytes    = 1 << 16
)

// RoleAdaptiveReplay is the governed role of the C half of the adaptive pair. RolePStaticReplay and
// RolePAdaptiveReplay are the roles of the P half (Configuration P under its registered delta).
// RolePAdaptiveReplay gives the longest admission id of a case. RoleBCompliant and RoleBAmbiguous are the
// roles of Configuration B (the real model through the same containment and proxy tool path, an allow-all
// gate and admit-all/v1 admission); their admission ids never collide with those of C.
const (
	RoleAdaptiveReplay  = "adaptive-replay"
	RolePStaticReplay   = "p-static-replay"
	RolePAdaptiveReplay = "p-adaptive-replay"
	RoleBCompliant      = "b-compliant"
	RoleBAmbiguous      = "b-ambiguous"
)

// Ceilings are the host ceilings of a signed service config. A trial budget above any of
// them is not admitted.
type Ceilings struct {
	MaxHostCalls    uint64
	MaxTokens       uint64
	DurationSeconds uint64
	// MaxPreEffectRetries is the signed config's max_pre_effect_retries. A pre-effect provider
	// retry re-dispatches the provider turn, so it reserves one more host call: a scripted trial's
	// budget must cover its script plus one host call per retry. Zero when the caller has no
	// service config to read (then the script's own minimum applies, and the service is the
	// authority on the margin).
	MaxPreEffectRetries uint64
	// ModelTokensPerCall is the per-call completion cap of the provider route. Every provider call books it in
	// full, so a trial that a model role runs needs MaxTokens >= MaxHostCalls x this cap. Zero skips the check
	// (a caller without a service config to read).
	ModelTokensPerCall uint64
}

// ThesisModelTokensPerCall is the per-call completion cap of the thesis governed and corpus path (decision D50:
// reasoning on, 4,096). The service and the corpus-check tool both admit against it.
const ThesisModelTokensPerCall uint64 = 4096

// RoleModelDriven reports whether a governed role calls the provider (trajectory, the variance repeats, the
// compliant and ambiguous roles and Configuration B) or replays a script or an attacker machine (the static,
// adaptive and P replay roles), which have no token dimension of their own.
func RoleModelDriven(role string) bool {
	switch role {
	case "trajectory", "variance-1", "variance-2", "compliant", "ambiguous", RoleBCompliant, RoleBAmbiguous:
		return true
	}
	return false
}

// caseModelDriven reports whether a case of this kind is run by a model role: an adversarial case by trajectory,
// a compliant or ambiguous case by its compliant or ambiguous role and Configuration B. Every registered kind is.
func caseModelDriven(kind evalcorpus.Kind) bool {
	return kind == evalcorpus.KindAdversarial || kind == evalcorpus.KindCompliant || kind == evalcorpus.KindAmbiguous
}

// AdmissionID is the one-use admission id of a trial: <case_id>/<role>/<attempt>.
func AdmissionID(caseID, role string, attempt int) string {
	return caseID + "/" + role + "/" + strconv.Itoa(attempt)
}

// BoundedIdentity reports whether value is a non-empty identity without surrounding
// space, control line breaks or NUL, of at most maximum bytes.
func BoundedIdentity(value string, maximum int) bool {
	return value != "" && strings.TrimSpace(value) == value && len([]byte(value)) <= maximum && !strings.ContainsAny(value, "\x00\r\n")
}

// BoundedPrompt reports whether value is a non-blank prompt without NUL, of at most
// PromptMaxBytes bytes.
func BoundedPrompt(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && len([]byte(value)) <= PromptMaxBytes && !strings.ContainsRune(value, 0)
}

// CheckTrials returns the first case without an admissible trial: no trial, an identity
// that is not a bounded task identity or whose longest admission id is not a bounded
// request identity, an unbounded prompt, or, when ceilings is not nil, a budget above a
// ceiling. A nil ceilings skips only the ceiling comparison.
func CheckTrials(cases []evalcorpus.Case, ceilings *Ceilings) error {
	for _, c := range cases {
		if c.Trial == nil {
			return fmt.Errorf("case %q has no trial", c.CaseID)
		}
		if !BoundedIdentity(c.CaseID, TaskIDMaxBytes) ||
			!BoundedIdentity(AdmissionID(c.CaseID, RolePAdaptiveReplay, 2), RequestIDMaxBytes) ||
			!BoundedPrompt(c.Trial.Prompt) {
			return fmt.Errorf("case %q is not a bounded task identity or prompt", c.CaseID)
		}
		if c.Trial.HasStimulus() {
			if c.Kind != evalcorpus.KindAdversarial {
				return fmt.Errorf("case %q is not adversarial but carries a script, p_delta or protected_paths", c.CaseID)
			}
			if err := c.Trial.CheckStimulus(); err != nil {
				return fmt.Errorf("case %q: %w", c.CaseID, err)
			}
			if err := evalcorpus.CheckCalibrationDigest(c); err != nil {
				return fmt.Errorf("case %q: %w", c.CaseID, err)
			}
		}
		if ceilings != nil && len(c.Trial.Script) != 0 {
			// The signed service config allows one pre-effect provider retry per host call, and a
			// retry re-dispatches the turn: the budget must cover the script and the retries.
			if needed := c.Trial.RequiredHostCalls() + ceilings.MaxPreEffectRetries; c.Trial.MaxHostCalls < needed {
				return fmt.Errorf("case %q: max_host_calls %d is below the %d host calls of the script and the %d pre-effect retries",
					c.CaseID, c.Trial.MaxHostCalls, c.Trial.RequiredHostCalls(), ceilings.MaxPreEffectRetries)
			}
		}
		if ceilings != nil && (c.Trial.MaxHostCalls > ceilings.MaxHostCalls || c.Trial.MaxTokens > ceilings.MaxTokens ||
			c.Trial.DurationSeconds > ceilings.DurationSeconds) {
			return fmt.Errorf("case %q budget exceeds the host ceilings of the signed config", c.CaseID)
		}
		if ceilings != nil && ceilings.ModelTokensPerCall != 0 && caseModelDriven(c.Kind) {
			if need := c.Trial.MaxHostCalls * ceilings.ModelTokensPerCall; c.Trial.MaxTokens < need {
				return fmt.Errorf("case %q: max_tokens %d is below the %d a model-driven trial needs (max_host_calls %d x %d completion tokens per call)",
					c.CaseID, c.Trial.MaxTokens, need, c.Trial.MaxHostCalls, ceilings.ModelTokensPerCall)
			}
		}
	}
	return nil
}
