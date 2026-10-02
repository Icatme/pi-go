package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

type oauthFixture struct {
	server        *httptest.Server
	mu            sync.Mutex
	scope         string
	granted       *string
	requests      []string
	iss           string
	metadataURL   string
	canceled      bool
	refresh       func(http.ResponseWriter, *http.Request)
	exchanges     atomic.Int32
	refreshes     atomic.Int32
	registrations atomic.Int32
}

func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	f := &oauthFixture{scope: "read"}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mcp":
			f.mu.Lock()
			scope, metadata := f.scope, f.metadataURL
			f.mu.Unlock()
			if metadata == "" {
				metadata = f.server.URL + "/resource-metadata"
			}
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata=%q, error="insufficient_scope", scope=%q`, metadata, scope))
			if r.Header.Get("Authorization") != "" {
				w.WriteHeader(http.StatusForbidden)
			} else {
				w.WriteHeader(http.StatusUnauthorized)
			}
		case "/resource-metadata":
			json.NewEncoder(w).Encode(oauthex.ProtectedResourceMetadata{Resource: f.server.URL + "/mcp", AuthorizationServers: []string{f.server.URL}, ScopesSupported: []string{"read"}})
		case "/.well-known/oauth-authorization-server", "/custom-authorization-metadata":
			json.NewEncoder(w).Encode(oauthex.AuthServerMeta{
				Issuer: f.server.URL, AuthorizationEndpoint: f.server.URL + "/authorize", TokenEndpoint: f.server.URL + "/token", RegistrationEndpoint: f.server.URL + "/register",
				CodeChallengeMethodsSupported: []string{"S256"}, ScopesSupported: []string{"read", "write", "offline_access"},
				AuthorizationResponseIssParameterSupported: true, TokenEndpointAuthMethodsSupported: []string{"client_secret_post"},
			})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
				return
			}
			if r.Form.Get("grant_type") == "refresh_token" {
				f.refreshes.Add(1)
				if f.refresh != nil {
					f.refresh(w, r)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"access_token": "refreshed-access", "refresh_token": "rotated-refresh", "token_type": "Bearer", "expires_in": 3600})
				return
			}
			f.exchanges.Add(1)
			if r.Form.Get("code_verifier") == "" || r.Form.Get("resource") != f.server.URL+"/mcp" {
				t.Errorf("exchange did not carry PKCE/resource: %v", r.Form)
			}
			response := map[string]any{"access_token": "initial-access", "refresh_token": "initial-refresh", "token_type": "Bearer", "expires_in": 3600}
			f.mu.Lock()
			if f.granted != nil {
				response["scope"] = *f.granted
			}
			f.mu.Unlock()
			json.NewEncoder(w).Encode(response)
		case "/register":
			f.registrations.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"client_id": "registered-client", "client_secret": "registered-secret", "token_endpoint_auth_method": "client_secret_post"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *oauthFixture) options() OAuthOptions {
	return OAuthOptions{SDKConfig: auth.AuthorizationCodeHandlerConfig{
		PreregisteredClient: &oauthex.ClientCredentials{ClientID: "client"},
		RedirectURL:         "http://127.0.0.1/callback", Client: f.server.Client(), RequestRefreshToken: true,
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			parsed, err := url.Parse(args.URL)
			if err != nil {
				return nil, err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			f.requests = append(f.requests, parsed.Query().Get("scope"))
			issuer := f.iss
			if issuer == "" {
				issuer = f.server.URL
			}
			if f.canceled {
				return nil, context.Canceled
			}
			return &auth.AuthorizationResult{Code: "authorization-code", State: parsed.Query().Get("state"), Iss: issuer}, nil
		},
	}}
}

func (f *oauthFixture) key() AuthKey {
	return AuthKey{Server: "server", URL: f.server.URL + "/mcp", Identity: "account", Issuer: f.server.URL, ClientID: "client"}
}

func TestOAuthInvalidAuthKeyRejectedBeforeStoreOrNetwork(t *testing.T) {
	f := newOAuthFixture(t)
	for _, mutate := range []func(*AuthKey, *OAuthOptions){
		func(key *AuthKey, _ *OAuthOptions) { key.Server = "server\xff" },
		func(key *AuthKey, _ *OAuthOptions) { key.Identity = "account\xff" },
		func(key *AuthKey, _ *OAuthOptions) { key.URL += "\xff" },
		func(key *AuthKey, _ *OAuthOptions) { key.Issuer += "\xff" },
		func(key *AuthKey, options *OAuthOptions) {
			key.ClientID = "client\xff"
			options.SDKConfig.PreregisteredClient.ClientID = key.ClientID
		},
		func(key *AuthKey, options *OAuthOptions) {
			key.ClientID = ""
			options.SDKConfig.PreregisteredClient.ClientID = "client\xff"
		},
		func(key *AuthKey, _ *OAuthOptions) { key.Server = strings.Repeat("x", 129) },
		func(key *AuthKey, _ *OAuthOptions) { key.Identity = strings.Repeat("x", 257) },
		func(key *AuthKey, options *OAuthOptions) {
			key.ClientID = ""
			key.Identity = ""
			options.SDKConfig.PreregisteredClient.ClientID = "client"
		},
	} {
		key, options := f.key(), f.options()
		mutate(&key, &options)
		if _, err := NewOAuth(t.Context(), key, options); !errors.Is(err, ErrOAuthConfiguration) {
			t.Fatalf("invalid auth binding was accepted: %v", err)
		}
	}
	if f.exchanges.Load() != 0 || f.refreshes.Load() != 0 || f.registrations.Load() != 0 {
		t.Fatal("invalid binding performed OAuth I/O")
	}
}

func TestOAuthCloneDCRMetadataCopiesAllSlices(t *testing.T) {
	metadata := &oauthex.ClientRegistrationMetadata{
		RedirectURIs:  []string{"http://127.0.0.1/callback"},
		GrantTypes:    []string{"authorization_code"},
		ResponseTypes: []string{"code"},
		Contacts:      []string{"host@example.com"},
		ClientName:    "host",
	}
	config := auth.AuthorizationCodeHandlerConfig{DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: metadata}}
	cloned := authCloneConfig(config).DynamicClientRegistrationConfig.Metadata
	if !reflect.DeepEqual(metadata, cloned) {
		t.Fatalf("metadata changed while copying: %+v", cloned)
	}
	metadata.RedirectURIs[0] = "changed"
	metadata.GrantTypes[0] = "changed"
	metadata.ResponseTypes[0] = "changed"
	metadata.Contacts[0] = "changed"
	if cloned.RedirectURIs[0] != "http://127.0.0.1/callback" || cloned.GrantTypes[0] != "authorization_code" || cloned.ResponseTypes[0] != "code" || cloned.Contacts[0] != "host@example.com" || cloned.ClientName != "host" {
		t.Fatalf("metadata retained mutable SDK slices: %+v", cloned)
	}
}

func TestOAuthIssuerValidationBeforeExchange(t *testing.T) {
	f := newOAuthFixture(t)
	f.iss = "https://wrong-issuer.example"
	o, err := NewOAuth(t.Context(), f.key(), f.options())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	if err := o.Authenticate(t.Context()); err == nil {
		t.Fatal("expected issuer mismatch rejection")
	}
	if f.exchanges.Load() != 0 || o.State().Authenticated || o.State().CredentialVersion != 0 {
		t.Fatalf("issuer mismatch exchanged or published credentials: exchanges=%d state=%+v", f.exchanges.Load(), o.State())
	}
}

func TestOAuthCredentialIsolationAndBinding(t *testing.T) {
	f := newOAuthFixture(t)
	store := NewMemoryCredentialStore()
	options := f.options()
	options.Store = store
	o, err := NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	if err := o.Authenticate(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*AuthKey, *OAuthOptions){
		func(k *AuthKey, _ *OAuthOptions) { k.Server = "second-server" },
		func(k *AuthKey, _ *OAuthOptions) { k.Identity = "second-account" },
		func(k *AuthKey, _ *OAuthOptions) { k.Issuer = "https://second-issuer.example" },
		func(k *AuthKey, opts *OAuthOptions) {
			k.ClientID = "second-client"
			opts.SDKConfig.PreregisteredClient = &oauthex.ClientCredentials{ClientID: "second-client"}
		},
	} {
		key := f.key()
		opts := f.options()
		opts.Store = store
		change(&key, &opts)
		other, err := NewOAuth(t.Context(), key, opts)
		if err != nil {
			t.Fatal(err)
		}
		if other.State().Authenticated || other.State().CredentialVersion != 0 {
			t.Fatalf("different binding reused credentials: %+v", key)
		}
		other.Close()
	}
	// A host store accidentally returning another binding is also rejected.
	snapshot, err := store.Load(t.Context(), o.Key())
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Credential.Binding.Identity = "other-account"
	if _, err := store.CompareAndSwap(t.Context(), o.Key(), snapshot.Version, snapshot.Credential); err != nil {
		t.Fatal(err)
	}
	if _, err := NewOAuth(t.Context(), f.key(), options); !errors.Is(err, ErrOAuthConfiguration) {
		t.Fatalf("wrong persisted binding accepted: %v", err)
	}
}

func TestOAuthRestoredScopesAndRefreshPersistence(t *testing.T) {
	f := newOAuthFixture(t)
	store, err := NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	options := f.options()
	options.Store = store
	var changes atomic.Int32
	options.OnChange = func(OAuthState) { changes.Add(1) }
	o, err := NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Authenticate(t.Context()); err != nil {
		t.Fatal(err)
	}
	o.Close()
	f.mu.Lock()
	f.scope = "write"
	f.mu.Unlock()
	// A fresh SDK handler has no grantedScopes; the adapter must restore them.
	o, err = NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	if err := o.Authenticate(t.Context()); err != nil {
		t.Fatal(err)
	}
	wantScopes := []string{"offline_access", "read", "write"}
	if !reflect.DeepEqual(o.State().GrantedScopes, wantScopes) {
		t.Fatalf("restored scopes lost: %+v", o.State())
	}
	f.mu.Lock()
	requested := strings.Fields(f.requests[len(f.requests)-1])
	f.mu.Unlock()
	if !reflect.DeepEqual(authUnionScopes(nil, requested), wantScopes) {
		t.Fatalf("step-up request lost previously granted scopes: %v", requested)
	}
	snapshot, err := store.Load(t.Context(), o.Key())
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Credential.Token.Expiry = time.Now().Add(-time.Hour)
	version, err := store.CompareAndSwap(t.Context(), o.Key(), snapshot.Version, snapshot.Credential)
	if err != nil {
		t.Fatal(err)
	}
	o.Close()
	o, err = NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	source, err := o.TokenSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	token, err := source.Token()
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Load(t.Context(), o.Key())
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "refreshed-access" || stored.Credential.Token.RefreshToken != "rotated-refresh" || stored.Credential.RefreshPending || stored.Version != version+2 || f.refreshes.Load() != 1 {
		t.Fatalf("refresh not persisted: token=%q version=%d refreshes=%d", token.AccessToken, stored.Version, f.refreshes.Load())
	}
	if changes.Load() != 3 || !reflect.DeepEqual(stored.Credential.Scopes, wantScopes) {
		t.Fatalf("refresh changed scopes or failed to notify: changes=%d scopes=%v", changes.Load(), stored.Credential.Scopes)
	}
}

func TestOAuthEmptyGrantedScopesAndCanceledLogin(t *testing.T) {
	f := newOAuthFixture(t)
	empty := ""
	f.granted = &empty
	o, err := NewOAuth(t.Context(), f.key(), f.options())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	if err := o.Authenticate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(o.State().GrantedScopes) != 0 {
		t.Fatalf("explicit empty scope treated as absent: %v", o.State().GrantedScopes)
	}
	f.mu.Lock()
	f.canceled = true
	f.mu.Unlock()
	state := o.State()
	if err := o.Authenticate(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled login: %v", err)
	}
	if !reflect.DeepEqual(o.State(), state) || f.exchanges.Load() != 1 {
		t.Fatalf("canceled login published state: before=%+v after=%+v", state, o.State())
	}
}

func TestOAuthCanceledRefreshPersistsClaimWithoutPublishingTokenOrRetry(t *testing.T) {
	f := newOAuthFixture(t)
	entered := make(chan struct{})
	finished := make(chan struct{})
	f.refresh = func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(finished)
	}
	store := NewMemoryCredentialStore()
	options := f.options()
	options.Store = store
	o, err := NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Authenticate(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load(t.Context(), o.Key())
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Credential.Token.Expiry = time.Now().Add(-time.Hour)
	version, err := store.CompareAndSwap(t.Context(), o.Key(), snapshot.Version, snapshot.Credential)
	if err != nil {
		t.Fatal(err)
	}
	o.Close()
	o, err = NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	source, err := o.TokenSource(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := source.Token(); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not enter")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("refresh cancellation not preserved: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not stop")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh server did not stop")
	}
	stored, err := store.Load(t.Context(), o.Key())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version != version+1 || stored.Credential == nil || !stored.Credential.RefreshPending || stored.Credential.Token.AccessToken != snapshot.Credential.Token.AccessToken || stored.Credential.Token.RefreshToken != snapshot.Credential.Token.RefreshToken || o.State().Authenticated {
		t.Fatalf("canceled refresh lost claim or published a token: version=%d state=%+v", stored.Version, o.State())
	}
	if _, err := o.TokenSource(t.Context()); !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("ambiguous refresh allowed a retry: %v", err)
	}
	if f.refreshes.Load() != 1 {
		t.Fatalf("refresh repeated: %d", f.refreshes.Load())
	}
	restored, err := NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restored.Close() })
	if _, err := restored.TokenSource(t.Context()); !errors.Is(err, ErrAuthRequired) || restored.State().Authenticated || f.refreshes.Load() != 1 {
		t.Fatalf("canceled refresh claim did not survive reconstruction: state=%+v err=%v", restored.State(), err)
	}
}

func TestOAuthRefreshFailureCannotAutoDetectRetryOrExposeSecrets(t *testing.T) {
	f := newOAuthFixture(t)
	f.refresh = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client", "error_description": "secret-response-body"})
	}
	store := NewMemoryCredentialStore()
	options := f.options()
	options.Store = store
	o, err := NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Authenticate(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load(t.Context(), o.Key())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Credential.Endpoint.AuthStyle == oauth2.AuthStyleAutoDetect {
		t.Fatal("refresh client-auth style was not pinned")
	}
	snapshot.Credential.Token.Expiry = time.Now().Add(-time.Hour)
	if _, err := store.CompareAndSwap(t.Context(), o.Key(), snapshot.Version, snapshot.Credential); err != nil {
		t.Fatal(err)
	}
	o.Close()
	o, err = NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	source, err := o.TokenSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Token(); !errors.Is(err, ErrAuthRequired) || strings.Contains(err.Error(), "secret-response-body") {
		t.Fatalf("refresh failure error classification/redaction: %v", err)
	} else {
		var cause *oauth2.RetrieveError
		if !errors.As(err, &cause) || !strings.Contains(string(cause.Body), "secret-response-body") {
			t.Fatal("refresh failure discarded the private token endpoint cause")
		}
	}
	if f.refreshes.Load() != 1 || o.State().Authenticated {
		t.Fatalf("ambiguous refresh retried or remained usable: refreshes=%d state=%+v", f.refreshes.Load(), o.State())
	}
}

func TestOAuthTokenOnlyHandlerCannotReplayMCPPost(t *testing.T) {
	f := newOAuthFixture(t)
	o, err := NewOAuth(t.Context(), f.key(), f.options())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	if err := o.Authenticate(t.Context()); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test", Version: "1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "write", Description: "write"}, func(context.Context, *sdkmcp.CallToolRequest, map[string]any) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{}, nil, nil
	})
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, &sdkmcp.StreamableHTTPOptions{Stateless: true})
	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var message struct {
				Method string `json:"method"`
			}
			json.Unmarshal(body, &message)
			if message.Method == "tools/call" {
				calls.Add(1)
				w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(mcpServer.Close)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &sdkmcp.StreamableClientTransport{Endpoint: mcpServer.URL, OAuthHandler: o.TokenOnlyHandler()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	if _, err := session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "write", Arguments: map[string]any{}}); err == nil {
		t.Fatal("expected authentication failure")
	}
	if calls.Load() != 1 || f.exchanges.Load() != 1 {
		t.Fatalf("tool POST replayed or login triggered: sends=%d exchanges=%d", calls.Load(), f.exchanges.Load())
	}
}

func TestOAuthMetadataOverrideAndDynamicClientRestore(t *testing.T) {
	f := newOAuthFixture(t)
	store := NewMemoryCredentialStore()
	options := f.options()
	options.Store = store
	options.MetadataURL = f.server.URL + "/custom-authorization-metadata"
	options.SDKConfig.PreregisteredClient = nil
	options.SDKConfig.DynamicClientRegistrationConfig = &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{RedirectURIs: []string{"http://127.0.0.1/callback"}, TokenEndpointAuthMethod: "client_secret_post"}}
	key := f.key()
	key.ClientID = ""
	o, err := NewOAuth(t.Context(), key, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Authenticate(t.Context()); err != nil {
		t.Fatal(err)
	}
	o.Close()
	o, err = NewOAuth(t.Context(), key, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	if err := o.Authenticate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.registrations.Load() != 1 || f.exchanges.Load() != 2 || !o.State().Authenticated {
		t.Fatalf("restored DCR client registered again: registrations=%d exchanges=%d state=%+v", f.registrations.Load(), f.exchanges.Load(), o.State())
	}
}
