package mcp

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

func TestOAuthCIMDFallbackPersistsResolvedClient(t *testing.T) {
	for _, method := range []string{"preregistered", "dcr"} {
		t.Run(method, func(t *testing.T) {
			f := newOAuthFixture(t)
			options := f.options()
			options.SDKConfig.ClientIDMetadataDocumentConfig = &auth.ClientIDMetadataDocumentConfig{URL: "https://client.example/client.json"}
			wantClient, wantRegistrations := "client", int32(0)
			if method == "dcr" {
				options.SDKConfig.PreregisteredClient = nil
				options.SDKConfig.DynamicClientRegistrationConfig = &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{RedirectURIs: []string{"http://127.0.0.1/callback"}, TokenEndpointAuthMethod: "client_secret_post"}}
				wantClient, wantRegistrations = "registered-client", 1
			}
			// Use the pinned SDK directly to establish the actual registration
			// choice when the AS does not advertise CIMD support.
			control := authCloneConfig(options.SDKConfig)
			var resolvedClient string
			control.NewTokenSource = func(_ context.Context, cfg *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
				resolvedClient = cfg.ClientID
				return oauth2.StaticTokenSource(token), nil
			}
			handler, err := auth.NewAuthorizationCodeHandler(&control)
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.server.URL+"/mcp", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := f.server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if err := handler.Authorize(t.Context(), req, resp); err != nil || resolvedClient != wantClient {
				t.Fatalf("direct SDK fallback: client=%q err=%v", resolvedClient, err)
			}
			f.exchanges.Store(0)
			f.registrations.Store(0)
			path := filepath.Join(t.TempDir(), "credentials.json")
			options.Store, err = NewFileCredentialStore(path)
			if err != nil {
				t.Fatal(err)
			}
			key := f.key()
			key.ClientID = ""
			o, err := NewOAuth(t.Context(), key, options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { o.Close() })
			if err := o.Authenticate(t.Context()); err != nil {
				t.Fatalf("SDK-valid fallback rejected after %d code exchanges: %v", f.exchanges.Load(), err)
			}
			snapshot, err := options.Store.Load(t.Context(), o.Key())
			if err != nil || snapshot.Credential == nil || snapshot.Credential.ClientID != wantClient || snapshot.Credential.Endpoint.AuthStyle != oauth2.AuthStyleInParams || snapshot.Credential.Resource != key.URL {
				t.Fatalf("resolved identity/style/resource not persisted: snapshot=%+v err=%v", snapshot, err)
			}
			o.Close()
			// AS capability changes must not silently switch a persisted client
			// identity or re-register a DCR client on the next explicit login.
			f.cimd.Store(true)
			options.Store, err = NewFileCredentialStore(path)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := NewOAuth(t.Context(), key, options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { restored.Close() })
			if !restored.State().Authenticated {
				t.Fatal("fresh file-store handle lost the fallback credential")
			}
			if err := restored.Authenticate(t.Context()); err != nil {
				t.Fatalf("persisted resolved identity was not reused: %v", err)
			}
			snapshot, err = options.Store.Load(t.Context(), restored.Key())
			if err != nil || snapshot.Credential.ClientID != wantClient || snapshot.Credential.Endpoint.AuthStyle != oauth2.AuthStyleInParams || f.exchanges.Load() != 2 || f.registrations.Load() != wantRegistrations {
				t.Fatalf("restored identity/style changed: exchanges=%d registrations=%d err=%v", f.exchanges.Load(), f.registrations.Load(), err)
			}
		})
	}
}

func TestOAuthMixedRegistrationBindingsIsolateConfiguration(t *testing.T) {
	f := newOAuthFixture(t)
	options := f.options()
	options.Store = NewMemoryCredentialStore()
	options.SDKConfig.ClientIDMetadataDocumentConfig = &auth.ClientIDMetadataDocumentConfig{URL: "https://client.example/client.json"}
	options.SDKConfig.DynamicClientRegistrationConfig = &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{RedirectURIs: []string{"http://127.0.0.1/callback"}, ClientName: "host"}}
	key := f.key()
	key.ClientID = ""
	o, err := NewOAuth(t.Context(), key, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	if err := o.Authenticate(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*AuthKey, *OAuthOptions)
	}{
		{"cimd", func(_ *AuthKey, opts *OAuthOptions) {
			opts.SDKConfig.ClientIDMetadataDocumentConfig.URL = "https://other.example/client.json"
		}},
		{"preregistered", func(_ *AuthKey, opts *OAuthOptions) { opts.SDKConfig.PreregisteredClient.ClientID = "other-client" }},
		{"registration", func(_ *AuthKey, opts *OAuthOptions) {
			opts.SDKConfig.DynamicClientRegistrationConfig.Metadata.ClientName = "other-host"
		}},
		{"redirect", func(_ *AuthKey, opts *OAuthOptions) {
			opts.SDKConfig.RedirectURL = "http://127.0.0.1/other"
			opts.SDKConfig.DynamicClientRegistrationConfig.Metadata.RedirectURIs = []string{opts.SDKConfig.RedirectURL}
		}},
		{"metadata_endpoint", func(_ *AuthKey, opts *OAuthOptions) {
			opts.MetadataURL = f.server.URL + "/custom-authorization-metadata"
		}},
		{"identity", func(key *AuthKey, _ *OAuthOptions) { key.Identity = "other-account" }},
		{"issuer", func(key *AuthKey, _ *OAuthOptions) { key.Issuer = "https://other.example" }},
		{"resource", func(key *AuthKey, _ *OAuthOptions) { key.URL = f.server.URL + "/other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			otherKey, otherOptions := key, *cloneOAuthOptions(&options)
			test.change(&otherKey, &otherOptions)
			other, err := NewOAuth(t.Context(), otherKey, otherOptions)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { other.Close() })
			if other.Key() == o.Key() || other.State().Authenticated || other.State().CredentialVersion != 0 {
				t.Fatal("different configured binding restored the original credential")
			}
		})
	}
	if f.exchanges.Load() != 1 || f.registrations.Load() != 0 {
		t.Fatal("constructing different bindings performed authorization I/O")
	}
}

func TestOAuthMixedRegistrationRejectsUnconfiguredStoredClient(t *testing.T) {
	f := newOAuthFixture(t)
	options := f.options()
	options.Store = NewMemoryCredentialStore()
	options.SDKConfig.ClientIDMetadataDocumentConfig = &auth.ClientIDMetadataDocumentConfig{URL: "https://client.example/client.json"}
	key := f.key()
	key.ClientID = ""
	o, err := NewOAuth(t.Context(), key, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { o.Close() })
	if err := o.Authenticate(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := options.Store.Load(t.Context(), o.Key())
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Credential.ClientID = "unconfigured-client"
	if _, err := options.Store.CompareAndSwap(t.Context(), o.Key(), snapshot.Version, snapshot.Credential); err != nil {
		t.Fatal(err)
	}
	if other, err := NewOAuth(t.Context(), key, options); !errors.Is(err, ErrOAuthConfiguration) || other != nil {
		t.Fatalf("unconfigured fallback identity was accepted: %v", err)
	}
	if f.exchanges.Load() != 1 {
		t.Fatal("invalid persisted binding performed code exchange")
	}
}
