package pigo

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type openAICodexTestAuthFile struct {
	OpenAICodex openAICodexTestAuthEntry `json:"openai-codex"`
}

type openAICodexTestAuthEntry struct {
	Type         string `json:"type"`
	AccessToken  string `json:"access"`
	RefreshToken string `json:"refresh"`
	Expires      int64  `json:"expires"`
}

func loadOpenAICodexTestToken(path string) string {
	credentials := loadOpenAICodexTestOAuth(path)
	if credentials == nil {
		return ""
	}
	return credentials.AccessToken
}

func loadOpenAICodexTestOAuth(path string) *OAuthCredentials {
	bytes, err := os.ReadFile(resolveSupportFilePath(path))
	if err != nil {
		return nil
	}

	var payload openAICodexTestAuthFile
	if err := json.Unmarshal(bytes, &payload); err != nil {
		return nil
	}
	if payload.OpenAICodex.Type != "oauth" {
		return nil
	}
	if strings.TrimSpace(payload.OpenAICodex.AccessToken) == "" {
		return nil
	}

	expiresUnix := payload.OpenAICodex.Expires
	if expiresUnix > 1_000_000_000_000 {
		expiresUnix = expiresUnix / 1000
	}

	return &OAuthCredentials{
		AccessToken:  payload.OpenAICodex.AccessToken,
		RefreshToken: payload.OpenAICodex.RefreshToken,
		ExpiresUnix:  expiresUnix,
	}
}

func TestCompleteSimpleOpenAICodexLive(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TEST") != "1" {
		t.Skip("set PIGO_LIVE_TEST=1 to run live openai-codex test")
	}

	token := loadOpenAICodexTestToken("01_auth.json")
	if token == "" {
		t.Skip("missing test-only openai codex token in 01_auth.json")
	}

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected openai-codex model")
	}

	response := CompleteSimple(*model, Context{
		SystemPrompt: "Reply with exactly OK.",
		Messages: []Message{
			UserMessage{Content: "Say OK"},
		},
	}, CompleteOptions{
		APIKey: token,
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected live stop reason stop, got %q with error %q", response.StopReason, response.ErrorMessage)
	}
	if len(response.Content) == 0 {
		t.Fatal("expected content from live openai codex response")
	}
	if response.ResponseID == "" {
		t.Fatal("expected live codex response id")
	}

	text, ok := response.Content[0].(TextContent)
	if !ok {
		t.Fatalf("expected first content block to be text, got %T", response.Content[0])
	}
	if !strings.Contains(strings.ToUpper(text.Text), "OK") {
		t.Fatalf("expected live codex response to contain OK, got %q", text.Text)
	}
}

func TestCompleteSimpleOpenAICodexLiveWithSessionAndReasoning(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TEST") != "1" {
		t.Skip("set PIGO_LIVE_TEST=1 to run live openai-codex test")
	}

	token := loadOpenAICodexTestToken("01_auth.json")
	if token == "" {
		t.Skip("missing test-only openai codex token in 01_auth.json")
	}

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected openai-codex model")
	}

	response := CompleteSimple(*model, Context{
		SystemPrompt: "Reply with exactly OK.",
		Messages: []Message{
			UserMessage{Content: "Say OK"},
		},
	}, CompleteOptions{
		APIKey:           token,
		SessionID:        "live-session-codex",
		Reasoning:        ThinkingLevelMinimal,
		ReasoningSummary: "auto",
		TextVerbosity:    "low",
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected live stop reason stop, got %q with error %q", response.StopReason, response.ErrorMessage)
	}
	if response.ResponseID == "" {
		t.Fatal("expected live codex response id")
	}
}

func TestCompleteSimpleOpenAICodexLiveRefresh(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TEST") != "1" {
		t.Skip("set PIGO_LIVE_TEST=1 to run live openai-codex refresh test")
	}

	credentials := loadOpenAICodexTestOAuth("01_auth.json")
	if credentials == nil || strings.TrimSpace(credentials.RefreshToken) == "" {
		t.Skip("missing test-only openai codex oauth credentials in 01_auth.json")
	}

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected openai-codex model")
	}

	response := CompleteSimple(*model, Context{
		SystemPrompt: "Reply with exactly OK.",
		Messages: []Message{
			UserMessage{Content: "Say OK"},
		},
	}, CompleteOptions{
		Auth: map[Provider]AuthConfig{
			"openai-codex": {
				Type: AuthTypeOAuth,
				OAuth: &OAuthCredentials{
					AccessToken:  credentials.AccessToken,
					RefreshToken: credentials.RefreshToken,
					ExpiresUnix:  1,
				},
			},
		},
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected live refreshed codex stop reason stop, got %q with error %q", response.StopReason, response.ErrorMessage)
	}
	if response.ResponseID == "" {
		t.Fatal("expected live codex refreshed response id")
	}
}
