package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpListsCommands(t *testing.T) {
	for _, arg := range []string{"--help", "-h", "help"} {
		var out, errOut bytes.Buffer
		if err := run([]string{arg}, &out, &errOut); err != nil {
			t.Fatalf("run(%q) returned %v, want nil", arg, err)
		}
		for _, want := range []string{"serve", "chat", "provider", "release"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("run(%q) output does not mention %q:\n%s", arg, want, out.String())
			}
		}
	}
}

func TestNoCommandPrintsHelpAndFails(t *testing.T) {
	var out, errOut bytes.Buffer
	err := run(nil, &out, &errOut)
	if err != errNoCommand {
		t.Fatalf("run(nil) returned %v, want errNoCommand", err)
	}
	if !strings.Contains(errOut.String(), "Commands:") {
		t.Fatalf("run(nil) stderr does not show the command list:\n%s", errOut.String())
	}
	if out.Len() != 0 {
		t.Fatalf("run(nil) wrote to stdout: %q", out.String())
	}
}
