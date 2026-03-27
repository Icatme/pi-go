package pigo

import (
	"os"
	"sync"
	"testing"
)

func TestRequiresOAuth(t *testing.T) {
	if !RequiresOAuth("openai-codex") {
		t.Fatal("expected openai-codex to require oauth")
	}
	if RequiresOAuth("kimi-coding") {
		t.Fatal("expected kimi-coding to not require oauth")
	}
}

func TestGetEnvAPIKeyReadsKimiKey(t *testing.T) {
	t.Setenv("KIMI_API_KEY", "kimi-env-token")
	dotEnvOnce = syncOnceForTests()
	dotEnvValues = nil

	if got := GetEnvAPIKey("kimi-coding"); got != "kimi-env-token" {
		t.Fatalf("expected kimi env token, got %q", got)
	}
	if got := GetEnvAPIKey("openai-codex"); got != "" {
		t.Fatalf("expected no env api key for openai-codex, got %q", got)
	}
}

func TestResolveAPIKeyPrefersExplicitAPIKey(t *testing.T) {
	t.Setenv("KIMI_API_KEY", "kimi-env-token")
	dotEnvOnce = syncOnceForTests()
	dotEnvValues = nil

	auth := map[Provider]AuthConfig{
		"kimi-coding": {
			Type:   AuthTypeAPIKey,
			APIKey: "kimi-explicit-token",
		},
	}

	if got := ResolveAPIKey("kimi-coding", auth); got != "kimi-explicit-token" {
		t.Fatalf("expected explicit kimi api key, got %q", got)
	}
}

func TestResolveAPIKeyUsesOAuthAccessToken(t *testing.T) {
	auth := map[Provider]AuthConfig{
		"openai-codex": {
			Type: AuthTypeOAuth,
			OAuth: &OAuthCredentials{
				AccessToken:  "openai-oauth-token",
				RefreshToken: "refresh-token",
			},
		},
	}

	if got := ResolveAPIKey("openai-codex", auth); got != "openai-oauth-token" {
		t.Fatalf("expected oauth access token, got %q", got)
	}
}

func TestResolveAPIKeyFallsBackToEnv(t *testing.T) {
	t.Setenv("KIMI_API_KEY", "kimi-env-token")
	dotEnvOnce = syncOnceForTests()
	dotEnvValues = nil

	if got := ResolveAPIKey("kimi-coding", nil); got != "" {
		t.Fatalf("expected runtime auth resolution to ignore env fallback, got %q", got)
	}
}

func TestGetEnvAPIKeyFallsBackToDotEnvFile(t *testing.T) {
	t.Setenv("KIMI_API_KEY", "")

	tempDir := t.TempDir()
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("expected working directory: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("expected chdir to temp dir: %v", err)
	}
	defer func() {
		_ = os.Chdir(previousDir)
	}()

	if err := os.WriteFile(".env", []byte("KIMI_API_KEY=kimi-dotenv-token\n"), 0o600); err != nil {
		t.Fatalf("expected dotenv write: %v", err)
	}

	dotEnvOnce = syncOnceForTests()
	dotEnvValues = nil

	if got := GetEnvAPIKey("kimi-coding"); got != "kimi-dotenv-token" {
		t.Fatalf("expected dotenv fallback token, got %q", got)
	}
}

func syncOnceForTests() sync.Once {
	return sync.Once{}
}
