package vrun

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/taskaccept"
)

func messageEnd(role string, in, out, cacheRead, cacheWrite int, cost float64) string {
	return fmt.Sprintf(`{"type":"message_end","message":{"role":%q,"content":[],"usage":{"input":%d,"output":%d,"cacheRead":%d,"cacheWrite":%d,"totalTokens":%d,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":%v}},"stopReason":"stop"}}`,
		role, in, out, cacheRead, cacheWrite, in+out+cacheRead+cacheWrite, cost)
}

func TestUsageScannerSumsAssistantMessageEnds(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"session","version":3,"id":"x","cwd":"/work"}`,
		`{"type":"agent_start"}`,
		messageEnd("user", 0, 0, 0, 0, 0),
		// Cumulative usage on message_update must not be counted on top of message_end.
		`{"type":"message_update","usage":{"input":100,"output":9,"cacheRead":0,"cacheWrite":0,"totalTokens":109,"cost":{"total":0.5}},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"hi"}}`,
		messageEnd("assistant", 100, 10, 40, 5, 0.000123),
		`{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","result":{"content":[]},"isError":false}`,
		`{"type":"tool_execution_end","toolCallId":"c2","toolName":"read","result":{"content":[]},"isError":true}`,
		messageEnd("assistant", 200, 20, 0, 0, 0.000377),
		`{"type":"agent_settled"}`,
	}, "\n") + "\n"
	var s UsageScanner
	// Feed it in awkward chunks, including a split inside a line.
	for i := 0; i < len(stream); i += 37 {
		end := min(i+37, len(stream))
		if n, err := s.Write([]byte(stream[i:end])); err != nil || n != end-i {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	s.Flush()
	got := s.Usage()
	want := Usage{TokensIn: 100 + 40 + 5 + 200, TokensOut: 30, CostMicroUSD: 500, ToolCalls: 2, Events: 2}
	if got != want {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
}

func TestUsageScannerWithoutUsageAndMalformedLines(t *testing.T) {
	var s UsageScanner
	_, _ = s.Write([]byte("not json \"message_end\"\n{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\"}}\n"))
	_, _ = s.Write([]byte(`{"type":"message_end","message":{"role":"assistant","usage":{"input":-5,"output":0}}}`))
	s.Flush()
	got := s.Usage()
	if got.Events != 1 || got.TokensIn != 0 || got.TokensOut != 0 || got.Skipped < 1 {
		t.Fatalf("usage = %+v", got)
	}
}

func TestUsageScannerSkipsAnOversizeLine(t *testing.T) {
	var s UsageScanner
	big := strings.Repeat("a", MaxLineBytes+10)
	_, _ = s.Write([]byte(`{"type":"message_end","message":{"role":"assistant","x":"` + big + `"}}` + "\n"))
	_, _ = s.Write([]byte(messageEnd("assistant", 1, 2, 0, 0, 0) + "\n"))
	got := s.Usage()
	if got.Skipped != 1 || got.Events != 1 || got.TokensIn != 1 || got.TokensOut != 2 {
		t.Fatalf("usage = %+v", got)
	}
}

func validSpec() Spec {
	return Spec{
		Name: "tbound-v-0123456789abcdef", ImageRef: "localhost/tbound/pi-cell@sha256:" + strings.Repeat("ab", 32),
		CrunPath: "/usr/bin/crun", UID: 995, GID: 986,
		Workspace: "/var/lib/tbound/v-work/0123/ws", RuntimeBundle: "/var/lib/tbound/runtime",
		KeyFile: "/etc/tbound/provider/opencode-go.key", Model: "deepseek-v4.1-flash",
		Prompt: "-- fix the bug; $(touch /pwned) `id` 'x'\n", DurationSeconds: 600,
		PidsMax: 128, MemoryMB: 1024, CPUs: "1.5",
	}
}

func TestPodmanArgsShape(t *testing.T) {
	spec := validSpec()
	args, err := PodmanArgs(spec)
	if err != nil {
		t.Fatal(err)
	}
	has := func(words ...string) {
		t.Helper()
		for i := 0; i+len(words) <= len(args); i++ {
			if slices.Equal(args[i:i+len(words)], words) {
				return
			}
		}
		t.Errorf("args lack %q:\n%q", words, args)
	}
	has("run", "--name", spec.Name, "--rm", "--pull=never", "--runtime", "/usr/bin/crun")
	has("--read-only")
	has("--cap-drop=ALL")
	has("--security-opt", "no-new-privileges")
	has("--uidmap", "995:0:1", "--gidmap", "986:0:1", "--user", "995:986")
	has("--timeout", "600")
	has("--pids-limit", "128", "--memory", "1024m", "--cpus", "1.5")
	has("--mount", "type=bind,src=/var/lib/tbound/v-work/0123/ws,dst=/work,rw,bind-propagation=rprivate")
	has("--mount", "type=bind,src=/var/lib/tbound/runtime,dst=/runtime,ro,bind-propagation=rprivate")
	has("--mount", "type=bind,src=/etc/tbound/provider/opencode-go.key,dst=/run/tbound/opencode-go.key,ro,bind-propagation=rprivate")
	has("--entrypoint", "/bin/bash", spec.ImageRef)
	if slices.Contains(args, "--network=none") || slices.Contains(args, "--privileged") {
		t.Errorf("unexpected network or privilege flag: %q", args)
	}
	// The image appears once, pinned by digest, and the command words follow it.
	at := slices.Index(args, spec.ImageRef)
	if at < 0 || slices.Index(args[at+1:], spec.ImageRef) >= 0 {
		t.Fatalf("image reference not exactly once: %q", args)
	}
	cmd := args[at+1:]
	if cmd[0] != "-c" || cmd[2] != "pi-v" || cmd[3] != NodePath || cmd[4] != PiEntry || cmd[5] != spec.Model || cmd[6] != spec.Prompt || len(cmd) != 7 {
		t.Fatalf("command words = %q", cmd)
	}
	// The key is read in the container from the mounted file and never put in an argument; the
	// prompt reaches Pi as one argv word after --.
	script := cmd[1]
	for _, want := range []string{`OPENCODE_API_KEY="$(cat /run/tbound/opencode-go.key)"`, `--mode json`, `--no-extensions`, `--provider opencode-go`, `-- "$4"`} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
	for _, banned := range []string{"tbound-cli-extension", "--extension", "--no-builtin-tools", "--mode rpc"} {
		if strings.Contains(script, banned) {
			t.Errorf("script carries %q: V is native Pi:\n%s", banned, script)
		}
	}
	for _, a := range args {
		if strings.Contains(a, "sk-") || (a != cmd[1] && strings.Contains(a, "OPENCODE_API_KEY")) {
			t.Errorf("argument %q looks like a key", a)
		}
	}
}

func TestPodmanArgsRefusals(t *testing.T) {
	for name, mutate := range map[string]func(*Spec){
		"tag instead of digest":  func(s *Spec) { s.ImageRef = "localhost/tbound/pi-cell:latest" },
		"short digest":           func(s *Spec) { s.ImageRef = "localhost/tbound/pi-cell@sha256:abc" },
		"root":                   func(s *Spec) { s.UID = 0 },
		"no gid":                 func(s *Spec) { s.GID = 0 },
		"relative workspace":     func(s *Spec) { s.Workspace = "ws" },
		"comma in a mount path":  func(s *Spec) { s.Workspace = "/a,b" },
		"option in a mount path": func(s *Spec) { s.KeyFile = "/k,src=/etc/shadow" },
		"empty prompt":           func(s *Spec) { s.Prompt = "" },
		"NUL prompt":             func(s *Spec) { s.Prompt = "a\x00b" },
		"no deadline":            func(s *Spec) { s.DurationSeconds = 0 },
		"no limits":              func(s *Spec) { s.PidsMax = 0 },
		"bad cpus":               func(s *Spec) { s.CPUs = "all" },
		"bad name":               func(s *Spec) { s.Name = "-x" },
		"bad model":              func(s *Spec) { s.Model = "a b" },
	} {
		s := validSpec()
		mutate(&s)
		if _, err := PodmanArgs(s); err == nil {
			t.Errorf("%s: PodmanArgs succeeded", name)
		}
	}
}

func goodDoc() Doc {
	return Doc{Schema: Schema, CaseID: "cmp-1", ImageDigest: "sha256:" + strings.Repeat("a", 64), FixtureDigest: "sha256:" + strings.Repeat("b", 64),
		TokensIn: 5, TokensOut: 3, StartedNS: 10, FinishedNS: 20,
		Accept: &taskaccept.AcceptDoc{Schema: taskaccept.ResultSchema, CaseID: "cmp-1", Passed: true}}
}

func TestParseDoc(t *testing.T) {
	raw, _ := json.Marshal(goodDoc())
	d, err := Parse(raw)
	if err != nil || d.Accept == nil || !d.Accept.Passed || d.CaseID != "cmp-1" {
		t.Fatalf("Parse = %+v, %v", d, err)
	}
	null := goodDoc()
	null.Accept, null.AcceptError = nil, "tree unreadable"
	raw, _ = json.Marshal(null)
	if !strings.Contains(string(raw), `"accept":null`) {
		t.Fatalf("accept must be present as null: %s", raw)
	}
	if d, err := Parse(raw); err != nil || d.Accept != nil || d.AcceptError == "" {
		t.Fatalf("Parse(null accept) = %+v, %v", d, err)
	}
	for name, mutate := range map[string]func(*Doc){
		"schema":   func(d *Doc) { d.Schema = "other" },
		"case":     func(d *Doc) { d.CaseID = "" },
		"image":    func(d *Doc) { d.ImageDigest = "" },
		"fixture":  func(d *Doc) { d.FixtureDigest = "" },
		"negative": func(d *Doc) { d.TokensOut = -1 },
		"time":     func(d *Doc) { d.FinishedNS = 1 },
	} {
		d := goodDoc()
		mutate(&d)
		raw, _ := json.Marshal(d)
		if _, err := Parse(raw); err == nil {
			t.Errorf("%s: Parse accepted %s", name, raw)
		}
	}
	good, _ := json.Marshal(goodDoc())
	for name, raw := range map[string]string{
		"unknown member": strings.Replace(string(good), `"exit":0`, `"exit":0,"pass":true`, 1),
		"trailing data":  string(good) + `{}`,
		"not json":       `x`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: Parse accepted it", name)
		}
	}
}
