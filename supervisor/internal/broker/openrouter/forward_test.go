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
