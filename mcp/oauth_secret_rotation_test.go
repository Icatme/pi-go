package mcp

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

func TestOAuthPreregisteredSecretRotation(t *testing.T) {
	for _, grant := range []string{"authorization_code", "refresh_token"} {
		t.Run(grant, func(t *testing.T) {
			f := newOAuthFixture(t)
			var rotated atomic.Bool
			previous := f.server.Config.Handler
			f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					_ = r.ParseForm()
					want := "old-client-secret"
					if rotated.Load() {
						want = "new-client-secret"
					}
					if r.Form.Get("client_id") != "client" || r.Form.Get("client_secret") != want {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusUnauthorized)
						_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
						return
					}
				}
				previous.ServeHTTP(w, r)
			})
			options := f.options()
			options.Store = NewMemoryCredentialStore()
			options.SDKConfig.PreregisteredClient.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: "old-client-secret"}
			o, err := NewOAuth(t.Context(), f.key(), options)
			if err != nil {
				t.Fatal(err)
			}
			if err := o.Authenticate(t.Context()); err != nil {
				t.Fatal(err)
			}
			o.Close()
			rotated.Store(true)
			options.SDKConfig.PreregisteredClient.ClientSecretAuth.ClientSecret = "new-client-secret"
			restored, err := NewOAuth(t.Context(), f.key(), options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { restored.Close() })
			stored, err := options.Store.Load(t.Context(), restored.Key())
			if err != nil || stored.Credential == nil || stored.Credential.ClientSecret != "old-client-secret" {
				t.Fatalf("construction mutated the persisted credential: %v", err)
			}
			if grant == "authorization_code" {
				err = restored.Authenticate(t.Context())
			} else {
				restored.mu.Lock()
				restored.snapshot.Credential.Token.Expiry = time.Now().Add(-time.Hour)
				restored.mu.Unlock()
				_, err = restored.token(t.Context())
			}
			if err != nil {
				t.Fatalf("%s ignored newly configured client secret: %v", grant, err)
			}
			stored, err = options.Store.Load(t.Context(), restored.Key())
			if err != nil || stored.Credential == nil || stored.Credential.ClientID != "client" || stored.Credential.ClientSecret != "new-client-secret" {
				t.Fatalf("replacement client credential was not persisted: %v", err)
			}
		})
	}
}
