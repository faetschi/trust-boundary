package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tbound/supervisor/internal/runbooklint"
)

// runbookRepoRoot is the repository root, three levels above this package.
const runbookRepoRoot = "../../.."

// runbookRequiredFlags are the flags the code refuses a subcommand without.
// TestRunbookRequiredFlagsAreEnforced fails if the code stops refusing one. corpus-digest's panel
// flags are optional, so they are not listed; corpus_digest_linux_test.go tests their all-or-none rule.
var runbookRequiredFlags = map[string][]string{
	"corpus-digest":       {"manifest"},
	"managed-root-verify": {"registration", "sig", "pub"},
}

// runbookProfileCall runs a tbound-profile command line through the dispatcher that main uses.
func runbookProfileCall(args ...string) error {
	return run(args, io.Discard, io.Discard)
}

// runbookSubcommand returns the words that name the subcommand. guest-evidence takes a kind after
// it, and a bare "guest-evidence" in prose names the group, not a command.
func runbookSubcommand(words []string) ([]string, bool) {
	if len(words) == 0 {
		return nil, false
	}
	if words[0] == "guest-evidence" {
		if len(words) < 2 {
			return nil, false
		}
		return append([]string{}, words[:2]...), true
	}
	return []string{words[0]}, true
}

// runbookPilotNeedsNode reports whether the pilot artifacts declare node_test acceptance specs.
// corpusadmit refuses those unless corpus-digest gets a node binary, so --node is then required.
func runbookPilotNeedsNode(t *testing.T) bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(runbookRepoRoot, "corpus", "pilot", "artifacts", "accept-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"node_test"`) {
			return true
		}
	}
	return false
}

// runbookProfileLint holds what the tbound-profile checks read from the code. kinds are the
// guest-evidence kinds the dispatch accepts. needNode is set when the pilot artifacts declare
// node_test specs, which corpusadmit refuses without a node binary.
type runbookProfileLint struct {
	kinds    map[string]bool
	needNode bool
}

// problems returns what is wrong with one tbound-profile command. The probes parse "--flag=x -h"
// through the real FlagSet of the subcommand and never run it.
func (l runbookProfileLint) problems(c runbooklint.Command) []string {
	where := fmt.Sprintf("%s:%d", c.File, c.Line)
	sub, ok := runbookSubcommand(c.Words)
	if !ok {
		return nil
	}
	name := strings.Join(sub, " ")
	if sub[0] == "guest-evidence" {
		if !l.kinds[sub[1]] {
			return []string{fmt.Sprintf("%s: %q is not a guest-evidence kind", where, sub[1])}
		}
		if sub[1] == netSinkProbeVerb {
			// Calling this kind runs its probe, so it is never probed. It takes no flags.
			var out []string
			for _, f := range runbooklint.Flags(c.Words) {
				out = append(out, fmt.Sprintf("%s: %s takes no --%s flag", where, name, f))
			}
			return out
		}
	}
	if !runbooklint.ParseReached(runbookProfileCall(append(append([]string{}, sub...), "-h")...)) {
		return []string{fmt.Sprintf("%s: %q is not a tbound-profile subcommand", where, name)}
	}
	flags := runbooklint.Flags(c.Words)
	var out []string
	for _, f := range flags {
		if !runbooklint.FlagDefined(runbookProfileCall(append(append([]string{}, sub...), "--"+f+"=x", "-h")...)) {
			out = append(out, fmt.Sprintf("%s: %s has no --%s flag", where, name, f))
		}
	}
	for _, need := range runbookRequiredFlags[sub[0]] {
		if !runbookContains(flags, need) {
			out = append(out, fmt.Sprintf("%s: %s needs --%s", where, name, need))
		}
	}
	if sub[0] == "corpus-digest" && runbookContains(flags, "artifacts") && l.needNode && !runbookContains(flags, "node") {
		out = append(out, fmt.Sprintf("%s: corpus-digest with --artifacts needs --node: the pilot artifacts declare node_test acceptance specs, which are refused without a node runner", where))
	}
	return out
}

// runbookProfileLintFromCode reads the guest-evidence kinds from runGuestEvidence and the node rule
// from the pilot artifacts.
func runbookProfileLintFromCode(t *testing.T) runbookProfileLint {
	t.Helper()
	kinds, err := runbooklint.CaseLabels("guest_evidence_linux.go", "runGuestEvidence")
	if err != nil {
		t.Fatal(err)
	}
	// netsink-probe is handled before the switch, and calling it runs its probe, so it is never probed.
	kinds[netSinkProbeVerb] = true
	return runbookProfileLint{kinds: kinds, needNode: runbookPilotNeedsNode(t)}
}

// TestRunbookProfileCommands checks every tbound-profile command the runbooks name. The subcommand
// must exist, each --flag must be defined by that subcommand's FlagSet, and the required flags
// must be present.
func TestRunbookProfileCommands(t *testing.T) {
	cmds, err := runbooklint.Commands(runbookRepoRoot, "tbound-profile")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) == 0 {
		t.Fatal("no tbound-profile commands found in the runbooks")
	}
	lint := runbookProfileLintFromCode(t)
	for _, c := range cmds {
		for _, p := range lint.problems(c) {
			t.Error(p)
		}
	}
}

// TestRunbookLintRejectsMisspelledKind feeds the lint a guest-evidence kind that the dispatch does
// not have. The shared FlagSet parses before the kind switch, so the flag probes alone pass it.
func TestRunbookLintRejectsMisspelledKind(t *testing.T) {
	lint := runbookProfileLintFromCode(t)
	c := runbooklint.Command{File: "synthetic", Line: 1, Binary: "tbound-profile", Words: []string{"guest-evidence", "tsts", "--commit", "x"}}
	if got := lint.problems(c); len(got) == 0 {
		t.Fatal("a misspelled guest-evidence kind passed the lint")
	}
}

// TestRunbookRequiredFlagsAreEnforced pins runbookRequiredFlags to the code. A call that omits one
// required flag must be refused with a message that names the required flags. The refusal comes
// before any file is read, so the test reads nothing.
func TestRunbookRequiredFlagsAreEnforced(t *testing.T) {
	for sub, names := range runbookRequiredFlags {
		for _, omit := range names {
			args := []string{sub}
			for _, n := range names {
				if n != omit {
					args = append(args, "--"+n+"=x")
				}
			}
			if err := runbookProfileCall(args...); err == nil || !strings.Contains(err.Error(), "required") {
				t.Errorf("%s without --%s: want a refusal naming the required flags, got %v", sub, omit, err)
			}
		}
	}
}

func runbookContains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
