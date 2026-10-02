package mcp

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestManagerOAuthRefreshCallbackCanRetireSession(t *testing.T) {
	for _, action := range []string{"set_scope", "close_connection"} {
		t.Run(action, func(t *testing.T) {
			f := newManagerOAuthFixture(t)
			options := f.options()
			var m *Manager
			var c *Connection
			var armed atomic.Bool
			changed := make(chan error, 1)
			options.OnChange = func(OAuthState) {
				if !armed.Load() {
					return
				}
				if action == "close_connection" {
					changed <- c.Close()
					return
				}
				scope := m.Scope()
				scope.AuthEpoch++
				changed <- m.SetScope(scope)
			}
			m = f.manager(t, options)
			c = authenticateAndConnect(t, m)
			expireConnectionToken(t, c)
			armed.Store(true)
			finished := make(chan error, 1)
			go func() {
				_, record, err := c.CallToolTracked(t.Context(), &sdk.CallToolParams{Name: "write"})
				if record.Attempts != 0 || f.toolPosts.Load() != 0 {
					t.Error("callback retirement allowed a physical tool dispatch")
				}
				finished <- err
			}()
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("retired session accepted the triggering tool call")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("refresh callback deadlocked while retiring its SDK session")
			}
			select {
			case err := <-changed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("refresh callback did not finish session teardown")
			}
			awaitOAuthConnectionClose(t, c)
		})
	}
}

func TestManagerOAuthRefreshCallbackFencesConcurrentDispatch(t *testing.T) {
	f := newManagerOAuthFixture(t)
	options := f.options()
	var armed atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	options.OnChange = func(OAuthState) {
		if armed.Load() {
			close(entered)
			<-release
		}
	}
	m := f.manager(t, options)
	c := authenticateAndConnect(t, m)
	expireConnectionToken(t, c)
	armed.Store(true)
	finished := make(chan error, 1)
	go func() {
		_, _, err := c.CallToolTracked(t.Context(), &sdk.CallToolParams{Name: "write"})
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("refresh did not reach the external callback")
	}
	_, record, err := c.CallToolTracked(t.Context(), &sdk.CallToolParams{Name: "write"})
	if !errors.Is(err, ErrNotReady) || record.Attempts != 0 || f.toolPosts.Load() != 0 {
		t.Errorf("pending callback allowed concurrent dispatch: record=%+v err=%v posts=%d", record, err, f.toolPosts.Load())
	}
	close(release)
	select {
	case err := <-finished:
		if err != nil || f.toolPosts.Load() != 1 || f.refreshes.Load() != 1 {
			t.Fatalf("same-scope callback did not resume its original call: err=%v posts=%d refreshes=%d", err, f.toolPosts.Load(), f.refreshes.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completed callback left the token source blocked")
	}
	if _, err := c.CallTool(t.Context(), &sdk.CallToolParams{Name: "write"}); err != nil || f.toolPosts.Load() != 2 {
		t.Fatalf("completed callback left a dispatch fence behind: err=%v posts=%d", err, f.toolPosts.Load())
	}
}

func TestManagerOAuthRefreshCallbackCanRetireConnectingSession(t *testing.T) {
	f := newManagerOAuthFixture(t)
	options := f.options()
	options.Store = NewMemoryCredentialStore()
	var m *Manager
	var armed atomic.Bool
	changed := make(chan error, 1)
	options.OnChange = func(OAuthState) {
		if armed.Load() {
			scope := m.Scope()
			scope.AuthEpoch++
			changed <- m.SetScope(scope)
		}
	}
	m = f.manager(t, options)
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
	armed.Store(true)
	finished := make(chan error, 1)
	go func() {
		_, err := m.Connect(t.Context(), "fixture")
		finished <- err
	}()
	select {
	case err := <-finished:
		if !errors.Is(err, ErrStale) {
			t.Fatalf("retired connection attempt did not report stale: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh callback deadlocked while retiring a connecting session")
	}
	select {
	case err := <-changed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh callback did not finish retiring the connection attempt")
	}
}

func TestManagerOAuthRefreshCallbackPanicRetiresSession(t *testing.T) {
	f := newManagerOAuthFixture(t)
	options := f.options()
	var armed atomic.Bool
	options.OnChange = func(OAuthState) {
		if armed.Load() {
			panic("host callback failed")
		}
	}
	m := f.manager(t, options)
	c := authenticateAndConnect(t, m)
	expireConnectionToken(t, c)
	armed.Store(true)
	_, record, err := c.CallToolTracked(t.Context(), &sdk.CallToolParams{Name: "write"})
	if err == nil || record.Attempts != 0 || f.toolPosts.Load() != 0 {
		t.Fatalf("panicking callback allowed dispatch: record=%+v err=%v posts=%d", record, err, f.toolPosts.Load())
	}
	awaitOAuthConnectionClose(t, c)
	if !errors.Is(c.available(), ErrStale) {
		t.Fatalf("panicking callback retained a usable session: %v", c.available())
	}
}
