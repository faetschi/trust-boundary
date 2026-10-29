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
// TestRunbookRequiredFlagsAreEnforced fails if the code stops refusing one.
var runbookRequiredFlags = map[string][]string{
	"corpus-digest":       {"manifest", "panel-receipt", "panel-audit", "panel-stats"},
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

// TestRunbookProfileCommands checks every tbound-profile command the runbooks name. The subcommand
// must exist, each --flag must be defined by that subcommand's FlagSet, and the required flags
// must be present. Each probe parses "--flag=x -h" through the real FlagSet and never runs the
// command.
func TestRunbookProfileCommands(t *testing.T) {
	cmds, err := runbooklint.Commands(runbookRepoRoot, "tbound-profile")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) == 0 {
		t.Fatal("no tbound-profile commands found in the runbooks")
	}
	needNode := runbookPilotNeedsNode(t)
	for _, c := range cmds {
		where := fmt.Sprintf("%s:%d", c.File, c.Line)
		sub, ok := runbookSubcommand(c.Words)
		if !ok {
			continue
		}
		name := strings.Join(sub, " ")
		if !runbooklint.ParseReached(runbookProfileCall(append(append([]string{}, sub...), "-h")...)) {
			t.Errorf("%s: %q is not a tbound-profile subcommand", where, name)
			continue
		}
		flags := runbooklint.Flags(c.Words)
		for _, f := range flags {
			if !runbooklint.FlagDefined(runbookProfileCall(append(append([]string{}, sub...), "--"+f+"=x", "-h")...)) {
				t.Errorf("%s: %s has no --%s flag", where, name, f)
			}
		}
		for _, need := range runbookRequiredFlags[sub[0]] {
			if !runbookContains(flags, need) {
				t.Errorf("%s: %s needs --%s", where, name, need)
			}
		}
		if sub[0] == "corpus-digest" && runbookContains(flags, "artifacts") && needNode && !runbookContains(flags, "node") {
			t.Errorf("%s: corpus-digest with --artifacts needs --node: the pilot artifacts declare node_test acceptance specs, which are refused without a node runner", where)
		}
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
