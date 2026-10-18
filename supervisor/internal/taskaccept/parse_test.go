package taskaccept

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseAcceptRefusals(t *testing.T) {
	good := `{"schema":"tbound-acceptance/v1","checks":[{"op":"absent","path":"a"}]}`
	if s, err := ParseAccept([]byte(good)); err != nil || s.Digest() != sha(good) {
		t.Fatalf("ParseAccept = %+v, %v", s, err)
	}
	dg := sha("x")
	chk := func(body string) string {
		return `{"schema":"tbound-acceptance/v1","checks":[` + body + `]}`
	}
	long := strings.Repeat("a", 300)
	for name, raw := range map[string]string{
		"unknown top-level field": `{"schema":"tbound-acceptance/v1","checks":[{"op":"absent","path":"a"}],"extra":1}`,
		"unknown check field":     chk(`{"op":"absent","path":"a","exec":"rm"}`),
		"trailing data":           good + ` {}`,
		"trailing garbage":        good + `x`,
		"not json":                `{`,
		"wrong schema":            strings.Replace(good, "v1", "v2", 1),
		"no schema":               `{"checks":[{"op":"absent","path":"a"}]}`,
		"no checks":               `{"schema":"tbound-acceptance/v1","checks":[]}`,
		"unknown op":              chk(`{"op":"exec","path":"a"}`),
		"missing op":              chk(`{"path":"a"}`),
		"absolute path":           chk(`{"op":"absent","path":"/etc/passwd"}`),
		"dot dot path":            chk(`{"op":"absent","path":"a/../../b"}`),
		"leading dot dot":         chk(`{"op":"absent","path":"../b"}`),
		"dot component":           chk(`{"op":"absent","path":"./b"}`),
		"trailing slash":          chk(`{"op":"absent","path":"b/"}`),
		"double slash":            chk(`{"op":"absent","path":"a//b"}`),
		"backslash":               chk(`{"op":"absent","path":"a\\b"}`),
		"control character":       chk(`{"op":"absent","path":"a\u0001b"}`),
		"lost+found":              chk(`{"op":"absent","path":"lost+found/x"}`),
		"empty path":              chk(`{"op":"absent"}`),
		"long name":               chk(`{"op":"absent","path":"` + long + `"}`),
		"sha256 without digest":   chk(`{"op":"file_sha256","path":"a"}`),
		"sha256 bad digest":       chk(`{"op":"file_sha256","path":"a","digest":"sha256:ABC"}`),
		"sha256 bare hex":         chk(`{"op":"file_sha256","path":"a","digest":"` + strings.TrimPrefix(dg, "sha256:") + `"}`),
		"sha256 with text":        chk(`{"op":"file_sha256","path":"a","digest":"` + dg + `","text":"x"}`),
		"contains empty text":     chk(`{"op":"contains","path":"a"}`),
		"contains with digest":    chk(`{"op":"contains","path":"a","text":"x","digest":"` + dg + `"}`),
		"contains huge text":      chk(`{"op":"contains","path":"a","text":"` + strings.Repeat("x", MaxTextBytes+1) + `"}`),
		"absent with text":        chk(`{"op":"absent","path":"a","text":"x"}`),
		"except with path":        chk(`{"op":"unchanged_except","path":"a","paths":["b"]}`),
		"except bad entry":        chk(`{"op":"unchanged_except","paths":["a","../b"]}`),
		"except duplicate":        chk(`{"op":"unchanged_except","paths":["a","a"]}`),
		"paths on absent":         chk(`{"op":"absent","path":"a","paths":["b"]}`),
	} {
		if _, err := ParseAccept([]byte(raw)); err == nil {
			t.Errorf("%s: ParseAccept accepted %.80s", name, raw)
		}
	}
}

