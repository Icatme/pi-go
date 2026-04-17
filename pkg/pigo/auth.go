package pigo

import (
	"context"
	"net/http"
	"strings"
)

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
	module := resolveProviderModule(provider)
	if module == nil {
		return false
	}
	return module.Auth.RequiresOAuth
}

func GetEnvAPIKey(provider Provider) string {
	module := resolveProviderModule(provider)
	if module == nil || strings.TrimSpace(module.Auth.EnvAPIKeyName) == "" {
		return ""
	}
	return lookupEnvValue(module.Auth.EnvAPIKeyName)
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
	return ""
}

func ResolveAuthorization(provider Provider, auth map[Provider]AuthConfig, httpClient *http.Client, requestContext context.Context) (string, error) {
	if auth == nil {
		return "", nil
	}

	config, ok := auth[provider]
	if !ok {
		return "", nil
	}

	switch config.Type {
	case AuthTypeAPIKey:
		return config.APIKey, nil
	case AuthTypeOAuth:
		if config.OAuth == nil {
			return "", nil
		}
		module := resolveProviderModule(provider)
		if module != nil && module.Auth.ResolveAuthorization != nil {
			token, err := module.Auth.ResolveAuthorization(provider, config, httpClient, requestContext)
			if err != nil {
				return "", err
			}
			return token, nil
		}
		return config.OAuth.AccessToken, nil
	default:
		return "", nil
	}
}
