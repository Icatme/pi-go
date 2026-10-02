package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

func expireStoredOAuth(t *testing.T, o *OAuth, store CredentialStore) uint64 {
	t.Helper()
	snapshot, err := store.Load(t.Context(), o.Key())
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Credential.Token.Expiry = time.Now().Add(-time.Hour)
	version, err := store.CompareAndSwap(t.Context(), o.Key(), snapshot.Version, snapshot.Credential)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	return version
}

func TestOAuthRefreshClaimSurvivesRecreationAndRequiresExplicitLogin(t *testing.T) {
	f := newOAuthFixture(t)
	f.refresh = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }
	path := filepath.Join(t.TempDir(), "credentials.json")
	store, err := NewFileCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	options := f.options()
	options.Store = store
	o, err := NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Authenticate(t.Context()); err != nil {
		t.Fatal(err)
	}
	version := expireStoredOAuth(t, o, store)
	o, err = NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	source, err := o.TokenSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Token(); !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("ambiguous refresh: %v", err)
	}
	o.Close()
	// Reopen the actual durable file through an independent store object.
	options.Store, err = NewFileCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restored.Close() })
	if restored.State().Authenticated || restored.State().CredentialVersion != version+1 {
		t.Fatalf("pending refresh restored as authenticated: %+v", restored.State())
	}
	if _, err := restored.TokenSource(t.Context()); !errors.Is(err, ErrAuthRequired) || f.refreshes.Load() != 1 {
		t.Fatalf("recreated OAuth retransmitted an ambiguous refresh: sends=%d err=%v", f.refreshes.Load(), err)
	}
	if err := restored.Authenticate(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := options.Store.Load(t.Context(), restored.Key())
	if err != nil || snapshot.Credential == nil || snapshot.Credential.RefreshPending || !restored.State().Authenticated || f.exchanges.Load() != 2 || f.refreshes.Load() != 1 {
		t.Fatalf("explicit login did not replace pending credentials: state=%+v err=%v exchanges=%d refreshes=%d", restored.State(), err, f.exchanges.Load(), f.refreshes.Load())
	}
}

func TestOAuthConcurrentRefreshClaimsBeforePhysicalDispatch(t *testing.T) {
	f := newOAuthFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	f.refresh = func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "rotated-access", "refresh_token": "rotated-refresh", "token_type": "Bearer", "expires_in": 3600})
	}
	options := f.options()
	options.Store = NewMemoryCredentialStore()
	o, err := NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Authenticate(t.Context()); err != nil {
		t.Fatal(err)
	}
	version := expireStoredOAuth(t, o, options.Store)
	winner, err := NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { winner.Close() })
	loser, err := NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { loser.Close() })
	winnerSource, err := winner.TokenSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := winnerSource.Token(); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("winner did not reach physical refresh")
	}
	pending, err := options.Store.Load(t.Context(), winner.Key())
	if err != nil || pending.Version != version+1 || !pending.Credential.RefreshPending {
		t.Fatalf("remote refresh started without durable claim: version=%d err=%v", pending.Version, err)
	}
	loserSource, err := loser.TokenSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loserSource.Token(); !errors.Is(err, ErrOAuthCredentialConflict) || f.refreshes.Load() != 1 {
		t.Fatalf("competing refresh sent the same token before CAS: sends=%d err=%v", f.refreshes.Load(), err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("winner did not publish refresh")
	}
	published, err := options.Store.Load(t.Context(), winner.Key())
	if err != nil || published.Version != version+2 || published.Credential.RefreshPending || published.Credential.Token.RefreshToken != "rotated-refresh" {
		t.Fatalf("winner did not complete its claimed version: version=%d err=%v", published.Version, err)
	}
}

func TestOAuthRestoredDCRPreservesRegisteredAuthStyle(t *testing.T) {
	for _, test := range []struct {
		method string
		style  oauth2.AuthStyle
	}{{"client_secret_basic", oauth2.AuthStyleInHeader}, {"client_secret_post", oauth2.AuthStyleInParams}} {
		t.Run(test.method, func(t *testing.T) {
			f := newOAuthFixture(t)
			previous := f.server.Config.Handler
			f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/.well-known/oauth-authorization-server":
					json.NewEncoder(w).Encode(oauthex.AuthServerMeta{
						Issuer: f.server.URL, AuthorizationEndpoint: f.server.URL + "/authorize", TokenEndpoint: f.server.URL + "/token", RegistrationEndpoint: f.server.URL + "/register",
						CodeChallengeMethodsSupported: []string{"S256"}, AuthorizationResponseIssParameterSupported: true,
						TokenEndpointAuthMethodsSupported: []string{"client_secret_post", "client_secret_basic"},
					})
				case "/register":
					f.registrations.Add(1)
					json.NewEncoder(w).Encode(map[string]any{"client_id": "registered-client", "client_secret": "registered-secret", "token_endpoint_auth_method": test.method})
				case "/token":
					if err := r.ParseForm(); err != nil {
						t.Error(err)
						return
					}
					id, secret, basic := r.BasicAuth()
					valid := basic && id == "registered-client" && secret == "registered-secret" && r.Form.Get("client_secret") == ""
					if test.style == oauth2.AuthStyleInParams {
						valid = !basic && r.Form.Get("client_id") == "registered-client" && r.Form.Get("client_secret") == "registered-secret"
					}
					if !valid {
						w.WriteHeader(http.StatusUnauthorized)
						json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client"})
						return
					}
					previous.ServeHTTP(w, r)
				default:
					previous.ServeHTTP(w, r)
				}
			})
			options := f.options()
			options.Store = NewMemoryCredentialStore()
			options.SDKConfig.PreregisteredClient = nil
			options.SDKConfig.DynamicClientRegistrationConfig = &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{RedirectURIs: []string{"http://127.0.0.1/callback"}, TokenEndpointAuthMethod: test.method}}
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
			restored, err := NewOAuth(t.Context(), key, options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { restored.Close() })
			if err := restored.Authenticate(t.Context()); err != nil {
				t.Fatalf("restored client auth changed: method=%s registrations=%d err=%v", test.method, f.registrations.Load(), err)
			}
			snapshot, err := options.Store.Load(t.Context(), restored.Key())
			if err != nil || snapshot.Credential.Endpoint.AuthStyle != test.style || f.registrations.Load() != 1 || f.exchanges.Load() != 2 {
				t.Fatalf("registered client identity/style was not preserved: state=%+v registrations=%d exchanges=%d err=%v", restored.State(), f.registrations.Load(), f.exchanges.Load(), err)
			}
		})
	}
}

