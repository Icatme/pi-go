package cli

import (
	"strings"

	"github.com/Icatme/pi-go/pkg/pigo"
)

func loadRuntimeAuth(path string) map[pigo.Provider]pigo.AuthConfig {
	return buildRuntimeAuth(loadAuth(path))
}

func buildRuntimeAuth(stored map[string]storedOAuthCredentials) map[pigo.Provider]pigo.AuthConfig {
	if len(stored) == 0 {
		return map[pigo.Provider]pigo.AuthConfig{}
	}

	auth := make(map[pigo.Provider]pigo.AuthConfig, len(stored))
	for providerID, credentials := range stored {
		if strings.TrimSpace(providerID) == "" {
			continue
		}
		if credentials.Type != "oauth" {
			continue
		}
		if strings.TrimSpace(credentials.Access) == "" {
			continue
		}

		auth[pigo.Provider(providerID)] = pigo.AuthConfig{
			Type: pigo.AuthTypeOAuth,
			OAuth: &pigo.OAuthCredentials{
				AccessToken:  credentials.Access,
				RefreshToken: credentials.Refresh,
				ExpiresUnix:  credentials.Expires / 1000,
			},
		}
	}
	return auth
}
