package mcp

import (
	"context"
	"errors"
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
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not finish promptly", operation)
	}
}

func awaitOAuthCancellationResult(t *testing.T, done <-chan error, operation string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
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
				case <-time.After(2 * time.Second):
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
