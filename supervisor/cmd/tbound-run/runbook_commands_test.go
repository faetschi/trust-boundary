package main

import (
	"context"
	"fmt"
	"io"
	"testing"

	"tbound/supervisor/internal/runbooklint"
)

// TestRunbookRunCommands checks every tbound-run command the runbooks name. The subcommand must
// exist and each --flag must be defined by its FlagSet. Each probe parses "--flag=x -h" through
// the real dispatch and never loads a plan or runs a command.
func TestRunbookRunCommands(t *testing.T) {
	cmds, err := runbooklint.Commands("../../..", "tbound-run")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) == 0 {
		t.Fatal("no tbound-run commands found in the runbooks")
	}
	for _, c := range cmds {
		where := fmt.Sprintf("%s:%d", c.File, c.Line)
		sub := c.Words[0]
		probe := func(args ...string) error {
			a := &app{out: io.Discard, errOut: io.Discard, ctx: context.Background()}
			return a.run(args)
		}
		if !runbooklint.ParseReached(probe(sub, "-h")) {
			t.Errorf("%s: %q is not a tbound-run subcommand", where, sub)
			continue
		}
		for _, f := range runbooklint.Flags(c.Words) {
			if !runbooklint.FlagDefined(probe(sub, "--"+f+"=x", "-h")) {
				t.Errorf("%s: %s has no --%s flag", where, sub, f)
			}
		}
	}
}
