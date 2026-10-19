package broker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/protocol"
)

// The Pi adapter (TypeScript, adapter/src/proxy-tools.ts) registers one proxy tool per declared tool, and
// Go validates the arguments of the same four tools. EVQ-SURFACE-ADAPTER-EQUIVALENCE needs the two
// surfaces to be the same tool by tool, so this test reads the adapter source of the same tree and holds
// the Go projection to it: the registered names and order, and for each tool the exact argument names.

var (
	tsDeclaredRE   = regexp.MustCompile(`declaredToolNames = \[([^\]]*)\] as const`)
	tsParametersRE = regexp.MustCompile(`(?m)^const (\w+)Parameters = Type\.Object\(`)
	tsPropertyRE   = regexp.MustCompile(`(?m)^\s*(\w+): Type.`)
	tsRegisteredRE = regexp.MustCompile(`makeProxyTool\(\s*"(\w+)"`)
	quotedRE       = regexp.MustCompile(`"(\w+)"`)
)

// adapterSurface is what the adapter declares: the tool order and the argument names of each tool.
type adapterSurface struct {
	declared, registered []string
	properties           map[string][]string
}

func readAdapterSurface(t *testing.T) adapterSurface {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "adapter", "src", "proxy-tools.ts"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	var s adapterSurface
	m := tsDeclaredRE.FindStringSubmatch(src)
	if m == nil {
		t.Fatal("adapter/src/proxy-tools.ts declares no declaredToolNames")
	}
	for _, q := range quotedRE.FindAllStringSubmatch(m[1], -1) {
		s.declared = append(s.declared, q[1])
	}
	for _, r := range tsRegisteredRE.FindAllStringSubmatch(src, -1) {
		s.registered = append(s.registered, r[1])
	}
	s.properties = map[string][]string{}
	starts := tsParametersRE.FindAllStringSubmatchIndex(src, -1)
	for i, loc := range starts {
		end := len(src)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		} else if j := strings.Index(src[loc[1]:], "\nconst proxyParameters"); j >= 0 {
			end = loc[1] + j
		}
		var names []string
		for _, p := range tsPropertyRE.FindAllStringSubmatch(src[loc[1]:end], -1) {
			names = append(names, p[1])
		}
		slices.Sort(names)
		s.properties[src[loc[2]:loc[3]]] = names
	}
	return s
}

func TestGoProjectionEqualsTheAdapterToolSurfaceForEveryTool(t *testing.T) {
	s := readAdapterSurface(t)
	want := DeclaredToolManifest()
	if !slices.Equal(s.declared, want) || !slices.Equal(s.registered, want) {
		t.Fatalf("adapter declares %v and registers %v, Go declares %v", s.declared, s.registered, want)
	}
	// Argument names the adapter's schema lists (edit's items add oldText and newText), and what Go requires.
	type shape struct {
		required, optional []string
		value              map[string]any
	}
	shapes := map[string]shape{
		"read":  {[]string{"path"}, []string{"limit", "offset"}, map[string]any{"path": "a", "offset": 1, "limit": 2}},
		"write": {[]string{"content", "path"}, nil, map[string]any{"path": "a", "content": "b"}},
		"edit":  {[]string{"edits", "path"}, nil, map[string]any{"path": "a", "edits": []any{map[string]any{"oldText": "x", "newText": "y"}}}},
		"bash":  {[]string{"command"}, []string{"timeout"}, map[string]any{"command": "true", "timeout": 1}},
	}
	for _, tool := range want {
		sh, ok := shapes[tool]
		if !ok {
			t.Fatalf("no expectation for the declared tool %q", tool)
		}
		names := slices.Concat(sh.required, sh.optional)
		if tool == "edit" {
			names = append(names, "newText", "oldText")
		}
		slices.Sort(names)
		if got := s.properties[tool]; !slices.Equal(got, names) {
			t.Errorf("%s: the adapter schema has arguments %v, Go expects %v", tool, got, names)
		}
		canonical := func(v map[string]any) error {
			raw, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			_, err = protocol.CanonicalArguments(tool, raw)
			return err
		}
		if err := canonical(sh.value); err != nil {
			t.Errorf("%s: Go refuses arguments of the adapter's schema: %v", tool, err)
		}
		for _, req := range sh.required {
			without := map[string]any{}
			for k, v := range sh.value {
				if k != req {
					without[k] = v
				}
			}
			if canonical(without) == nil {
				t.Errorf("%s: Go accepts arguments without the required %q", tool, req)
			}
		}
		withExtra := map[string]any{"unlisted": 1}
		for k, v := range sh.value {
			withExtra[k] = v
		}
		if canonical(withExtra) == nil {
			t.Errorf("%s: Go accepts an argument the adapter's schema does not list", tool)
		}
	}
	if _, err := protocol.CanonicalArguments("write2", json.RawMessage(`{}`)); err == nil {
		t.Error("Go accepts a tool the adapter does not register")
	}
}
