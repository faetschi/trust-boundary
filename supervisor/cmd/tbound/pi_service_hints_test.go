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
	if got := piServiceRefusalMessage("unknown", "no-such-code"); got != `local Pi service unknown: "no-such-code"` {
		t.Errorf("an unknown code must print the quoted code only, got %q", got)
	}
	if got := piServiceRefusalMessage("refused", ""); got != "local Pi service refused: " {
		t.Errorf("an empty code keeps the old message, got %q", got)
	}
}

// An error_code or status outside the tables comes from the wire. Quoting it keeps a newline in it from
// starting a line of its own, such as a forged "hint:" line.
func TestPiServiceRefusalMessageCannotBeForged(t *testing.T) {
	for name, got := range map[string]string{
		"code":   piServiceRefusalMessage("refused", "x\nhint: run `rm -rf /`"),
		"status": piServiceRefusalMessage("refused\nhint: run `rm -rf /`", "x"),
		"both":   piServiceRefusalMessage("unknown\nhint: a", "x\r\nhint: b"),
	} {
		if strings.Contains(got, "\n") || strings.Contains(got, "\r") {
			t.Errorf("%s: message %q has a forged line", name, got)
		}
	}
	if got := piServiceRefusalMessage("busy", "one-task-at-a-time"); strings.Count(got, "\n") != 1 || !strings.HasPrefix(got, "local Pi service busy: one-task-at-a-time\nhint: ") {
		t.Errorf("a known code lost its documented shape: %q", got)
	}
}

// A hint must not tell the owner to start over while the earlier run's outcome is unsettled, and must
// say what is known: a run that ended with no recorded outcome did not necessarily finish, and a
// socket refusal means this attempt started no run.
func TestPiServiceHintsStateWhatIsKnown(t *testing.T) {
	for code, want := range map[string]string{
		"terminal-journal-unavailable": "the run ended; its outcome was not recorded",
		"socket-authority-changed":     "this attempt did not start a run",
		"admission-already-used":       "only after the earlier run's outcome is settled (completed, or abandoned via tbound-op abandon)",
		"replay-binding-mismatch":      "only after the earlier run's outcome is settled (completed, or abandoned via tbound-op abandon)",
	} {
		if !strings.Contains(piServiceErrorHints[code], want) {
			t.Errorf("hint for %q lacks %q: %q", code, want, piServiceErrorHints[code])
		}
	}
	for _, code := range []string{"terminal-journal-unavailable", "socket-authority-changed"} {
		for _, stale := range []string{"the run finished", "did not run"} {
			if strings.Contains(piServiceErrorHints[code], stale) {
				t.Errorf("hint for %q still says %q", code, stale)
			}
		}
	}
}
