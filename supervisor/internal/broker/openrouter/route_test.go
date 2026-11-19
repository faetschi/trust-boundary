package openrouter

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const (
	researchTestModel    = "deepseek/deepseek-v4.1-flash"
	researchTestEndpoint = "https://openrouter.ai/api/v1/chat/completions"
	researchTestMembers  = `{"provider":{"order":["example-provider"],"allow_fallbacks":false,"data_collection":"deny","require_parameters":true,"zdr":true},"temperature":0,"seed":7,"usage":{"include":true}}`
	researchTestDigest   = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func researchTestRoute(t *testing.T) Route {
	t.Helper()
	route, err := NewResearchRoute(ResearchSpec{Model: researchTestModel, Endpoint: researchTestEndpoint,
		ProfileDigest: researchTestDigest, Members: researchTestMembers, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("NewResearchRoute: %v", err)
	}
	return route
}

func TestZeroRouteIsTheEngineeringRoute(t *testing.T) {
	var zero Route
	if zero.IsResearch() || zero.Resolved() != EngineeringRoute() {
		t.Fatalf("zero route is not the engineering route: %+v", zero.Resolved())
	}
	if EngineeringRoute().Model != ApprovedModel || EngineeringRoute().Endpoint != Endpoint {
		t.Fatalf("engineering route drifted: %+v", EngineeringRoute())
	}
}

func TestBuildRequestForEngineeringRouteIsByteIdenticalToBuildRequest(t *testing.T) {
	messages := []Message{{Role: User, Content: strptr("hello")}}
	for _, limit := range []uint64{0, 512} {
		want, err := BuildRequestWithCompletionLimit(ApprovedModel, messages, limit)
		if err != nil {
			t.Fatal(err)
		}
		for _, route := range []Route{{}, EngineeringRoute()} {
			got, err := BuildRequestForRoute(route, messages, limit)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Body(), want.Body()) || got.RequestURL() != Endpoint {
				t.Fatalf("engineering route changed the request for limit %d", limit)
			}
		}
	}
}

func TestBuildRequestForResearchRouteUsesTheProfileEndpointModelAndParameters(t *testing.T) {
	route := researchTestRoute(t)
	built, err := BuildRequestForRoute(route, []Message{{Role: User, Content: strptr("hello")}}, 256)
	if err != nil {
		t.Fatal(err)
	}
	if built.RequestURL() != researchTestEndpoint {
		t.Fatalf("research endpoint = %q", built.RequestURL())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(built.Body(), &body); err != nil {
		t.Fatalf("research body is not one JSON object: %v", err)
	}
	for _, key := range []string{"model", "messages", "tools", "stream", "tool_choice",
		"max_completion_tokens", "provider", "temperature", "seed", "usage"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("research body lacks %q: %s", key, built.Body())
		}
	}
	if _, ok := body["thinking"]; ok {
		t.Fatal("research body carries the OpenCode Go thinking member")
	}
	// require_parameters drops every provider that lacks a sent member; DeepInfra lacks parallel_tool_calls.
	none, err := BuildToolsDisabledRequestForRoute(route, []Message{{Role: User, Content: strptr("hello")}}, 256)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{built.Body(), none.Body()} {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(raw, &members); err != nil {
			t.Fatal(err)
		}
		if _, ok := members["parallel_tool_calls"]; ok {
			t.Fatalf("research body carries parallel_tool_calls: %s", raw)
		}
	}
	var model string
	if err := json.Unmarshal(body["model"], &model); err != nil || model != researchTestModel {
		t.Fatalf("research model = %q (%v)", model, err)
	}
	if string(body["max_completion_tokens"]) != "256" || string(body["temperature"]) != "0" || string(body["seed"]) != "7" {
		t.Fatalf("research request limits differ: %s", built.Body())
	}
	if !strings.HasSuffix(string(built.Body()), `,"provider":{"order":["example-provider"],"allow_fallbacks":false,"data_collection":"deny","require_parameters":true,"zdr":true},"temperature":0,"seed":7,"usage":{"include":true}}`) {
		t.Fatalf("frozen members are not appended verbatim: %s", built.Body())
	}
	again, err := BuildRequestForRoute(route, []Message{{Role: User, Content: strptr("hello")}}, 256)
	if err != nil || !bytes.Equal(again.Body(), built.Body()) {
		t.Fatalf("research request is not deterministic: %v", err)
	}
}

func TestResearchRouteIsFixedAndValidated(t *testing.T) {
	base := ResearchSpec{Model: researchTestModel, Endpoint: researchTestEndpoint,
		ProfileDigest: researchTestDigest, Members: researchTestMembers, Timeout: time.Second}
	mutate := func(edit func(*ResearchSpec)) ResearchSpec { spec := base; edit(&spec); return spec }
	for name, spec := range map[string]ResearchSpec{
		"engineering model":   mutate(func(s *ResearchSpec) { s.Model = ApprovedModel }),
		"empty model":         mutate(func(s *ResearchSpec) { s.Model = "" }),
		"plain http":          mutate(func(s *ResearchSpec) { s.Endpoint = "http://openrouter.ai/api/v1/chat/completions" }),
		"foreign host":        mutate(func(s *ResearchSpec) { s.Endpoint = "https://example.invalid/api/v1/chat/completions" }),
		"engineering host":    mutate(func(s *ResearchSpec) { s.Endpoint = Endpoint }),
		"short digest":        mutate(func(s *ResearchSpec) { s.ProfileDigest = "abc" }),
		"upper digest":        mutate(func(s *ResearchSpec) { s.ProfileDigest = strings.ToUpper(researchTestDigest) }),
		"empty members":       mutate(func(s *ResearchSpec) { s.Members = "" }),
		"members not object":  mutate(func(s *ResearchSpec) { s.Members = `[1]` }),
		"members whitespace":  mutate(func(s *ResearchSpec) { s.Members = `{"seed": 7}` }),
		"members override":    mutate(func(s *ResearchSpec) { s.Members = `{"model":"other"}` }),
		"members stream":      mutate(func(s *ResearchSpec) { s.Members = `{"stream":false}` }),
		"members thinking":    mutate(func(s *ResearchSpec) { s.Members = `{"thinking":{"type":"enabled"}}` }),
		"members max tokens":  mutate(func(s *ResearchSpec) { s.Members = `{"max_tokens":1}` }),
		"members tools":       mutate(func(s *ResearchSpec) { s.Members = `{"tools":[]}` }),
		"no timeout":          mutate(func(s *ResearchSpec) { s.Timeout = 0 }),
		"timeout above bound": mutate(func(s *ResearchSpec) { s.Timeout = MaxHTTPTimeout + time.Second }),
	} {
		if _, err := NewResearchRoute(spec); err == nil {
			t.Errorf("research route accepted %s", name)
		}
	}
}

func TestResearchRouteAcceptsOnlyTheOpenRouterURLOrALoopbackFixture(t *testing.T) {
	spec := ResearchSpec{Model: researchTestModel, Endpoint: "http://127.0.0.1:8080/v1/chat/completions",
		ProfileDigest: researchTestDigest, Members: researchTestMembers, Timeout: time.Second}
	if _, err := NewResearchRoute(spec); err != nil {
		t.Fatalf("loopback offline-fixture endpoint refused: %v", err)
	}
	spec.Endpoint = "http://192.0.2.1/v1/chat/completions"
	if _, err := NewResearchRoute(spec); err == nil {
		t.Fatal("non-loopback plain-HTTP endpoint accepted")
	}
}

func TestEngineeringBuilderRefusesAResearchModelAndTheResearchBuilderRefusesAnEngineeringModel(t *testing.T) {
	messages := []Message{{Role: User, Content: strptr("hello")}}
	if _, err := BuildRequest(researchTestModel, messages); err == nil {
		t.Fatal("engineering builder accepted the research model")
	}
	if _, err := BuildRequestForRoute(Route{Model: researchTestModel, Endpoint: researchTestEndpoint}, messages, 0); err == nil {
		t.Fatal("a route without frozen members was built")
	}
	if _, err := BuildRequestForRoute(Route{Model: "other", Endpoint: Endpoint}, messages, 0); err == nil {
		t.Fatal("an unregistered engineering model was built")
	}
}

func TestHTTPDoerForEndpointRequiresHTTPSExceptLoopback(t *testing.T) {
	key := func() (string, error) { return "test-key-123", nil }
	if _, err := NewHTTPDoerForEndpoint(key, time.Second, researchTestEndpoint); err != nil {
		t.Fatalf("research HTTPS endpoint refused: %v", err)
	}
	if _, err := NewHTTPDoerForEndpoint(key, time.Second, "http://127.0.0.1:1/x"); err != nil {
		t.Fatalf("loopback fixture endpoint refused: %v", err)
	}
	for _, endpoint := range []string{"http://openrouter.ai/api/v1/chat/completions", "http://203.0.113.5/x", "ftp://127.0.0.1/x", "https://user@openrouter.ai/x", ""} {
		if _, err := NewHTTPDoerForEndpoint(key, time.Second, endpoint); err == nil {
			t.Errorf("endpoint %q accepted", endpoint)
		}
	}
}
