package pigo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

	auth := map[Provider]AuthConfig{
		"openai-codex": {
			Type: AuthTypeOAuth,
			OAuth: &OAuthCredentials{
				AccessToken:  "expired-token",
				RefreshToken: "refresh-old",
				ExpiresUnix:  expiredAt,
			},
		},
	}

	token, err := ResolveAuthorization("openai-codex", auth, server.Client(), context.Background())
	if err != nil {
		t.Fatalf("expected refreshed oauth token, got error: %v", err)
	}
	if token != refreshedToken {
		t.Fatalf("expected refreshed access token, got %q", token)
	}
	if auth["openai-codex"].OAuth == nil || auth["openai-codex"].OAuth.RefreshToken != "refresh-old" {
		t.Fatalf("expected auth map to remain unchanged, got %+v", auth["openai-codex"].OAuth)
	}
	if auth["openai-codex"].OAuth.ExpiresUnix != expiredAt {
		t.Fatalf("expected expired credentials to remain unchanged in caller auth map, got %d", auth["openai-codex"].OAuth.ExpiresUnix)
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

func syncOnceForTests() sync.Once {
	return sync.Once{}
}
