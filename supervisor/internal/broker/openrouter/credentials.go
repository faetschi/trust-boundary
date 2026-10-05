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
	APIKeyEnv          = "TBOUND_OPENROUTER_API_KEY"
	APIKeyFileEnv      = "TBOUND_OPENROUTER_API_KEY_FILE"
	ModelEnv           = "TBOUND_OPENROUTER_MODEL"
	maxCredentialBytes = 4096
)

// Credentials holds the model ID and API key loaded for one provider run. The
// key is deliberately private and every printable representation redacts it.
type Credentials struct {
	apiKey  string
	modelID string
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

	modelID, modelSet := lookupEnv(ModelEnv)
	modelID = strings.TrimSpace(modelID)
	if !modelSet || modelID == "" {
		return Credentials{}, errors.New("TBOUND_OPENROUTER_MODEL is required")
	}

	apiKey, keySet := lookupEnv(APIKeyEnv)
	if keySet && strings.TrimSpace(apiKey) != "" {
		key, err := normalizeAPIKey(apiKey)
		if err != nil {
			return Credentials{}, errors.New("invalid OpenRouter credential")
		}
		return credentialsFor(key, modelID)
	}

	path, pathSet := lookupEnv(APIKeyFileEnv)
	path = strings.TrimSpace(path)
	if !pathSet || path == "" {
		return Credentials{}, errors.New("OpenRouter credentials are required via TBOUND_OPENROUTER_API_KEY or TBOUND_OPENROUTER_API_KEY_FILE")
	}
	content, err := readSecureCredentialFile(path)
	if err != nil {
		return Credentials{}, errors.New("OpenRouter credential file is missing or insecure")
	}
	key, err := normalizeAPIKey(string(content))
	if err != nil {
		return Credentials{}, errors.New("invalid OpenRouter credential")
	}
	return credentialsFor(key, modelID)
}

func (c Credentials) ModelID() string { return c.modelID }

// Redact replaces this credential's key in a string before it is emitted in
// diagnostics or a user-visible summary.
func (c Credentials) Redact(value string) string {
	if c.apiKey == "" {
		return value
	}
	return strings.ReplaceAll(value, c.apiKey, "[REDACTED]")
}

func (c Credentials) String() string {
	return "Credentials{ModelID:" + c.Redact(c.modelID) + ", APIKey:[REDACTED]}"
}

func (c Credentials) GoString() string { return c.String() }

// MarshalJSON intentionally never serializes the credential value.
func (c Credentials) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ModelID string `json:"model_id"`
		APIKey  string `json:"api_key"`
	}{ModelID: c.Redact(c.modelID), APIKey: "[REDACTED]"})
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

func credentialsFor(key, modelID string) (Credentials, error) {
	if strings.Contains(modelID, key) {
		return Credentials{}, errors.New("OpenRouter model ID contains credential data")
	}
	return Credentials{apiKey: key, modelID: modelID}, nil
}
