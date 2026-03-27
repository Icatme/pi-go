package pigo

import (
	"bufio"
	"os"
	"strings"
	"sync"
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

var (
	dotEnvValues map[string]string
	dotEnvOnce   sync.Once
)

func RequiresOAuth(provider Provider) bool {
	return provider == "openai-codex"
}

func GetEnvAPIKey(provider Provider) string {
	switch provider {
	case "kimi-coding":
		return lookupEnvValue("KIMI_API_KEY")
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
	return ""
}

func lookupEnvValue(name string) string {
	if value, ok := os.LookupEnv(name); ok && value != "" {
		return value
	}

	dotEnvOnce.Do(func() {
		dotEnvValues = loadDotEnvFile(".env")
	})

	return dotEnvValues[name]
}

func loadDotEnvFile(path string) map[string]string {
	file, err := os.Open(path)
	if err != nil {
		return map[string]string{}
	}
	defer file.Close()

	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return values
}
