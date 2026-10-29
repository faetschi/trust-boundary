package runbooklint

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const repoRoot = "../../.."

func TestExtractReadsOnlyCodeAndSpans(t *testing.T) {
	doc := "Prose that names tbound-run is not a command.\n" +
		"Define `TBP` as `tbound-profile`, and `TBP = the frozen-commit tbound-profile` is not one either.\n" +
		"```\n" +
		"GSSH 'sudo -n tbound-profile verify --profile p; tbound-profile template-digest --template \"t\"' # comment\n" +
		"tbound serve --pi --attempt <1|2> --bash-no-outputs\n" +
		"```\n" +
		"| `tbound-op svc start` | ok |\n"
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "doc.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Extract(root, "doc.md")
	if err != nil {
		t.Fatal(err)
	}
	want := []Command{
		{File: "doc.md", Line: 4, Binary: "tbound-profile", Words: []string{"verify", "--profile", "p"}},
		{File: "doc.md", Line: 4, Binary: "tbound-profile", Words: []string{"template-digest", "--template", "t"}},
		{File: "doc.md", Line: 5, Binary: "tbound", Words: []string{"serve", "--pi", "--attempt", "<1_2>", "--bash-no-outputs"}},
		{File: "doc.md", Line: 7, Binary: "tbound-op", Words: []string{"svc", "start"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Extract:\n got %+v\nwant %+v", got, want)
	}
}

// TestExtractJoinsContinuedLines checks that a command continued with a trailing backslash is one
// command, with the flags of its continuation lines and the line number of its first line.
func TestExtractJoinsContinuedLines(t *testing.T) {
	doc := "```\n" +
		"TBP init --out x \\\n" +
		"  --source-digest y \\\n" +
		"  --provider-route engineering\n" +
		"tbound serve --pi\n" +
		"```\n"
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "doc.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Extract(root, "doc.md")
	if err != nil {
		t.Fatal(err)
	}
	want := []Command{
		{File: "doc.md", Line: 2, Binary: "tbound-profile", Words: []string{"init", "--out", "x", "--source-digest", "y", "--provider-route", "engineering"}},
		{File: "doc.md", Line: 5, Binary: "tbound", Words: []string{"serve", "--pi"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Extract:\n got %+v\nwant %+v", got, want)
	}
}

func TestFlagsAndProbeClassification(t *testing.T) {
	// Optional groups in the runbooks write their flags as "[--c" and "--d]".
	if got := Flags([]string{"x", "--a=1", "--", "--b", "-c", "[--c", "--d]"}); !reflect.DeepEqual(got, []string{"a", "b", "c", "d"}) {
		t.Fatalf("Flags = %v", got)
	}
	help := errors.New("flag: help requested")
	if !ParseReached(help) || ParseReached(errors.New("unknown tbound-run subcommand")) {
		t.Fatal("ParseReached does not separate help from an unknown subcommand")
	}
	if FlagDefined(errors.New("flag provided but not defined: -nope")) || !FlagDefined(help) || !FlagDefined(errors.New("invalid boolean value")) {
		t.Fatal("FlagDefined does not separate an undefined flag from a defined one")
	}
	if !ParseReached(flag.ErrHelp) {
		t.Fatalf("ParseReached does not match the flag package's help sentinel: %q", flag.ErrHelp)
	}
}

// TestTboundOpRunbookVerbsExist checks every tbound-op command in the runbooks against the
// dispatcher of the script itself. The script is text here, never executed.
func TestTboundOpRunbookVerbsExist(t *testing.T) {
	script, err := os.ReadFile(filepath.Join(repoRoot, "infra", "guest", "operator", "tbound-op"))
	if err != nil {
		t.Fatal(err)
	}
	verbs := TboundOpVerbs(string(script))
	if len(verbs) < 15 {
		t.Fatalf("found %d tbound-op verbs, want the dispatcher's full list: %v", len(verbs), verbs)
	}
	cmds, err := Commands(repoRoot, "tbound-op")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) == 0 {
		t.Fatal("no tbound-op commands found in the runbooks")
	}
	known := map[string]bool{}
	for _, v := range verbs {
		known[v] = true
	}
	for _, c := range cmds {
		if !known[c.Words[0]] {
			t.Errorf("%s:%d: tbound-op has no verb %q", c.File, c.Line, c.Words[0])
		}
	}
}
