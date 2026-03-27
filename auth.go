package pigo

import "os"

type AuthType string

const (
	AuthTypeAPIKey AuthType = "apiKey"
	AuthTypeOAuth  AuthType = "oauth"
)

type OAuthCredentials struct {
	AccessToken  string
	RefreshToken string
	ExpiresUnix  int64
}

type AuthConfig struct {
	Type   AuthType
	APIKey string
	OAuth  *OAuthCredentials
}

func RequiresOAuth(provider Provider) bool {
	return provider == "openai-codex"
}

func GetEnvAPIKey(provider Provider) string {
	switch provider {
	case "kimi-coding":
		return os.Getenv("KIMI_API_KEY")
	default:
		return ""
	}
}

func ResolveAPIKey(provider Provider, auth map[Provider]AuthConfig) string {
	if auth != nil {
		if config, ok := auth[provider]; ok {
			switch config.Type {
			case AuthTypeAPIKey:
				return config.APIKey
			case AuthTypeOAuth:
				if config.OAuth != nil {
					return config.OAuth.AccessToken
				}
			}
		}
	}
	return GetEnvAPIKey(provider)
}
