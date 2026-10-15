package openrouter

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	APIKeyEnv          = "TBOUND_OPENCODE_GO_API_KEY"
	APIKeyFileEnv      = "TBOUND_OPENCODE_GO_API_KEY_FILE"
	maxCredentialBytes = 4096
)

// Credentials holds the API key for the fixed OpenCode Go profile. The key is
// deliberately private and every printable representation redacts it.
type Credentials struct {
	apiKey string
}

func LoadCredentials() (Credentials, error) {
	return LoadCredentialsFrom(os.LookupEnv)
}

// LoadCredentialsFrom is an injectable environment seam for tests and callers
// that supply an explicit environment lookup function.
func LoadCredentialsFrom(lookupEnv func(string) (string, bool)) (Credentials, error) {
	if lookupEnv == nil {
		return Credentials{}, errors.New("credential environment lookup is required")
	}

	apiKey, keySet := lookupEnv(APIKeyEnv)
	if keySet && strings.TrimSpace(apiKey) != "" {
		key, err := normalizeAPIKey(apiKey)
		if err != nil {
			return Credentials{}, errors.New("invalid OpenRouter credential")
		}
		return credentialsFor(key)
	}

	path, pathSet := lookupEnv(APIKeyFileEnv)
	path = strings.TrimSpace(path)
	if !pathSet || path == "" {
		return Credentials{}, errors.New("OpenCode Go credentials are required via TBOUND_OPENCODE_GO_API_KEY or TBOUND_OPENCODE_GO_API_KEY_FILE")
	}
	content, err := readSecureCredentialFile(path)
	if err != nil {
		return Credentials{}, errors.New("OpenRouter credential file is missing or insecure")
	}
	key, err := normalizeAPIKey(string(content))
	if err != nil {
		return Credentials{}, errors.New("invalid OpenRouter credential")
	}
	return credentialsFor(key)
}

func (c Credentials) ModelID() string { return ApprovedModel }

// Redact replaces this credential's key in a string before it is emitted in
// diagnostics or a user-visible summary.
func (c Credentials) Redact(value string) string {
	if c.apiKey == "" {
		return value
	}
	return strings.ReplaceAll(value, c.apiKey, "[REDACTED]")
}

func (c Credentials) String() string {
	return "Credentials{ModelID:" + ApprovedModel + ", APIKey:[REDACTED]}"
}

func (c Credentials) GoString() string { return c.String() }

// MarshalJSON intentionally never serializes the credential value.
func (c Credentials) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ModelID string `json:"model_id"`
		APIKey  string `json:"api_key"`
	}{ModelID: ApprovedModel, APIKey: "[REDACTED]"})
}

// NewHTTPDoer injects this credential into a new, redirect-forbidding HTTP
// transport. The key is passed to the transport through a closure, not a
// package-global variable or request record.
func (c Credentials) NewHTTPDoer(timeout time.Duration) (*HTTPDoer, error) {
	return NewHTTPDoer(func() (string, error) { return c.apiKey, nil }, timeout)
}

func normalizeAPIKey(value string) (string, error) {
	key := strings.TrimSpace(value)
	if key == "" || len(key) > maxCredentialBytes || !utf8.ValidString(key) {
		return "", errors.New("invalid OpenRouter credential")
	}
	for _, character := range key {
		if character < 0x21 || character > 0x7e {
			return "", errors.New("invalid OpenRouter credential")
		}
	}
	return key, nil
}

func credentialsFor(key string) (Credentials, error) {
	return Credentials{apiKey: key}, nil
}
