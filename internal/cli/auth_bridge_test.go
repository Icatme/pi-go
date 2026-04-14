package cli

import (
	"path/filepath"
	"testing"

	"github.com/Icatme/pi-go/pkg/pigo"
)

func TestBuildRuntimeAuthConvertsOAuthEntries(t *testing.T) {
	auth := buildRuntimeAuth(map[string]storedOAuthCredentials{
		"openai-codex": {
			Type:      "oauth",
			Access:    "access-token",
			Refresh:   "refresh-token",
			Expires:   1710000000123,
			AccountID: "acc_test",
		},
		"kimi-coding": {
			Type:   "apiKey",
			Access: "should-be-ignored",
		},
		"": {
			Type:   "oauth",
			Access: "ignored",
		},
	})

	if len(auth) != 1 {
		t.Fatalf("expected one runtime auth entry, got %+v", auth)
	}

	config, ok := auth[pigo.Provider("openai-codex")]
	if !ok {
		t.Fatalf("expected openai-codex runtime auth, got %+v", auth)
	}
	if config.Type != pigo.AuthTypeOAuth {
		t.Fatalf("expected oauth auth type, got %+v", config)
	}
	if config.OAuth == nil {
		t.Fatalf("expected oauth credentials, got %+v", config)
	}
	if config.OAuth.AccessToken != "access-token" || config.OAuth.RefreshToken != "refresh-token" {
		t.Fatalf("unexpected oauth values: %+v", config.OAuth)
	}
	if config.OAuth.ExpiresUnix != 1710000000 {
		t.Fatalf("expected unix seconds conversion, got %d", config.OAuth.ExpiresUnix)
	}
}

func TestLoadRuntimeAuthReadsAuthJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := saveAuth(path, map[string]storedOAuthCredentials{
		"openai-codex": {
			Type:    "oauth",
			Access:  "access-token",
			Refresh: "refresh-token",
			Expires: 1710000000123,
		},
	}); err != nil {
		t.Fatalf("saveAuth returned error: %v", err)
	}

	auth := loadRuntimeAuth(path)
	config := auth[pigo.Provider("openai-codex")]
	if config.OAuth == nil || config.OAuth.AccessToken != "access-token" {
		t.Fatalf("expected runtime auth from file, got %+v", auth)
	}
}
