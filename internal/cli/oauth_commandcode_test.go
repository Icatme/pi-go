package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCommandCodeCallbackServerAcceptsValidPostAndCORS(t *testing.T) {
	server, err := startCommandCodeCallbackServer(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, commandCodeCallbackURL(server.port), strings.NewReader(`{"apiKey":"user-key","state":"state","userId":"user","userName":"User","keyName":"key"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://commandcode.ai")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Access-Control-Allow-Origin") != "https://commandcode.ai" || response.Header.Get("Access-Control-Allow-Private-Network") != "true" {
		t.Fatalf("unexpected callback response: status=%d headers=%v", response.StatusCode, response.Header)
	}
	callback, err := server.Wait(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if callback.APIKey != "user-key" || callback.State != "state" || callback.UserID != "user" || callback.UserName != "User" || callback.KeyName != "key" {
		t.Fatalf("unexpected callback: %+v", callback)
	}
}

func TestCommandCodeCallbackServerHandlesPreflightAndInvalidRoutes(t *testing.T) {
	server, err := startCommandCodeCallbackServer(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	request, err := http.NewRequest(http.MethodOptions, commandCodeCallbackURL(server.port), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "https://commandcode.ai")
	request.Header.Set("Access-Control-Request-Headers", "content-type,x-requested-with")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent || response.Header.Get("Access-Control-Allow-Headers") != "content-type,x-requested-with" {
		t.Fatalf("unexpected preflight response: status=%d headers=%v", response.StatusCode, response.Header)
	}

	response, err = http.Post(commandCodeCallbackURL(server.port)+"/other", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", response.StatusCode)
	}
}

func TestCommandCodeOAuthLoginCompletesBrowserCallback(t *testing.T) {
	authURLs := make(chan string, 1)
	provider := &commandCodeOAuthProvider{
		openBrowser: func(authURL string) error {
			authURLs <- authURL
			return nil
		},
		startServer: func() (*commandCodeCallbackServer, error) {
			return startCommandCodeCallbackServer(0, 1)
		},
		now: func() time.Time { return time.Unix(1_700_000_000, 0) },
	}

	type loginResult struct {
		credentials storedOAuthCredentials
		err         error
	}
	result := make(chan loginResult, 1)
	go func() {
		credentials, err := provider.Login(context.Background(), oauthLoginCallbacks{
			OnAuth: func(oauthAuthInfo) {},
			OnPrompt: func(oauthPrompt) (string, error) {
				return "", errors.New("manual prompt should not be used")
			},
		})
		result <- loginResult{credentials: credentials, err: err}
	}()

	authURL := <-authURLs
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != "commandcode.ai" || parsed.Path != "/studio/auth/cli" {
		t.Fatalf("unexpected auth URL %q", authURL)
	}
	callbackURL := parsed.Query().Get("callback")
	state := parsed.Query().Get("state")
	if callbackURL == "" || state == "" {
		t.Fatalf("missing callback/state in %q", authURL)
	}
	body, err := json.Marshal(commandCodeAuthCallback{
		APIKey: "browser-key", State: state, UserID: "user", UserName: "User", KeyName: "browser",
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(callbackURL, "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unexpected callback status %d", response.StatusCode)
	}

	login := <-result
	if login.err != nil {
		t.Fatal(login.err)
	}
	if login.credentials.Type != "oauth" || login.credentials.Access != "browser-key" || login.credentials.Refresh != "browser-key" || login.credentials.Expires <= 1_700_000_000_000 {
		t.Fatalf("unexpected stored credentials: %+v", login.credentials)
	}
}

func TestCommandCodeOAuthLoginFallsBackToSanitizedManualKey(t *testing.T) {
	provider := &commandCodeOAuthProvider{
		openBrowser: func(string) error { return nil },
		startServer: func() (*commandCodeCallbackServer, error) {
			return nil, errors.New("port unavailable")
		},
		now: time.Now,
	}
	credentials, err := provider.Login(context.Background(), oauthLoginCallbacks{
		OnPrompt: func(prompt oauthPrompt) (string, error) {
			if !strings.Contains(prompt.Message, "Paste your Command Code API key") {
				t.Fatalf("unexpected prompt %q", prompt.Message)
			}
			return "\x1b[200~  user_manualKey\n\x1b[201~", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if credentials.Access != "user_manualKey" || credentials.Refresh != "user_manualKey" {
		t.Fatalf("unexpected manual credentials: %+v", credentials)
	}
}

func TestCommandCodeOAuthLoginClosesServerBeforeTimeoutFallback(t *testing.T) {
	t.Setenv("COMMANDCODE_AUTH_TIMEOUT_MS", "1")
	serverStarted := make(chan *commandCodeCallbackServer, 1)
	promptStarted := make(chan struct{})
	releasePrompt := make(chan struct{})
	provider := &commandCodeOAuthProvider{
		openBrowser: func(string) error { return nil },
		startServer: func() (*commandCodeCallbackServer, error) {
			server, err := startCommandCodeCallbackServer(0, 1)
			if err == nil {
				serverStarted <- server
			}
			return server, err
		},
		now: time.Now,
	}

	type loginResult struct {
		credentials storedOAuthCredentials
		err         error
	}
	results := make(chan loginResult, 1)
	go func() {
		credentials, err := provider.Login(context.Background(), oauthLoginCallbacks{
			OnPrompt: func(oauthPrompt) (string, error) {
				close(promptStarted)
				<-releasePrompt
				return "manual-key", nil
			},
		})
		results <- loginResult{credentials: credentials, err: err}
	}()

	server := <-serverStarted
	select {
	case <-promptStarted:
	case <-time.After(time.Second):
		close(releasePrompt)
		t.Fatal("manual fallback did not start")
	}
	response, postErr := http.Post(commandCodeCallbackURL(server.port), "application/json", strings.NewReader(`{"apiKey":"late-key","state":"late-state","userId":"user","userName":"User","keyName":"late"}`))
	if response != nil {
		_ = response.Body.Close()
	}
	close(releasePrompt)
	login := <-results
	if login.err != nil || login.credentials.Access != "manual-key" {
		t.Fatalf("unexpected manual fallback result: credentials=%+v err=%v", login.credentials, login.err)
	}
	if postErr == nil {
		t.Fatal("expected callback server to be closed before manual fallback")
	}
}

func TestCommandCodeOAuthLoginRejectsStateMismatch(t *testing.T) {
	authURLs := make(chan string, 1)
	provider := &commandCodeOAuthProvider{
		openBrowser: func(authURL string) error {
			authURLs <- authURL
			return nil
		},
		startServer: func() (*commandCodeCallbackServer, error) {
			return startCommandCodeCallbackServer(0, 1)
		},
		now: time.Now,
	}
	errorsCh := make(chan error, 1)
	go func() {
		_, err := provider.Login(context.Background(), oauthLoginCallbacks{})
		errorsCh <- err
	}()

	parsed, err := url.Parse(<-authURLs)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(commandCodeAuthCallback{
		APIKey: "attacker-key", State: "wrong-state", UserID: "user", UserName: "User", KeyName: "key",
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(parsed.Query().Get("callback"), "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if loginErr := <-errorsCh; loginErr == nil || !strings.Contains(loginErr.Error(), "state token mismatch") {
		t.Fatalf("expected state mismatch error, got %v", loginErr)
	}
}

func commandCodeCallbackURL(port int) string {
	return "http://127.0.0.1:" + fmt.Sprint(port) + "/callback"
}
