package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	commandCodeStudioBaseURL      = "https://commandcode.ai"
	commandCodeCallbackStartPort  = 5959
	commandCodeCallbackPortRange  = 10
	commandCodeDefaultAuthTimeout = 15 * time.Second
	commandCodeCredentialLifetime = 10 * 365 * 24 * time.Hour
	commandCodeCallbackBodyLimit  = 10_000
)

var commandCodeAllowedOrigins = []string{
	"http://localhost:3000",
	"https://staging.commandcode.ai",
	"https://commandcode.ai",
}

type commandCodeOAuthProvider struct {
	openBrowser func(string) error
	startServer func() (*commandCodeCallbackServer, error)
	now         func() time.Time
}

type commandCodeAuthCallback struct {
	APIKey           string `json:"apiKey"`
	State            string `json:"state"`
	UserID           string `json:"userId"`
	UserName         string `json:"userName"`
	KeyName          string `json:"keyName"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

type commandCodeCallbackResult struct {
	callback commandCodeAuthCallback
	err      error
}

type commandCodeCallbackServer struct {
	server  *http.Server
	port    int
	results chan commandCodeCallbackResult
	once    sync.Once
}

func newCommandCodeOAuthProvider() *commandCodeOAuthProvider {
	return &commandCodeOAuthProvider{
		openBrowser: openBrowser,
		startServer: func() (*commandCodeCallbackServer, error) {
			return startCommandCodeCallbackServer(commandCodeCallbackStartPort, commandCodeCallbackPortRange)
		},
		now: time.Now,
	}
}

func (p *commandCodeOAuthProvider) ID() string {
	return "commandcode"
}

func (p *commandCodeOAuthProvider) Name() string {
	return "Command Code"
}

func (p *commandCodeOAuthProvider) Login(ctx context.Context, callbacks oauthLoginCallbacks) (storedOAuthCredentials, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	server, err := p.startServer()
	if err != nil {
		return p.promptForAPIKey(callbacks, "Could not start browser auth. Paste your Command Code API key:")
	}
	defer server.Close()

	state, err := commandCodeStateToken()
	if err != nil {
		return storedOAuthCredentials{}, err
	}
	callbackURL := fmt.Sprintf("http://localhost:%d/callback", server.port)
	authURL := commandCodeStudioBaseURL + "/studio/auth/cli?callback=" + url.QueryEscape(callbackURL) + "&state=" + url.QueryEscape(state)
	if callbacks.OnAuth != nil {
		callbacks.OnAuth(oauthAuthInfo{
			URL:          authURL,
			Instructions: "A browser window should open. Complete Command Code login to finish.",
		})
	}
	if err := p.openBrowser(authURL); err != nil && callbacks.OnOutput != nil {
		callbacks.OnOutput("Failed to open browser automatically. Open the URL above manually.\n")
	}

	callback, err := server.Wait(ctx, commandCodeAuthTimeout())
	if err != nil {
		if ctx.Err() != nil {
			return storedOAuthCredentials{}, ctx.Err()
		}
		if errors.Is(err, errCommandCodeAuthTimeout) {
			server.Close()
			return p.promptForAPIKey(callbacks, "Automatic transfer failed or timed out. Paste your Command Code API key:")
		}
		return storedOAuthCredentials{}, err
	}
	if callback.State != state {
		return storedOAuthCredentials{}, errors.New("state token mismatch; authentication may have been tampered with")
	}
	return commandCodeStoredCredentials(callback.APIKey, p.now()), nil
}

func (p *commandCodeOAuthProvider) promptForAPIKey(callbacks oauthLoginCallbacks, message string) (storedOAuthCredentials, error) {
	if callbacks.OnPrompt == nil {
		return storedOAuthCredentials{}, errors.New("missing prompt handler for Command Code API key")
	}
	input, err := callbacks.OnPrompt(oauthPrompt{Message: message})
	if err != nil {
		return storedOAuthCredentials{}, err
	}
	apiKey := sanitizeCommandCodeAPIKey(input)
	if apiKey == "" {
		return storedOAuthCredentials{}, errors.New("no Command Code API key provided")
	}
	return commandCodeStoredCredentials(apiKey, p.now()), nil
}

func commandCodeStoredCredentials(apiKey string, now time.Time) storedOAuthCredentials {
	return storedOAuthCredentials{
		Type:    "oauth",
		Access:  apiKey,
		Refresh: apiKey,
		Expires: now.Add(commandCodeCredentialLifetime).UnixMilli(),
	}
}

func sanitizeCommandCodeAPIKey(input string) string {
	input = strings.NewReplacer(
		"\x1b[200~", "",
		"\x1b[201~", "",
		"[200~", "",
		"[201~", "",
	).Replace(input)
	return strings.TrimSpace(strings.Map(func(value rune) rune {
		if value <= 31 || value == 127 {
			return -1
		}
		return value
	}, input))
}

func commandCodeStateToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

var errCommandCodeAuthTimeout = errors.New("Command Code browser authentication timed out")

func commandCodeAuthTimeout() time.Duration {
	value := strings.TrimSpace(os.Getenv("COMMANDCODE_AUTH_TIMEOUT_MS"))
	if value == "" {
		return commandCodeDefaultAuthTimeout
	}
	duration, err := time.ParseDuration(value + "ms")
	if err != nil || duration <= 0 {
		return commandCodeDefaultAuthTimeout
	}
	return duration
}

func startCommandCodeCallbackServer(startPort, portRange int) (*commandCodeCallbackServer, error) {
	listener, err := listenForCommandCodeCallback(startPort, portRange)
	if err != nil {
		return nil, fmt.Errorf("failed to start Command Code auth server: %w", err)
	}

	server := &commandCodeCallbackServer{
		port:    listener.Addr().(*net.TCPAddr).Port,
		results: make(chan commandCodeCallbackResult, 1),
	}
	server.server = &http.Server{
		Handler:           http.HandlerFunc(server.handleCallback),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if serveErr := server.server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			server.complete(commandCodeCallbackResult{err: serveErr})
		}
	}()
	return server, nil
}

func listenForCommandCodeCallback(startPort, portRange int) (net.Listener, error) {
	if startPort > 0 {
		for offset := 0; offset < max(1, portRange); offset++ {
			listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", startPort+offset))
			if err == nil {
				return listener, nil
			}
		}
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

func (s *commandCodeCallbackServer) handleCallback(w http.ResponseWriter, request *http.Request) {
	setCommandCodeCORSHeaders(w, request)
	w.Header().Set("Content-Type", "application/json")
	if request.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if request.URL.Path != "/callback" {
		writeCommandCodeJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": "Not found"})
		return
	}
	if request.Method != http.MethodPost {
		writeCommandCodeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "error": "Method not allowed. Use POST."})
		return
	}

	request.Body = http.MaxBytesReader(w, request.Body, commandCodeCallbackBodyLimit)
	defer request.Body.Close()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		writeCommandCodeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Invalid JSON"})
		return
	}
	var callback commandCodeAuthCallback
	if err := json.Unmarshal(body, &callback); err != nil {
		writeCommandCodeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Invalid JSON"})
		return
	}
	if callback.Error != "" {
		writeCommandCodeJSON(w, http.StatusOK, map[string]any{"success": true})
		message := strings.TrimSpace(callback.ErrorDescription)
		if message == "" {
			message = callback.Error
		}
		if callback.Error == "access_denied" && message == "" {
			message = "Authorization was denied by the user"
		}
		s.complete(commandCodeCallbackResult{err: errors.New(message)})
		go s.Close()
		return
	}
	if callback.APIKey == "" || callback.State == "" || callback.UserID == "" || callback.UserName == "" || callback.KeyName == "" {
		writeCommandCodeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "Missing required fields"})
		return
	}

	writeCommandCodeJSON(w, http.StatusOK, map[string]any{"success": true})
	s.complete(commandCodeCallbackResult{callback: callback})
	go s.Close()
}

func setCommandCodeCORSHeaders(w http.ResponseWriter, request *http.Request) {
	origin := request.Header.Get("Origin")
	responseOrigin := commandCodeAllowedOrigins[0]
	for _, allowed := range commandCodeAllowedOrigins {
		if origin == allowed {
			responseOrigin = origin
			break
		}
	}
	w.Header().Set("Access-Control-Allow-Origin", responseOrigin)
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	requestedHeaders := request.Header.Get("Access-Control-Request-Headers")
	if requestedHeaders == "" {
		requestedHeaders = "Content-Type"
	}
	w.Header().Set("Access-Control-Allow-Headers", requestedHeaders)
	w.Header().Set("Access-Control-Allow-Private-Network", "true")
}

func writeCommandCodeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *commandCodeCallbackServer) complete(result commandCodeCallbackResult) {
	s.once.Do(func() {
		s.results <- result
	})
}

func (s *commandCodeCallbackServer) Wait(ctx context.Context, timeout time.Duration) (commandCodeAuthCallback, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return commandCodeAuthCallback{}, ctx.Err()
	case <-timer.C:
		return commandCodeAuthCallback{}, errCommandCodeAuthTimeout
	case result := <-s.results:
		return result.callback, result.err
	}
}

func (s *commandCodeCallbackServer) Close() {
	if s == nil || s.server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.server.Shutdown(ctx)
}