func TestParseAcceptBounds(t *testing.T) {
	check := `{"op":"absent","path":"a"}`
	many := func(n int) string {
		return `{"schema":"tbound-acceptance/v1","checks":[` + strings.TrimSuffix(strings.Repeat(check+",", n), ",") + `]}`
	}
	if _, err := ParseAccept([]byte(many(MaxChecks))); err != nil {
		t.Fatalf("MaxChecks checks: %v", err)
	}
	if _, err := ParseAccept([]byte(many(MaxChecks + 1))); err == nil {
		t.Error("more than MaxChecks checks were accepted")
	}
	pad := `{"schema":"tbound-acceptance/v1","checks":[{"op":"absent","path":"a"}]}` + strings.Repeat(" ", MaxSpecBytes)
	if _, err := ParseAccept([]byte(pad)); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Errorf("oversize input: %v", err)
	}
	if _, err := ParseAccept([]byte(`{"schema":"tbound-acceptance/v1","checks":[{"op":"unchanged_except"}]}`)); err != nil {
		t.Errorf("unchanged_except with no paths: %v", err)
	}
}

func TestParseSolutionRefusals(t *testing.T) {
	good := `{"schema":"tbound-solution/v1","ops":[{"op":"write","path":"a","mode":"0644","content_b64":"` + b64("x") + `"}]}`
	if _, err := ParseSolution([]byte(good)); err != nil {
		t.Fatalf("ParseSolution: %v", err)
	}
	ops := func(body string) string { return `{"schema":"tbound-solution/v1","ops":[` + body + `]}` }
	for name, raw := range map[string]string{
		"unknown field":       strings.Replace(good, `"ops"`, `"extra":1,"ops"`, 1),
		"unknown op field":    ops(`{"op":"remove","path":"a","type":"symlink"}`),
		"trailing data":       good + `{}`,
		"wrong schema":        strings.Replace(good, "solution/v1", "solution/v2", 1),
		"no ops":              `{"schema":"tbound-solution/v1","ops":[]}`,
		"unknown op":          ops(`{"op":"symlink","path":"a"}`),
		"escape":              ops(`{"op":"write","path":"../a","mode":"0644"}`),
		"absolute":            ops(`{"op":"remove","path":"/a"}`),
		"write without mode":  ops(`{"op":"write","path":"a"}`),
		"write bad mode":      ops(`{"op":"write","path":"a","mode":"644"}`),
		"write setuid mode":   ops(`{"op":"write","path":"a","mode":"4755"}`),
		"write bad base64":    ops(`{"op":"write","path":"a","mode":"0644","content_b64":"!!"}`),
		"write loose base64":  ops(`{"op":"write","path":"a","mode":"0644","content_b64":"eA"}`),
		"write huge":          ops(`{"op":"write","path":"a","mode":"0644","content_b64":"` + b64(strings.Repeat("x", 1<<20+1)) + `"}`),
		"mkdir with content":  ops(`{"op":"mkdir","path":"a","mode":"0755","content_b64":"eA=="}`),
		"mkdir without rwx":   ops(`{"op":"mkdir","path":"a","mode":"0500"}`),
		"mkdir without mode":  ops(`{"op":"mkdir","path":"a"}`),
		"remove with mode":    ops(`{"op":"remove","path":"a","mode":"0644"}`),
		"remove with content": ops(`{"op":"remove","path":"a","content_b64":"eA=="}`),
		"lost+found":          ops(`{"op":"mkdir","path":"lost+found","mode":"0700"}`),
	} {
		if _, err := ParseSolution([]byte(raw)); err == nil {
			t.Errorf("%s: ParseSolution accepted %.80s", name, raw)
		}
	}
	var tooMany []Op
	for i := 0; i <= MaxOps; i++ {
		tooMany = append(tooMany, Op{Op: SolRemove, Path: "a"})
	}
	raw, _ := json.Marshal(SolutionSpec{Schema: SolutionSchema, Ops: tooMany})
	if _, err := ParseSolution(raw); err == nil {
		t.Error("more than MaxOps ops were accepted")
	}
}
