package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
)

const (
	profileID            = "tbound-openrouter-one-shot-v1"
	callIssuer           = "openrouter/one-shot-tool-call/v1"
	responseIDIssuer     = "openrouter/one-shot-response/v1"
	registeredGeneration = "one-shot"
)

func main() {
	credentials, err := openrouter.LoadCredentials()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tbound-provider: "+err.Error())
		os.Exit(2)
	}
	doer, err := credentials.NewHTTPDoer(openrouter.DefaultHTTPTimeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tbound-provider: provider transport configuration failed")
		os.Exit(2)
	}
	if err := runOneShot(context.Background(), credentials, doer, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "tbound-provider: "+credentials.Redact(err.Error()))
		os.Exit(1)
	}
}

func runOneShot(ctx context.Context, credentials openrouter.Credentials, doer broker.HTTPDoer, output io.Writer) error {
	if credentials.ModelID() == "" || doer == nil || output == nil {
		return errors.New("one-shot provider exchange is not configured")
	}
	prompt := "Call the read tool exactly once with arguments {\"path\":\"README.md\"}. Do not add commentary."
	profile := broker.Profile{
		ID:               profileID,
		Model:            credentials.ModelID(),
		Messages:         []openrouter.Message{{Role: openrouter.User, Content: &prompt}},
		ToolManifest:     broker.DeclaredToolManifest(),
		ToolCallIssuer:   callIssuer,
		ResponseIDIssuer: responseIDIssuer,
		Generation:       registeredGeneration,
	}
	providerBroker, err := broker.New(profile, doer)
	if err != nil {
		return errors.New(credentials.Redact(err.Error()))
	}
	captured, err := providerBroker.Exchange(ctx)
	if err != nil {
		return errors.New(credentials.Redact(err.Error()))
	}
	records := providerBroker.Records()
	if len(records) != 1 || !records[0].HasResponse() {
		return errors.New("one-shot exchange did not produce exactly one bounded response record")
	}
	responseBody := records[0].ResponseBody()
	if len(responseBody) == 0 || len(responseBody) > openrouter.MaxResponseBytes {
		return errors.New("one-shot response length is outside the configured bound")
	}
	manifestRespected, err := requestManifestRespected(records[0].Request().Body())
	if err != nil {
		return errors.New("registered tool manifest could not be verified")
	}
	callCount := 0
	if call, ok := captured.ToolCall(); ok {
		callCount = 1
		manifestRespected = manifestRespected && contains(broker.DeclaredToolManifest(), call.Name)
	}
	_, err = fmt.Fprintf(output,
		"redacted_summary=true status_code=%d response_id=%s response_model=%s captured_tool_calls=%d ordered_four_tool_manifest_respected=%t response_bytes=%d max_response_bytes=%d containment=not-established g1=false\n",
		records[0].StatusCode(),
		strconv.Quote(credentials.Redact(captured.ResponseID())),
		strconv.Quote(credentials.Redact(captured.Model())),
		callCount,
		manifestRespected,
		len(responseBody),
		openrouter.MaxResponseBytes,
	)
	if err != nil {
		return errors.New("could not write the redacted provider summary")
	}
	return nil
}

func requestManifestRespected(body []byte) (bool, error) {
	var request struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return false, err
	}
	manifest := broker.DeclaredToolManifest()
	if len(request.Tools) != len(manifest) {
		return false, nil
	}
	for index, name := range manifest {
		if request.Tools[index].Function.Name != name {
			return false, nil
		}
	}
	return true, nil
}

func contains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
