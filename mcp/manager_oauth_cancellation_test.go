package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Block a real token endpoint until the client cancels it. The cleanup release
// keeps a regression from stranding the fixture's HTTP server or manager.
func blockOAuthRefresh(t *testing.T, f *managerOAuthFixture) (<-chan struct{}, <-chan struct{}) {
	t.Helper()
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var enteredOnce, canceledOnce sync.Once
	f.refresh = func(w http.ResponseWriter, r *http.Request) {
		enteredOnce.Do(func() { close(entered) })
		select {
		case <-r.Context().Done():
			canceledOnce.Do(func() { close(canceled) })
		case <-release:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}
	t.Cleanup(func() { close(release) })
	return entered, canceled
}

func awaitOAuthCancellation(t *testing.T, done <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not finish promptly", operation)
	}
}

func awaitOAuthCancellationResult(t *testing.T, done <-chan error, operation string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not finish promptly", operation)
		return nil
	}
}

func retireOAuthFixture(m *Manager, c *Connection, action string) error {
	switch action {
	case "close_connection":
		return c.Close()
	case "cancel_connection":
		c.cancel()
		return nil
	case "manager_close":
		return m.Close(context.Background())
	case "reconnect":
		_, err := m.Reconnect(context.Background(), "fixture")
		return err
	default:
		scope := m.Scope()
		scope.AuthEpoch++
		return m.SetScope(scope)
	}
}

