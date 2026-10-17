//go:build linux

package main

import (
	"errors"
	"fmt"

	"tbound/supervisor/internal/audit"
	"tbound/supervisor/internal/broker/openrouter"
	"tbound/supervisor/internal/piruntime"
	"tbound/supervisor/internal/providerbridge"
)

// ErrServiceProviderCredentials is returned, with no fallback, whenever the
// fixed service key file is absent or fails the existing secure-file rule.
var ErrServiceProviderCredentials = errors.New("service provider credential file is unavailable")

// loadServiceProviderCredentials is the only credential source of the
// production Pi service. The closed systemd unit cannot carry the key (no
// EnvironmentFile, LoadCredential or extra Environment keys), so the key lives
// in one code-defined file, piruntime.D09ManagedPiProviderCredentialPath:
// a regular file (never a symlink), owned by the service uid, mode 0400 (the
// existing loader also admits 0600), at most 4096 bytes, one printable token.
// /etc is read-only but readable under ProtectSystem=strict, so no unit change
// is needed. The lookup exposes only the _FILE key, so neither
// TBOUND_OPENCODE_GO_API_KEY nor a caller-chosen path is ever consulted.
func loadServiceProviderCredentials(path string) (openrouter.Credentials, error) {
	credentials, err := openrouter.LoadCredentialsFrom(func(name string) (string, bool) {
		if name == openrouter.APIKeyFileEnv {
			return path, true
		}
		return "", false
	})
	if err != nil {
		return openrouter.Credentials{}, errors.Join(ErrServiceProviderCredentials,
			fmt.Errorf("%s must be a regular file owned by the service uid with mode 0400, at most 4096 bytes, holding one printable API key: %w", path, err))
	}
	return credentials, nil
}

// serviceProviderConversationFactory replaces providerbridge.NewFromEnvironment
// on the production path. Any credential refusal ends the session start.
func serviceProviderConversationFactory(path string) func(providerbridge.Config, *audit.Journal) (*providerbridge.Conversation, error) {
	return func(config providerbridge.Config, journal *audit.Journal) (*providerbridge.Conversation, error) {
		credentials, err := loadServiceProviderCredentials(path)
		if err != nil {
			return nil, err
		}
		return providerbridge.NewFromCredentials(config, credentials, journal)
	}
}

var productionProviderConversationFactory = serviceProviderConversationFactory(piruntime.D09ManagedPiProviderCredentialPath)
