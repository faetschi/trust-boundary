package openrouter

import (
	"errors"
	"strings"
	"testing"
)

const piBody = `{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true},"store":false,"max_completion_tokens":16384,"tools":[{"type":"function","function":{"name":"read","description":"d","parameters":{"type":"object"}}}]}`

func TestForwardedRequestKeepsOnlyAllowlistedMembersAndPinsTheRest(t *testing.T) {
	route := researchTestRoute(t)
	built, err := BuildForwardedRequest(route, []byte(piBody), 4096)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read","description":"d","parameters":{"type":"object"}}}],"stream":true,"max_completion_tokens":4096,` + researchTestMembers[1:]
	if string(built.Body()) != want || built.RequestURL() != researchTestEndpoint {
		t.Fatalf("body\n%s\nwant\n%s", built.Body(), want)
	}
}

func TestForwardedRequestDropsEveryNonAllowlistedMember(t *testing.T) {
	route := researchTestRoute(t)
	hostile := `{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}],"stream":true,` +
		`"models":["x/y"],"provider":{"order":["evil"]},"route":"fallback","transforms":["middle-out"],"plugins":[{"id":"web"}],` +
		`"max_tokens":999999,"max_completion_tokens":999999,"temperature":2,"seed":1,"reasoning":{"effort":"xhigh"},"usage":{"include":false},"user":"u"}`
	built, err := BuildForwardedRequest(route, []byte(hostile), 4096)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}],"stream":true,"max_completion_tokens":4096,` + researchTestMembers[1:]
	if string(built.Body()) != want {
		t.Fatalf("body\n%s\nwant\n%s", built.Body(), want)
	}
}

func TestForwardedRequestRefusesARebuiltBodyOverTheSizeLimit(t *testing.T) {
	route := researchTestRoute(t)
	body := func(c string) []byte {
		return []byte(`{"model":"deepseek/deepseek-v4.1-flash","stream":true,"messages":[{"role":"user","content":"` + strings.Repeat(c, 3<<18) + `"}]}`)
	}
	if raw := body("<"); len(raw) > MaxRequestBytes {
		t.Fatalf("the raw body (%d bytes) is already over the limit", len(raw))
	} else if _, err := BuildForwardedRequest(route, raw, 4096); !errors.Is(err, ErrForwardedRequest) {
		t.Fatalf("a body whose rebuilt form is over MaxRequestBytes: %v", err)
	}
	if _, err := BuildForwardedRequest(route, body("a"), 4096); err != nil {
		t.Fatalf("the same size without escaping: %v", err)
	}
}

func TestForwardedRequestRefusals(t *testing.T) {
	route := researchTestRoute(t)
	tooLarge := `{"model":"deepseek/deepseek-v4.1-flash","stream":true,"messages":[{"role":"user","content":"` + strings.Repeat("a", MaxRequestBytes) + `"}]}`
	for name, tc := range map[string]struct {
		body string
		want error
	}{
		"other model":        {strings.Replace(piBody, "deepseek/deepseek-v4.1-flash", "other/model", 1), ErrForwardedModel},
		"missing model":      {`{"messages":[{"role":"user","content":"hi"}],"stream":true}`, ErrForwardedRequest},
		"not streaming":      {strings.Replace(piBody, `"stream":true`, `"stream":false`, 1), ErrForwardedRequest},
		"no stream member":   {`{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`, ErrForwardedRequest},
		"no messages":        {`{"model":"deepseek/deepseek-v4.1-flash","stream":true}`, ErrForwardedRequest},
		"empty messages":     {`{"model":"deepseek/deepseek-v4.1-flash","stream":true,"messages":[]}`, ErrForwardedRequest},
		"tools not an array": {`{"model":"deepseek/deepseek-v4.1-flash","stream":true,"messages":[{}],"tools":{}}`, ErrForwardedRequest},
		"bad tool choice":    {`{"model":"deepseek/deepseek-v4.1-flash","stream":true,"messages":[{}],"tool_choice":{"type":"function"}}`, ErrForwardedRequest},
		"duplicate member":   {`{"model":"deepseek/deepseek-v4.1-flash","stream":true,"messages":[{}],"messages":[{}]}`, ErrForwardedRequest},
		"not an object":      {`[]`, ErrForwardedRequest},
		"empty":              {``, ErrForwardedRequest},
		"over the byte cap":  {tooLarge, ErrForwardedRequest},
	} {
		if _, err := BuildForwardedRequest(route, []byte(tc.body), 4096); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	if _, err := BuildForwardedRequest(route, []byte(piBody), 0); !errors.Is(err, ErrForwardedRequest) {
		t.Errorf("zero completion cap: %v", err)
	}
	if _, err := BuildForwardedRequest(Route{}, []byte(piBody), 4096); !errors.Is(err, ErrForwardedRequest) {
		t.Errorf("engineering route: %v", err)
	}
}

func TestForwardedRequestKeepsToolChoice(t *testing.T) {
	route := researchTestRoute(t)
	body := strings.Replace(piBody, `"store":false`, `"store":false,"tool_choice":"none"`, 1)
	built, err := BuildForwardedRequest(route, []byte(body), 4096)
	if err != nil || !strings.Contains(string(built.Body()), `"stream":true,"tool_choice":"none","max_completion_tokens":4096`) {
		t.Fatalf("%s %v", built.Body(), err)
	}
}

func forwardedBody(messages, tools string) string {
	body := `{"model":"deepseek/deepseek-v4.1-flash","stream":true,"messages":` + messages
	if tools != "" {
		body += `,"tools":` + tools
	}
	return body + `}`
}

func TestForwardedRequestRebuildsToolsAndMessagesFromTheirTypedMembers(t *testing.T) {
	route := researchTestRoute(t)
	messages := `[{"role":"system","content":"s"},{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]},` +
		`{"role":"assistant","content":null,"reasoning":"r","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{\"path\":\"x\"}"}},{"id":"c2","type":"function","function":{"name":"bash","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","name":"read","content":"out"},{"role":"tool","tool_call_id":"c2","content":[{"type":"text","text":"o"}]}]`
	tools := `[{"type":"function","function":{"name":"read","description":"d","parameters":{"type":"object"}}}]`
	built, err := BuildForwardedRequest(route, []byte(forwardedBody(messages, tools)), 4096)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"deepseek/deepseek-v4.1-flash","messages":[{"role":"system","content":"s"},{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{\"path\":\"x\"}"}},{"id":"c2","type":"function","function":{"name":"bash","arguments":"{}"}}],"reasoning":"r"},` +
		`{"role":"tool","content":"out","tool_call_id":"c1","name":"read"},{"role":"tool","content":[{"type":"text","text":"o"}],"tool_call_id":"c2"}],` +
		`"tools":` + tools + `,"stream":true,"max_completion_tokens":4096,` + researchTestMembers[1:]
	if string(built.Body()) != want {
		t.Fatalf("body\n%s\nwant\n%s", built.Body(), want)
	}
}

