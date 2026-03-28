package pigo

type HandoffCredentials struct {
	Version   int                              `json:"version"`
	Providers map[Provider]HandoffProviderAuth `json:"providers"`
}

type HandoffProviderAuth struct {
	Type   AuthType                 `json:"type"`
	APIKey string                   `json:"apiKey,omitempty"`
	OAuth  *HandoffOAuthCredentials `json:"oauth,omitempty"`
}

type HandoffOAuthCredentials struct {
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresUnix  int64  `json:"expiresUnix,omitempty"`
}

func HandoffCredentialsFromAuth(auth map[Provider]AuthConfig) HandoffCredentials {
	credentials := HandoffCredentials{
		Version:   1,
		Providers: map[Provider]HandoffProviderAuth{},
	}
	if len(auth) == 0 {
		return credentials
	}

	for provider, config := range auth {
		entry := HandoffProviderAuth{
			Type:   config.Type,
			APIKey: config.APIKey,
		}
		if config.OAuth != nil {
			entry.OAuth = &HandoffOAuthCredentials{
				AccessToken:  config.OAuth.AccessToken,
				RefreshToken: config.OAuth.RefreshToken,
				ExpiresUnix:  config.OAuth.ExpiresUnix,
			}
		}
		credentials.Providers[provider] = entry
	}

	return credentials
}

func (credentials HandoffCredentials) ToAuthConfigMap() map[Provider]AuthConfig {
	if len(credentials.Providers) == 0 {
		return map[Provider]AuthConfig{}
	}

	auth := make(map[Provider]AuthConfig, len(credentials.Providers))
	for provider, entry := range credentials.Providers {
		config := AuthConfig{
			Type:   entry.Type,
			APIKey: entry.APIKey,
		}
		if entry.OAuth != nil {
			config.OAuth = &OAuthCredentials{
				AccessToken:  entry.OAuth.AccessToken,
				RefreshToken: entry.OAuth.RefreshToken,
				ExpiresUnix:  entry.OAuth.ExpiresUnix,
			}
		}
		auth[provider] = config
	}

	return auth
}
