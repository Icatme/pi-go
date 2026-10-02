package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

func oauthFixtureAuthorizationEndpoint(f *oauthFixture, endpoint string) {
	original := f.server.Config.Handler
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-authorization-server" {
			original.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(oauthex.AuthServerMeta{
			Issuer: f.server.URL, AuthorizationEndpoint: endpoint, TokenEndpoint: f.server.URL + "/token", RegistrationEndpoint: f.server.URL + "/register",
			CodeChallengeMethodsSupported: []string{"S256"}, ScopesSupported: []string{"read", "write", "offline_access"},
			AuthorizationResponseIssParameterSupported: true, TokenEndpointAuthMethodsSupported: []string{"client_secret_post"},
		})
	})
}

func TestOAuthAuthorizationEndpointFixedQuery(t *testing.T) {
	for _, test := range []struct {
		name, suffix string
	}{
		{"plain", ""},
		{"tenant", "?tenant=alpha"},
		{"repeated_fixed", "?tenant=alpha&tenant=beta&tenant=alpha"},
		{"encoded_and_empty", "?tenant=alpha+team&hint=%2Fone%3Ftwo%3D3&flag"},
		{"empty_query", "?"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newOAuthFixture(t)
			endpoint := f.server.URL + "/authorize" + test.suffix
			oauthFixtureAuthorizationEndpoint(f, endpoint)
			expected, err := url.Parse(endpoint)
			if err != nil {
				t.Fatal(err)
			}
			options := f.options()
			options.Store = NewMemoryCredentialStore()
			fetch := options.SDKConfig.AuthorizationCodeFetcher
			var callbacks atomic.Int32
			options.SDKConfig.AuthorizationCodeFetcher = func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
				generated, err := url.Parse(args.URL)
				if err != nil {
					return nil, err
				}
				query, err := url.ParseQuery(generated.RawQuery)
				if err != nil {
					return nil, err
				}
				for name, values := range expected.Query() {
					if !reflect.DeepEqual(query[name], values) {
						t.Errorf("fixed parameter %q was not preserved: got %q, want %q", name, query[name], values)
					}
				}
				if query.Get("state") == "" || query.Get("code_challenge") == "" || query.Get("code_challenge_method") != "S256" || query.Get("resource") != f.key().URL {
					t.Error("SDK state, PKCE or resource parameter was lost")
				}
				callbacks.Add(1)
				return fetch(ctx, args)
			}

			// Compare with the pinned SDK using the same metadata, challenge,
			// and callback. AuthCodeURL retains fixed endpoint parameters.
			control := authCloneConfig(options.SDKConfig)
			control.NewTokenSource = func(_ context.Context, _ *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
				return oauth2.StaticTokenSource(token), nil
			}
			handler, err := auth.NewAuthorizationCodeHandler(&control)
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.key().URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := control.Client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if err := handler.Authorize(t.Context(), req, resp); err != nil {
				t.Fatalf("SDK control: %v", err)
			}
			if callbacks.Load() != 1 || f.exchanges.Load() != 1 {
				t.Fatalf("SDK control callbacks=%d exchanges=%d", callbacks.Load(), f.exchanges.Load())
			}

			o, err := NewOAuth(t.Context(), f.key(), options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { o.Close() })
			if err := o.Authenticate(t.Context()); err != nil {
				t.Fatalf("SDK-valid endpoint rejected: %v; callbacks=%d exchanges=%d", err, callbacks.Load(), f.exchanges.Load())
			}
			snapshot, err := options.Store.Load(t.Context(), o.Key())
			if err != nil || snapshot.Credential == nil || snapshot.Credential.Endpoint.AuthURL != endpoint || !o.State().Authenticated || callbacks.Load() != 2 || f.exchanges.Load() != 2 {
				t.Fatalf("authorization was not persisted with the original endpoint: callbacks=%d exchanges=%d err=%v", callbacks.Load(), f.exchanges.Load(), err)
			}
		})
	}
}

func TestOAuthAuthorizationEndpointRejectsOAuthQueryCollisions(t *testing.T) {
	for _, name := range []string{"client_id", "response_type", "redirect_uri", "scope", "state", "code_challenge", "code_challenge_method", "resource"} {
		t.Run(name, func(t *testing.T) {
			for _, repeated := range []bool{false, true} {
				kind := "single_fixed"
				if repeated {
					kind = "repeated_fixed"
				}
				t.Run(kind, func(t *testing.T) {
					f := newOAuthFixture(t)
					values := map[string]string{
						"client_id": "client", "response_type": "code", "redirect_uri": "http://127.0.0.1/callback", "scope": "read offline_access",
						"state": "fixed", "code_challenge": "fixed", "code_challenge_method": "S256", "resource": f.key().URL,
					}
					fixed := url.Values{"tenant": {"alpha"}, name: {values[name]}}
					if repeated {
						fixed.Add(name, values[name])
					}
					oauthFixtureAuthorizationEndpoint(f, f.server.URL+"/authorize?"+fixed.Encode())
					options := f.options()
					options.Store = NewMemoryCredentialStore()
					var callbacks atomic.Int32
					options.SDKConfig.AuthorizationCodeFetcher = func(context.Context, *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
						callbacks.Add(1)
						return nil, errors.New("ambiguous authorization must not reach the host")
					}
					o, err := NewOAuth(t.Context(), f.key(), options)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { o.Close() })
					if err := o.Authenticate(t.Context()); !errors.Is(err, ErrOAuthConfiguration) {
						t.Fatalf("ambiguous metadata query was not rejected: %v", err)
					}
					snapshot, err := options.Store.Load(t.Context(), o.Key())
					if err != nil || snapshot.Version != 0 || snapshot.Credential != nil || callbacks.Load() != 0 || f.exchanges.Load() != 0 {
						t.Fatalf("ambiguous request invoked the host, exchanged a code or published credentials: callbacks=%d exchanges=%d err=%v", callbacks.Load(), f.exchanges.Load(), err)
					}
				})
			}
		})
	}
}

