package pigo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRequiresOAuth(t *testing.T) {
	if !RequiresOAuth("openai-codex") {
		t.Fatal("expected openai-codex to require oauth")
	}
	if RequiresOAuth("kimi-coding") {
		t.Fatal("expected kimi-coding to not require oauth")
	}
}

func TestGetEnvAPIKeyReadsBuiltInProviderKeys(t *testing.T) {
	t.Setenv("KIMI_API_KEY", "kimi-env-token")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-env-token")
	dotEnvOnce = syncOnceForTests()
	dotEnvValues = nil

	if got := GetEnvAPIKey("kimi-coding"); got != "kimi-env-token" {
		t.Fatalf("expected kimi env token, got %q", got)
	}
	if got := GetEnvAPIKey("anthropic"); got != "anthropic-env-token" {
		t.Fatalf("expected anthropic env token, got %q", got)
	}
	if got := GetEnvAPIKey("openai-codex"); got != "" {
		t.Fatalf("expected no env api key for openai-codex, got %q", got)
	}
}

func TestGetEnvAPIKeyUsesRegisteredProviderAuthMetadata(t *testing.T) {
	provider := Provider("test-env-provider")
	RegisterProviderModule(ProviderModule{
		Provider: provider,
		Auth: ProviderAuth{
			EnvAPIKeyName: "TEST_PROVIDER_KEY",
		},
		Models: map[string]Model{
			"test-model": {
				ID: "test-model",
			},
		},
	})

	t.Setenv("TEST_PROVIDER_KEY", "provider-env-token")
	dotEnvOnce = syncOnceForTests()
	dotEnvValues = nil

	if got := GetEnvAPIKey(provider); got != "provider-env-token" {
		t.Fatalf("expected provider env token from module metadata, got %q", got)
	}
}

func TestResolveAPIKeyPrefersExplicitAPIKey(t *testing.T) {
	t.Setenv("KIMI_API_KEY", "kimi-env-token")
	dotEnvOnce = syncOnceForTests()
	dotEnvValues = nil

	auth := map[Provider]AuthConfig{
		"kimi-coding": {
			Type:   AuthTypeAPIKey,
			APIKey: "kimi-explicit-token",
		},
	}

	if got := ResolveAPIKey("kimi-coding", auth); got != "kimi-explicit-token" {
		t.Fatalf("expected explicit kimi api key, got %q", got)
	}
}

func TestResolveAPIKeyUsesOAuthAccessToken(t *testing.T) {
	auth := map[Provider]AuthConfig{
		"openai-codex": {
			Type: AuthTypeOAuth,
			OAuth: &OAuthCredentials{
				AccessToken:  "openai-oauth-token",
				RefreshToken: "refresh-token",
			},
		},
	}

	if got := ResolveAPIKey("openai-codex", auth); got != "openai-oauth-token" {
		t.Fatalf("expected oauth access token, got %q", got)
	}
}

func TestResolveAPIKeyFallsBackToEnv(t *testing.T) {
	t.Setenv("KIMI_API_KEY", "kimi-env-token")
	dotEnvOnce = syncOnceForTests()
	dotEnvValues = nil

	if got := ResolveAPIKey("kimi-coding", nil); got != "" {
		t.Fatalf("expected runtime auth resolution to ignore env fallback, got %q", got)
	}
}

