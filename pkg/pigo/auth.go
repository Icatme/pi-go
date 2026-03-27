package pigo

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
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

type openAICodexRefreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

var (
	dotEnvValues map[string]string
	dotEnvOnce   sync.Once
)

var openAICodexOAuthTokenURL = "https://auth.openai.com/oauth/token"

const (
	openAICodexOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	oauthRefreshSkew         = 60
)

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
			updated, token, err := module.Auth.ResolveAuthorization(provider, config, httpClient, requestContext)
			if err != nil {
				return "", err
			}
			auth[provider] = updated
			return token, nil
		}
		return config.OAuth.AccessToken, nil
	default:
		return "", nil
	}
}

func resolveOpenAICodexAuthorization(
	_ Provider,
	config AuthConfig,
	httpClient *http.Client,
	requestContext context.Context,
) (AuthConfig, string, error) {
	if config.OAuth == nil {
		return config, "", nil
	}
	if !oauthCredentialsNeedRefresh(config.OAuth) {
		return config, config.OAuth.AccessToken, nil
	}

	refreshed, err := refreshOpenAICodexOAuth(config.OAuth.RefreshToken, httpClient, requestContext)
	if err != nil {
		return config, "", err
	}

	config.OAuth = refreshed
	return config, refreshed.AccessToken, nil
}

func oauthCredentialsNeedRefresh(credentials *OAuthCredentials) bool {
	if credentials == nil {
		return false
	}
	if strings.TrimSpace(credentials.AccessToken) == "" {
		return false
	}
	if credentials.ExpiresUnix <= 0 || strings.TrimSpace(credentials.RefreshToken) == "" {
		return false
	}
	return time.Now().Unix() >= credentials.ExpiresUnix-oauthRefreshSkew
}

func refreshOpenAICodexOAuth(refreshToken string, httpClient *http.Client, requestContext context.Context) (*OAuthCredentials, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("missing refresh token")
	}

	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if requestContext == nil {
		requestContext = context.Background()
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", openAICodexOAuthClientID)

	request, err := http.NewRequestWithContext(
		requestContext,
		http.MethodPost,
		openAICodexOAuthTokenURL,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return nil, err
	}
	request.Header.Set("content-type", "application/x-www-form-urlencoded")

	response, err := httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	var payload openAICodexRefreshResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("openai codex oauth refresh failed: %s", response.Status)
	}
	if strings.TrimSpace(payload.AccessToken) == "" || strings.TrimSpace(payload.RefreshToken) == "" || payload.ExpiresIn <= 0 {
		return nil, fmt.Errorf("openai codex oauth refresh returned incomplete credentials")
	}

	return &OAuthCredentials{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		ExpiresUnix:  time.Now().Unix() + payload.ExpiresIn,
	}, nil
}

func lookupEnvValue(name string) string {
	if value, ok := os.LookupEnv(name); ok && value != "" {
		return value
	}

	dotEnvOnce.Do(func() {
		dotEnvValues = loadDotEnvFile(resolveSupportFilePath(".env"))
	})

	return dotEnvValues[name]
}

func loadDotEnvFile(path string) map[string]string {
	if strings.TrimSpace(path) == "" {
		return map[string]string{}
	}

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

func resolveSupportFilePath(name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}

	candidate := name
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}

	workingDir, err := os.Getwd()
	if err != nil {
		return name
	}

	dir := workingDir
	for {
		candidate = filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}

		parent := filepath.Dir(dir)
		if parent == dir || parent == "" {
			break
		}
		dir = parent
	}

	return name
}
