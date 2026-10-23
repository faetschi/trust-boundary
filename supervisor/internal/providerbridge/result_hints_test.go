package providerbridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
)

func TestPermittedAlternativeHintsCoverPublicReasons(t *testing.T) {
	// Read the producers and the independent D32 allowlist so adding a public
	// reason in any of them requires a deliberate model-feedback decision.
	want := map[string]bool{protocol.ResultStatusCommandFailed: true}
	for _, source := range []struct{ file, pattern string }{
		{"gate/gate.go", `ReasonCode(?::|\s*=)\s*"([a-z_]+)"`},
		{"sessionrepo/admission_scope_linux.go", `DenyReason\w+\s*=\s*"([a-z_]+)"`},
		{"attacker/spec.go", `"([a-z_]+)":\s*"`},
	} {
		data, err := os.ReadFile(filepath.Join("..", source.file))
		if err != nil {
			t.Fatal(err)
		}
		matches := regexp.MustCompile(source.pattern).FindAllStringSubmatch(string(data), -1)
		if len(matches) == 0 {
			t.Fatalf("no public reasons found in %s", source.file)
		}
		for _, match := range matches {
			if match[1] != "policy_rule_allow" {
				want[match[1]] = true
			}
		}
	}
	if len(want) != len(permittedAlternativeHints) {
		t.Errorf("public reasons=%v, hints=%v", want, permittedAlternativeHints)
	}
	pathLike := regexp.MustCompile(`[\\/:~$%{}\[\]]|\b\w+\.\w+\b`)
	for reason := range want {
		hint := permittedAlternativeHints[reason]
		if hint == "" || len(hint) > 128 || pathLike.MatchString(hint) || strings.ContainsAny(hint, "\r\n\t") {
			t.Errorf("%s has missing, oversized or path-like hint (%d bytes): %q", reason, len(hint), hint)
		}
	}
}

func TestAdapterHintTableEqualsGo(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "adapter", "src", "result-hints.ts"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(permittedAlternativeHints, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want := "// Go-owned table: supervisor/internal/providerbridge/result_hints.go.\n" +
		"// Golden-file checked by TestAdapterHintTableEqualsGo; edit the Go table first.\n" +
		"export const permittedAlternativeHints: Readonly<Record<string, string>> = " + string(encoded) + ";\n"
	if strings.ReplaceAll(string(data), "\r\n", "\n") != want {
		t.Fatalf("adapter hint table differs from Go; replace it with:\n%s", want)
	}
}

func TestModelResultTextOnlyHintsDenialsAndFailedCommands(t *testing.T) {
	for _, tc := range []struct{ verdict, reason, status, hint string }{
		{"DENY", "unknown_tool", "", permittedAlternativeHints["unknown_tool"]},
		{"ALLOW", "policy_rule_allow", protocol.ResultStatusCommandFailed, permittedAlternativeHints["command_failed"]},
		{"ALLOW", "policy_rule_allow", "", ""},
		{"ALLOW", "unknown_tool", "", ""},
		{"DENY", "unknown_reason", "", ""},
		{"DENY", "__proto__", "", ""},
	} {
		result := protocol.Result{Verdict: tc.verdict, ReasonCode: tc.reason, Status: tc.status}
		raw := []byte(`{"unchanged":"<output>"}`)
		want := string(raw)
		if tc.hint != "" {
			want += "\nhint: " + tc.hint
		}
		if got := modelResultText(raw, result); got != want {
			t.Errorf("%+v: got %q want %q", tc, got, want)
		}
	}
}
