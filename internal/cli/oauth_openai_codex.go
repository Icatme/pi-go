package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const openAICodexOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

var (
	openAICodexAuthorizeURL    = "https://auth.openai.com/oauth/authorize"
	openAICodexTokenURL        = "https://auth.openai.com/oauth/token"
	openAICodexRedirectURL     = "http://localhost:1455/auth/callback"
	openAICodexCallbackAddress = "127.0.0.1:1455"
	openAICodexCallbackTimeout = 2 * time.Minute
)

type openAICodexOAuthProvider struct {
	httpClient *http.Client
}

type openAICodexAuthorizationFlow struct {
	Verifier string
	State    string
	URL      string
}

type openAICodexTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

type openAICodexCallbackServer struct {
	server *http.Server
	codes  chan string
}

func newOpenAICodexOAuthProvider() oauthProvider {
	return &openAICodexOAuthProvider{
		httpClient: http.DefaultClient,
	}
}

func (p *openAICodexOAuthProvider) ID() string {
	return "openai-codex"
}

func (p *openAICodexOAuthProvider) Name() string {
	return "ChatGPT Plus/Pro (Codex Subscription)"
}

func (p *openAICodexOAuthProvider) Login(ctx context.Context, callbacks oauthLoginCallbacks) (storedOAuthCredentials, error) {
	flow, err := createOpenAICodexAuthorizationFlow()
	if err != nil {
		return storedOAuthCredentials{}, err
	}

	server, err := startOpenAICodexCallbackServer(flow.State)
	serverReady := err == nil
	if err != nil && callbacks.OnOutput != nil {
		callbacks.OnOutput("Local callback server unavailable. Falling back to manual code paste.\n")
	}
	if server != nil {
		defer server.Close()
	}

	if callbacks.OnAuth != nil {
		callbacks.OnAuth(oauthAuthInfo{
			URL:          flow.URL,
			Instructions: "A browser window should open. Complete login to finish.",
		})
	}

	if err := openBrowser(flow.URL); err != nil && callbacks.OnOutput != nil {
		callbacks.OnOutput("Failed to open browser automatically. Open the URL above manually.\n")
	}

	var code string
	if serverReady && server != nil {
		if callbacks.OnOutput != nil {
			callbacks.OnOutput("Waiting for browser callback...\n")
		}
		code, err = server.WaitForCode(ctx, openAICodexCallbackTimeout)
		if err != nil && callbacks.OnOutput != nil {
			callbacks.OnOutput("Browser callback did not complete in time. Falling back to manual code paste.\n")
		}
	}
	if strings.TrimSpace(code) == "" {
		if callbacks.OnPrompt == nil {
			return storedOAuthCredentials{}, errors.New("missing prompt handler for manual authorization code input")
		}
		input, promptErr := callbacks.OnPrompt(oauthPrompt{
			Message: "Paste the authorization code (or full redirect URL):",
		})
		if promptErr != nil {
			return storedOAuthCredentials{}, promptErr
		}
		parsedCode, _, parseErr := parseAuthorizationInput(input)
		if parseErr != nil {
			return storedOAuthCredentials{}, parseErr
		}
		code = parsedCode
	}
	if strings.TrimSpace(code) == "" {
		return storedOAuthCredentials{}, errors.New("missing authorization code")
	}

	token, err := exchangeOpenAICodexAuthorizationCode(ctx, p.httpClient, code, flow.Verifier)
	if err != nil {
		return storedOAuthCredentials{}, err
	}

	accountID, err := extractOpenAICodexAccountID(token.AccessToken)
	if err != nil {
		return storedOAuthCredentials{}, err
	}

	return storedOAuthCredentials{
		Type:      "oauth",
		Access:    token.AccessToken,
		Refresh:   token.RefreshToken,
		Expires:   time.Now().UnixMilli() + token.ExpiresIn*1000,
		AccountID: accountID,
	}, nil
}