func TestResolveAuthorizationRefreshesExpiredOpenAICodexOAuth(t *testing.T) {
	previousURL := openAICodexOAuthTokenURL
	defer func() {
		openAICodexOAuthTokenURL = previousURL
	}()

	refreshedToken := makeOpenAICodexToken("acc_refresh")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Fatalf("expected oauth token path, got %s", r.URL.Path)
		}
		if got := r.Header.Get("content-type"); got != "application/x-www-form-urlencoded" {
			t.Fatalf("expected form content-type, got %q", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("expected refresh form body: %v", err)
		}
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Fatalf("expected refresh grant_type, got %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("refresh_token") != "refresh-old" {
			t.Fatalf("expected refresh token in form, got %q", r.Form.Get("refresh_token"))
		}
		if r.Form.Get("client_id") != openAICodexOAuthClientID {
			t.Fatalf("expected client_id %q, got %q", openAICodexOAuthClientID, r.Form.Get("client_id"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  refreshedToken,
			"refresh_token": "refresh-new",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	openAICodexOAuthTokenURL = server.URL + "/oauth/token"
	expiredAt := time.Now().Unix() - 10
	var (
		callbackProvider    Provider
		callbackCredentials OAuthCredentials
		callbackCalls       int
	)

	auth := map[Provider]AuthConfig{
		"openai-codex": {
			Type: AuthTypeOAuth,
			OAuth: &OAuthCredentials{
				AccessToken:  "expired-token",
				RefreshToken: "refresh-old",
				ExpiresUnix:  expiredAt,
			},
			OnOAuthCredentialsRefreshed: func(provider Provider, credentials OAuthCredentials) {
				callbackCalls++
				callbackProvider = provider
				callbackCredentials = credentials
				credentials.AccessToken = "callback-local-mutation"
			},
		},
	}

	refreshStartedAt := time.Now().Unix()
	token, err := ResolveAuthorization("openai-codex", auth, server.Client(), context.Background())
	if err != nil {
		t.Fatalf("expected refreshed oauth token, got error: %v", err)
	}
	if token != refreshedToken {
		t.Fatalf("expected refreshed access token, got %q", token)
	}
	if callbackCalls != 1 {
		t.Fatalf("expected one refreshed-credentials callback, got %d", callbackCalls)
	}
	if callbackProvider != "openai-codex" {
		t.Fatalf("expected callback provider openai-codex, got %q", callbackProvider)
	}
	if callbackCredentials.AccessToken != refreshedToken {
		t.Fatalf("expected callback access token %q, got %q", refreshedToken, callbackCredentials.AccessToken)
	}
	if callbackCredentials.RefreshToken != "refresh-new" {
		t.Fatalf("expected callback refresh token refresh-new, got %q", callbackCredentials.RefreshToken)
	}
	if callbackCredentials.ExpiresUnix < refreshStartedAt+3600 || callbackCredentials.ExpiresUnix > time.Now().Unix()+3600 {
		t.Fatalf("expected callback expiry from refreshed credentials, got %d", callbackCredentials.ExpiresUnix)
	}
	if auth["openai-codex"].OAuth == nil || auth["openai-codex"].OAuth.RefreshToken != "refresh-old" {
		t.Fatalf("expected auth map to remain unchanged, got %+v", auth["openai-codex"].OAuth)
	}
	if auth["openai-codex"].OAuth.ExpiresUnix != expiredAt {
		t.Fatalf("expected expired credentials to remain unchanged in caller auth map, got %d", auth["openai-codex"].OAuth.ExpiresUnix)
	}
}

func TestResolveAuthorizationCoalescesConcurrentOpenAICodexRefresh(t *testing.T) {
	previousURL := openAICodexOAuthTokenURL
	defer func() {
		openAICodexOAuthTokenURL = previousURL
	}()

	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	var (
		requestMu    sync.Mutex
		requestCount int
		startedOnce  sync.Once
	)
	refreshedToken := makeOpenAICodexToken("acc_shared_refresh")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestMu.Lock()
		requestCount++
		requestMu.Unlock()
		startedOnce.Do(func() { close(requestStarted) })
		<-releaseResponse
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  refreshedToken,
			"refresh_token": "refresh-new",
			"expires_in":    3600,
		})
	}))
	defer server.Close()
	openAICodexOAuthTokenURL = server.URL

	auth := map[Provider]AuthConfig{
		"openai-codex": {
			Type: AuthTypeOAuth,
			OAuth: &OAuthCredentials{
				AccessToken:  "expired-token",
				RefreshToken: "refresh-old",
				ExpiresUnix:  time.Now().Add(-time.Minute).Unix(),
			},
		},
	}
	start := make(chan struct{})
	results := make(chan string, 2)
	errors := make(chan error, 2)
	var callers sync.WaitGroup
	callers.Add(2)
	for range 2 {
		go func() {
			defer callers.Done()
			<-start
			token, err := ResolveAuthorization("openai-codex", auth, server.Client(), context.Background())
			results <- token
			errors <- err
		}()
	}
	close(start)
	<-requestStarted
	time.Sleep(25 * time.Millisecond)
	close(releaseResponse)
	callers.Wait()
	close(results)
	close(errors)

	for err := range errors {
		if err != nil {
			t.Fatalf("expected shared refresh to succeed, got %v", err)
		}
	}
	for token := range results {
		if token != refreshedToken {
			t.Fatalf("expected shared refreshed token, got %q", token)
		}
	}
	requestMu.Lock()
	defer requestMu.Unlock()
	if requestCount != 1 {
		t.Fatalf("expected one refresh request for concurrent callers, got %d", requestCount)
	}
}

