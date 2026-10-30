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
	corrected, _ := json.Marshal(tools(false))
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

// bashExitOnlyDescription retains the short description pinned before product output was introduced.
const bashExitOnlyDescription = "Execute a bash command. Requests are forwarded to tbound."

// The zero route keeps its frozen description even though its executor releases thesis stream tails.
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

// A product turn's route says its executor releases bounded output, and then only the bash description
// differs: every name, every other description and every parameter schema, so every pinned digest, is the same.
func TestCommandOutputOnlyChangesTheBashDescription(t *testing.T) {
	product := EngineeringRoute().WithCommandOutput(true)
	if err := product.Validate(); err != nil {
		t.Fatal(err)
	}
	built, err := BuildRequestForRoute(product, []Message{{Role: User, Content: strptr("hello")}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := bashDescriptionOf(t, built); got == bashExitOnlyDescription ||
		!strings.Contains(got, "stdout and stderr") || !strings.Contains(got, "last 16 KiB") {
		t.Errorf("the product route advertises the bash description %q", got)
	}
	exitOnly, withOutput := tools(false), tools(true)
	if len(exitOnly) != len(withOutput) {
		t.Fatalf("the two engineering surfaces list %d and %d tools", len(exitOnly), len(withOutput))
	}
	for i, tool := range exitOnly {
		other := withOutput[i]
		if tool.Function.Name != other.Function.Name ||
			sortedSchemaDigest(t, tool.Function.Parameters) != sortedSchemaDigest(t, other.Function.Parameters) {
			t.Errorf("%s: the schema or the name depends on the command output mode", tool.Function.Name)
		}
		if differs := tool.Function.Description != other.Function.Description; differs != (tool.Function.Name == "bash") {
			t.Errorf("%s: the description differs between the output modes = %t", tool.Function.Name, differs)
		}
	}
}

// The mark belongs to the engineering route alone: a research route keeps its frozen surface, and a route
// assembled by hand does not become valid by carrying it.
func TestCommandOutputMarkOnlyBelongsToTheEngineeringRoute(t *testing.T) {
	if (Route{}).Resolved() != EngineeringRoute() || EngineeringRoute().CommandOutput {
		t.Fatal("the unmarked engineering route changed")
	}
	if marked := (Route{}).WithCommandOutput(true); marked.Model != ApprovedModel || marked.Endpoint != Endpoint ||
		!marked.CommandOutput || marked.WithCommandOutput(false) != EngineeringRoute() {
		t.Errorf("the marked route is not the engineering route with the mark: %+v", marked)
	}
	research := researchTestRoute(t)
	research.CommandOutput = true
	for name, route := range map[string]Route{"research route": research, "route without a model": {CommandOutput: true}} {
		if err := route.Validate(); err == nil {
			t.Errorf("%s with the command output mark was accepted", name)
		}
		if _, err := BuildRequestForRoute(route, []Message{{Role: User, Content: strptr("hello")}}, 0); err == nil {
			t.Errorf("%s with the command output mark built a request", name)
		}
	}
}
