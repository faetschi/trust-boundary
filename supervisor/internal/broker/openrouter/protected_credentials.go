package openrouter

import (
	"net/http"
	"time"
)

// CredentialsFromKey applies the same normalization as the environment loader
// without consulting the process environment. Printable forms remain redacted.
func CredentialsFromKey(key []byte) (Credentials, error) {
	normalized, err := normalizeAPIKey(string(key))
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{apiKey: normalized}, nil
}

// ReadSecureCredentialFile exposes the existing no-follow, owner/mode and size
// checks to administrative importers. It never includes file contents in errors.
func ReadSecureCredentialFile(path string) ([]byte, error) {
	return readSecureCredentialFile(path)
}

// NewCheckHTTPDoer uses the production transport, retaining a refused redirect
// response so a one-shot diagnostic can distinguish it from a network failure.
// ErrUseLastResponse never follows the redirect or sends a second request.
func (c Credentials) NewCheckHTTPDoer(timeout time.Duration) (*HTTPDoer, error) {
	d, err := c.NewHTTPDoer(timeout)
	if err != nil {
		return nil, err
	}
	d.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return d, nil
}
