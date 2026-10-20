package providerbridge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"tbound/supervisor/internal/broker/openrouter"
)

func promptRoles(t *testing.T, route openrouter.Route) (roles []string, contents []string) {
	t.Helper()
	user := "user task"
	messages := append(initialPromptMessages(route, "system text", "developer text"), openrouter.Message{Role: openrouter.User, Content: &user})
	built, err := openrouter.BuildRequestForRoute(route, messages, 512)
	if err != nil {
		t.Fatalf("the immutable prompt does not build a request: %v", err)
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(built.Body(), &body); err != nil {
		t.Fatal(err)
	}
	for _, message := range body.Messages {
		roles, contents = append(roles, message.Role), append(contents, message.Content)
	}
	return roles, contents
}

// OpenCode Go answers 400 "chat message role developer is not supported" for the developer role.
func TestEngineeringRouteSendsTheDeveloperPromptAsASecondSystemMessage(t *testing.T) {
	roles, contents := promptRoles(t, openrouter.EngineeringRoute())
	if strings.Join(roles, ",") != "system,system,user" || contents[0] != "system text" || contents[1] != "developer text" {
		t.Fatalf("wire messages = %v %v, want system(system text), system(developer text), user", roles, contents)
	}
	// The zero route is the engineering route; the fresh and recovery paths resolve it before use.
	if zeroRoles, _ := promptRoles(t, openrouter.Route{}.Resolved()); strings.Join(zeroRoles, ",") != "system,system,user" {
		t.Fatalf("zero route roles = %v", zeroRoles)
	}
}

func TestResearchRouteKeepsTheDeveloperRole(t *testing.T) {
	route, err := openrouter.NewResearchRoute(openrouter.ResearchSpec{Model: "deepseek/deepseek-v4.1-flash",
		Endpoint:      "https://openrouter.ai/api/v1/chat/completions",
		ProfileDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Members:       `{"provider":{"order":["example-provider"],"allow_fallbacks":false,"data_collection":"deny","require_parameters":true,"zdr":true},"temperature":0,"seed":7,"usage":{"include":true}}`,
		Timeout:       30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if roles, _ := promptRoles(t, route); strings.Join(roles, ",") != "system,developer,user" {
		t.Fatalf("research roles = %v, want the frozen system,developer,user", roles)
	}
}

func TestRebuiltPromptPrefixIsIdentical(t *testing.T) {
	first := initialPromptMessages(openrouter.EngineeringRoute(), "s", "d")
	second := initialPromptMessages(openrouter.EngineeringRoute(), "s", "d")
	a, errA := json.Marshal(first)
	b, errB := json.Marshal(second)
	if errA != nil || errB != nil || string(a) != string(b) {
		t.Fatalf("the prompt prefix is not reproducible: %s vs %s", a, b)
	}
}