func TestForwardedRequestRefusesMembersThatCouldBeBilledOutsideTheSizeGate(t *testing.T) {
	route := researchTestRoute(t)
	const user = `{"role":"user","content":"hi"}`
	tool := func(extra string) string {
		return `[{"type":"function","function":{"name":"read","description":"d","parameters":{"type":"object"}` + extra + `}}]`
	}
	for name, body := range map[string]string{
		"provider server tool":         forwardedBody(`[`+user+`]`, `[{"type":"openrouter:web_search"}]`),
		"server tool with parameters":  forwardedBody(`[`+user+`]`, `[{"type":"openrouter:web_search","parameters":{"max_results":100}}]`),
		"tool without a type":          forwardedBody(`[`+user+`]`, `[{"function":{"name":"read","description":"d","parameters":{}}}]`),
		"extra tool member":            forwardedBody(`[`+user+`]`, `[{"type":"function","cache_control":{"type":"ephemeral"},"function":{"name":"read","description":"d","parameters":{}}}]`),
		"extra function member":        forwardedBody(`[`+user+`]`, tool(`,"cache_control":{"type":"ephemeral"}`)),
		"function without description": forwardedBody(`[`+user+`]`, `[{"type":"function","function":{"name":"read","parameters":{}}}]`),
		"function with a bad name":     forwardedBody(`[`+user+`]`, `[{"type":"function","function":{"name":"","description":"d","parameters":{}}}]`),
		"parameters not an object":     forwardedBody(`[`+user+`]`, `[{"type":"function","function":{"name":"read","description":"d","parameters":"x"}}]`),
		"tool not an object":           forwardedBody(`[`+user+`]`, `["read"]`),
		"image part":                   forwardedBody(`[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]`, ""),
		"file part":                    forwardedBody(`[{"role":"user","content":[{"type":"file","file":{"file_data":"https://example.com/a.pdf"}}]}]`, ""),
		"audio part":                   forwardedBody(`[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}]}]`, ""),
		"text part with extra member":  forwardedBody(`[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]}]`, ""),
		"text part without text":       forwardedBody(`[{"role":"user","content":[{"type":"text"}]}]`, ""),
		"empty parts":                  forwardedBody(`[{"role":"user","content":[]}]`, ""),
		"content a number":             forwardedBody(`[{"role":"user","content":5}]`, ""),
		"content an object":            forwardedBody(`[{"role":"user","content":{"type":"text","text":"a"}}]`, ""),
		"extra message member":         forwardedBody(`[{"role":"user","content":"hi","audio":{"id":"a"}}]`, ""),
		"reasoning_content member":     forwardedBody(`[{"role":"assistant","content":"hi","reasoning_content":"r"}]`, ""),
		"message without a role":       forwardedBody(`[{"content":"hi"}]`, ""),
		"unknown role":                 forwardedBody(`[{"role":"function","content":"hi"}]`, ""),
		"user message with tool calls": forwardedBody(`[{"role":"user","content":"hi","tool_calls":[{"id":"c","type":"function","function":{"name":"read","arguments":"{}"}}]}]`, ""),
		"reasoning on a user message":  forwardedBody(`[{"role":"user","content":"hi","reasoning":"r"}]`, ""),
		"tool result without an id":    forwardedBody(`[{"role":"tool","content":"x"}]`, ""),
		"tool call of another type":    forwardedBody(`[{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"custom","function":{"name":"read","arguments":"{}"}}]}]`, ""),
		"tool call extra member":       forwardedBody(`[{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","index":0,"function":{"name":"read","arguments":"{}"}}]}]`, ""),
		"tool call extra function":     forwardedBody(`[{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"read","arguments":"{}","extra":1}}]}]`, ""),
		"tool arguments not a string":  forwardedBody(`[{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"read","arguments":{}}}]}]`, ""),
		"repeated tool call id":        forwardedBody(`[{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"read","arguments":"{}"}},{"id":"c","type":"function","function":{"name":"read","arguments":"{}"}}]}]`, ""),
		"empty message name":           forwardedBody(`[{"role":"tool","tool_call_id":"c","name":"","content":"x"}]`, ""),
	} {
		if _, err := BuildForwardedRequest(route, []byte(body), 4096); !errors.Is(err, ErrForwardedRequest) {
			t.Errorf("%s: err = %v, want ErrForwardedRequest", name, err)
		}
	}
}

func TestForwardedRequestDropsPiToolStrictFlag(t *testing.T) {
	route := researchTestRoute(t)
	messages := `[{"role":"user","content":"a"}]`
	sent := `[{"type":"function","function":{"name":"read","description":"d","parameters":{"type":"object"},"strict":false}}]`
	built, err := BuildForwardedRequest(route, []byte(forwardedBody(messages, sent)), 4096)
	if err != nil {
		t.Fatalf("Pi's strict:false tool flag was refused: %v", err)
	}
	if strings.Contains(string(built.Body()), "strict") {
		t.Fatalf("strict reached the upstream body: %s", built.Body())
	}
	bad := `[{"type":"function","function":{"name":"read","description":"d","parameters":{"type":"object"},"strict":"no"}}]`
	if _, err := BuildForwardedRequest(route, []byte(forwardedBody(messages, bad)), 4096); err == nil {
		t.Fatal("a non-boolean strict was accepted")
	}
}