func createOpenAICodexAuthorizationFlow() (openAICodexAuthorizationFlow, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return openAICodexAuthorizationFlow{}, err
	}

	state, err := randomHex(16)
	if err != nil {
		return openAICodexAuthorizationFlow{}, err
	}

	parsed, err := url.Parse(openAICodexAuthorizeURL)
	if err != nil {
		return openAICodexAuthorizationFlow{}, err
	}

	query := parsed.Query()
	query.Set("response_type", "code")
	query.Set("client_id", openAICodexOAuthClientID)
	query.Set("redirect_uri", openAICodexRedirectURL)
	query.Set("scope", "openid profile email offline_access")
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("state", state)
	query.Set("id_token_add_organizations", "true")
	query.Set("codex_cli_simplified_flow", "true")
	query.Set("originator", "pi")
	parsed.RawQuery = query.Encode()

	return openAICodexAuthorizationFlow{
		Verifier: verifier,
		State:    state,
		URL:      parsed.String(),
	}, nil
}

func generatePKCE() (string, string, error) {
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return "", "", err
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func randomHex(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", buffer), nil
}

func startOpenAICodexCallbackServer(state string) (*openAICodexCallbackServer, error) {
	listener, err := net.Listen("tcp", openAICodexCallbackAddress)
	if err != nil {
		return nil, err
	}

	server := &openAICodexCallbackServer{
		codes: make(chan string, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "<html><body>State mismatch.</body></html>")
			return
		}

		code := strings.TrimSpace(r.URL.Query().Get("code"))
		if code == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "<html><body>Missing authorization code.</body></html>")
			return
		}

		select {
		case server.codes <- code:
		default:
		}

		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "<html><body>OpenAI authentication completed. You can close this window.</body></html>")
	})

	server.server = &http.Server{
		Handler: mux,
	}

	go func() {
		_ = server.server.Serve(listener)
	}()

	return server, nil
}

func (s *openAICodexCallbackServer) WaitForCode(ctx context.Context, timeout time.Duration) (string, error) {
	if s == nil {
		return "", errors.New("callback server not available")
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return "", errors.New("oauth callback timeout")
	case code := <-s.codes:
		return code, nil
	}
}

func (s *openAICodexCallbackServer) Close() {
	if s == nil || s.server == nil {
		return
	}
	closeContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.server.Shutdown(closeContext)
}

func parseAuthorizationInput(input string) (string, string, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", "", nil
	}

	if parsedURL, err := url.Parse(value); err == nil && parsedURL.Scheme != "" && parsedURL.Host != "" {
		return strings.TrimSpace(parsedURL.Query().Get("code")), strings.TrimSpace(parsedURL.Query().Get("state")), nil
	}

	if strings.Contains(value, "#") {
		parts := strings.SplitN(value, "#", 2)
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
	}

	if strings.Contains(value, "code=") {
		query, err := url.ParseQuery(value)
		if err != nil {
			return "", "", err
		}
		return strings.TrimSpace(query.Get("code")), strings.TrimSpace(query.Get("state")), nil
	}

	return value, "", nil
}

func exchangeOpenAICodexAuthorizationCode(
	ctx context.Context,
	httpClient *http.Client,
	code string,
	verifier string,
) (*openAICodexTokenResponse, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", openAICodexOAuthClientID)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", openAICodexRedirectURL)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, openAICodexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("content-type", "application/x-www-form-urlencoded")

	response, err := httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	var payload openAICodexTokenResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("openai codex oauth token exchange failed: %s", response.Status)
	}
	if strings.TrimSpace(payload.AccessToken) == "" || strings.TrimSpace(payload.RefreshToken) == "" || payload.ExpiresIn <= 0 {
		return nil, errors.New("openai codex oauth token exchange returned incomplete credentials")
	}
	return &payload, nil
}

func extractOpenAICodexAccountID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("failed to extract accountId from token")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payloadBytes, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return "", errors.New("failed to extract accountId from token")
		}
	}

	var payload map[string]any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return "", errors.New("failed to extract accountId from token")
	}

	authClaim, ok := payload["https://api.openai.com/auth"].(map[string]any)
	if !ok {
		return "", errors.New("failed to extract accountId from token")
	}

	accountID, ok := authClaim["chatgpt_account_id"].(string)
	if !ok || strings.TrimSpace(accountID) == "" {
		return "", errors.New("failed to extract accountId from token")
	}
	return accountID, nil
}
