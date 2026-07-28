package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestParseAuthorizationInput(t *testing.T) {
	code, state, err := parseAuthorizationInput("http://localhost:1455/auth/callback?code=abc&state=xyz")
	if err != nil {
		t.Fatalf("parseAuthorizationInput returned error: %v", err)
	}
	if code != "abc" || state != "xyz" {
		t.Fatalf("expected code/state, got %q %q", code, state)
	}
}

func TestOpenAICodexLoginManualFallback(t *testing.T) {
	previousTokenURL := openAICodexTokenURL
	previousAuthorizeURL := openAICodexAuthorizeURL
	previousRedirectURL := openAICodexRedirectURL
	previousCallbackAddress := openAICodexCallbackAddress
	defer func() {
		openAICodexTokenURL = previousTokenURL
		openAICodexAuthorizeURL = previousAuthorizeURL
		openAICodexRedirectURL = previousRedirectURL
		openAICodexCallbackAddress = previousCallbackAddress
	}()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Fatalf("expected /oauth/token, got %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm failed: %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "authorization_code" {
			t.Fatalf("expected authorization_code grant, got %q", got)
		}
		if got := r.Form.Get("code"); got != "code-123" {
			t.Fatalf("expected code-123, got %q", got)
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(openAICodexTokenResponse{
			AccessToken:  buildJWTWithAccountID(t, "acc_test"),
			RefreshToken: "refresh-token",
			ExpiresIn:    3600,
		})
	}))
	defer server.Close()

	openAICodexTokenURL = server.URL + "/oauth/token"
	openAICodexAuthorizeURL = "https://auth.example.com/oauth/authorize"
	openAICodexRedirectURL = "http://localhost:1455/auth/callback"
	openAICodexCallbackAddress = "bad-address"

	provider := newOpenAICodexOAuthProvider()
	var authURL string
	var openedURL string
	provider.openBrowser = func(target string) error {
		openedURL = target
		return nil
	}
	var output bytes.Buffer
	credentials, err := provider.Login(context.Background(), oauthLoginCallbacks{
		OnAuth: func(info oauthAuthInfo) {
			authURL = info.URL
		},
		OnPrompt: func(prompt oauthPrompt) (string, error) {
			if !strings.Contains(prompt.Message, "authorization code") {
				t.Fatalf("unexpected prompt message: %q", prompt.Message)
			}
			return "code-123", nil
		},
		OnOutput: func(line string) {
			output.WriteString(line)
		},
	})
	if err != nil {
		t.Fatalf("Login returned error: %v", err)
	}
	if credentials.Type != "oauth" || credentials.Refresh != "refresh-token" || credentials.AccountID != "acc_test" {
		t.Fatalf("unexpected credentials: %+v", credentials)
	}

	parsedURL, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("auth URL parse failed: %v", err)
	}
	if got := parsedURL.Query().Get("client_id"); got != openAICodexOAuthClientID {
		t.Fatalf("expected client_id %q, got %q", openAICodexOAuthClientID, got)
	}
	if openedURL != authURL {
		t.Fatalf("expected browser opener to receive auth URL %q, got %q", authURL, openedURL)
	}
	if !strings.Contains(output.String(), "Falling back to manual code paste.") {
		t.Fatalf("expected manual fallback output, got %q", output.String())
	}
}

func buildJWTWithAccountID(t *testing.T, accountID string) string {
	t.Helper()

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payloadBytes, err := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
		},
	})
	if err != nil {
		t.Fatalf("Marshal payload failed: %v", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return header + "." + payload + ".signature"
}