func TestResolveAuthorizationKeepsSharedRefreshAliveForRemainingCaller(t *testing.T) {
	previousURL := openAICodexOAuthTokenURL
	defer func() {
		openAICodexOAuthTokenURL = previousURL
	}()

	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	requestCanceled := make(chan struct{}, 1)
	var (
		requestMu    sync.Mutex
		requestCount int
		startedOnce  sync.Once
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requestMu.Lock()
		requestCount++
		requestMu.Unlock()
		startedOnce.Do(func() { close(requestStarted) })
		select {
		case <-releaseResponse:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  makeOpenAICodexToken("acc_remaining_waiter"),
				"refresh_token": "refresh-new",
				"expires_in":    3600,
			})
		case <-request.Context().Done():
			requestCanceled <- struct{}{}
		}
	}))
	defer server.Close()
	openAICodexOAuthTokenURL = server.URL
	httpClient := server.Client()
	auth := map[Provider]AuthConfig{
		"openai-codex": {
			Type: AuthTypeOAuth,
			OAuth: &OAuthCredentials{
				AccessToken:  "expired-token",
				RefreshToken: "refresh-old",
				ExpiresUnix:  time.Now().Add(-time.Minute).Unix(),
			},
		},
	}

	type authorizationResult struct {
		token string
		err   error
	}
	leaderContext, cancelLeader := context.WithCancel(context.Background())
	leaderResult := make(chan authorizationResult, 1)
	go func() {
		token, err := ResolveAuthorization("openai-codex", auth, httpClient, leaderContext)
		leaderResult <- authorizationResult{token: token, err: err}
	}()
	<-requestStarted

	followerResult := make(chan authorizationResult, 1)
	go func() {
		token, err := ResolveAuthorization("openai-codex", auth, httpClient, context.Background())
		followerResult <- authorizationResult{token: token, err: err}
	}()

	key := openAICodexOAuthRefreshKey{
		refreshToken: "refresh-old",
		tokenURL:     openAICodexOAuthTokenURL,
		httpClient:   httpClient,
	}
	waitDeadline := time.Now().Add(time.Second)
	for {
		openAICodexOAuthRefreshMu.Lock()
		flight := openAICodexOAuthRefreshFlights[key]
		waiters := 0
		if flight != nil {
			waiters = flight.waiters
		}
		openAICodexOAuthRefreshMu.Unlock()
		if waiters == 2 {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatal("timed out waiting for both callers to share the refresh")
		}
		time.Sleep(time.Millisecond)
	}

	cancelLeader()
	if result := <-leaderResult; !errors.Is(result.err, context.Canceled) {
		t.Fatalf("expected canceled leader to return context.Canceled, got %+v", result)
	}
	select {
	case <-requestCanceled:
		t.Fatal("leader cancellation stopped refresh needed by follower")
	default:
	}

	close(releaseResponse)
	result := <-followerResult
	if result.err != nil {
		t.Fatalf("expected remaining caller to receive shared refresh, got %v", result.err)
	}
	if result.token != makeOpenAICodexToken("acc_remaining_waiter") {
		t.Fatalf("expected remaining caller to receive refreshed token, got %q", result.token)
	}
	requestMu.Lock()
	defer requestMu.Unlock()
	if requestCount != 1 {
		t.Fatalf("expected callers to share one refresh request, got %d", requestCount)
	}
}

