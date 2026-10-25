// Package productprovider defines the closed product provider table. Research
// profiles and caller-selected URLs never enter this table.
package productprovider

import (
	"math"

	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
)

const (
	Schema              = "tbound-product-provider-profile/v1"
	OpenCodeGo          = "opencode-go-engineering"
	CredentialDirectory = "/etc/tbound-product/credentials"
	NoPriceTable        = "no price table: monetary ceilings unsupported"
)

// Profile is descriptive. Execution always looks up the compiled ID again;
// modifying a returned value cannot change the endpoint or any bound.
type Profile struct {
	Schema                string   `json:"schema"`
	ID                    string   `json:"id"`
	Endpoint              string   `json:"endpoint"`
	Protocol              string   `json:"protocol"`
	Model                 string   `json:"model"`
	ResponseModels        []string `json:"response_models"`
	RequestOptions        string   `json:"request_options"`
	MinCompletionTokens   uint64   `json:"min_completion_tokens"`
	MaxCompletionTokens   uint64   `json:"max_completion_tokens"`
	TimeoutSeconds        int      `json:"timeout_seconds"`
	MaxRequestBytes       int      `json:"max_request_bytes"`
	MaxResponseBytes      int      `json:"max_response_bytes"`
	MaxExchangeBytes      int      `json:"max_exchange_bytes"`
	MaxSSEEventBytes      int      `json:"max_sse_event_bytes"`
	MaxSSEEvents          int      `json:"max_sse_events"`
	MaxToolArgumentsBytes int      `json:"max_tool_arguments_bytes"`
	MaxAssistantTextBytes int      `json:"max_assistant_text_bytes"`
	Pricing               string   `json:"pricing"`
}

func Lookup(id string) (Profile, bool) {
	if id != OpenCodeGo {
		return Profile{}, false
	}
	r := openrouter.EngineeringRoute()
	return Profile{
		Schema: Schema, ID: id, Endpoint: r.Endpoint, Protocol: "chat-completions-sse",
		Model: r.Model, ResponseModels: r.ResponseModels(),
		RequestOptions:      "stream=true; parallel_tool_calls=false; tool_choice=auto; thinking=disabled; tools=read,write,edit,bash; max_completion_tokens=host-selected; no other options",
		MinCompletionTokens: 1, MaxCompletionTokens: math.MaxInt32,
		TimeoutSeconds:  int(openrouter.DefaultHTTPTimeout.Seconds()),
		MaxRequestBytes: openrouter.MaxRequestBytes, MaxResponseBytes: openrouter.MaxResponseBytes,
		MaxExchangeBytes: broker.MaxDurableExchangeBytes,
		MaxSSEEventBytes: openrouter.MaxSSEEventBytes, MaxSSEEvents: openrouter.MaxSSEEvents,
		MaxToolArgumentsBytes: openrouter.MaxToolArgumentsBytes, MaxAssistantTextBytes: openrouter.MaxAssistantTextBytes,
		Pricing: NoPriceTable,
	}, true
}

func Advertised() []Profile { p, _ := Lookup(OpenCodeGo); return []Profile{p} }

func CredentialPath(id string) (string, bool) {
	if _, ok := Lookup(id); !ok {
		return "", false
	}
	return CredentialDirectory + "/" + id + ".key", true
}
