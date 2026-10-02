package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type managerOAuthFixture struct {
	*oauthFixture
	toolPosts atomic.Int32
	toolCalls atomic.Int32
}

// The existing OAuth fixture owns metadata/code/refresh requests. Authenticated
// MCP POSTs reach a real SDK server; GET remains an explicit host login probe.
func newManagerOAuthFixture(t *testing.T) *managerOAuthFixture {
	t.Helper()
	f := &managerOAuthFixture{oauthFixture: newOAuthFixture(t)}
	server := wireTestServer()
	addWireTool(server, "write", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		f.toolCalls.Add(1)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "written"}}}, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	challenge := f.server.Config.Handler
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" || r.Method == http.MethodGet {
			challenge.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodPost {
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("MCP POST body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(data))
			var call struct {
				Method string `json:"method"`
			}
			if json.Unmarshal(data, &call) == nil && call.Method == "tools/call" {
				f.toolPosts.Add(1)
			}
		}
		if value := r.Header.Get("Authorization"); value != "Bearer initial-access" && value != "Bearer refreshed-access" {
			challenge.ServeHTTP(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})
	return f
}

func (f *managerOAuthFixture) manager(t *testing.T, options OAuthOptions) *Manager {
	t.Helper()
	m, err := New(Config{
		Scope:   Scope{Identity: "account", AuthEpoch: 1},
		Servers: []ServerConfig{{Name: "fixture", URL: f.server.URL + "/mcp", OAuth: &options, AuthKey: f.key(), Timeout: 5 * time.Second}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeManager(t, m) })
	return m
}

func authenticateAndConnect(t *testing.T, m *Manager) *Connection {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := m.Authenticate(ctx, "fixture"); err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	c, err := m.Connect(ctx, "fixture")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return c
}

func expireConnectionToken(t *testing.T, c *Connection) {
	t.Helper()
	if c.oauth == nil {
		t.Fatal("connection has no OAuth owner")
	}
	if err := c.oauth.acquire(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer c.oauth.release()
	c.oauth.mu.Lock()
	defer c.oauth.mu.Unlock()
	if c.oauth.snapshot.Credential == nil {
		t.Fatal("connection has no credential")
	}
	c.oauth.snapshot.Credential.Token.Expiry = time.Now().Add(-time.Hour)
}

func awaitOAuthConnectionClose(t *testing.T, c *Connection) {
	t.Helper()
	select {
	case <-c.closeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("changed OAuth state did not close actual SDK connection")
	}
}

func TestManagerOAuthDefaultStoreSurvivesAuthenticate(t *testing.T) {
	f := newManagerOAuthFixture(t)
	options := f.options()
	if options.Store != nil {
		t.Fatal("fixture must exercise a nil host store")
	}
	m := f.manager(t, options)
	state, err := m.OAuthState(t.Context(), "fixture")
	if err != nil || state.Authenticated || state.CredentialVersion != 0 || f.exchanges.Load() != 0 {
		t.Fatalf("OAuthState performed login or exposed a credential: %+v %v", state, err)
	}
	c := authenticateAndConnect(t, m)
	if got := m.Scope(); got != (Scope{Identity: "account", AuthEpoch: 2}) || c.Scope() != got {
		t.Fatalf("new session did not use authenticated epoch: manager=%+v connection=%+v", got, c.Scope())
	}
	state, err = m.OAuthState(t.Context(), "fixture")
	if err != nil || !state.Authenticated || state.CredentialVersion == 0 {
		t.Fatalf("default store lost authenticated credential: %+v %v", state, err)
	}
	result, record, err := c.CallToolTracked(t.Context(), &sdk.CallToolParams{Name: "write", Arguments: map[string]any{}})
	if err != nil || result == nil || record.Attempts != 1 || f.toolPosts.Load() != 1 || f.toolCalls.Load() != 1 || f.exchanges.Load() != 1 {
		t.Fatalf("credential was not restored into real MCP session: result=%v record=%+v err=%v posts=%d calls=%d exchanges=%d", result, record, err, f.toolPosts.Load(), f.toolCalls.Load(), f.exchanges.Load())
	}
}

func TestManagerOAuthSameScopeRefreshPersistsAndKeepsSession(t *testing.T) {
	f := newManagerOAuthFixture(t)
	storePath := filepath.Join(t.TempDir(), "credentials.json")
	store, err := NewFileCredentialStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	options := f.options()
	options.Store = store
	m := f.manager(t, options)
	c := authenticateAndConnect(t, m)
	before := c.oauth.State()
	scope := m.Scope()
	expireConnectionToken(t, c)
	if _, record, err := c.CallToolTracked(t.Context(), &sdk.CallToolParams{Name: "write", Arguments: map[string]any{}}); err != nil || record.Attempts != 1 {
		t.Fatalf("same-scope refresh retired usable session: record=%+v err=%v", record, err)
	}
	after := c.oauth.State()
	if !after.Authenticated || after.CredentialVersion <= before.CredentialVersion || !reflect.DeepEqual(after.GrantedScopes, before.GrantedScopes) {
		t.Fatalf("refresh state is wrong: before=%+v after=%+v", before, after)
	}
	if got, err := m.Ready("fixture"); err != nil || got != c || m.Scope() != scope {
		t.Fatalf("token rotation changed identity/session: ready=%p scope=%+v err=%v", got, m.Scope(), err)
	}
	persisted, err := store.Load(t.Context(), c.oauth.Key())
	if err != nil || persisted.Version != after.CredentialVersion || persisted.Credential == nil || persisted.Credential.Token.AccessToken != "refreshed-access" || persisted.Credential.Token.RefreshToken != "rotated-refresh" || !reflect.DeepEqual(persisted.Credential.Scopes, before.GrantedScopes) {
		t.Fatalf("rotated token/scopes were not atomically persisted: version=%d err=%v", persisted.Version, err)
	}
	// A fresh store handle must see the published credential, independently of
	// this connection's in-memory token source.
	reopened, err := NewFileCredentialStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := reopened.Load(t.Context(), c.oauth.Key())
	if err != nil || restored.Version != after.CredentialVersion || restored.Credential == nil || restored.Credential.Token.RefreshToken != "rotated-refresh" {
		t.Fatalf("fresh store could not restore published refresh: version=%d err=%v", restored.Version, err)
	}
	state, err := m.OAuthState(t.Context(), "fixture")
	if err != nil || !state.Authenticated || state.CredentialVersion != after.CredentialVersion {
		t.Fatalf("manager did not observe persisted refresh: %+v %v", state, err)
	}
	if _, err := c.CallTool(t.Context(), &sdk.CallToolParams{Name: "write", Arguments: map[string]any{}}); err != nil || f.refreshes.Load() != 1 || f.toolCalls.Load() != 2 {
		t.Fatalf("refreshed session was not reusable: err=%v refreshes=%d calls=%d", err, f.refreshes.Load(), f.toolCalls.Load())
	}
}

func TestManagerOAuthRefreshScopeChangeOrRevocationStopsPhysicalTool(t *testing.T) {
	for _, test := range []struct {
		name   string
		scope  string
		revoke bool
	}{
		{name: "added_scope", scope: "read write offline_access"},
		{name: "removed_scope", scope: "read"},
		{name: "empty_scope", scope: ""},
		{name: "revoked_token", revoke: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newManagerOAuthFixture(t)
			f.refresh = func(w http.ResponseWriter, r *http.Request) {
				if test.revoke {
					w.WriteHeader(http.StatusBadRequest)
					json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "private token diagnostic"})
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"access_token": "refreshed-access", "refresh_token": "rotated-refresh", "token_type": "Bearer", "expires_in": 3600, "scope": test.scope})
			}
			m := f.manager(t, f.options())
			c := authenticateAndConnect(t, m)
			expireConnectionToken(t, c)
			_, record, err := c.CallToolTracked(t.Context(), &sdk.CallToolParams{Name: "write", Arguments: map[string]any{}})
			if err == nil || record.Attempts != 0 || f.toolPosts.Load() != 0 || f.toolCalls.Load() != 0 || f.refreshes.Load() != 1 {
				t.Fatalf("scope/revocation sent a tool POST: record=%+v err=%v posts=%d calls=%d refreshes=%d", record, err, f.toolPosts.Load(), f.toolCalls.Load(), f.refreshes.Load())
			}
			if !errors.Is(c.available(), ErrStale) {
				t.Fatalf("changed credential retained live SDK session: %v", c.available())
			}
			awaitOAuthConnectionClose(t, c)
			if _, err := m.Ready("fixture"); !errors.Is(err, ErrStale) {
				t.Fatalf("Ready returned invalidated session: %v", err)
			}
			if _, _, err := c.CallToolTracked(t.Context(), &sdk.CallToolParams{Name: "write"}); !errors.Is(err, ErrStale) || f.refreshes.Load() != 1 || f.toolPosts.Load() != 0 {
				t.Fatalf("retired session retransmitted or refreshed again: err=%v refreshes=%d posts=%d", err, f.refreshes.Load(), f.toolPosts.Load())
			}
		})
	}
}

