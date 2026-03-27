package pigo

import "testing"

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

	if got := GetEnvAPIKey("kimi-coding"); got != "kimi-env-token" {
		t.Fatalf("expected kimi env token, got %q", got)
	}
	if got := GetEnvAPIKey("openai-codex"); got != "" {
		t.Fatalf("expected no env api key for openai-codex, got %q", got)
	}
}

func TestResolveAPIKeyPrefersExplicitAPIKey(t *testing.T) {
	t.Setenv("KIMI_API_KEY", "kimi-env-token")

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

	if got := ResolveAPIKey("kimi-coding", nil); got != "kimi-env-token" {
		t.Fatalf("expected env fallback token, got %q", got)
	}
}