func TestResolveAuthorizationRefreshesOpenAICodexOAuthWithoutAccessToken(t *testing.T) {
	previousURL := openAICodexOAuthTokenURL
	defer func() {
		openAICodexOAuthTokenURL = previousURL
	}()

	refreshedToken := makeOpenAICodexToken("acc_refresh_only")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("expected refresh form body: %v", err)
		}
		if r.Form.Get("refresh_token") != "refresh-only" {
			t.Fatalf("expected refresh-only token in form, got %q", r.Form.Get("refresh_token"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  refreshedToken,
			"refresh_token": "refresh-new",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	openAICodexOAuthTokenURL = server.URL
	auth := map[Provider]AuthConfig{
		"openai-codex": {
			Type: AuthTypeOAuth,
			OAuth: &OAuthCredentials{
				AccessToken:  "",
				RefreshToken: "refresh-only",
				ExpiresUnix:  time.Now().Unix() + 3600,
			},
		},
	}

	token, err := ResolveAuthorization("openai-codex", auth, server.Client(), context.Background())
	if err != nil {
		t.Fatalf("expected refresh to recover missing access token, got error: %v", err)
	}
	if token != refreshedToken {
		t.Fatalf("expected refreshed access token, got %q", token)
	}
}

func TestResolveAuthorizationKeepsValidOpenAICodexOAuthWithoutRefresh(t *testing.T) {
	auth := map[Provider]AuthConfig{
		"openai-codex": {
			Type: AuthTypeOAuth,
			OAuth: &OAuthCredentials{
				AccessToken:  "still-valid",
				RefreshToken: "refresh-token",
				ExpiresUnix:  time.Now().Unix() + 3600,
			},
		},
	}

	token, err := ResolveAuthorization("openai-codex", auth, nil, context.Background())
	if err != nil {
		t.Fatalf("expected valid oauth token without refresh, got error: %v", err)
	}
	if token != "still-valid" {
		t.Fatalf("expected current access token, got %q", token)
	}
}

func TestResolveAuthorizationRefreshDoesNotMutateSharedAuthMap(t *testing.T) {
	previousURL := openAICodexOAuthTokenURL
	defer func() {
		openAICodexOAuthTokenURL = previousURL
	}()

	refreshedToken := makeOpenAICodexToken("acc_shared")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  refreshedToken,
			"refresh_token": "refresh-updated",
			"expires_in":    3600,
		})
	}))
	defer server.Close()
	openAICodexOAuthTokenURL = server.URL

	originalExpiry := time.Now().Unix() - 10
	auth := map[Provider]AuthConfig{
		"openai-codex": {
			Type: AuthTypeOAuth,
			OAuth: &OAuthCredentials{
				AccessToken:  "expired-token",
				RefreshToken: "refresh-old",
				ExpiresUnix:  originalExpiry,
			},
		},
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := ResolveAuthorization("openai-codex", auth, server.Client(), context.Background())
			if err != nil {
				t.Errorf("expected refresh to succeed, got %v", err)
				return
			}
			if token != refreshedToken {
				t.Errorf("expected refreshed token %q, got %q", refreshedToken, token)
			}
		}()
	}
	wg.Wait()

	if auth["openai-codex"].OAuth == nil {
		t.Fatal("expected caller auth map credentials to remain present")
	}
	if auth["openai-codex"].OAuth.RefreshToken != "refresh-old" || auth["openai-codex"].OAuth.ExpiresUnix != originalExpiry {
		t.Fatalf("expected shared auth map to remain unchanged, got %+v", auth["openai-codex"].OAuth)
	}
}

func TestResolveAuthorizationReturnsRefreshHTTPStatusBeforeDecodeError(t *testing.T) {
	previousURL := openAICodexOAuthTokenURL
	defer func() {
		openAICodexOAuthTokenURL = previousURL
	}()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()

	openAICodexOAuthTokenURL = server.URL
	auth := map[Provider]AuthConfig{
		"openai-codex": {
			Type: AuthTypeOAuth,
			OAuth: &OAuthCredentials{
				AccessToken:  "expired-token",
				RefreshToken: "refresh-old",
				ExpiresUnix:  time.Now().Unix() - 10,
			},
		},
	}

	_, err := ResolveAuthorization("openai-codex", auth, server.Client(), context.Background())
	if err == nil {
		t.Fatal("expected oauth refresh failure")
	}
	if !strings.Contains(err.Error(), "401 Unauthorized") {
		t.Fatalf("expected refresh error to preserve http status, got %q", err.Error())
	}
}