func TestOAuthRefreshPreservesSDKValidatedResourceIndicator(t *testing.T) {
	for _, rootMetadata := range []bool{false, true} {
		name := "endpoint"
		if rootMetadata {
			name = "root_metadata"
		}
		t.Run(name, func(t *testing.T) {
			f := newOAuthFixture(t)
			resource := f.key().URL
			if rootMetadata {
				resource = f.server.URL
			}
			resources := make(chan string, 1)
			previous := f.server.Config.Handler
			f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case rootMetadata && r.URL.Path == "/resource-metadata":
					w.WriteHeader(http.StatusNotFound)
				case rootMetadata && r.URL.Path == "/.well-known/oauth-protected-resource":
					json.NewEncoder(w).Encode(oauthex.ProtectedResourceMetadata{Resource: resource, AuthorizationServers: []string{f.server.URL}, ScopesSupported: []string{"read"}})
				case r.URL.Path == "/token":
					if err := r.ParseForm(); err != nil {
						t.Error(err)
						return
					}
					if r.Form.Get("resource") != resource {
						w.WriteHeader(http.StatusBadRequest)
						json.NewEncoder(w).Encode(map[string]any{"error": "invalid_target"})
						return
					}
					if r.Form.Get("grant_type") == "refresh_token" {
						f.refreshes.Add(1)
						resources <- r.Form.Get("resource")
					} else {
						f.exchanges.Add(1)
					}
					json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "refresh_token": "refresh", "token_type": "Bearer", "expires_in": 3600})
				default:
					previous.ServeHTTP(w, r)
				}
			})
			options := f.options()
			options.Store = NewMemoryCredentialStore()
			o, err := NewOAuth(t.Context(), f.key(), options)
			if err != nil {
				t.Fatal(err)
			}
			if err := o.Authenticate(t.Context()); err != nil {
				t.Fatal(err)
			}
			expireStoredOAuth(t, o, options.Store)
			restored, err := NewOAuth(t.Context(), f.key(), options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { restored.Close() })
			source, err := restored.TokenSource(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.Token(); err != nil {
				t.Fatal(err)
			}
			if sent := <-resources; sent != resource || f.refreshes.Load() != 1 {
				t.Fatalf("refresh resource differs from SDK code exchange: sent=%q expected=%q", sent, resource)
			}
		})
	}
}

func TestOAuthPrivateFlowErrorsRetainCause(t *testing.T) {
	f := newOAuthFixture(t)
	private := errors.New("private callback diagnostic")
	options := f.options()
	options.SDKConfig.AuthorizationCodeFetcher = func(context.Context, *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) { return nil, private }
	o, err := NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	if err := o.Authenticate(t.Context()); !errors.Is(err, private) || strings.Contains(err.Error(), "private callback diagnostic") {
		t.Fatalf("flow error lost cause or exposed it: %v", err)
	}
}

func TestOAuthStoredResourceRequiredAndHostCASRecovery(t *testing.T) {
	f := newOAuthFixture(t)
	options := f.options()
	options.Store = NewMemoryCredentialStore()
	o, err := NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Authenticate(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := options.Store.Load(t.Context(), o.Key())
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Credential.Resource = ""
	version, err := options.Store.CompareAndSwap(t.Context(), o.Key(), snapshot.Version, snapshot.Credential)
	if err != nil {
		t.Fatal(err)
	}
	o.Close()
	if restored, err := NewOAuth(t.Context(), f.key(), options); !errors.Is(err, ErrOAuthConfiguration) || restored != nil {
		t.Fatalf("unbound legacy resource was silently accepted: err=%v", err)
	}
	if _, err := options.Store.CompareAndSwap(t.Context(), o.Key(), version, nil); err != nil {
		t.Fatal(err)
	}
	restored, err := NewOAuth(t.Context(), f.key(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restored.Close() })
	if err := restored.Authenticate(t.Context()); err != nil || !restored.State().Authenticated || f.exchanges.Load() != 2 {
		t.Fatalf("host CAS tombstone did not enable explicit recovery: %+v %v", restored.State(), err)
	}
}

func TestOAuthCommittedClearErrorCannotReuseLocalToken(t *testing.T) {
	f := newOAuthFixture(t)
	store, _ := newOAuthMutationGateStore(t)
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
	before := o.State()
	private := errors.New("private cleanup failure")
	store.failure, store.failureAfterPublication = private, true
	if err := o.Clear(t.Context()); !errors.Is(err, private) || strings.Contains(err.Error(), "private cleanup failure") {
		t.Fatalf("clear error lost cause or exposed it: %v", err)
	}
	if o.State().Authenticated || o.State().CredentialVersion != before.CredentialVersion+1 {
		t.Fatalf("durable tombstone did not clear local credential: %+v", o.State())
	}
	if _, err := o.TokenSource(t.Context()); !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("clear cleanup error allowed cached token reuse: %v", err)
	}
}
