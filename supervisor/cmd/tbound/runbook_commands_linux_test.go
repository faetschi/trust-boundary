package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"tbound/supervisor/internal/runbooklint"
)

// runbookTboundCall runs a top-level tbound command line through the dispatch that main uses.
func runbookTboundCall(args ...string) error {
	return run(args, io.Discard, io.Discard)
}

func runbookContains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// runbookTboundLint holds what the tbound checks read from the code. trialVerbs are the case labels
// of runTrial, the dispatch that init reaches. Evidence kinds come from the evidenceFlags and
// evidenceExtraKinds tables that runEvidence consults.
type runbookTboundLint struct {
	trialVerbs map[string]bool
}

// runbookTboundLintFromCode reads the trial verbs from the dispatch.
func runbookTboundLintFromCode(t *testing.T) runbookTboundLint {
	t.Helper()
	trial, err := runbooklint.CaseLabels("trial_linux.go", "runTrial")
	if err != nil {
		t.Fatal(err)
	}
	return runbookTboundLint{trialVerbs: trial}
}

// evidenceKindFlags returns the flags an evidence kind takes, and whether the kind exists.
func evidenceKindFlags(kind string) ([]string, bool) {
	if extra, ok := evidenceExtraKinds[kind]; ok {
		return extra.flags, true
	}
	flags, ok := evidenceFlags[kind]
	return flags, ok
}

// problems returns what is wrong with one tbound command. The top-level routes are probed through
// main's dispatch and netsink through its FlagSet. Each probe parses "--flag=x -h" and never runs
// the command.
func (l runbookTboundLint) problems(c runbooklint.Command) []string {
	where := fmt.Sprintf("%s:%d", c.File, c.Line)
	sub := c.Words[0]
	flags := runbooklint.Flags(c.Words)
	var out []string
	switch sub {
	case "trial":
		if len(c.Words) < 2 || !l.trialVerbs[c.Words[1]] {
			out = append(out, fmt.Sprintf("%s: tbound trial has no verb %q", where, strings.Join(c.Words[1:], " ")))
		}
	case "evidence":
		if len(c.Words) < 2 {
			return []string{where + ": tbound evidence needs a kind"}
		}
		kind := c.Words[1]
		if strings.HasPrefix(kind, "<") {
			return nil // a placeholder such as <kind> names no kind
		}
		allowed, known := evidenceKindFlags(kind)
		if !known {
			return []string{fmt.Sprintf("%s: tbound evidence has no kind %q", where, kind)}
		}
		for _, f := range flags {
			if !runbookContains(allowed, "--"+f) {
				out = append(out, fmt.Sprintf("%s: tbound evidence %s has no --%s flag", where, kind, f))
			}
		}
	case "netsink":
		for _, f := range flags {
			if !runbooklint.FlagDefined(runNetsink(context.Background(), []string{"--" + f + "=x", "-h"}, io.Discard, netsinkEnv{})) {
				out = append(out, fmt.Sprintf("%s: tbound netsink has no --%s flag", where, f))
			}
		}
	case "workflow-root-init":
		// The route takes no arguments and refuses them by name. Its refusal is the route check, and
		// any flag is an error, because the probe cannot show a flag on this route.
		if err := runbookTboundCall(sub, "-h"); err == nil || !strings.Contains(err.Error(), "takes no arguments") {
			return []string{fmt.Sprintf("%s: %q is not a tbound route", where, sub)}
		}
		if len(c.Words) > 1 {
			return []string{fmt.Sprintf("%s: tbound workflow-root-init takes no arguments, so %q is not valid", where, strings.Join(c.Words[1:], " "))}
		}
	case clientForcedVerb:
		// The forced command takes no flags; init dispatches on this exact word.
	default:
		err := runbookTboundCall(sub, "-h")
		if err == nil || strings.Contains(err.Error(), "unexpected positional arguments") {
			return []string{fmt.Sprintf("%s: %q is not a tbound route", where, sub)}
		}
		for _, f := range flags {
			if !runbooklint.FlagDefined(runbookTboundCall(sub, "--"+f+"=x", "-h")) {
				out = append(out, fmt.Sprintf("%s: tbound %s has no --%s flag", where, sub, f))
			}
		}
	}
	return out
}

// TestRunbookLintRejectsFlagOnNoArgumentRoute feeds the lint a flag on workflow-root-init, which takes
// no arguments. Its refusal names the arguments, not the flag, so the flag probe alone passed it.
func TestRunbookLintRejectsFlagOnNoArgumentRoute(t *testing.T) {
	lint := runbookTboundLintFromCode(t)
	c := runbooklint.Command{File: "synthetic", Line: 1, Binary: "tbound", Words: []string{"workflow-root-init", "--bogus", "x"}}
	if got := lint.problems(c); len(got) == 0 {
		t.Fatal("a flag on workflow-root-init passed the lint")
	}
}

// TestRunbookTboundCommands checks every tbound command the runbooks name.
//   - serve, abandon-unknown, verify-retained and workflow-root-init are top-level routes: the
//     route must exist and each --flag must be defined by its FlagSet.
//   - netsink is a route whose FlagSet is runNetsink's.
//   - evidence takes a kind from evidenceFlags or evidenceExtraKinds, and each --flag must be in
//     that kind's flag list.
//   - trial takes a verb, which must be a case label of runTrial. Its verbs take positional
//     arguments, so their flags are not checked here.
//   - client-forced must be the clientForcedVerb the init dispatch compares against.
func TestRunbookTboundCommands(t *testing.T) {
	cmds, err := runbooklint.Commands("../../..", "tbound")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) == 0 {
		t.Fatal("no tbound commands found in the runbooks")
	}
	lint := runbookTboundLintFromCode(t)
	for _, c := range cmds {
		for _, p := range lint.problems(c) {
			t.Error(p)
		}
	}
}