func TestOAuthAuthorizationURLQueryBinding(t *testing.T) {
	const endpoint = "https://as.example/authorize?tenant=alpha&tenant=beta&flag="
	transport := &authBoundTransport{issuer: "https://as.example", metadata: &oauthex.AuthServerMeta{AuthorizationEndpoint: endpoint}}
	cfg := oauth2.Config{ClientID: "client", RedirectURL: "http://127.0.0.1/callback", Scopes: []string{"read"}, Endpoint: oauth2.Endpoint{AuthURL: endpoint}}
	generated := cfg.AuthCodeURL("sdk-state", oauth2.S256ChallengeOption("sdk-verifier"), oauth2.SetAuthURLParam("resource", "https://resource.example/mcp"))
	for _, test := range []struct {
		name   string
		change func(*url.URL, url.Values)
		accept bool
	}{
		{"unchanged", func(*url.URL, url.Values) {}, true},
		{"changed_endpoint", func(u *url.URL, _ url.Values) { u.Host = "other.example" }, false},
		{"changed_path", func(u *url.URL, _ url.Values) { u.Path = "/other" }, false},
		{"fragment", func(u *url.URL, _ url.Values) { u.Fragment = "other" }, false},
		{"dropped_fixed", func(_ *url.URL, q url.Values) { q.Del("tenant") }, false},
		{"changed_fixed", func(_ *url.URL, q url.Values) { q["tenant"][1] = "other" }, false},
		{"extra_fixed_duplicate", func(_ *url.URL, q url.Values) { q.Add("tenant", "alpha") }, false},
		{"reordered_fixed_duplicate", func(_ *url.URL, q url.Values) { q["tenant"] = []string{"beta", "alpha"} }, false},
		{"dropped_empty_fixed", func(_ *url.URL, q url.Values) { q.Del("flag") }, false},
		{"unexpected_parameter", func(_ *url.URL, q url.Values) { q.Set("other", "value") }, false},
		{"same_state_duplicate", func(_ *url.URL, q url.Values) { q.Add("state", q.Get("state")) }, false},
		{"different_state_duplicate", func(_ *url.URL, q url.Values) { q.Add("state", "other") }, false},
		{"optional_scope", func(_ *url.URL, q url.Values) { q.Del("scope") }, true},
		{"missing_state", func(_ *url.URL, q url.Values) { q.Del("state") }, false},
		{"missing_redirect", func(_ *url.URL, q url.Values) { q.Del("redirect_uri") }, false},
		{"missing_resource", func(_ *url.URL, q url.Values) { q.Del("resource") }, false},
		{"missing_challenge", func(_ *url.URL, q url.Values) { q.Del("code_challenge") }, false},
		{"empty_client", func(_ *url.URL, q url.Values) { q.Set("client_id", "") }, false},
		{"wrong_response_type", func(_ *url.URL, q url.Values) { q.Set("response_type", "token") }, false},
		{"wrong_challenge_method", func(_ *url.URL, q url.Values) { q.Set("code_challenge_method", "plain") }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			u, err := url.Parse(generated)
			if err != nil {
				t.Fatal(err)
			}
			query := u.Query()
			test.change(u, query)
			u.RawQuery = query.Encode()
			if got := transport.matchesAuthorization(u.String()); got != test.accept {
				t.Fatalf("authorization URL accepted=%v, want %v", got, test.accept)
			}
		})
	}
	for _, malformed := range []string{generated + "&bad=%GG", generated + "&bad=a;b"} {
		if transport.matchesAuthorization(malformed) {
			t.Error("malformed query was accepted")
		}
	}
	encodedCollision := *transport
	encodedCollision.metadata = &oauthex.AuthServerMeta{AuthorizationEndpoint: endpoint + "&st%61te=sdk-state"}
	encodedConfig := cfg
	encodedConfig.Endpoint.AuthURL = encodedCollision.metadata.AuthorizationEndpoint
	if encodedCollision.matchesAuthorization(encodedConfig.AuthCodeURL("sdk-state", oauth2.S256ChallengeOption("sdk-verifier"), oauth2.SetAuthURLParam("resource", "https://resource.example/mcp"))) {
		t.Error("encoded fixed OAuth parameter name was accepted")
	}
}
