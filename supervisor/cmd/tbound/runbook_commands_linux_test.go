package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"strconv"
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

// runbookCaseLabels returns the string case labels of the switch statements inside func name of a
// source file. The trial and evidence routes are dispatched from init and need a root or a
// service environment, so the verbs are read from the switch that dispatches them.
func runbookCaseLabels(t *testing.T, file, name string) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]bool{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil {
						labels[s] = true
					}
				}
			}
			return true
		})
	}
	return labels
}

// TestRunbookTboundCommands checks every tbound command the runbooks name.
//   - serve, abandon-unknown, verify-retained and workflow-root-init are top-level routes: the
//     route must exist and each --flag must be defined by its FlagSet.
//   - netsink is a route whose FlagSet is runNetsink's.
//   - evidence takes a kind; the kind must be in evidenceFlags or be a verb of runEvidence, and
//     each --flag must be in that kind's entry of evidenceFlags.
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
	trialVerbs := runbookCaseLabels(t, "trial_linux.go", "runTrial")
	evidenceVerbs := runbookCaseLabels(t, "evidence_linux.go", "runEvidence")
	for _, c := range cmds {
		where := fmt.Sprintf("%s:%d", c.File, c.Line)
		sub := c.Words[0]
		flags := runbooklint.Flags(c.Words)
		switch sub {
		case "trial":
			if len(c.Words) < 2 || !trialVerbs[c.Words[1]] {
				t.Errorf("%s: tbound trial has no verb %q", where, strings.Join(c.Words[1:], " "))
			}
		case "evidence":
			if len(c.Words) < 2 {
				t.Errorf("%s: tbound evidence needs a kind", where)
				continue
			}
			kind := c.Words[1]
			if strings.HasPrefix(kind, "<") {
				continue // a placeholder such as <kind> names no kind
			}
			allowed, isKind := evidenceFlags[kind]
			if !isKind && !evidenceVerbs[kind] {
				t.Errorf("%s: tbound evidence has no kind %q", where, kind)
				continue
			}
			for _, f := range flags {
				if !runbookContains(allowed, "--"+f) {
					t.Errorf("%s: tbound evidence %s has no --%s flag", where, kind, f)
				}
			}
		case "netsink":
			for _, f := range flags {
				if !runbooklint.FlagDefined(runNetsink(context.Background(), []string{"--" + f + "=x", "-h"}, io.Discard, netsinkEnv{})) {
					t.Errorf("%s: tbound netsink has no --%s flag", where, f)
				}
			}
		case clientForcedVerb:
			// The forced command takes no flags; init dispatches on this exact word.
		default:
			err := runbookTboundCall(sub, "-h")
			if err == nil || strings.Contains(err.Error(), "unexpected positional arguments") {
				t.Errorf("%s: %q is not a tbound route", where, sub)
				continue
			}
			for _, f := range flags {
				if !runbooklint.FlagDefined(runbookTboundCall(sub, "--"+f+"=x", "-h")) {
					t.Errorf("%s: tbound %s has no --%s flag", where, sub, f)
				}
			}
		}
	}
}
