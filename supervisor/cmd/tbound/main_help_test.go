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
		for _, cmd := range []string{"serve", "chat", "provider", "project", "product", "release", "abandon-unknown", "verify-retained"} {
			if !strings.Contains(out.String(), "\n  "+cmd+" ") {
				t.Fatalf("run(%q) output does not list %q:\n%s", arg, cmd, out.String())
			}
		}
	}
}

func TestVersionPrintsBuildVersion(t *testing.T) {
	old := version
	version = "1.2.3-test"
	defer func() { version = old }()

	for _, arg := range []string{"--version", "-version", "version"} {
		var out, errOut bytes.Buffer
		if err := run([]string{arg}, &out, &errOut); err != nil {
			t.Fatalf("run(%q) returned %v, want nil", arg, err)
		}
		if got, want := out.String(), "tbound 1.2.3-test\n"; got != want {
			t.Fatalf("run(%q) = %q, want %q", arg, got, want)
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
