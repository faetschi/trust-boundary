package openrouter_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"tbound/supervisor/internal/broker"
	"tbound/supervisor/internal/broker/openrouter"
)

func TestBrokerDoesNotRecordCredentialOrCredentialSourceError(t *testing.T) {
	const key = "private-test-key-123"
	doer, err := openrouter.NewHTTPDoer(func() (string, error) {
		return key, errors.New("source failed: " + key)
	}, openrouter.DefaultHTTPTimeout)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "a fixed test request"
	providerBroker, err := broker.New(broker.Profile{
		ID: "credential-redaction-test", Model: "vendor/model",
		Messages:         []openrouter.Message{{Role: openrouter.User, Content: &prompt}},
		ToolManifest:     broker.DeclaredToolManifest(),
		ToolCallIssuer:   "test/tool-call/v1",
		ResponseIDIssuer: "test/response/v1",
		Generation:       "g0",
	}, doer)
	if err != nil {
		t.Fatal(err)
	}
	_, exchangeErr := providerBroker.Exchange(context.Background())
	if exchangeErr == nil || strings.Contains(exchangeErr.Error(), key) || strings.Contains(exchangeErr.Error(), "Bearer ") {
		t.Fatal("credential source error was absent or leaked authentication data")
	}
	records := providerBroker.Records()
	if len(records) != 1 {
		t.Fatalf("record count = %d, want exactly one failed exchange", len(records))
	}
	record := records[0]
	request := record.Request()
	if request.Header().Get("Authorization") != "" || strings.Contains(string(request.Body()), key) ||
		strings.Contains(record.Failure(), key) || strings.Contains(record.Failure(), "Bearer ") {
		t.Fatal("credential was retained in broker request metadata or failure")
	}
}

func TestRealOpenRouterExchange(t *testing.T) {
	key, keySet := os.LookupEnv(openrouter.APIKeyEnv)
	keyFile, keyFileSet := os.LookupEnv(openrouter.APIKeyFileEnv)
	if (!keySet || strings.TrimSpace(key) == "") && (!keyFileSet || strings.TrimSpace(keyFile) == "") {
		t.Skip("set TBOUND_OPENROUTER_API_KEY or TBOUND_OPENROUTER_API_KEY_FILE to enable the real-network exchange")
	}
	credentials, err := openrouter.LoadCredentials()
	if err != nil {
		t.Fatalf("load provider configuration: %v", err)
	}
	doer, err := credentials.NewHTTPDoer(openrouter.DefaultHTTPTimeout)
	if err != nil {
		t.Fatalf("configure provider transport: %v", err)
	}
	prompt := "Reply with a short acknowledgment. Do not call a tool unless necessary."
	providerBroker, err := broker.New(broker.Profile{
		ID: "real-openrouter-exchange-test", Model: credentials.ModelID(),
		Messages:         []openrouter.Message{{Role: openrouter.User, Content: &prompt}},
		ToolManifest:     broker.DeclaredToolManifest(),
		ToolCallIssuer:   "test/real-openrouter-tool-call/v1",
		ResponseIDIssuer: "test/real-openrouter-response/v1",
		Generation:       "real-provider-test",
	}, doer)
	if err != nil {
		t.Fatalf("register provider profile: %v", credentials.Redact(err.Error()))
	}
	captured, err := providerBroker.Exchange(context.Background())
	if err != nil {
		t.Fatalf("one real provider exchange failed: %v", credentials.Redact(err.Error()))
	}
	if captured.ResponseID() == "" || captured.Model() != credentials.ModelID() || len(providerBroker.Records()) != 1 {
		t.Fatal("real exchange did not produce one model-matched provider capture")
	}
}

var _ broker.HTTPDoer = (*openrouter.HTTPDoer)(nil)
