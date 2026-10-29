package broker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker/openrouter"
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
		"read":  {[]string{"path"}, nil, map[string]any{"path": "a"}},
		"write": {[]string{"content", "path"}, nil, map[string]any{"path": "a", "content": "b"}},
		"edit":  {[]string{"edits", "path"}, nil, map[string]any{"path": "a", "edits": []any{map[string]any{"oldText": "x", "newText": "y"}}}},
		"bash":  {[]string{"command"}, nil, map[string]any{"command": "true"}},
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

// The corrected surface no longer lists a read offset or limit or a bash timeout, but Go keeps parsing
// them: research calls and retained provider evidence carry them, and the durable executor refuses an
// offset or limit and ignores a timeout. Dropping this tolerance is a research behaviour change.
func TestGoStillParsesTheArgumentsTheCorrectedSurfaceRetired(t *testing.T) {
	for tool, value := range map[string]map[string]any{
		"read": {"path": "a", "offset": 1, "limit": 2},
		"bash": {"command": "true", "timeout": 1},
	} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := protocol.CanonicalArguments(tool, raw); err != nil {
			t.Errorf("%s: Go refuses the retired arguments of recorded calls: %v", tool, err)
		}
	}
}

type advertisedTool struct {
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// advertisedTools is what a provider is told on an engineering route.
func advertisedTools(t *testing.T, route openrouter.Route) []advertisedTool {
	t.Helper()
	prompt := "hello"
	built, err := openrouter.BuildRequestForRoute(route, []openrouter.Message{{Role: openrouter.User, Content: &prompt}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Tools []advertisedTool `json:"tools"`
	}
	if err := json.Unmarshal(built.Body(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Tools
}

func descriptionsIn(value any, found *[]string) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if text, ok := child.(string); ok && key == "description" {
				*found = append(*found, text)
			}
			descriptionsIn(child, found)
		}
	case []any:
		for _, child := range v {
			descriptionsIn(child, found)
		}
	}
}

// What a provider is told on the engineering route of a session whose executor returns the exit code alone
// (serve --pi) is what the adapter registers, word for word, and each schema hashes to the digest
// adapter/profile.json pins. A product turn, whose executor releases command output, is told the same
// except for the bash description, and its schemas hash to the same pins. (The adapter's own test holds its
// schema to the same pins, and piruntime holds the worker's copy to them, so the three cannot drift apart.)
func TestAdvertisedToolSurfaceEqualsTheAdapterTextAndItsPins(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "adapter", "src", "proxy-tools.ts"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := string(data)
	registered := map[string]string{}
	for _, m := range regexp.MustCompile(`makeProxyTool\(\s*"(\w+)",\s*[\w.]+,\s*"([^"]*)"`).FindAllStringSubmatch(adapter, -1) {
		registered[m[1]] = m[2]
	}
	pinned, err := os.ReadFile(filepath.Join("..", "..", "..", "adapter", "profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	var profile struct {
		Digests map[string]string `json:"schema_sha256"`
	}
	if err := json.Unmarshal(pinned, &profile); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name       string
		route      openrouter.Route
		bashOutput bool
	}{
		{"serve --pi, frozen description", openrouter.Route{}, false},
		{"product turn, bounded output", openrouter.EngineeringRoute().WithCommandOutput(true), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			checkAdvertisedSurface(t, adapter, registered, profile.Digests, c.route, c.bashOutput)
		})
	}
}

func checkAdvertisedSurface(t *testing.T, adapter string, registered, digests map[string]string, route openrouter.Route, bashOutput bool) {
	t.Helper()
	tools := advertisedTools(t, route)
	if len(tools) != len(DeclaredToolManifest()) {
		t.Fatalf("the provider is told %d tools, want %d", len(tools), len(DeclaredToolManifest()))
	}
	for _, tool := range tools {
		name := tool.Function.Name
		if told := tool.Function.Description; name == "bash" && bashOutput {
			if told == registered[name] || !strings.Contains(told, "stdout and stderr") {
				t.Errorf("bash: the provider is told %q on a route whose executor releases output, the adapter registers %q", told, registered[name])
			}
		} else if registered[name] != told {
			t.Errorf("%s: the adapter registers the description %q, the provider is told %q", name, registered[name], told)
		}
		var schema any
		if err := json.Unmarshal(tool.Function.Parameters, &schema); err != nil {
			t.Fatal(err)
		}
		var texts []string
		descriptionsIn(schema, &texts)
		for _, text := range texts {
			if !strings.Contains(adapter, strconv.Quote(text)) {
				t.Errorf("%s: the adapter source lacks the parameter description %q", name, text)
			}
		}
		var canonical bytes.Buffer
		encoder := json.NewEncoder(&canonical)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(schema); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(bytes.TrimSuffix(canonical.Bytes(), []byte("\n")))
		if got := hex.EncodeToString(sum[:]); got != digests[name] {
			t.Errorf("%s: the advertised schema hashes to %s, adapter/profile.json pins %s", name, got, digests[name])
		}
	}
}
