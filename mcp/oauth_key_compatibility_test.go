package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// Reproduce the old serialized binding, without invoking the new normalizer.
func legacyMixedClientKey(t *testing.T, key AuthKey, options OAuthOptions) AuthKey {
	t.Helper()
	cfg := options.SDKConfig
	binding := struct {
		CIMD          *auth.ClientIDMetadataDocumentConfig  `json:"cimd,omitempty"`
		Preregistered *oauthex.ClientCredentials            `json:"preregistered,omitempty"`
		Dynamic       *auth.DynamicClientRegistrationConfig `json:"dynamic,omitempty"`
		RedirectURL   string                                `json:"redirectUrl"`
		MetadataURL   string                                `json:"metadataUrl"`
		Resource      string                                `json:"resource"`
		Issuer        string                                `json:"issuer"`
	}{cfg.ClientIDMetadataDocumentConfig, cfg.PreregisteredClient, cfg.DynamicClientRegistrationConfig, cfg.RedirectURL, options.MetadataURL, key.URL, key.Issuer}
	data, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	key.ClientID = "registration:" + hex.EncodeToString(digest[:])
	return key
}

func TestOAuthMixedRegistrationKeyCompatibility(t *testing.T) {
	f := newOAuthFixture(t)
	options := f.options()
	options.SDKConfig.ClientIDMetadataDocumentConfig = &auth.ClientIDMetadataDocumentConfig{URL: "https://client.example/client.json"}
	key := f.key()
	key.ClientID = ""
	public, err := authNormalizeKey(key, options.SDKConfig, options.MetadataURL)
	if err != nil || public != legacyMixedClientKey(t, key, options) {
		t.Fatal("mixed public-client key changed")
	}
	options.SDKConfig.PreregisteredClient.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: "old-test-secret"}
	options.Store = NewMemoryCredentialStore()
	o, err := NewOAuth(t.Context(), key, options)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	if o.Key() == public {
		t.Fatal("public and confidential client methods share a binding")
	}
	if err := o.Authenticate(t.Context()); err != nil {
		t.Fatal(err)
	}
	saved, err := options.Store.Load(t.Context(), o.Key())
	if err != nil {
		t.Fatal(err)
	}
	legacy := legacyMixedClientKey(t, key, options)
	if legacy == o.Key() {
		t.Fatal("legacy secret-bearing key was retained")
	}
	// Prepare a store containing only an old-format credential. NewOAuth must
	// not copy its refresh token across keys or erase the old version/claim.
	options.Store = NewMemoryCredentialStore()
	saved.Credential.Binding = legacy
	saved.Credential.RefreshPending = true
	if _, err := options.Store.CompareAndSwap(t.Context(), legacy, 0, saved.Credential); err != nil {
		t.Fatal(err)
	}
	before, _ := options.Store.Load(t.Context(), legacy)
	for _, secret := range []string{"old-test-secret", "rotated-test-secret"} {
		options.SDKConfig.PreregisteredClient.ClientSecretAuth.ClientSecret = secret
		restored, err := NewOAuth(t.Context(), key, options)
		if err != nil {
			t.Fatal(err)
		}
		if restored.Key() != o.Key() || restored.State().Authenticated || restored.State().CredentialVersion != 0 {
			t.Fatal("old secret-bearing credential was implicitly migrated")
		}
		if _, err := restored.TokenSource(t.Context()); !errors.Is(err, ErrAuthRequired) {
			t.Fatalf("old-format credential must require explicit authentication: %v", err)
		}
		restored.Close()
	}
	after, err := options.Store.Load(t.Context(), legacy)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("loading the new binding modified the legacy credential")
	}
	if f.refreshes.Load() != 0 || f.exchanges.Load() != 1 {
		t.Fatal("loading legacy credentials performed authorization I/O")
	}
}
