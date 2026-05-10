package pigo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type openAICodexRefreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

var openAICodexOAuthTokenURL = "https://auth.openai.com/oauth/token"

const (
	openAICodexOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	oauthRefreshSkew         = 60
)

func resolveOpenAICodexAuthorization(
	_ Provider,
	config AuthConfig,
	httpClient *http.Client,
	requestContext context.Context,
) (string, error) {
	if config.OAuth == nil {
		return "", nil
	}
	if !oauthCredentialsNeedRefresh(config.OAuth) {
		return config.OAuth.AccessToken, nil
	}

	refreshed, err := refreshOpenAICodexOAuth(config.OAuth.RefreshToken, httpClient, requestContext)
	if err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

func oauthCredentialsNeedRefresh(credentials *OAuthCredentials) bool {
	if credentials == nil {
		return false
	}
	refreshToken := strings.TrimSpace(credentials.RefreshToken)
	if strings.TrimSpace(credentials.AccessToken) == "" {
		return refreshToken != ""
	}
	if credentials.ExpiresUnix <= 0 || refreshToken == "" {
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

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("openai codex oauth refresh failed: %s", response.Status)
	}

	var payload openAICodexRefreshResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
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