func TestManagerOAuthRetirementCancelsConcurrentCallsDuringRefresh(t *testing.T) {
	for _, action := range []string{"set_scope", "close_connection", "cancel_connection", "manager_close", "reconnect"} {
		t.Run(action, func(t *testing.T) {
			f := newManagerOAuthFixture(t)
			m := f.manager(t, f.options())
			entered, canceled := blockOAuthRefresh(t, f)
			c := authenticateAndConnect(t, m)
			expireConnectionToken(t, c)
			type callResult struct {
				record DispatchRecord
				err    error
			}
			const calls = 4
			finished := make(chan callResult, calls)
			started := make(chan struct{}, calls)
			for range calls {
				go func() {
					started <- struct{}{}
					_, record, err := c.CallToolTracked(context.Background(), &sdk.CallToolParams{Name: "write"})
					finished <- callResult{record, err}
				}()
			}
			for range calls {
				<-started
			}
			awaitOAuthCancellation(t, entered, "token endpoint entry")
			retired := make(chan error, 1)
			go func() { retired <- retireOAuthFixture(m, c, action) }()
			awaitOAuthCancellation(t, canceled, "token request cancellation")
			err := awaitOAuthCancellationResult(t, retired, "connection retirement")
			if action == "reconnect" {
				// An interrupted refresh is ambiguous; reconnect must not reuse its
				// durable claim or silently start another token exchange.
				if !errors.Is(err, ErrAuthRequired) {
					t.Fatalf("reconnect reused an interrupted refresh: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			for range calls {
				select {
				case result := <-finished:
					if result.err == nil || result.record.Attempts != 0 {
						t.Fatalf("canceled call dispatched: record=%+v err=%v", result.record, result.err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("concurrent call remained blocked after retirement")
				}
			}
			awaitOAuthCancellation(t, c.closeDone, "SDK connection close")
			if f.refreshes.Load() != 1 || f.toolPosts.Load() != 0 || f.toolCalls.Load() != 0 {
				t.Fatalf("retirement replayed refresh or dispatched a tool: refreshes=%d posts=%d calls=%d", f.refreshes.Load(), f.toolPosts.Load(), f.toolCalls.Load())
			}
		})
	}
}

func TestManagerOAuthRetirementCancelsConnectingRefresh(t *testing.T) {
	for _, action := range []string{"set_scope", "manager_close", "reconnect"} {
		t.Run(action, func(t *testing.T) {
			f := newManagerOAuthFixture(t)
			options := f.options()
			options.Store = NewMemoryCredentialStore()
			m := f.manager(t, options)
			entered, canceled := blockOAuthRefresh(t, f)
			if err := m.Authenticate(t.Context(), "fixture"); err != nil {
				t.Fatal(err)
			}
			key := f.key()
			key.Server = "fixture"
			snapshot, err := options.Store.Load(t.Context(), key)
			if err != nil || snapshot.Credential == nil {
				t.Fatalf("load authenticated credential: %v", err)
			}
			snapshot.Credential.Token.Expiry = time.Now().Add(-time.Hour)
			if _, err := options.Store.CompareAndSwap(t.Context(), key, snapshot.Version, snapshot.Credential); err != nil {
				t.Fatal(err)
			}
			connected := make(chan error, 1)
			go func() { _, err := m.Connect(context.Background(), "fixture"); connected <- err }()
			awaitOAuthCancellation(t, entered, "connecting token endpoint entry")
			retired := make(chan error, 1)
			go func() { retired <- retireOAuthFixture(m, nil, action) }()
			awaitOAuthCancellation(t, canceled, "connecting token request cancellation")
			err = awaitOAuthCancellationResult(t, retired, "connecting session retirement")
			if action == "reconnect" {
				if !errors.Is(err, ErrAuthRequired) {
					t.Fatalf("reconnect reused an interrupted refresh: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			err = awaitOAuthCancellationResult(t, connected, "original connection attempt")
			if !errors.Is(err, ErrStale) && !errors.Is(err, ErrClosed) {
				t.Fatalf("retired connection attempt remained usable: %v", err)
			}
			if f.refreshes.Load() != 1 || f.toolPosts.Load() != 0 {
				t.Fatalf("retired connection replayed refresh or sent a tool: refreshes=%d posts=%d", f.refreshes.Load(), f.toolPosts.Load())
			}
		})
	}
}

func TestManagerOAuthRefreshCallbackRetirementVariants(t *testing.T) {
	for _, action := range []string{"manager_close", "reconnect", "goexit"} {
		t.Run(action, func(t *testing.T) {
			f := newManagerOAuthFixture(t)
			options := f.options()
			var m *Manager
			var armed atomic.Bool
			changed := make(chan error, 1)
			options.OnChange = func(OAuthState) {
				if !armed.Swap(false) {
					return
				}
				if action == "goexit" {
					runtime.Goexit()
				}
				changed <- retireOAuthFixture(m, nil, action)
			}
			m = f.manager(t, options)
			c := authenticateAndConnect(t, m)
			expireConnectionToken(t, c)
			armed.Store(true)
			finished := make(chan error, 1)
			go func() {
				_, record, err := c.CallToolTracked(t.Context(), &sdk.CallToolParams{Name: "write"})
				if record.Attempts != 0 {
					t.Error("callback retirement allowed a physical tool dispatch")
				}
				finished <- err
			}()
			if err := awaitOAuthCancellationResult(t, finished, "callback-triggering call"); err == nil {
				t.Fatal("retired callback accepted a tool call")
			}
			if action != "goexit" {
				if err := awaitOAuthCancellationResult(t, changed, "reentrant callback"); err != nil {
					t.Fatal(err)
				}
			}
			awaitOAuthCancellation(t, c.closeDone, "callback-retired SDK session")
			if f.toolPosts.Load() != 0 || f.toolCalls.Load() != 0 {
				t.Fatal("callback retirement allowed a physical tool dispatch")
			}
		})
	}
}

func newStatefulOAuthManager(t *testing.T) (*managerOAuthFixture, *Manager, *atomic.Int32) {
	t.Helper()
	f := &managerOAuthFixture{oauthFixture: newOAuthFixture(t)}
	server := sdk.NewServer(&sdk.Implementation{Name: "stateful", Version: "1"}, &sdk.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25"}})
	addWireTool(server, "write", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		f.toolCalls.Add(1)
		return &sdk.CallToolResult{}, nil
	})
	t.Cleanup(func() {
		for session := range server.Sessions() {
			_ = session.Close()
		}
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true})
	challenge := f.server.Config.Handler
	deletes := new(atomic.Int32)
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			authorized := r.Header.Get("Authorization") == "Bearer initial-access" || r.Header.Get("Authorization") == "Bearer refreshed-access"
			if r.Method == http.MethodDelete {
				deletes.Add(1)
				if !authorized {
					t.Error("session DELETE sent without a usable access token")
				}
			}
			if authorized {
				handler.ServeHTTP(w, r)
				return
			}
		}
		challenge.ServeHTTP(w, r)
	})
	options := f.options()
	m, err := New(Config{
		Scope:   Scope{Identity: "account", AuthEpoch: 1},
		Servers: []ServerConfig{{Name: "fixture", URL: f.server.URL + "/mcp", OAuth: &options, AuthKey: f.key(), Timeout: 5 * time.Second}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Interrupted refreshes intentionally leave no usable cleanup token.
		if err := m.Close(ctx); err != nil && !errors.Is(err, ErrAuthRequired) {
			t.Errorf("stateful manager close: %v", err)
		}
	})
	return f, m, deletes
}

func TestManagerOAuthStatefulRetirementDeletesSession(t *testing.T) {
	for _, action := range []string{"close_connection", "set_scope", "manager_close", "reconnect"} {
		t.Run(action, func(t *testing.T) {
			f, m, deletes := newStatefulOAuthManager(t)
			c := authenticateAndConnect(t, m)
			if c.session.ID() == "" {
				t.Fatal("fixture did not establish a stateful session")
			}
			if err := retireOAuthFixture(m, c, action); err != nil {
				t.Fatalf("authenticated session cleanup failed: %v", err)
			}
			awaitOAuthCancellation(t, c.closeDone, "stateful session close")
			if deletes.Load() != 1 || f.refreshes.Load() != 0 {
				t.Fatalf("cleanup did not use the cached token exactly once: deletes=%d refreshes=%d", deletes.Load(), f.refreshes.Load())
			}
			if _, record, err := c.CallToolTracked(t.Context(), &sdk.CallToolParams{Name: "write"}); err == nil || record.Attempts != 0 || f.toolCalls.Load() != 0 {
				t.Fatalf("cleanup token allowed a retired tool call: record=%+v err=%v", record, err)
			}
		})
	}
}

func TestManagerOAuthStatefulRetirementCancelsRefresh(t *testing.T) {
	for _, action := range []string{"close_connection", "set_scope", "manager_close", "reconnect"} {
		t.Run(action, func(t *testing.T) {
			f, m, deletes := newStatefulOAuthManager(t)
			entered, canceled := blockOAuthRefresh(t, f)
			c := authenticateAndConnect(t, m)
			expireConnectionToken(t, c)
			finished := make(chan error, 1)
			go func() {
				_, record, err := c.CallToolTracked(context.Background(), &sdk.CallToolParams{Name: "write"})
				if record.Attempts != 0 {
					t.Error("canceled refresh dispatched a tool")
				}
				finished <- err
			}()
			awaitOAuthCancellation(t, entered, "stateful token endpoint entry")
			retired := make(chan error, 1)
			go func() { retired <- retireOAuthFixture(m, c, action) }()
			awaitOAuthCancellation(t, canceled, "stateful token request cancellation")
			if err := awaitOAuthCancellationResult(t, retired, "stateful retirement"); !errors.Is(err, ErrAuthRequired) {
				t.Fatalf("interrupted refresh supplied a cleanup token: %v", err)
			}
			if err := awaitOAuthCancellationResult(t, finished, "stateful canceled call"); err == nil {
				t.Fatal("canceled refresh accepted a tool call")
			}
			awaitOAuthCancellation(t, c.closeDone, "stateful canceled session close")
			if f.refreshes.Load() != 1 || deletes.Load() != 0 || f.toolCalls.Load() != 0 {
				t.Fatalf("ambiguous credential was reused: refreshes=%d deletes=%d calls=%d", f.refreshes.Load(), deletes.Load(), f.toolCalls.Load())
			}
		})
	}
}

func TestManagerOAuthStatefulCloseDoesNotStartRefresh(t *testing.T) {
	f, m, deletes := newStatefulOAuthManager(t)
	c := authenticateAndConnect(t, m)
	expireConnectionToken(t, c)
	before := c.oauth.State().CredentialVersion
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	if err := awaitOAuthCancellationResult(t, closed, "expired stateful session close"); err != nil {
		t.Fatalf("skipping DELETE with an expired token failed local teardown: %v", err)
	}
	if f.refreshes.Load() != 0 || deletes.Load() != 0 || c.oauth.State().CredentialVersion != before {
		t.Fatal("already canceled connection claimed or sent a new refresh")
	}
}

func TestManagerOAuthStatefulReconnectExpiredToken(t *testing.T) {
	for _, persistedExpired := range []bool{false, true} {
		name := "cached_expired"
		if persistedExpired {
			name = "cached_and_persisted_expired"
		}
		t.Run(name, func(t *testing.T) {
			f, m, deletes := newStatefulOAuthManager(t)
			c := authenticateAndConnect(t, m)
			f.refresh = func(w http.ResponseWriter, _ *http.Request) {
				select {
				case <-c.closeDone:
				default:
					t.Error("refresh started before the old session finished closing")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "refreshed-access", "refresh_token": "rotated-refresh", "token_type": "Bearer", "expires_in": 3600})
			}
			expireConnectionToken(t, c)
			if persistedExpired {
				// Simulate expiry in both the live token source and durable store.
				c.oauth.mu.RLock()
				expired := cloneCredentialSnapshot(c.oauth.snapshot)
				c.oauth.mu.RUnlock()
				version, err := c.oauth.store.CompareAndSwap(t.Context(), c.oauth.Key(), expired.Version, expired.Credential)
				if err != nil {
					t.Fatal(err)
				}
				c.oauth.mu.Lock()
				c.oauth.snapshot.Version = version
				c.oauth.mu.Unlock()
			}
			before := c.oauth.State().CredentialVersion
			next, err := m.Reconnect(t.Context(), "fixture")
			if err != nil {
				t.Fatalf("reconnect rejected a renewable credential: %v", err)
			}
			if next == c || next.Generation() <= c.Generation() || next.session.ID() == "" || next.session.ID() == c.session.ID() {
				t.Fatal("reconnect did not establish a new stateful session")
			}
			if ready, err := m.Ready("fixture"); err != nil || ready != next {
				t.Fatalf("replacement connection was not ready: %v", err)
			}
			awaitOAuthCancellation(t, c.closeDone, "expired session close")
			wantRefreshes := int32(0)
			if persistedExpired {
				wantRefreshes = 1
			}
			if deletes.Load() != 0 || f.refreshes.Load() != wantRefreshes || f.exchanges.Load() != 1 || c.oauth.State().CredentialVersion != before {
				t.Fatalf("retirement sent DELETE or changed credentials: deletes=%d refreshes=%d exchanges=%d", deletes.Load(), f.refreshes.Load(), f.exchanges.Load())
			}
			stored, err := next.oauth.store.Load(t.Context(), next.oauth.Key())
			if err != nil || stored.Credential == nil || stored.Credential.RefreshPending || stored.Version != before+2*uint64(wantRefreshes) {
				t.Fatalf("replacement did not preserve or refresh the credential: version=%d err=%v", stored.Version, err)
			}
			if _, record, err := c.CallToolTracked(t.Context(), &sdk.CallToolParams{Name: "write"}); err == nil || record.Attempts != 0 || f.toolCalls.Load() != 0 {
				t.Fatalf("expired retired session dispatched a tool: record=%+v err=%v", record, err)
			}
			if _, record, err := next.CallToolTracked(t.Context(), &sdk.CallToolParams{Name: "write"}); err != nil || record.Attempts != 1 || f.toolCalls.Load() != 1 {
				t.Fatalf("replacement session could not execute a tool: record=%+v err=%v", record, err)
			}
		})
	}
}

func TestManagerOAuthStatefulReconnectPreservesDeleteFailure(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause error
	}{
		{"canceled", context.Canceled},
		{"deadline", context.DeadlineExceeded},
		{"transport", io.ErrUnexpectedEOF},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, m, _ := newStatefulOAuthManager(t)
			var deleteAttempts atomic.Int32
			m.config.HTTPClient = &http.Client{Transport: managerTransportFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodDelete {
					deleteAttempts.Add(1)
					return nil, test.cause
				}
				return http.DefaultTransport.RoundTrip(r)
			})}
			c := authenticateAndConnect(t, m)
			if next, err := m.Reconnect(t.Context(), "fixture"); !errors.Is(err, test.cause) || next != nil {
				t.Fatalf("reconnect hid a real DELETE failure: %v", err)
			}
			awaitOAuthCancellation(t, c.closeDone, "failed DELETE teardown")
			if err := c.Close(); !errors.Is(err, test.cause) {
				t.Fatalf("repeated Close lost the DELETE cause: %v", err)
			}
			if deleteAttempts.Load() != 1 || f.refreshes.Load() != 0 {
				t.Fatalf("failed teardown retried: deletes=%d refreshes=%d", deleteAttempts.Load(), f.refreshes.Load())
			}
			if _, err := m.Ready("fixture"); !errors.Is(err, ErrNotReady) {
				t.Fatalf("failed teardown started a replacement session: %v", err)
			}
		})
	}
}