func TestManagerOAuthClearCredentialsRetiresEpochAndPersistsTombstone(t *testing.T) {
	f := newManagerOAuthFixture(t)
	m := f.manager(t, f.options())
	c := authenticateAndConnect(t, m)
	before := c.oauth.State()
	scope := m.Scope()
	if err := m.ClearCredentials(t.Context(), "fixture"); err != nil {
		t.Fatal(err)
	}
	if got := m.Scope(); got.Identity != scope.Identity || got.AuthEpoch != scope.AuthEpoch+1 {
		t.Fatalf("logout did not advance the identity epoch: before=%+v after=%+v", scope, got)
	}
	awaitOAuthConnectionClose(t, c)
	state, err := m.OAuthState(t.Context(), "fixture")
	if err != nil || state.Authenticated || state.CredentialVersion <= before.CredentialVersion {
		t.Fatalf("logout was not a versioned tombstone: %+v %v", state, err)
	}
	store := m.Servers()[0].OAuth.Store
	snapshot, err := store.Load(t.Context(), c.oauth.Key())
	if err != nil || snapshot.Credential != nil || snapshot.Version != state.CredentialVersion {
		t.Fatalf("logout credential remained in store: version=%d credentialPresent=%t err=%v", snapshot.Version, snapshot.Credential != nil, err)
	}
	if _, record, err := c.CallToolTracked(t.Context(), &sdk.CallToolParams{Name: "write"}); err == nil || record.Attempts != 0 || f.toolPosts.Load() != 0 {
		t.Fatalf("old epoch sent a tool after logout: %+v %v", record, err)
	}
	if _, err := m.Ready("fixture"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("logout kept old manager session: %v", err)
	}
	next := authenticateAndConnect(t, m)
	if next == c || next.Generation() <= c.Generation() || next.Scope().AuthEpoch != scope.AuthEpoch+2 || f.exchanges.Load() != 2 {
		t.Fatalf("explicit reauthentication reused cleared epoch: old=%+v next=%+v exchanges=%d", c.Scope(), next.Scope(), f.exchanges.Load())
	}
}

func TestManagerOAuthIdentityChangeCannotPublishOldEpoch(t *testing.T) {
	f := newManagerOAuthFixture(t)
	options := f.options()
	original := options.SDKConfig.AuthorizationCodeFetcher
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	options.SDKConfig.AuthorizationCodeFetcher = func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return original(ctx, args)
	}
	m := f.manager(t, options)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Authenticate(ctx, "fixture") }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("login callback was not reached")
	}
	newScope := Scope{Identity: "other-account", AuthEpoch: 17}
	if err := m.SetScope(newScope); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		if !errors.Is(err, ErrStale) {
			t.Fatalf("obsolete login did not reject publication: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("login did not complete")
	}
	if m.Scope() != newScope {
		t.Fatalf("obsolete login overwrote current identity: %+v", m.Scope())
	}
	state, err := m.OAuthState(t.Context(), "fixture")
	if err != nil || state.Authenticated || state.CredentialVersion != 0 {
		t.Fatalf("old login leaked credentials into another account: %+v %v", state, err)
	}
}
