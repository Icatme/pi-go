package pigo

import "testing"

func TestHandoffCredentialsRoundTripAPIKeyProvider(t *testing.T) {
	source := map[Provider]AuthConfig{
		"kimi-coding": {
			Type:   AuthTypeAPIKey,
			APIKey: "kimi-test-key",
		},
	}

	handoff := HandoffCredentialsFromAuth(source)
	if handoff.Version != 1 {
		t.Fatalf("expected handoff version 1, got %d", handoff.Version)
	}
	entry, ok := handoff.Providers["kimi-coding"]
	if !ok {
		t.Fatal("expected kimi-coding provider in handoff credentials")
	}
	if entry.Type != AuthTypeAPIKey || entry.APIKey != "kimi-test-key" {
		t.Fatalf("expected apiKey handoff entry, got %+v", entry)
	}

	roundTrip := handoff.ToAuthConfigMap()
	config, ok := roundTrip["kimi-coding"]
	if !ok {
		t.Fatal("expected kimi-coding provider after round trip")
	}
	if config.Type != AuthTypeAPIKey || config.APIKey != "kimi-test-key" {
		t.Fatalf("expected apiKey config after round trip, got %+v", config)
	}
	if config.OAuth != nil {
		t.Fatalf("expected nil oauth for apiKey provider, got %+v", config.OAuth)
	}
}

func TestHandoffCredentialsRoundTripOAuthProvider(t *testing.T) {
	source := map[Provider]AuthConfig{
		"openai-codex": {
			Type: AuthTypeOAuth,
			OAuth: &OAuthCredentials{
				AccessToken:  "access-token",
				RefreshToken: "refresh-token",
				ExpiresUnix:  123456789,
			},
		},
	}

	handoff := HandoffCredentialsFromAuth(source)
	entry, ok := handoff.Providers["openai-codex"]
	if !ok {
		t.Fatal("expected openai-codex provider in handoff credentials")
	}
	if entry.Type != AuthTypeOAuth || entry.OAuth == nil {
		t.Fatalf("expected oauth handoff entry, got %+v", entry)
	}
	if entry.OAuth.AccessToken != "access-token" || entry.OAuth.RefreshToken != "refresh-token" || entry.OAuth.ExpiresUnix != 123456789 {
		t.Fatalf("expected oauth fields to round-trip into handoff entry, got %+v", entry.OAuth)
	}

	roundTrip := handoff.ToAuthConfigMap()
	config, ok := roundTrip["openai-codex"]
	if !ok {
		t.Fatal("expected openai-codex provider after round trip")
	}
	if config.Type != AuthTypeOAuth || config.OAuth == nil {
		t.Fatalf("expected oauth config after round trip, got %+v", config)
	}
	if config.OAuth.AccessToken != "access-token" || config.OAuth.RefreshToken != "refresh-token" || config.OAuth.ExpiresUnix != 123456789 {
		t.Fatalf("expected oauth values after round trip, got %+v", config.OAuth)
	}
}

func TestHandoffCredentialsRoundTripOAuthAccessTokenOnly(t *testing.T) {
	handoff := HandoffCredentials{
		Version: 1,
		Providers: map[Provider]HandoffProviderAuth{
			"openai-codex": {
				Type: AuthTypeOAuth,
				OAuth: &HandoffOAuthCredentials{
					AccessToken: "access-only",
				},
			},
		},
	}

	auth := handoff.ToAuthConfigMap()
	config, ok := auth["openai-codex"]
	if !ok {
		t.Fatal("expected openai-codex provider after conversion")
	}
	if config.Type != AuthTypeOAuth || config.OAuth == nil {
		t.Fatalf("expected oauth config, got %+v", config)
	}
	if config.OAuth.AccessToken != "access-only" || config.OAuth.RefreshToken != "" || config.OAuth.ExpiresUnix != 0 {
		t.Fatalf("expected access-token-only oauth config, got %+v", config.OAuth)
	}
}

func TestHandoffCredentialsEmptyProvidersReturnEmptyMap(t *testing.T) {
	handoff := HandoffCredentials{Version: 1}

	auth := handoff.ToAuthConfigMap()
	if auth == nil {
		t.Fatal("expected empty auth map, got nil")
	}
	if len(auth) != 0 {
		t.Fatalf("expected empty auth map, got %+v", auth)
	}
}

func TestHandoffCredentialsPreservesUnknownProvider(t *testing.T) {
	handoff := HandoffCredentials{
		Version: 1,
		Providers: map[Provider]HandoffProviderAuth{
			"custom-provider": {
				Type:   AuthTypeAPIKey,
				APIKey: "custom-token",
			},
		},
	}

	auth := handoff.ToAuthConfigMap()
	config, ok := auth["custom-provider"]
	if !ok {
		t.Fatal("expected custom-provider to be preserved")
	}
	if config.Type != AuthTypeAPIKey || config.APIKey != "custom-token" {
		t.Fatalf("expected preserved custom provider config, got %+v", config)
	}
}
