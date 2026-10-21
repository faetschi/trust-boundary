package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// serviceErrorCodes reads every error_code the service can answer from the sources that
// produce them, so that a new code without a hint fails this test.
func serviceErrorCodes(t *testing.T) []string {
	t.Helper()
	patterns := map[string][]*regexp.Regexp{
		"pi_service_handoff_linux.go": {
			regexp.MustCompile(`ErrorCode:\s*"([a-z][a-z-]*)"`),
			regexp.MustCompile(`(?m)\berrorCode\s*:?=\s*"([a-z][a-z-]*)"\s*$`),
			regexp.MustCompile(`"busy",\s*"([a-z][a-z-]*)"`),
			regexp.MustCompile(`"unknown",\s*"([a-z][a-z-]*)"`),
		},
		"production_corpus_admission_linux.go": {
			regexp.MustCompile(`return\s+"([a-z]+(?:-[a-z]+)+)"`),
			regexp.MustCompile(`replayBindingMismatch\s*=\s*"([a-z-]+)"`),
		},
	}
	seen := map[string]bool{}
	var codes []string
	for file, res := range patterns {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, re := range res {
			for _, m := range re.FindAllStringSubmatch(string(data), -1) {
				if !seen[m[1]] {
					seen[m[1]] = true
					codes = append(codes, m[1])
				}
			}
		}
	}
	if len(codes) < 20 {
		t.Fatalf("found only %d error codes in the sources: %v", len(codes), codes)
	}
	return codes
}

func TestEveryServiceErrorCodeHasAHint(t *testing.T) {
	for _, code := range serviceErrorCodes(t) {
		if piServiceErrorHints[code] == "" {
			t.Errorf("error_code %q has no hint in piServiceErrorHints", code)
		}
	}
}

func TestHintTableHasNoStaleCodes(t *testing.T) {
	live := map[string]bool{}
	for _, code := range serviceErrorCodes(t) {
		live[code] = true
	}
	for code := range piServiceErrorHints {
		if !live[code] {
			t.Errorf("piServiceErrorHints[%q] names a code the service no longer answers", code)
		}
	}
}

func TestPiServiceRefusalMessage(t *testing.T) {
	got := piServiceRefusalMessage("refused", "prior-workflow-unsettled")
	for _, want := range []string{"local Pi service refused: prior-workflow-unsettled\n", "hint: ", "settlement check", "abandon-unknown"} {
		if !strings.Contains(got, want) {
			t.Errorf("message %q lacks %q", got, want)
		}
	}
	// The first line keeps the documented "local Pi service <status>: <error_code>" shape.
	if first, _, _ := strings.Cut(got, "\n"); first != "local Pi service refused: prior-workflow-unsettled" {
		t.Errorf("first line = %q", first)
	}
	if got := piServiceRefusalMessage("unknown", "no-such-code"); got != "local Pi service unknown: no-such-code" {
		t.Errorf("an unknown code must print the code only, got %q", got)
	}
	if got := piServiceRefusalMessage("refused", ""); got != "local Pi service refused: " {
		t.Errorf("an empty code keeps the old message, got %q", got)
	}
}