func TestGetEnvAPIKeyFallsBackToDotEnvFile(t *testing.T) {
	t.Setenv("KIMI_API_KEY", "")

	tempDir := t.TempDir()
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("expected working directory: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("expected chdir to temp dir: %v", err)
	}
	defer func() {
		_ = os.Chdir(previousDir)
	}()

	if err := os.MkdirAll(".pigo", 0o700); err != nil {
		t.Fatalf("expected local support dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(".pigo", ".env"), []byte("KIMI_API_KEY=kimi-dotenv-token\n"), 0o600); err != nil {
		t.Fatalf("expected dotenv write: %v", err)
	}

	dotEnvOnce = syncOnceForTests()
	dotEnvValues = nil

	if got := GetEnvAPIKey("kimi-coding"); got != "kimi-dotenv-token" {
		t.Fatalf("expected dotenv fallback token, got %q", got)
	}
}

func TestFindEnvKeysReturnsConfiguredKeys(t *testing.T) {
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "oauth-token")
	t.Setenv("ANTHROPIC_API_KEY", "api-key")
	dotEnvOnce = syncOnceForTests()
	dotEnvValues = nil

	keys := FindEnvKeys("anthropic")
	if len(keys) != 2 {
		t.Fatalf("expected 2 env keys, got %d: %v", len(keys), keys)
	}
	if keys[0] != "ANTHROPIC_OAUTH_TOKEN" {
		t.Fatalf("expected first key to be ANTHROPIC_OAUTH_TOKEN, got %q", keys[0])
	}
	if keys[1] != "ANTHROPIC_API_KEY" {
		t.Fatalf("expected second key to be ANTHROPIC_API_KEY, got %q", keys[1])
	}
}

func TestFindEnvKeysReturnsOnlySetKeys(t *testing.T) {
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "api-key")
	dotEnvOnce = syncOnceForTests()
	dotEnvValues = nil

	keys := FindEnvKeys("anthropic")
	if len(keys) != 1 {
		t.Fatalf("expected 1 env key, got %d: %v", len(keys), keys)
	}
	if keys[0] != "ANTHROPIC_API_KEY" {
		t.Fatalf("expected ANTHROPIC_API_KEY, got %q", keys[0])
	}
}

func TestFindEnvKeysReturnsNilForMissingProvider(t *testing.T) {
	keys := FindEnvKeys("nonexistent-provider")
	if keys != nil {
		t.Fatalf("expected nil for unregistered provider, got %v", keys)
	}
}

func TestFindEnvKeysFallsBackToDotEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	tempDir := t.TempDir()
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("expected working directory: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("expected chdir to temp dir: %v", err)
	}
	defer func() {
		_ = os.Chdir(previousDir)
	}()

	if err := os.MkdirAll(".pigo", 0o700); err != nil {
		t.Fatalf("expected local support dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(".pigo", ".env"), []byte("ANTHROPIC_API_KEY=dotenv-api-key\n"), 0o600); err != nil {
		t.Fatalf("expected dotenv write: %v", err)
	}

	dotEnvOnce = syncOnceForTests()
	dotEnvValues = nil

	keys := FindEnvKeys("anthropic")
	if len(keys) != 1 {
		t.Fatalf("expected 1 env key from dotenv, got %d: %v", len(keys), keys)
	}
	if keys[0] != "ANTHROPIC_API_KEY" {
		t.Fatalf("expected ANTHROPIC_API_KEY from dotenv, got %q", keys[0])
	}
}

func TestGetEnvAPIKeyPrefersFirstEnvKey(t *testing.T) {
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "oauth-token")
	t.Setenv("ANTHROPIC_API_KEY", "api-key")
	dotEnvOnce = syncOnceForTests()
	dotEnvValues = nil

	if got := GetEnvAPIKey("anthropic"); got != "oauth-token" {
		t.Fatalf("expected oauth token (first env key), got %q", got)
	}
}

func TestGetEnvAPIKeyFallsBackToSecondEnvKey(t *testing.T) {
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "api-key")
	dotEnvOnce = syncOnceForTests()
	dotEnvValues = nil

	if got := GetEnvAPIKey("anthropic"); got != "api-key" {
		t.Fatalf("expected api key (second env key), got %q", got)
	}
}

func syncOnceForTests() sync.Once {
	return sync.Once{}
}
