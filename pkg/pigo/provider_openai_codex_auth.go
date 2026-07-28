package pigo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type openAICodexRefreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

var openAICodexOAuthTokenURL = "https://auth.openai.com/oauth/token"

var (
	openAICodexOAuthRefreshMu      sync.Mutex
	openAICodexOAuthRefreshFlights = map[openAICodexOAuthRefreshKey]*openAICodexOAuthRefreshFlight{}
)

type openAICodexOAuthRefreshKey struct {
	refreshToken string
	tokenURL     string
	httpClient   *http.Client
}

type openAICodexOAuthRefreshFlight struct {
	done        chan struct{}
	credentials *OAuthCredentials
	err         error
	cancel      context.CancelFunc
	waiters     int
	completed   bool
}

const (
	openAICodexOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	oauthRefreshSkew         = 60
)

func resolveOpenAICodexAuthorization(
	provider Provider,
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

	refreshed, err := refreshOpenAICodexOAuthShared(config.OAuth.RefreshToken, httpClient, requestContext)
	if err != nil {
		return "", err
	}
	if config.OnOAuthCredentialsRefreshed != nil {
		config.OnOAuthCredentialsRefreshed(provider, *refreshed)
	}
	return refreshed.AccessToken, nil
}

func refreshOpenAICodexOAuthShared(refreshToken string, httpClient *http.Client, requestContext context.Context) (*OAuthCredentials, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if requestContext == nil {
		requestContext = context.Background()
	}
	if err := requestContext.Err(); err != nil {
		return nil, err
	}

	key := openAICodexOAuthRefreshKey{
		refreshToken: refreshToken,
		tokenURL:     openAICodexOAuthTokenURL,
		httpClient:   httpClient,
	}
	openAICodexOAuthRefreshMu.Lock()
	if flight := openAICodexOAuthRefreshFlights[key]; flight != nil {
		flight.waiters++
		openAICodexOAuthRefreshMu.Unlock()
		return waitForOpenAICodexOAuthRefresh(key, flight, requestContext)
	}
	refreshContext, cancel := context.WithCancel(context.Background())
	flight := &openAICodexOAuthRefreshFlight{
		done:    make(chan struct{}),
		cancel:  cancel,
		waiters: 1,
	}
	openAICodexOAuthRefreshFlights[key] = flight
	openAICodexOAuthRefreshMu.Unlock()

	go runOpenAICodexOAuthRefresh(key, flight, refreshToken, httpClient, refreshContext)
	return waitForOpenAICodexOAuthRefresh(key, flight, requestContext)
}

func runOpenAICodexOAuthRefresh(
	key openAICodexOAuthRefreshKey,
	flight *openAICodexOAuthRefreshFlight,
	refreshToken string,
	httpClient *http.Client,
	refreshContext context.Context,
) {
	defer flight.cancel()
	credentials, err := refreshOpenAICodexOAuth(refreshToken, httpClient, refreshContext)

	openAICodexOAuthRefreshMu.Lock()
	if openAICodexOAuthRefreshFlights[key] == flight {
		delete(openAICodexOAuthRefreshFlights, key)
	}
	flight.credentials = cloneOAuthCredentials(credentials)
	flight.err = err
	flight.completed = true
	close(flight.done)
	openAICodexOAuthRefreshMu.Unlock()
}

func waitForOpenAICodexOAuthRefresh(
	key openAICodexOAuthRefreshKey,
	flight *openAICodexOAuthRefreshFlight,
	requestContext context.Context,
) (*OAuthCredentials, error) {
	select {
	case <-flight.done:
		return cloneOAuthCredentials(flight.credentials), flight.err
	case <-requestContext.Done():
		var cancel context.CancelFunc
		openAICodexOAuthRefreshMu.Lock()
		if !flight.completed {
			flight.waiters--
			if flight.waiters == 0 {
				if openAICodexOAuthRefreshFlights[key] == flight {
					delete(openAICodexOAuthRefreshFlights, key)
				}
				cancel = flight.cancel
			}
		}
		openAICodexOAuthRefreshMu.Unlock()
		if cancel != nil {
			cancel()
		}
		return nil, requestContext.Err()
	}
}

func cloneOAuthCredentials(credentials *OAuthCredentials) *OAuthCredentials {
	if credentials == nil {
		return nil
	}
	cloned := *credentials
	return &cloned
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
