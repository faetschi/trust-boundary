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

// RoleAdaptiveReplay is the governed role whose admission id is the longest of a case.
const RoleAdaptiveReplay = "adaptive-replay"

// Ceilings are the host ceilings of a signed service config. A trial budget above any of
// them is not admitted.
type Ceilings struct {
	MaxHostCalls    uint64
	MaxTokens       uint64
	DurationSeconds uint64
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
			!BoundedIdentity(AdmissionID(c.CaseID, RoleAdaptiveReplay, 2), RequestIDMaxBytes) ||
			!BoundedPrompt(c.Trial.Prompt) {
			return fmt.Errorf("case %q is not a bounded task identity or prompt", c.CaseID)
		}
		if ceilings != nil && (c.Trial.MaxHostCalls > ceilings.MaxHostCalls || c.Trial.MaxTokens > ceilings.MaxTokens ||
			c.Trial.DurationSeconds > ceilings.DurationSeconds) {
			return fmt.Errorf("case %q budget exceeds the host ceilings of the signed config", c.CaseID)
		}
	}
	return nil
}
