package openrouter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// sortedSchemaDigest is the pin format of adapter/profile.json: the SHA-256 of the schema as compact JSON
// with sorted keys and no HTML escaping.
func sortedSchemaDigest(t *testing.T, schema json.RawMessage) string {
	t.Helper()
	var decoded any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(decoded); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bytes.TrimSuffix(buffer.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:])
}

// The research tool surface is what the thesis provider-request digests were measured with. These are the
// digests it had before the engineering surface was corrected.
func TestResearchToolSurfaceIsFrozen(t *testing.T) {
	want := map[string]string{
		"read":  "5fad0fa7493528bc4284f5c447781325f67099f5fc018206efdc461d33962e85",
		"write": "7e51d1de0f2ccb5fe8de82d51ee3a1d9065e3c4f7c53497ce12f283d6f2f5aa0",
		"edit":  "394741d0cb4c9a6fe96d3275897405c8b13a1c7ff712426a942c10d484993fa1",
		"bash":  "854beb37435894c8f97dcad8d4610c0ea1458b57a6a8d4bb5b0ea435cdaa5893",
	}
	for _, tool := range researchTools() {
		if got := sortedSchemaDigest(t, tool.Function.Parameters); got != want[tool.Function.Name] {
			t.Errorf("research %s schema digest = %s, want %s", tool.Function.Name, got, want[tool.Function.Name])
		}
	}
	descriptions := map[string]string{
		"read":  "Read the contents of a file. Supports text files and images. Requests are forwarded to tbound.",
		"write": "Write content to a file. Requests are forwarded to tbound.",
		"edit":  "Edit a single file using exact text replacements. Requests are forwarded to tbound.",
		"bash":  "Execute a bash command. Requests are forwarded to tbound.",
	}
	for _, tool := range researchTools() {
		if tool.Function.Description != descriptions[tool.Function.Name] {
			t.Errorf("research %s description changed: %q", tool.Function.Name, tool.Function.Description)
		}
	}
}

// A research route sends the frozen surface, and the engineering route sends the corrected one.
func TestRoutesSendTheirOwnToolSurface(t *testing.T) {
	messages := []Message{{Role: User, Content: strptr("hello")}}
	research, err := BuildRequestForRoute(researchTestRoute(t), messages, 0)
	if err != nil {
		t.Fatal(err)
	}
	engineering, err := BuildRequestForRoute(EngineeringRoute(), messages, 0)
	if err != nil {
		t.Fatal(err)
	}
	frozen, _ := json.Marshal(researchTools())
	corrected, _ := json.Marshal(tools())
	if !bytes.Contains(research.Body(), frozen) || bytes.Contains(research.Body(), corrected) {
		t.Error("the research request does not carry exactly the frozen tool surface")
	}
	if !bytes.Contains(engineering.Body(), corrected) || bytes.Contains(engineering.Body(), frozen) {
		t.Error("the engineering request does not carry exactly the corrected tool surface")
	}
	for _, retired := range []string{`"timeout"`, `"offset"`, `"limit"`, "images", "relative or absolute"} {
		if strings.Contains(string(corrected), retired) {
			t.Errorf("the corrected surface still advertises %s", retired)
		}
	}
}

// bashDescriptionOf is the description of the bash tool in a built request.
func bashDescriptionOf(t *testing.T, built BuiltRequest) string {
	t.Helper()
	var body struct {
		Tools []struct {
			Function struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(built.Body(), &body); err != nil {
		t.Fatal(err)
	}
	for _, tool := range body.Tools {
		if tool.Function.Name == "bash" {
			return tool.Function.Description
		}
	}
	t.Fatal("the request advertises no bash tool")
	return ""
}

// bashExitOnlyDescription is the bash description of every engineering request before a product turn
// could show the model command output. A session whose executor returns only the exit code still sends it.
const bashExitOnlyDescription = "Execute a bash command. Requests are forwarded to tbound."

// The zero route is a session that was never given a command output bound (serve --pi). Its executor
// returns the exit code alone, so its request must not promise stdout and stderr.
func TestEngineeringRouteAdvertisesTheExitOnlyBashDescription(t *testing.T) {
	messages := []Message{{Role: User, Content: strptr("hello")}}
	for name, route := range map[string]Route{"zero route": {}, "engineering route": EngineeringRoute()} {
		built, err := BuildRequestForRoute(route, messages, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := bashDescriptionOf(t, built); got != bashExitOnlyDescription {
			t.Errorf("%s advertises the bash description %q, want %q", name, got, bashExitOnlyDescription)
		}
	}
}
